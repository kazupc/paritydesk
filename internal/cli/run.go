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
		var jerr error
		if err != nil && pderr.CodeOf(err) != pderr.OK {
			// The result says why it ended with this code.
			jerr = p.JSONWithError(v, err)
		} else {
			jerr = p.JSON(v)
		}
		if jerr != nil {
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
func warn(p *render.Printer, ws []i18n.Text) {
	for _, w := range ws {
		p.WarnText(w)
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
	invoking := getString(fs, flagInvokingSession)
	if invoking != "" && !ids.IsUUID(invoking) {
		return usageError(i18n.UsageSessionUuid, flagInvokingSession)
	}
	svc, err := e.service(ctx, e.newUI(false, false))
	if err != nil {
		return err
	}
	save := !e.readOnly()
	res, err := svc.Preview(ctx, orchestrator.PreviewOptions{To: to, Only: only, Skip: skip, Save: save, InvokingCLISession: invoking})
	if res == nil {
		return orInternal(err, "plan")
	}
	v := newRunView(res)
	v.Saved = &save
	return e.emit(v, func(p *render.Printer) {
		p.Lines(v.Lines)
		p.Lines(v.humanBusy)
		warn(p, v.Warnings)
		switch {
		case !save:
			p.Msg(i18n.PlanNotSaved)
		default:
			p.Msg(i18n.PlanSaved)
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
			p.Msg(i18n.SwitchDetached, v.Run)
		case v.QuitPending:
			p.Msg(i18n.SwitchQuitPending)
		case v.Code == pderr.Declined:
			// The plan was shown with the question; the error says the rest.
		default:
			p.Lines(v.Lines)
			p.Lines(v.humanBusy)
			if v.Code == pderr.OK || v.Code == pderr.SwapOnly || v.Code == pderr.Degraded {
				p.Msg(i18n.SwitchDone, string(v.From), string(v.To))
			}
			if v.Mode != "" {
				p.Msg(i18n.SwitchMode, string(v.Mode))
			}
			if v.Generation > 0 {
				p.Msg(i18n.SwitchGeneration, v.Generation)
			}
			if v.Online != nil && v.Online.Queued {
				p.Msg(i18n.SwitchOnlineNext)
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
		e.printer().WarnText(i18n.Tx(i18n.SwitchJobResult))
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
		return usageError(i18n.UsageNegative, orchestrator.FlagAllowDeletions)
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
			p.Msg(i18n.SyncDone, string(v.To))
		}
		if v.Online != nil && v.Online.Queued {
			p.Msg(i18n.SwitchOnlineNext)
		}
		warn(p, v.Warnings)
	}, resultErr("sync", res.Code, err))
}

// verifyView is the --json document of verify.
type verifyView struct {
	Drift          []model.RowID `json:"drift"`
	Replaced       []model.RowID `json:"replaced"`
	Unverified     []model.RowID `json:"unverified,omitempty"`
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
			return usageError(i18n.UsageBadRunID, orchestrator.FlagAfterRun)
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
	v := verifyView{Drift: res.Drift, Replaced: res.Replaced, Unverified: res.Unverified, SignInNeeded: res.SignInNeeded,
		SessionFocused: res.SessionFocused, PairingOK: res.PairingOK, BridgeSeen: res.BridgeSeen}
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
			p.Msg(i18n.VerifyReplaced, joinRows(v.Replaced))
		}
		if len(v.Unverified) > 0 {
			p.Msg(i18n.VerifyUnverified, joinRows(v.Unverified))
		}
		switch {
		case len(v.Drift) > 0:
			p.Msg(i18n.VerifyDrift, joinRows(v.Drift))
		case !v.SignInNeeded:
			p.Msg(i18n.VerifyOK)
		}
		if v.SignInNeeded {
			p.Msg(i18n.VerifySignIn)
		}
		if v.SessionFocused {
			p.Msg(i18n.VerifyFocused)
		}
		// Claude in Chrome connects on first use, not at launch, so no
		// connection line is no sign of a problem: only a seen one is shown.
		if v.BridgeSeen {
			p.Msg(i18n.VerifyBridge)
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
	v.Actions = sanitizeTexts(v.Actions)
	if v.Actions == nil {
		v.Actions = []i18n.Text{}
	}
	return e.emit(v, func(p *render.Printer) {
		if len(v.Actions) == 0 {
			p.Msg(i18n.RecoverNone)
		}
		for _, a := range v.Actions {
			p.Msg(i18n.RecoverAction, a)
		}
		if v.Active != "" {
			p.Msg(i18n.RecoverActive, string(v.Active))
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
		p.Msg(i18n.RollbackDone, v.Generation, v.Restored)
		for _, c := range v.Conflicts {
			p.Msg(i18n.RollbackConflict, c)
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
	return e.emit(okView{OK: true}, func(p *render.Printer) { p.Msg(i18n.RebaseDone) }, nil)
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
		// The waiting online changes come from root.json only (G5).
		tail := ""
		if n := s.Online.Pending(); n > 0 {
			tail = " " + i18n.T(i18n.OnlineShort, n)
		}
		switch {
		case s.NeedsRecover:
			p.Line("%s%s", p.Render(i18n.Tx(i18n.StatusNeedsRecover), 0), tail)
		case s.Active != "":
			p.Line("%s%s", p.Render(i18n.Tx(i18n.StatusActive, string(s.Active)), 0), tail)
		default:
			p.Line("%s%s", p.Render(i18n.Tx(i18n.StatusNoActive), 0), tail)
		}
		return
	}
	if s.Active != "" {
		p.Msg(i18n.StatusActive, string(s.Active))
	} else {
		p.Msg(i18n.StatusNoActive)
	}
	if s.Previous != "" {
		p.Msg(i18n.StatusPrevious, string(s.Previous))
	}
	if len(s.Profiles) > 0 {
		printProfiles(p, s.Profiles)
	}
	if s.AppRunning {
		p.Msg(i18n.StatusAppRunning)
	}
	if s.NeedsRecover {
		p.Msg(i18n.StatusNeedsRecover)
	}
	if s.UpdateStaged {
		p.Msg(i18n.StatusUpdate)
	}
	if lv := s.LastVerify; lv != nil {
		if lv.SignIn {
			p.Msg(i18n.StatusSignIn)
		}
		if len(lv.Drift) > 0 {
			p.Msg(i18n.StatusDrift, joinRows(lv.Drift))
		}
	}
	p.Msg(i18n.StatusGenerations, s.Generations)
	if o := s.Online; o.Pending() > 0 {
		p.Msg(i18n.OnlineStatusPending, o.Counts.PinsAdd, o.Counts.PinsRemove, o.Counts.Sidebar, render.Sanitize(string(o.Profile), 32))
	}
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
		rows[i] = []string{string(s.Alias), s.Folder, yn(s.Active), s.Account, yn(s.Managed), base(s.BaseValid), p.Render(s.Problem, 0)}
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
			p.Msg(i18n.ProfileNone)
			return
		}
		printProfiles(p, s.Profiles)
	}, err)
}

func runProfileAdopt(ctx context.Context, e *env, fs *flag.FlagSet, args []string) error {
	if getBool(fs, flagFromLegacy) {
		if len(args) > 0 {
			return usageError(i18n.UsageLegacyNoArgs)
		}
		return runProfileAdoptLegacy(ctx, e, fs)
	}
	if len(args) != 2 {
		return usageError(i18n.UsageSyntax, fs.Name(), "<alias> <folder>")
	}
	for _, name := range legacyOnlyFlags {
		if flagSet(fs, name) {
			return usageError(i18n.UsageNeedsFlag, name, flagFromLegacy)
		}
	}
	alias, err := parseAlias(args[0])
	if err != nil {
		return err
	}
	if err := ids.ValidateFolder(args[1]); err != nil {
		return usageError(i18n.UsageBadFolder, args[1])
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
		p.Msg(i18n.ProfileAdopted, string(alias))
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
		p.Msg(i18n.ProfileAdded, string(alias))
		p.Msg(i18n.ChromeSetup)
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
		p.Msg(i18n.ProfileRenamed, string(from), string(to))
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
		p.Msg(i18n.ProfileRemoved, string(alias))
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
		detail := c.Detail.MapStrings(red.String).MapStrings(func(s string) string { return render.Sanitize(s, 0) }).Cut(300)
		v.Checks[i] = orchestrator.Check{ID: render.Sanitize(c.ID, 64), Status: render.Sanitize(c.Status, 8), Detail: detail}
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
			rows[i] = []string{c.ID, c.Status, p.Render(c.Detail, 0)}
		}
		p.TableWith([]string{i18n.T(i18n.ColCheck), i18n.T(i18n.ColStatus), i18n.T(i18n.ColDetail)}, rows,
			render.TableOptions{FreeLast: true})
		if v.HookBinary != "" {
			p.Msg(i18n.DoctorHookBinary, v.HookBinary)
		}
		if v.Bundle != "" {
			p.Msg(i18n.DoctorBundleDone, v.Bundle)
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
		return false, usageError(i18n.UsageManagedBoth)
	case managed || unmanaged:
		return managed, nil
	case !e.interactive():
		return false, usageError(i18n.UsageManagedRequired)
	}
	return e.newUI(false, false).ask(ctx, i18n.T(i18n.AskManaged, string(alias)))
}

func runInit(ctx context.Context, e *env, fs *flag.FlagSet, _ []string) error {
	raw := getString(fs, "alias")
	if raw == "" {
		if !e.interactive() {
			return usageError(i18n.UsageAliasRequired)
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
		return usageError(i18n.UsageLabelTooLong, config.MaxLabelRunes)
	}
	managed, err := managedChoice(ctx, e, fs, alias)
	if err != nil {
		return err
	}
	svc, err := e.service(ctx, e.newUI(false, false))
	if err != nil {
		return err
	}
	opts := orchestrator.InitOptions{Alias: alias, Label: label, Managed: managed}
	if err := svc.Init(ctx, opts); err != nil {
		return err
	}
	v := initView{OK: true, Alias: alias}
	if v.HookBinary, err = e.recordHookBinary(); err != nil {
		return err
	}
	return e.emit(v, func(p *render.Printer) {
		p.Msg(i18n.InitDone, string(alias))
		p.Msg(i18n.ChromeSetup)
		if v.HookBinary != "" {
			p.Msg(i18n.DoctorHookBinary, v.HookBinary)
		}
	}, nil)
}

// orInternal returns err, or an internal error when a service returned
// neither a result nor an error.
func orInternal(err error, op string) error {
	if err != nil {
		return err
	}
	return pderr.Bug(op, "no result")
}
