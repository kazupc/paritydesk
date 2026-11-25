package cli

import (
	"context"
	"flag"
	"path/filepath"
	"strings"

	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/orchestrator"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/render"
	"github.com/kazupc/paritydesk/internal/runner"
)

// This file maps the commands that the orchestrator runs: each run
// function parses its flags into options, calls one orchestrator.Service
// method and renders the result (one JSON document with --json).

// service returns the orchestrator for one command.
func (e *env) service(ctx context.Context, ui orchestrator.UI) (orchestrator.Service, error) {
	return e.sys.service(ctx, e, ui)
}

// emit prints v as the JSON document in JSON mode and runs text
// otherwise. When err is set too, the output is printed and err decides
// the exit code; in JSON mode its catalog entry is not printed again.
func (e *env) emit(v any, text func(p *render.Printer), err error) error {
	p := e.printer()
	if p.Mode.JSON {
		if jerr := p.JSON(v); jerr != nil {
			return jerr
		}
		if err != nil {
			return &quietError{err}
		}
		return nil
	}
	text(p)
	return err
}

// resultErr returns err, or an error for a non-zero result code.
func resultErr(op string, code pderr.Code, err error) error {
	if err == nil && code != pderr.OK {
		return pderr.New(code, op, "")
	}
	return err
}

// warn prints sanitized warnings on stderr.
func warn(p *render.Printer, ws []string) {
	for _, w := range ws {
		p.Warn(w)
	}
}

func runPlan(ctx context.Context, e *env, fs *flag.FlagSet, args []string) error {
	to, err := parseTarget(args)
	if err != nil {
		return err
	}
	only, skip, err := categoryOptions(fs)
	if err != nil {
		return err
	}
	svc, err := e.service(ctx, e.newUI(false, false))
	if err != nil {
		return err
	}
	save := !e.readOnly()
	res, err := svc.Preview(ctx, orchestrator.PreviewOptions{To: to, Only: only, Skip: skip, Save: save})
	if res == nil {
		return orInternal(err, "plan")
	}
	v := newRunView(res)
	v.Saved = &save
	return e.emit(v, func(p *render.Printer) {
		p.Lines(v.Lines)
		warn(p, v.Warnings)
		switch {
		case !save:
			p.Line(i18n.T(i18n.PlanNotSaved))
		default:
			p.Line(i18n.T(i18n.PlanSaved))
		}
	}, resultErr("plan", res.Code, err))
}

func runSwitch(ctx context.Context, e *env, fs *flag.FlagSet, args []string) error {
	opts, err := switchOptions(fs, args)
	if err != nil {
		return err
	}
	ui := e.newUI(opts.Yes, getBool(fs, flagJSONProgress))
	svc, err := e.service(ctx, ui)
	if err != nil {
		return err
	}
	res, err := svc.Switch(ctx, opts)
	if res == nil {
		finishJob(e, opts, &orchestrator.Result{To: opts.To, Code: pderr.CodeOf(orInternal(err, "switch"))})
		return orInternal(err, "switch")
	}
	if !res.Detached {
		e.writeRunLog(res, ui.events)
	}
	finishJob(e, opts, res)
	v := newRunView(res)
	return e.emit(v, func(p *render.Printer) {
		switch {
		case v.Detached:
			p.Line(i18n.T(i18n.SwitchDetached, "%s"), v.Run)
		case v.QuitPending:
			p.Line(i18n.T(i18n.SwitchQuitPending))
		case v.Code == pderr.Declined:
			// The plan was shown with the question; the error says the rest.
		default:
			p.Lines(v.Lines)
			if v.Code == pderr.OK || v.Code == pderr.SwapOnly || v.Code == pderr.Degraded {
				p.Line(i18n.T(i18n.SwitchDone, "%s", "%s"), string(v.From), string(v.To))
			}
			if v.Mode != "" {
				p.Line(i18n.T(i18n.SwitchMode, "%s"), string(v.Mode))
			}
			if v.Generation > 0 {
				p.Line(i18n.T(i18n.SwitchGeneration, v.Generation))
			}
		}
		warn(p, v.Warnings)
	}, resultErr("switch", res.Code, err))
}

// finishJob is the detached job's last step (runner.Finish): when this
// process is the launchd job (--from-runner with a valid run id in
// runner.EnvRunID), it writes the job's result file and removes its plist.
// A failure is reported as a warning and never changes the exit code.
func finishJob(e *env, opts orchestrator.SwitchOptions, res *orchestrator.Result) {
	if !opts.FromRunner {
		return
	}
	id := e.sys.getenv(runner.EnvRunID)
	if !runner.ValidRunID(id) {
		return
	}
	if err := writeJobResult(e, id, res); err != nil {
		e.printer().Warn(i18n.T(i18n.SwitchJobResult))
	}
}

func writeJobResult(e *env, id string, res *orchestrator.Result) error {
	paths, _, err := e.paths()
	if err != nil {
		return err
	}
	exe, err := e.sys.executable()
	if err != nil {
		return err
	}
	j, err := runner.NewJob(paths.RunDir, paths.LogDir, exe, id, nil)
	if err != nil {
		return err
	}
	r := runner.Result{RunID: id, Code: int(res.Code), From: string(res.From), To: string(res.To), Finished: e.sys.now()}
	if res.Code != pderr.OK {
		r.ErrorID = pderr.ID(res.Code)
	}
	return runner.Finish(j, r)
}

func runSync(ctx context.Context, e *env, fs *flag.FlagSet, _ []string) error {
	opts := orchestrator.SyncOptions{
		Yes:            getBool(fs, orchestrator.FlagYes),
		AllowDeletions: getInt(fs, orchestrator.FlagAllowDeletions),
		NoLaunch:       getBool(fs, orchestrator.FlagNoLaunch),
	}
	if opts.AllowDeletions < 0 {
		return usageErrorf("--%s must not be negative", orchestrator.FlagAllowDeletions)
	}
	ui := e.newUI(opts.Yes, getBool(fs, flagJSONProgress))
	svc, err := e.service(ctx, ui)
	if err != nil {
		return err
	}
	res, err := svc.Sync(ctx, opts)
	if res == nil {
		return orInternal(err, "sync")
	}
	e.writeRunLog(res, ui.events)
	v := newRunView(res)
	return e.emit(v, func(p *render.Printer) {
		p.Lines(v.Lines)
		if v.Code == pderr.OK {
			p.Line(i18n.T(i18n.SyncDone, "%s"), string(v.To))
		}
		warn(p, v.Warnings)
	}, resultErr("sync", res.Code, err))
}

// verifyView is the --json document of verify.
type verifyView struct {
	Drift          []model.RowID `json:"drift"`
	Replaced       []model.RowID `json:"replaced"`
	SignInNeeded   bool          `json:"sign_in_needed"`
	SessionFocused bool          `json:"session_focused"`
	PairingOK      bool          `json:"pairing_ok"`
	BridgeSeen     bool          `json:"bridge_connected"`
	Code           pderr.Code    `json:"code"`
}

func runVerify(ctx context.Context, e *env, fs *flag.FlagSet, _ []string) error {
	svc, err := e.service(ctx, e.newUI(false, false))
	if err != nil {
		return err
	}
	var res *orchestrator.VerifyResult
	after := getString(fs, orchestrator.FlagAfterRun)
	if after != "" {
		if !runner.ValidRunID(after) {
			return usageErrorf("--%s is not a run id", orchestrator.FlagAfterRun)
		}
		res, err = svc.VerifyAfter(ctx, after)
		// The background job writes its result so the runner can reap it.
		if id := e.sys.getenv(runner.EnvRunID); runner.ValidRunID(id) {
			_ = writeJobResult(e, id, &orchestrator.Result{Code: pderr.CodeOf(err)})
		}
	} else {
		res, err = svc.Verify(ctx)
	}
	if res == nil {
		return orInternal(err, "verify")
	}
	v := verifyView{Drift: res.Drift, Replaced: res.Replaced, SignInNeeded: res.SignInNeeded, SessionFocused: res.SessionFocused,
		PairingOK: res.PairingOK, BridgeSeen: res.BridgeSeen}
	if v.Drift == nil {
		v.Drift = []model.RowID{}
	}
	if v.Replaced == nil {
		v.Replaced = []model.RowID{}
	}
	if err == nil {
		switch {
		case res.SignInNeeded:
			err = pderr.New(pderr.NeedsSignIn, "verify", "")
		case len(res.Drift) > 0:
			err = pderr.New(pderr.VerifyDrift, "verify", "")
		}
	}
	v.Code = pderr.CodeOf(err)
	return e.emit(v, func(p *render.Printer) {
		if len(v.Replaced) > 0 {
			p.Line(i18n.T(i18n.VerifyReplaced, "%s"), joinRows(v.Replaced))
		}
		switch {
		case len(v.Drift) > 0:
			p.Line(i18n.T(i18n.VerifyDrift, "%s"), joinRows(v.Drift))
		case !v.SignInNeeded:
			p.Line(i18n.T(i18n.VerifyOK))
		}
		if v.SignInNeeded {
			p.Line(i18n.T(i18n.VerifySignIn))
		}
		if v.SessionFocused {
			p.Line(i18n.T(i18n.VerifyFocused))
		}
		if v.BridgeSeen {
			p.Line(i18n.T(i18n.VerifyBridge))
		} else {
			p.Line(i18n.T(i18n.VerifyNoBridge))
		}
	}, err)
}

func joinRows(rs []model.RowID) string {
	s := make([]string, len(rs))
	for i, r := range rs {
		s[i] = string(r)
	}
	return strings.Join(s, ", ")
}

func runRecover(ctx context.Context, e *env, fs *flag.FlagSet, _ []string) error {
	opts := orchestrator.RecoverOptions{Rollback: getBool(fs, "rollback"), Yes: getBool(fs, orchestrator.FlagYes)}
	svc, err := e.service(ctx, e.newUI(opts.Yes, false))
	if err != nil {
		return err
	}
	res, err := svc.Recover(ctx, opts)
	if res == nil {
		return orInternal(err, "recover")
	}
	v := *res
	v.Actions = sanitizeAll(v.Actions)
	if v.Actions == nil {
		v.Actions = []string{}
	}
	return e.emit(v, func(p *render.Printer) {
		if len(v.Actions) == 0 {
			p.Line(i18n.T(i18n.RecoverNone))
		}
		for _, a := range v.Actions {
			p.Line(i18n.T(i18n.RecoverAction, "%s"), a)
		}
		if v.Active != "" {
			p.Line(i18n.T(i18n.RecoverActive, "%s"), string(v.Active))
		}
	}, err)
}

func runRollback(ctx context.Context, e *env, fs *flag.FlagSet, args []string) error {
	n, err := parseGeneration(args)
	if err != nil {
		return err
	}
	yes := getBool(fs, orchestrator.FlagYes)
	svc, err := e.service(ctx, e.newUI(yes, false))
	if err != nil {
		return err
	}
	res, err := svc.Rollback(ctx, n, getBool(fs, "propagate"))
	if res == nil {
		return orInternal(err, "rollback")
	}
	v := *res
	v.Conflicts = sanitizeAll(v.Conflicts)
	return e.emit(v, func(p *render.Printer) {
		p.Line(i18n.T(i18n.RollbackDone, v.Generation, v.Restored))
		for _, c := range v.Conflicts {
			p.Line(i18n.T(i18n.RollbackConflict, "%s"), c)
		}
	}, err)
}

func runRebase(ctx context.Context, e *env, _ *flag.FlagSet, _ []string) error {
	svc, err := e.service(ctx, e.newUI(false, false))
	if err != nil {
		return err
	}
	if err := svc.Rebase(ctx); err != nil {
		return err
	}
	return e.emit(okView{OK: true}, func(p *render.Printer) { p.Line(i18n.T(i18n.RebaseDone)) }, nil)
}

func runStatus(ctx context.Context, e *env, fs *flag.FlagSet, _ []string) error {
	short := getBool(fs, "short")
	svc, err := e.service(ctx, e.newUI(false, false))
	if err != nil {
		return err
	}
	st, err := svc.Status(ctx, short)
	if st == nil {
		return orInternal(err, "status")
	}
	s := normalizeStatus(st)
	return e.emit(s, func(p *render.Printer) { printStatus(p, s, short) }, err)
}

func printStatus(p *render.Printer, s *orchestrator.Status, short bool) {
	if short {
		switch {
		case s.NeedsRecover:
			p.Line(i18n.T(i18n.StatusNeedsRecover))
		case s.Active != "":
			p.Line(i18n.T(i18n.StatusActive, "%s"), string(s.Active))
		default:
			p.Line(i18n.T(i18n.StatusNoActive))
		}
		return
	}
	if s.Active != "" {
		p.Line(i18n.T(i18n.StatusActive, "%s"), string(s.Active))
	} else {
		p.Line(i18n.T(i18n.StatusNoActive))
	}
	if s.Previous != "" {
		p.Line(i18n.T(i18n.StatusPrevious, "%s"), string(s.Previous))
	}
	if len(s.Profiles) > 0 {
		printProfiles(p, s.Profiles)
	}
	if s.AppRunning {
		p.Line(i18n.T(i18n.StatusAppRunning))
	}
	if s.NeedsRecover {
		p.Line(i18n.T(i18n.StatusNeedsRecover))
	}
	if s.UpdateStaged {
		p.Line(i18n.T(i18n.StatusUpdate))
	}
	if lv := s.LastVerify; lv != nil {
		if lv.SignIn {
			p.Line(i18n.T(i18n.StatusSignIn))
		}
		if len(lv.Drift) > 0 {
			p.Line(i18n.T(i18n.StatusDrift, "%s"), joinRows(lv.Drift))
		}
	}
	p.Line(i18n.T(i18n.StatusGenerations, s.Generations))
	warn(p, s.Warnings)
}

func printProfiles(p *render.Printer, ps []orchestrator.ProfileStatus) {
	yn := func(b bool) string {
		if b {
			return i18n.T(i18n.Yes)
		}
		return i18n.T(i18n.No)
	}
	base := func(b bool) string {
		if b {
			return i18n.T(i18n.Valid)
		}
		return i18n.T(i18n.Invalid)
	}
	rows := make([][]string, len(ps))
	for i, s := range ps {
		rows[i] = []string{string(s.Alias), s.Folder, yn(s.Active), s.Account, yn(s.Managed), base(s.BaseValid), s.Problem}
	}
	p.Table([]string{i18n.T(i18n.ColAlias), i18n.T(i18n.ColFolder), i18n.T(i18n.ColActive), i18n.T(i18n.ColAccount),
		i18n.T(i18n.ColManaged), i18n.T(i18n.ColBase), i18n.T(i18n.ColProblem)}, rows)
}

// profilesView is the --json document of profile list.
type profilesView struct {
	Active   ids.Alias                    `json:"active,omitempty"`
	Profiles []orchestrator.ProfileStatus `json:"profiles"`
}

// runProfileList lists profiles from the short status (root.json and
// folder names only).
func runProfileList(ctx context.Context, e *env, _ *flag.FlagSet, _ []string) error {
	svc, err := e.service(ctx, e.newUI(false, false))
	if err != nil {
		return err
	}
	st, err := svc.Status(ctx, true)
	if st == nil {
		return orInternal(err, "profile list")
	}
	s := normalizeStatus(st)
	return e.emit(profilesView{Active: s.Active, Profiles: s.Profiles}, func(p *render.Printer) {
		if len(s.Profiles) == 0 {
			p.Line(i18n.T(i18n.ProfileNone))
			return
		}
		printProfiles(p, s.Profiles)
	}, err)
}

func runProfileAdopt(ctx context.Context, e *env, fs *flag.FlagSet, args []string) error {
	alias, err := parseAlias(args[0])
	if err != nil {
		return err
	}
	if err := ids.ValidateFolder(args[1]); err != nil {
		return usageErrorf("%q is not a parked folder name (Claude-<name>)", args[1])
	}
	managed, err := managedChoice(ctx, e, fs, alias)
	if err != nil {
		return err
	}
	svc, err := e.service(ctx, e.newUI(false, false))
	if err != nil {
		return err
	}
	if err := svc.ProfileAdopt(ctx, alias, args[1], managed); err != nil {
		return err
	}
	return e.emit(okView{OK: true, Alias: alias}, func(p *render.Printer) {
		p.Line(i18n.T(i18n.ProfileAdopted, "%s"), string(alias))
	}, nil)
}

func runProfileAdd(ctx context.Context, e *env, fs *flag.FlagSet, args []string) error {
	alias, err := parseAlias(args[0])
	if err != nil {
		return err
	}
	managed, err := managedChoice(ctx, e, fs, alias)
	if err != nil {
		return err
	}
	svc, err := e.service(ctx, e.newUI(false, false))
	if err != nil {
		return err
	}
	if err := svc.ProfileAdd(ctx, alias, managed); err != nil {
		return err
	}
	return e.emit(okView{OK: true, Alias: alias}, func(p *render.Printer) {
		p.Line(i18n.T(i18n.ProfileAdded, "%s"), string(alias))
		p.Line(i18n.T(i18n.ChromeSetup))
	}, nil)
}

func runProfileRename(ctx context.Context, e *env, _ *flag.FlagSet, args []string) error {
	from, err := parseAlias(args[0])
	if err != nil {
		return err
	}
	to, err := parseAlias(args[1])
	if err != nil {
		return err
	}
	svc, err := e.service(ctx, e.newUI(false, false))
	if err != nil {
		return err
	}
	if err := svc.ProfileRename(ctx, from, to); err != nil {
		return err
	}
	return e.emit(okView{OK: true, From: from, To: to}, func(p *render.Printer) {
		p.Line(i18n.T(i18n.ProfileRenamed, "%s", "%s"), string(from), string(to))
	}, nil)
}

func runProfileRemove(ctx context.Context, e *env, _ *flag.FlagSet, args []string) error {
	alias, err := parseAlias(args[0])
	if err != nil {
		return err
	}
	svc, err := e.service(ctx, e.newUI(false, false))
	if err != nil {
		return err
	}
	if err := svc.ProfileRemove(ctx, alias); err != nil {
		return err
	}
	return e.emit(okView{OK: true, Alias: alias}, func(p *render.Printer) {
		p.Line(i18n.T(i18n.ProfileRemoved, "%s"), string(alias))
	}, nil)
}

// doctorView is the --json document of doctor.
type doctorView struct {
	Checks     []orchestrator.Check `json:"checks"`
	HookBinary string               `json:"hook_binary,omitempty"`
	// Bundle is the file name (without its directory) of the support
	// archive written with --bundle.
	Bundle string `json:"bundle,omitempty"`
}

func runDoctor(ctx context.Context, e *env, fs *flag.FlagSet, _ []string) error {
	bundlePath, bundleLogs := getString(fs, flagBundle), getInt(fs, flagBundleLogs)
	if bundlePath != "" {
		abs, err := e.checkBundleTarget(bundlePath, bundleLogs)
		if err != nil {
			return err
		}
		bundlePath = abs
	}
	svc, err := e.service(ctx, e.newUI(false, false))
	if err != nil {
		return err
	}
	checks, err := svc.Doctor(ctx, orchestrator.DoctorOptions{GrantAutomation: getBool(fs, "grant-automation")})
	if err != nil {
		return err
	}
	// Doctor output is meant to be attached to issues: details and the
	// hook binary path are redacted (home paths hashed, ids masked).
	red := e.redactor()
	v := doctorView{Checks: make([]orchestrator.Check, len(checks))}
	for i, c := range checks {
		v.Checks[i] = orchestrator.Check{ID: render.Sanitize(c.ID, 64), Status: render.Sanitize(c.Status, 8),
			Detail: render.Sanitize(red.String(c.Detail), 300)}
	}
	hookBinary, err := e.recordHookBinary()
	if err != nil {
		return err
	}
	v.HookBinary = red.Path(hookBinary)
	if bundlePath != "" {
		if err := e.writeBundle(bundlePath, bundleLogs, v); err != nil {
			return err
		}
		v.Bundle = render.Sanitize(filepath.Base(bundlePath), 120)
	}
	return e.emit(v, func(p *render.Printer) {
		rows := make([][]string, len(v.Checks))
		for i, c := range v.Checks {
			rows[i] = []string{c.ID, c.Status, c.Detail}
		}
		p.Table([]string{i18n.T(i18n.ColCheck), i18n.T(i18n.ColStatus), i18n.T(i18n.ColDetail)}, rows)
		if v.HookBinary != "" {
			p.Line(i18n.T(i18n.DoctorHookBinary, "%s"), v.HookBinary)
		}
		if v.Bundle != "" {
			p.Line(i18n.T(i18n.DoctorBundleDone, "%s"), v.Bundle)
		}
	}, nil)
}

// initView is the --json document of init.
type initView struct {
	OK         bool      `json:"ok"`
	Alias      ids.Alias `json:"alias"`
	HookBinary string    `json:"hook_binary,omitempty"`
}

// managedChoice resolves --managed and --unmanaged. With neither, a
// terminal asks; without a terminal one of them is required, because the
// answer decides whether preset secrets may cross to other accounts
// (sync.secrets = "same-boundary").
func managedChoice(ctx context.Context, e *env, fs *flag.FlagSet, alias ids.Alias) (bool, error) {
	managed, unmanaged := getBool(fs, "managed"), getBool(fs, "unmanaged")
	switch {
	case managed && unmanaged:
		return false, usageErrorf("--managed and --unmanaged exclude each other")
	case managed || unmanaged:
		return managed, nil
	case !e.interactive():
		return false, usageErrorf("--managed or --unmanaged is required when there is no terminal to ask")
	}
	return e.newUI(false, false).ask(ctx, i18n.T(i18n.AskManaged, string(alias)))
}

func runInit(ctx context.Context, e *env, fs *flag.FlagSet, _ []string) error {
	raw := getString(fs, "alias")
	if raw == "" {
		if !e.interactive() {
			return usageErrorf("--alias is required when there is no terminal to ask")
		}
		u := e.newUI(false, false)
		_, _ = u.p.Out.Write([]byte(i18n.T(i18n.AskAlias)))
		line, err := u.readLine(ctx)
		if err != nil {
			return err
		}
		raw = strings.TrimSpace(line)
	}
	alias, err := parseAlias(raw)
	if err != nil {
		return err
	}
	label := getString(fs, "label")
	if render.Sanitize(label, config.MaxLabelRunes) != label {
		return usageErrorf("--label: at most %d printable characters", config.MaxLabelRunes)
	}
	managed, err := managedChoice(ctx, e, fs, alias)
	if err != nil {
		return err
	}
	svc, err := e.service(ctx, e.newUI(false, false))
	if err != nil {
		return err
	}
	if err := svc.Init(ctx, orchestrator.InitOptions{Alias: alias, Label: label, Managed: managed}); err != nil {
		return err
	}
	v := initView{OK: true, Alias: alias}
	if v.HookBinary, err = e.recordHookBinary(); err != nil {
		return err
	}
	return e.emit(v, func(p *render.Printer) {
		p.Line(i18n.T(i18n.InitDone, "%s"), string(alias))
		p.Line(i18n.T(i18n.ChromeSetup))
		if v.HookBinary != "" {
			p.Line(i18n.T(i18n.DoctorHookBinary, "%s"), v.HookBinary)
		}
	}, nil)
}

// orInternal returns err, or an internal error when a service returned
// neither a result nor an error.
func orInternal(err error, op string) error {
	if err != nil {
		return err
	}
	return pderr.New(pderr.Internal, op, "no result")
}
