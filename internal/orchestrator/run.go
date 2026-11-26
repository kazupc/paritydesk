package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kazupc/paritydesk/internal/app"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/gen"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/profile"
	"github.com/kazupc/paritydesk/internal/runner"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/state"
	"github.com/kazupc/paritydesk/internal/store/kv"
)

// run is one switch or sync in progress.
type run struct {
	o           *Orchestrator
	kind        state.RunKind
	opts        SwitchOptions
	res         *Result
	interactive bool
	locked      bool
	// previewOnly marks plan-only runs (plan, read-only mode): no update
	// refusal, nothing quit.
	previewOnly bool
	// confirmedByPerson: a person saw the plan's DESTRUCTIVE line and said
	// yes (at the terminal, or through --confirmed-plan). Deletions found
	// after the quit are asked again or abort the run, so every deletion
	// applied was seen; the deletion breaker is then not needed.
	confirmedByPerson bool
	// sMarked: the leaving folder got its marker at P7 (markLeaving).
	sMarked bool
	// notified: a detached run already posted a notification.
	notified bool
	lock     *state.Lock

	root     *state.Root
	bundle   app.BundleIdentity
	manifest *compat.Manifest
	tested   bool
	sig      compat.Signals
	folders  []profile.Folder
	resn     *profile.Resolution
	S, T     *profile.Binding
	decision compat.Decision
	rows     []model.RowID
	skipped  map[model.RowID]string

	// sPath and tPath are the leaving and entering folders' current
	// paths; they change at P8 and P9.
	sPath, tPath string
	sLS, tLS     kv.Reader

	invoking ids.SessionID
	busyAck  map[ids.SessionID]bool
	// busyTitles are the Code titles of the last busy scan.
	busyTitles map[ids.SessionID]string
	// warnedStale, warnedTruncated and warnedWindow keep the busy and
	// account warnings to one each per run.
	warnedStale, warnedTruncated, warnedWindow bool
	// warnedBusyLog keeps the "log could not be read" warning to one.
	warnedBusyLog bool
	// changes are the account changes inside a folder logged since the
	// last committed run (nil: not known).
	changes []profile.AttributedChange
	// previewCLI is plan's --invoking-session.
	previewCLI string
	// since is the app's start time (appSince, memoized).
	since     time.Time
	sinceDone bool

	p2, p5    *planned
	t4        time.Time
	gen       uint64
	genLS     bool
	genWriter gen.Writer
	// touched and lsOpened are apply.Result's flags: whether P7 may have
	// changed the entering folder, and opened its Local Storage writable.
	touched, lsOpened bool
	fallback          bool
	quitByUs          bool
	tSess             *model.RowSnapshot
	launchAt          int64
	launched          bool
}

func (o *Orchestrator) newRun(kind state.RunKind, opts SwitchOptions) *run {
	return &run{
		o: o, kind: kind, opts: opts, busyAck: map[ids.SessionID]bool{},
		res: &Result{To: opts.To, Timings: map[Phase]time.Duration{}},
	}
}

// runID returns the run id: the job's own id when started by launchd,
// else a fresh one.
func (r *run) newRunID() error {
	if r.opts.FromRunner {
		if id := os.Getenv(runner.EnvRunID); runner.ValidRunID(id) {
			r.res.RunID = id
			return nil
		}
	}
	id, err := runner.NewRunID(r.o.now())
	if err != nil {
		return err
	}
	r.res.RunID = id
	return nil
}

func (r *run) d() *Deps { return &r.o.d }

// phase times a phase and reports its start.
func (r *run) phase(p Phase, msg string) func() {
	start := r.o.now()
	r.o.progress(p, "start", msg)
	return func() { r.res.Timings[p] += r.o.now().Sub(start) }
}

func (r *run) warn(p Phase, msg string) {
	r.res.Warnings = append(r.res.Warnings, msg)
	r.o.progress(p, "warn", msg)
}

// ctxCheck aborts cleanly when the context was cancelled at a phase
// boundary before the P5 commit.
func (r *run) ctxCheck(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return r.abort(ctx, pderr.Wrap(pderr.Declined, "interrupted", err))
	}
	return nil
}

func (r *run) release() {
	closeReader(r.sLS)
	closeReader(r.tLS)
	r.sLS, r.tLS = nil, nil
	if r.lock != nil {
		_ = r.lock.Release()
		r.lock = nil
	}
}

// sideS and sideT describe both sides for planning.
func (r *run) sideS() sideRef {
	return sideRef{Alias: r.S.Profile.Alias, Replica: r.S.Replica, Folder: r.sPath, LS: r.sLS,
		BaseValid: r.S.BaseValid, Managed: r.S.Profile.ManagedByOrg}
}

func (r *run) sideT() sideRef {
	ls := r.tLS
	if r.kind == state.RunSync {
		ls = r.sLS
	}
	return sideRef{Alias: r.T.Profile.Alias, Replica: r.T.Replica, Folder: r.tPath, LS: ls,
		BaseValid: r.T.BaseValid, Managed: r.T.Profile.ManagedByOrg}
}

func (r *run) swapOnly() bool { return r.decision.Mode == compat.ModeSwapOnly }

// openRun opens the run record in memory (committed by the next Begin).
func (r *run) openRun() {
	if r.root.Run != nil {
		return
	}
	r.root.Run = &state.Run{
		ID: r.res.RunID, Kind: r.kind, From: r.S.Profile.Alias, To: r.T.Profile.Alias,
		Started: r.o.now(), ConfirmedPlan: r.opts.ConfirmedPlan,
	}
}

// abort closes the open run after a failure before P8 and relaunches S
// when paritydesk quit the app (I4). It runs on an uncancellable context.
func (r *run) abort(ctx context.Context, cause error) error {
	cctx, cancel := uncancel(ctx)
	defer cancel()
	if errors.Is(cause, ErrCrashInjected) || r.root == nil || r.root.Run == nil {
		return cause
	}
	quitByUs := r.root.Run.QuitByUs
	r.root.Run = nil
	if err := r.o.commit(r.root); err != nil {
		return errors.Join(cause, err)
	}
	if quitByUs {
		r.o.relaunch(cctx, r.root, "relaunched "+string(r.S.Profile.Alias))
	}
	return cause
}

// relaunch launches the app if Claude/ holds a registered, identified
// profile (I4; the caller checked QuitByUs).
func (o *Orchestrator) relaunch(ctx context.Context, root *state.Root, note string) bool {
	if !o.claudeRegistered(ctx, root) {
		o.progress(P11Launch, "warn", "Claude was not relaunched: the active folder holds no registered profile")
		return false
	}
	if err := o.d.App.Launch(ctx, o.d.Config.App.BundleID); err != nil {
		o.progress(P11Launch, "warn", "Claude could not be launched; open it manually")
		return false
	}
	o.progress(P11Launch, "ok", note)
	return true
}

// claudeRegistered reports whether Claude/ holds an identified account
// that belongs to a registered profile.
func (o *Orchestrator) claudeRegistered(ctx context.Context, root *state.Root) bool {
	folders, err := o.d.Folders.Scan(ctx)
	if err != nil {
		return false
	}
	for _, f := range folders {
		if f.Name == ids.DefaultFolder && f.Identity != nil {
			return aliasOf(root, f.Identity.Account) != ""
		}
	}
	return false
}

// aliasOf returns the registered profile whose recorded account is a.
func aliasOf(root *state.Root, a ids.AccountID) ids.Alias {
	if a == "" || root == nil {
		return ""
	}
	for alias, ps := range root.Profiles {
		if ps != nil && ps.Replica.Account == a {
			return alias
		}
	}
	return ""
}

// preflight is P0. With lock set it takes the run lock and runs every
// check; without it (preview and the interactive parent of a detached
// run) it is read-only.
func (r *run) preflight(ctx context.Context, lock bool) error {
	defer r.phase(P0Preflight, "checking")()
	d := r.d()
	if lock {
		wait := time.Duration(0)
		if r.opts.FromRunner {
			wait = DetachedLockWait
		}
		l, err := d.State.Lock(ctx, wait)
		if err != nil {
			return err
		}
		r.lock, r.locked = l, true
	}
	root, err := r.o.loadRoot()
	if err != nil {
		return err
	}
	r.root = root
	if root.Run != nil {
		return pderr.New(pderr.NeedsRecover, "preflight", "a previous run was interrupted; run paritydesk recover")
	}
	if err := r.o.checkBinding(root); err != nil {
		return err
	}
	if root.PendingAdd != nil {
		return pderr.New(pderr.NeedsRecover, "preflight", "a profile add is waiting for its sign-in; run paritydesk recover")
	}
	if err := r.locate(ctx); err != nil {
		return err
	}
	if err := r.checkUpdate(ctx, lock); err != nil {
		return err
	}
	if err := r.resolve(ctx); err != nil {
		return r.abort(ctx, err)
	}
	if err := r.gate(ctx); err != nil {
		return r.abort(ctx, err)
	}
	if !lock {
		return nil
	}
	if err := r.lockedChecks(ctx); err != nil {
		return r.abort(ctx, err)
	}
	return nil
}

// locate reads the bundle identity and selects the manifest.
func (r *run) locate(ctx context.Context) error {
	d := r.d()
	b, err := d.App.Locate(ctx, d.Config.App.BundleID)
	if err != nil {
		return pderr.Wrap(pderr.CompatRefused, "locate", err)
	}
	r.bundle = b
	m, tested, err := d.Compat.Manifest(ctx, b.Version)
	if err != nil {
		m = nil
	}
	r.manifest, r.tested = m, tested
	r.sig = compat.Signals{}
	if m != nil {
		r.sig = m.Signals
	}
	return nil
}

// checkUpdate handles a staged update: the switch lets it install, then
// re-checks the new version before syncing.
func (r *run) checkUpdate(ctx context.Context, lock bool) error {
	d := r.d()
	staged, err := d.App.UpdateStaged(ctx, d.Config.App.BundleID)
	if err != nil {
		r.warn(P0Preflight, "could not check for a staged Claude update")
		return nil
	}
	if !staged {
		return nil
	}
	if r.previewOnly {
		r.warn(P0Preflight, "a Claude update is staged; it will install when Claude quits")
		return nil
	}
	if !lock {
		r.warn(P0Preflight, "a Claude update is staged; it will install when Claude quits")
		return nil
	}
	return r.updateQuit(ctx)
}

// updateQuit lets a staged update install: the quit
// intent with QuitByUs is committed first, then the busy wait, the quit,
// the install wait and a new identity baseline. The probes run afterwards
// on the new version; P3 quits again if the updater relaunched the app.
func (r *run) updateQuit(ctx context.Context) error {
	d := r.d()
	// The run record needs both aliases; resolve them first.
	if err := r.resolve(ctx); err != nil {
		return err
	}
	procs, err := d.App.Running(ctx, r.bundle.Path)
	if err != nil {
		return pderr.Wrap(pderr.Internal, "preflight", err)
	}
	r.openRun()
	r.root.Run.QuitByUs = len(procs) > 0
	if err := r.o.begin(r.root, P0Preflight, state.IntentQuit, map[string]string{"reason": "update"}); err != nil {
		return err
	}
	if len(procs) > 0 {
		if err := r.waitBusy(ctx); err != nil {
			r.root.Run.QuitByUs = false
			return r.abort(ctx, err)
		}
		if err := r.sendQuit(ctx); err != nil {
			return err
		}
	}
	if err := r.o.done(r.root, P0Preflight, state.IntentQuit); err != nil {
		return err
	}
	r.o.progress(P0Preflight, "start", "waiting for the Claude update to install")
	if err := d.App.WaitUpdateInstalled(ctx, d.Config.App.BundleID, updateTimeout); err != nil {
		return r.abort(ctx, pderr.Wrap(pderr.UpdatePending, "update", err))
	}
	if err := r.locate(ctx); err != nil {
		return r.abort(ctx, err)
	}
	// The updater may have relaunched the app: its start time is new.
	r.sinceDone = false
	r.o.progress(P0Preflight, "ok", "Claude updated to "+r.bundle.Version)
	return nil
}

// resolve scans folders, resolves profiles and picks S and T.
func (r *run) resolve(ctx context.Context) error {
	if r.resn != nil {
		return nil
	}
	d := r.d()
	folders, err := d.Folders.Scan(ctx)
	if err != nil {
		return pderr.Wrap(pderr.Internal, "scan", err)
	}
	r.folders = folders
	var hints profile.Hints
	// main.log is shared by every account: the org hint counts only when
	// its line names the account the active folder holds.
	var acct ids.AccountID
	for _, f := range folders {
		if f.Name == ids.DefaultFolder && f.Identity != nil {
			acct = f.Identity.Account
		}
	}
	if org, found, err := d.App.ActiveOrg(ctx, r.sig, acct, r.appSince(ctx)); err == nil && found {
		hints.ActiveOrg = org
	}
	res, err := d.Resolve(d.Config, r.root, folders, hints)
	if err != nil {
		return err
	}
	r.loadAccountChanges(ctx)
	if err := r.checkInFolderSignIn(res); err != nil {
		return err
	}
	r.resn = res
	if res.Active == nil || res.Active.Folder == nil {
		return pderr.New(pderr.IdentityMismatch, "resolve", "the active folder holds no registered profile")
	}
	r.S = res.Active
	r.sPath = res.Active.Folder.Path
	r.res.From = r.S.Profile.Alias
	if r.kind != state.RunSwitch {
		r.T, r.tPath = r.S, r.sPath
		r.res.To = r.S.Profile.Alias
		return nil
	}
	return r.pickTarget()
}

// pickTarget resolves the entering profile.
func (r *run) pickTarget() error {
	to := r.opts.To
	if to == "" || to == "-" {
		to = r.root.Previous
	}
	if to == "" {
		// No previous profile (before the first switch): the only other
		// profile is the target; with more, the person names one.
		var others []string
		for a, b := range r.resn.Bindings {
			if b != nil && a != r.S.Profile.Alias {
				others = append(others, string(a))
			}
		}
		sort.Strings(others)
		switch len(others) {
		case 0:
			return pderr.New(pderr.Usage, "switch", "no other profile to switch to; register one with paritydesk profile add or profile adopt")
		case 1:
			to = ids.Alias(others[0])
		default:
			return pderr.New(pderr.Usage, "switch", "no previous profile; name the profile to switch to: "+strings.Join(others, ", "))
		}
	}
	t, ok := r.resn.Bindings[to]
	if !ok || t == nil {
		return pderr.New(pderr.Usage, "switch", "unknown profile "+string(to))
	}
	if t.Profile.Alias == r.S.Profile.Alias {
		return pderr.New(pderr.Usage, "switch", string(to)+" is already active; use paritydesk sync to re-apply")
	}
	if t.Folder == nil {
		return pderr.New(pderr.IdentityMismatch, "switch", "the folder of "+string(to)+" is missing")
	}
	r.T, r.tPath = t, t.Folder.Path
	r.res.To = to
	return nil
}

// gate runs the probes and the format check, and decides the mode.
func (r *run) gate(ctx context.Context) error {
	d := r.d()
	var err error
	if r.sLS == nil {
		if r.sLS, err = r.o.lsReader(ctx, r.sPath); err != nil {
			return err
		}
	}
	if r.tLS == nil && r.kind != state.RunSync {
		if r.tLS, err = r.o.lsReader(ctx, r.tPath); err != nil {
			return err
		}
	}
	in := compat.DecideInput{
		Manifest: r.manifest, Version: r.bundle.Version, Tested: r.tested,
		RequireTested: d.Config.App.RequireTested, IdentityOK: r.S.Folder.Identity != nil,
		CompressionOK: true, OrgUnknown: r.S.Replica.Org == "" || r.T.Replica.Org == "",
	}
	if r.manifest != nil {
		probes, err := r.probes(ctx)
		if err != nil {
			return err
		}
		in.Probes = probes
		ok, err := r.formatOK(ctx)
		if err != nil {
			return err
		}
		in.CompressionOK = ok
	}
	_, chain := profile.NativeMultiFromLog(r.changes)
	in.NativeMultiAccount = r.nativeMultiAlias() != "" || chain >= 2
	for _, w := range accountChangeWarnings(r.changes) {
		r.warn(P0Preflight, w)
	}
	in.Downgraded = downgraded(r.root.LastAppVersion, r.bundle.Version)
	in.SQLiteLS = sqliteLS(r.sPath) || (r.kind != state.RunSync && sqliteLS(r.tPath))
	r.decision = d.Compat.Decide(in)
	r.res.Mode = r.decision.Mode
	switch r.decision.Mode {
	case compat.ModeRefuse:
		return pderr.New(pderr.CompatRefused, "compat", "this Claude version or folder layout cannot be handled safely")
	case compat.ModePreviewOnly:
		if r.locked {
			return pderr.New(pderr.CompatRefused, "compat", "this Claude version is untested and require_tested is set; only plan works")
		}
	case compat.ModeSwapOnly:
		warning, syncErr, known := r.nativeCause()
		if r.kind == state.RunSync {
			if known {
				return pderr.New(pderr.SwapOnly, "compat", syncErr)
			}
			return pderr.New(pderr.SwapOnly, "compat", "sync is not possible in swap-only mode")
		}
		if !known {
			warning = "swap-only mode: the switch will not carry anything"
		}
		r.warn(P0Preflight, warning)
	case compat.ModeDegraded:
		r.warn(P0Preflight, "degraded mode: some rows are skipped")
	}
	r.rows, r.skipped = r.o.selectRows(r.manifest, r.decision, r.opts.Only, r.opts.Skip)
	if r.swapOnly() {
		r.rows = nil
	}
	return nil
}

func (r *run) probes(ctx context.Context) ([]compat.ProbeResult, error) {
	d := r.d()
	out, err := d.Compat.Probes(ctx, r.manifest, compat.ProbeEnv{Folder: r.sPath, Replica: r.S.Replica, LS: r.sLS})
	if err != nil {
		return nil, pderr.Wrap(pderr.Internal, "probes", err)
	}
	if r.kind == state.RunSync {
		return out, nil
	}
	tp, err := d.Compat.Probes(ctx, r.manifest, compat.ProbeEnv{Folder: r.tPath, Replica: r.T.Replica, LS: r.tLS})
	if err != nil {
		return nil, pderr.Wrap(pderr.Internal, "probes", err)
	}
	return append(out, tp...), nil
}

// formatOK checks the block compression of both Local Storage copies.
func (r *run) formatOK(ctx context.Context) (bool, error) {
	paths := []string{r.sPath}
	if r.kind != state.RunSync {
		paths = append(paths, r.tPath)
	}
	for _, p := range paths {
		dir := sources.LevelDBPath(p)
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		if err := r.d().KV.CheckFormat(ctx, dir); err != nil {
			if errors.Is(err, kv.ErrUnknownCompression) {
				return false, nil
			}
			return false, pderr.Wrap(pderr.Internal, "format check", err)
		}
	}
	return true, nil
}

// downgraded reports whether the installed app version is older than the
// one the last committed run used.
func downgraded(last, now string) bool {
	return last != "" && now != "" && compat.CompareVersions(now, last) < 0
}

// sqliteLS reports whether a data folder has Chromium's SQLite Local
// Storage backend (<partition>/LocalStorage, next to the LevelDB
// "Local Storage"), which paritydesk cannot read; every Local Storage row
// is then refused (swap-only).
func sqliteLS(folder string) bool {
	if folder == "" {
		return false
	}
	_, err := os.Lstat(filepath.Join(folder, sqliteLSName))
	return err == nil
}

// sqliteLSName is the SQLite Local Storage entry of a data folder.
const sqliteLSName = "LocalStorage"

// nativeMultiAlias returns the profile whose folder shows signs of the
// app's own multi-account mode in its recorded account history, or "".
func (r *run) nativeMultiAlias() ids.Alias {
	for _, b := range []*profile.Binding{r.S, r.T} {
		if b == nil || b.Folder == nil || b.Folder.Identity == nil {
			continue
		}
		if profile.NativeMultiAccount(r.root.Profiles[b.Profile.Alias], b.Folder.Identity.Account) {
			return b.Profile.Alias
		}
	}
	return ""
}

// lockedChecks are the P0 checks that only matter for a run that will
// quit the app: other tools, free space, Automation, an outside switch
// and the busy list.
func (r *run) lockedChecks(ctx context.Context) error {
	d := r.d()
	if r.manifest != nil {
		if tools := r.o.foreignTools(r.manifest.Footprints); len(tools) > 0 {
			return pderr.New(pderr.ForeignTool, "preflight", "another account-switching tool was detected: "+tools[0])
		}
	}
	if r.kind == state.RunSwitch {
		if err := r.checkParkFree(); err != nil {
			return err
		}
	}
	if !r.swapOnly() {
		need := uint64(freeSpaceFactor*dirSize(sources.LevelDBPath(r.tPath))) + minFreeBytes
		free, err := d.FS.FreeBytes(d.DataRoot)
		if err == nil && free < need {
			return pderr.New(pderr.Internal, "preflight", "not enough free disk space for the backup generation")
		}
	}
	procs, err := d.App.Running(ctx, r.bundle.Path)
	if err != nil {
		return pderr.Wrap(pderr.Internal, "preflight", err)
	}
	if len(procs) > 0 {
		switch err := d.App.ProbeAutomation(ctx, d.Config.App.BundleID); {
		case errors.Is(err, app.ErrAutomationDenied):
			return pderr.New(pderr.AutomationMissing, "preflight", "Automation permission for Claude is missing")
		case errors.Is(err, app.ErrEventTimedOut):
			// -1712: Claude is busy, or macOS shows an Automation prompt
			// nobody answered. The quit can still work; say what to check.
			r.warn(P0Preflight, "Claude did not answer an Apple Event in time (AppleScript error -1712); if macOS asks whether paritydesk may control Claude, allow it")
		}
	}
	if r.resn.OutsideSwitch && r.kind == state.RunSwitch && !r.opts.Yes && r.opts.ConfirmedPlan == "" {
		c := Confirmation{Kind: ConfirmOutsideSwitch,
			Lines: []string{"The active account changed outside paritydesk. paritydesk sync can re-apply the shared state; continue with the switch?"}}
		if err := r.o.ask(ctx, r.interactive, c); err != nil {
			return err
		}
	}
	r.resolveInvoking(ctx)
	if len(procs) == 0 {
		return nil
	}
	return r.checkBusyP0(ctx)
}

// checkParkFree refuses, before anything is quit, a switch whose leaving
// profile cannot be parked: its parked folder name (or a case variant of
// it) is already taken. P9 would otherwise fail after the swap.
func (r *run) checkParkFree() error {
	name := r.S.Profile.ParkedFolder()
	p := r.o.folderPath(name)
	taken := false
	if _, err := os.Lstat(p); err == nil && p != r.tPath {
		taken = true
	}
	for _, f := range r.folders {
		if strings.EqualFold(f.Name, name) && f.Path != r.tPath {
			taken = true
		}
	}
	if !taken {
		return nil
	}
	return pderr.New(pderr.IdentityMismatch, "preflight", fmt.Sprintf(
		"%s already exists, so %s cannot be parked there; nothing was changed; see paritydesk doctor", name, r.S.Profile.Alias))
}

// resolveInvoking finds the local session of the invoking Code session.
func (r *run) resolveInvoking(ctx context.Context) {
	if r.opts.ReopenCLISession == "" || r.invoking != "" {
		return
	}
	id, err := r.d().Sessions.ResolveByCLISession(ctx, r.sPath, r.S.Replica, r.opts.ReopenCLISession)
	if err != nil || id == "" {
		r.warn(P0Preflight, "the invoking session was not found; it will not be reopened")
		return
	}
	r.invoking = id
}

// preview is P2: the approximate plan from snapshots.
func (r *run) preview(ctx context.Context) error {
	defer r.phase(P2Preview, "planning")()
	if r.swapOnly() {
		r.p2 = &planned{Plan: r.o.emptyPlan(r.res.RunID, r.sideS(), r.sideT(), r.skipped), Work: map[model.RowID]*rowWork{}}
	} else {
		pl, err := r.o.buildPlan(ctx, planInput{S: r.sideS(), T: r.sideT(), Manifest: r.manifest, Root: r.root,
			Rows: r.rows, Skipped: r.skipped, RunID: r.res.RunID, Approximate: true, Preview: r.confirmedPreview(), Seeded: r.seeded()})
		if err != nil {
			return err
		}
		r.p2 = pl
	}
	r.setPlan(r.p2.Plan)
	return nil
}

func (r *run) setPlan(p *plan.Plan) {
	s := p.Summarize()
	r.res.Plan, r.res.Summary = p, &s
	r.res.PlanHash, r.res.DestructiveDigest = p.Hash(), p.DestructiveDigest()
}

// confirmP2 checks a confirmed plan or asks for confirmation.
func (r *run) confirmP2(ctx context.Context) error {
	d := r.d()
	p := r.p2.Plan
	if r.opts.ConfirmedPlan != "" {
		confirmed, err := d.Plans.Load(r.opts.ConfirmedPlan, r.o.now())
		if err != nil {
			if pderr.CodeOf(err) != pderr.Declined {
				err = pderr.Wrap(pderr.Declined, "confirmed plan", err)
			}
			return err
		}
		if nd := plan.NewDestructive(confirmed, p); len(nd) > 0 {
			return pderr.New(pderr.Declined, "confirmed plan", fmt.Sprintf("the plan now deletes %d item(s) the confirmed plan did not; run paritydesk plan again", len(nd)))
		}
		// A confirmed plan that showed the first-sync line also confirmed
		// the union (NeedsConfirmation is part of its hash).
		if p.NeedsConfirmation && !confirmed.NeedsConfirmation && !r.opts.ConfirmFirstSync {
			return pderr.New(pderr.BaseInvalid, "confirmed plan", "the confirmed plan was not a first sync, but this run is; run paritydesk plan again")
		}
		r.confirmedByPerson = true
		return nil
	}
	if !r.opts.Yes {
		c := Confirmation{Kind: ConfirmPlan, Plan: p, Summary: r.res.Summary}
		if err := r.o.ask(ctx, r.interactive, c); err != nil {
			return err
		}
		r.confirmedByPerson = true
		// The plan just confirmed showed the first-sync line, so the
		// person already agreed to the union: one question is enough.
		if p.NeedsConfirmation {
			r.opts.ConfirmFirstSync = true
		}
	}
	if p.NeedsConfirmation && !r.opts.ConfirmFirstSync {
		if !r.interactive {
			return pderr.New(pderr.BaseInvalid, "plan", "a first-sync union needs confirmation")
		}
		c := Confirmation{Kind: ConfirmBaseInvalid, Plan: p, Summary: r.res.Summary}
		if err := r.o.ask(ctx, true, c); err != nil {
			return err
		}
		r.opts.ConfirmFirstSync = true
	}
	return nil
}

// quitApp is P3.
func (r *run) quitApp(ctx context.Context) error {
	defer r.phase(P3Quit, "quitting Claude")()
	d := r.d()
	if r.opts.FromRunner && r.invoking != "" {
		from := app.LogCursor{Ino: r.opts.LogInode, Off: max(r.opts.LogOffset, 0)}
		err := d.App.WaitTurnEnded(ctx, from, r.sig, r.invoking, r.o.quitTimeout())
		if err != nil && !errors.Is(err, app.ErrSignalUnknown) {
			if ctx.Err() != nil {
				return r.abort(ctx, pderr.Wrap(pderr.Declined, "interrupted", err))
			}
			r.warn(P3Quit, "the invoking session's turn did not end in time; Claude may ask before quitting")
		}
	}
	procs, err := d.App.Running(ctx, r.bundle.Path)
	if err != nil {
		return r.abort(ctx, pderr.Wrap(pderr.Internal, "quit", err))
	}
	if len(procs) > 0 {
		if err := r.waitBusy(ctx); err != nil {
			return r.abort(ctx, err)
		}
	}
	r.openRun()
	if len(procs) > 0 {
		r.root.Run.QuitByUs = true
	}
	if err := r.o.begin(r.root, P3Quit, state.IntentQuit, nil); err != nil {
		return err
	}
	if len(procs) > 0 {
		if err := r.sendQuit(ctx); err != nil {
			return err
		}
	}
	return r.o.done(r.root, P3Quit, state.IntentQuit)
}

// sendQuit sends the quit Apple Event and handles every outcome short of
// a clean exit by closing the run. Only missing Automation (AppleScript
// error -1743 or -1744, app.ErrAutomationDenied) exits 15. A quit the app
// vetoed, refused or left without a reply, and any other failure of the
// quit event, exit 10, with the underlying error kept in the chain and
// its AppleScript error number named in the message. After a veto, a
// refusal or a missing reply the app may still quit later, so the
// late-quit watch runs and relaunches the leaving profile if it does.
func (r *run) sendQuit(ctx context.Context) error {
	d := r.d()
	onVeto := func() {
		r.o.progress(P3Quit, "warn", "Claude asks whether to quit while work is running; choose Wait for Claude to let the switch continue, or Cancel")
	}
	qr, err := d.App.QuitAndWait(ctx, d.Config.App.BundleID, r.bundle.Path, r.sig, r.o.quitTimeout(), onVeto)
	if err == nil && qr.Exited {
		return nil
	}
	cctx, cancel := uncancel(ctx)
	defer cancel()
	switch {
	case errors.Is(err, app.ErrAutomationDenied):
		r.root.Run.QuitByUs = false
		return r.abort(cctx, pderr.Wrap(pderr.AutomationMissing, "quit", err))
	case qr.Pending:
		r.o.progress(P3Quit, "start", "Claude did not quit in time; watching for a late quit")
		exited, werr := d.App.WaitExited(cctx, r.bundle.Path, d.Config.App.LateQuitWatch.Duration)
		if werr == nil && exited {
			return r.abort(cctx, &pderr.Error{Code: pderr.AppDidNotQuit, Op: "quit",
				Msg: "Claude quit late; relaunched " + string(r.S.Profile.Alias), Err: err})
		}
		r.root.Run.QuitByUs = false
		if qr.Vetoed {
			r.res.QuitPending = true
			return r.abort(cctx, &pderr.Error{Code: pderr.AppDidNotQuit, Op: "quit",
				Msg: "Claude is still asking whether to quit; your answer there decides", Err: err})
		}
		return r.abort(cctx, &pderr.Error{Code: pderr.AppDidNotQuit, Op: "quit",
			Msg: quitFailure(err) + "; it is still running and nothing was changed", Err: err})
	}
	if procs, perr := d.App.Running(cctx, r.bundle.Path); perr == nil && len(procs) > 0 {
		r.root.Run.QuitByUs = false
	}
	if ctx.Err() != nil {
		return r.abort(cctx, pderr.Wrap(pderr.Declined, "interrupted", ctx.Err()))
	}
	// The rest is exit 10: a timeout without a veto, or a quit event that
	// failed before the app could answer (-10004, no number). Claude is
	// relaunched only if it is gone after all; the raw error stays in the
	// chain.
	return r.abort(cctx, &pderr.Error{Code: pderr.AppDidNotQuit, Op: "quit", Msg: quitFailure(err) + "; nothing was changed", Err: err})
}

// quitFailure describes a failed quit for the exit 10 message, naming the
// AppleScript error number when there is one, and what to do for the
// numbers that point outside Claude: -1712 can be an unanswered
// Automation prompt, -10004 a sandboxed paritydesk.
func quitFailure(err error) string {
	var ce *app.CommandError
	status := 0
	if errors.As(err, &ce) {
		status = ce.OSAStatus
	}
	num := ""
	if status != 0 {
		num = fmt.Sprintf(" (AppleScript error %d)", status)
	}
	switch {
	case errors.Is(err, app.ErrQuitRefused):
		return "Claude refused to quit" + num
	case errors.Is(err, app.ErrEventTimedOut):
		return "Claude did not answer the quit event in time" + num +
			"; if macOS asked whether paritydesk may control Claude, allow it (paritydesk doctor --grant-automation)"
	case status == -10004:
		return "macOS refused to send the quit event to Claude" + num +
			", as it does for a sandboxed process; run paritydesk outside any sandbox, for example from Terminal"
	case errors.Is(err, app.ErrQuitNotDelivered):
		return "the quit event to Claude failed" + num
	}
	return "Claude did not quit" + num
}

// quiesce is P4 (and the P8 pre-swap check): wait until every quiescence
// signal is clear for folders.
func (r *run) quiesce(ctx context.Context, folders ...string) (app.QuiescenceReport, error) {
	d := r.d()
	in := r.o.quiescenceInput(r.bundle.Path, folders...)
	deadline := r.o.now().Add(quiesceTimeout)
	for {
		rep, err := d.App.CheckQuiescence(ctx, in)
		if err != nil {
			return rep, pderr.Wrap(pderr.Internal, "quiescence", err)
		}
		if rep.Quiet {
			for range rep.ReadOnlyHolders {
				r.warn(P4Quiesce, "a read-only process (indexing or backup) has a file open; continuing")
				break
			}
			return rep, nil
		}
		if !r.o.now().Before(deadline) {
			return rep, pderr.New(pderr.AppDidNotQuit, "quiescence", "files in the data folders are still in use")
		}
		if err := d.Clock.Sleep(ctx, quiescePoll); err != nil {
			return rep, pderr.Wrap(pderr.Declined, "interrupted", err)
		}
	}
}

// quiescenceInput is the quiescence check of folders and their LevelDB
// LOCK files. An empty bundlePath leaves out app processes found by
// bundle path (and the updater), so only processes that use the folders
// count.
func (o *Orchestrator) quiescenceInput(bundlePath string, folders ...string) app.QuiescenceInput {
	in := app.QuiescenceInput{BundlePath: bundlePath, DataRoot: o.d.DataRoot, Folders: folders}
	if bundlePath != "" {
		in.BundleID = o.d.Config.App.BundleID
	}
	for _, f := range folders {
		in.LockFiles = append(in.LockFiles, filepath.Join(sources.LevelDBPath(f), "LOCK"))
	}
	return in
}

// folderInUse reports whether restoring folder now could race a process:
// a held LevelDB LOCK, a write holder, or an app using it by
// --user-data-dir. For Claude/ (live) any app or updater process counts
// too, since the app opens Claude/ when it starts.
func (o *Orchestrator) folderInUse(ctx context.Context, bundlePath, folder string, live bool) (bool, error) {
	if !live {
		bundlePath = ""
	}
	rep, err := o.d.App.CheckQuiescence(ctx, o.quiescenceInput(bundlePath, folder))
	if err != nil {
		return true, err
	}
	if live {
		return !rep.Quiet, nil
	}
	return len(rep.AppPIDs)+len(rep.LocksHeld)+len(rep.WriteHolders) > 0, nil
}

// p4 waits for quiescence after the quit.
func (r *run) p4(ctx context.Context) error {
	defer r.phase(P4Quiesce, "waiting for files to close")()
	folders := []string{r.sPath}
	if r.kind == state.RunSwitch {
		folders = append(folders, r.tPath)
	}
	if _, err := r.quiesce(ctx, folders...); err != nil {
		return r.abort(ctx, err)
	}
	r.t4 = r.o.now()
	return nil
}

// sameBundle compares two bundle identities.
func sameBundle(a, b app.BundleIdentity) bool {
	return a.BundleID == b.BundleID && a.Path == b.Path && a.Version == b.Version && a.Build == b.Build &&
		a.ExecIno == b.ExecIno && a.ExecMtime.Equal(b.ExecMtime) && a.ExecSize == b.ExecSize
}
