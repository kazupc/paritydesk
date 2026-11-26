package orchestrator

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"github.com/kazupc/paritydesk/internal/app"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/profile"
	"github.com/kazupc/paritydesk/internal/state"
)

// RecoverOptions are the flags of `recover`.
type RecoverOptions struct {
	Rollback bool // undo an interrupted run instead of completing it
	Yes      bool
}

// RecoverResult reports what recover did.
type RecoverResult struct {
	Actions []i18n.Text `json:"actions"`
	Active  ids.Alias   `json:"active"`
}

// recovery is one recover run.
type recovery struct {
	o        *Orchestrator
	opts     RecoverOptions
	root     *state.Root
	res      *RecoverResult
	bundle   app.BundleIdentity
	located  bool
	manifest *compat.Manifest
}

// facts are the folders found by a scan, mapped to registered profiles.
type facts struct {
	folders     []profile.Folder
	claude      *profile.Folder
	claudeAlias ids.Alias
}

func (f facts) find(name string) *profile.Folder {
	for i := range f.folders {
		if f.folders[i].Name == name {
			return &f.folders[i]
		}
	}
	return nil
}

// Recover reconciles an interrupted run from state intents and folder
// identities, or, without a journal, from identities alone. It never
// creates, merges or deletes data folders automatically.
func (o *Orchestrator) Recover(ctx context.Context, opts RecoverOptions) (*RecoverResult, error) {
	if o.d.ReadOnly {
		return nil, pderr.NewT(pderr.Usage, "recover", i18n.ErrRecoverReadOnly)
	}
	l, err := o.d.State.Lock(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer releaseLock(l)
	cctx, cancel := uncancel(ctx)
	defer cancel()
	rc := &recovery{o: o, opts: opts, res: &RecoverResult{}}
	root, err := o.d.State.Load()
	if err != nil || root == nil {
		return rc.res, rc.withoutJournal(cctx)
	}
	ensureMaps(root)
	if err := o.checkBinding(root); err != nil {
		return rc.res, err
	}
	rc.root = root
	rc.locate(cctx)
	rc.confirmOnline(cctx)
	err = rc.dispatch(cctx)
	rc.cleanupOnline(cctx)
	rc.res.Active = rc.root.Active
	return rc.res, err
}

func (rc *recovery) note(t i18n.Text) { rc.res.Actions = append(rc.res.Actions, t) }

func (rc *recovery) locate(ctx context.Context) {
	d := rc.o.d
	b, err := d.App.Locate(ctx, d.Config.App.BundleID)
	if err != nil {
		return
	}
	rc.bundle, rc.located = b, true
	if m, _, err := d.Compat.Manifest(ctx, b.Version); err == nil {
		rc.manifest = m
	}
}

func (rc *recovery) dispatch(ctx context.Context) error {
	run := rc.root.Run
	if run == nil {
		if rc.root.PendingAdd != nil {
			return rc.pendingAdd(ctx)
		}
		rc.note(i18n.Tx(i18n.RecoverNothing))
		return nil
	}
	switch run.Kind {
	case state.RunSwitch:
		return rc.recoverSwitch(ctx)
	case state.RunSync:
		return rc.recoverSync(ctx)
	case state.RunRollback:
		return rc.recoverRollback(ctx)
	case state.RunAdd:
		return rc.recoverAdd(ctx)
	case state.RunQuarantine:
		return rc.quarantine(ctx)
	}
	return pderr.Bug("recover", "unknown run kind")
}

// lastIntent returns the last intent's name and done flag.
func lastIntent(run *state.Run) (state.IntentName, bool) {
	if l := run.Last(); l != nil {
		return l.Name, l.Done
	}
	return "", false
}

// intentData returns the data of the last intent with that name.
func intentData(run *state.Run, name state.IntentName) (map[string]string, bool) {
	for i := len(run.Intents) - 1; i >= 0; i-- {
		if run.Intents[i].Name == name {
			return run.Intents[i].Data, true
		}
	}
	return nil, false
}

func (rc *recovery) scan(ctx context.Context) (facts, error) {
	folders, err := rc.o.d.Folders.Scan(ctx)
	if err != nil {
		return facts{}, pderr.Wrap(pderr.Internal, "recover", err)
	}
	f := facts{folders: folders}
	if c := f.find(ids.DefaultFolder); c != nil {
		f.claude = c
		if c.Identity != nil {
			f.claudeAlias = aliasOf(rc.root, c.Identity.Account)
		}
	}
	return f, nil
}

// holds reports whether folder name exists and holds alias's account.
func (rc *recovery) holds(f facts, name string, a ids.Alias) bool {
	x := f.find(name)
	return x != nil && x.Identity != nil && aliasOf(rc.root, x.Identity.Account) == a
}

// parked returns a profile's parked folder name.
func (rc *recovery) parked(a ids.Alias) string {
	if p, ok := rc.o.d.Config.Profile(a); ok {
		return p.ParkedFolder()
	}
	return ids.DefaultParkedFolder(a)
}

// requireStopped refuses folder changes while the app may run: the bundle
// must be found, no app process may run, and every folder recover may
// touch (Claude/ and the profiles' parked folders) must be quiet: no app
// or updater process, no held LevelDB LOCK, no write holder.
func (rc *recovery) requireStopped(ctx context.Context) error {
	if !rc.located {
		return pderr.NewT(pderr.NeedsRecover, "recover", i18n.ErrRecoverAppNotFound)
	}
	procs, err := rc.o.d.App.Running(ctx, rc.bundle.Path)
	if err != nil {
		return pderr.Wrap(pderr.Internal, "recover", err)
	}
	if len(procs) > 0 {
		return pderr.NewT(pderr.NeedsRecover, "recover", i18n.ErrRecoverQuitFirst)
	}
	in := rc.o.quiescenceInput(rc.bundle.Path, rc.touchedFolders()...)
	rep, err := rc.o.d.App.CheckQuiescence(ctx, in)
	if err != nil {
		return pderr.Wrap(pderr.Internal, "recover", err)
	}
	if !rep.Quiet {
		return pderr.NewT(pderr.NeedsRecover, "recover", i18n.ErrRecoverInUse)
	}
	return nil
}

// touchedFolders lists Claude/ and the parked folder of every profile the
// config or the open run names.
func (rc *recovery) touchedFolders() []string {
	out := []string{rc.o.claudePath()}
	seen := map[string]bool{out[0]: true}
	add := func(a ids.Alias) {
		if p := rc.o.folderPath(rc.parked(a)); a != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, p := range rc.o.d.Config.Profiles {
		add(p.Alias)
	}
	if rc.root != nil && rc.root.Run != nil {
		add(rc.root.Run.From)
		add(rc.root.Run.To)
	}
	return out
}

// closeRun closes the run and relaunches when paritydesk quit the app.
func (rc *recovery) closeRun(ctx context.Context, note i18n.Text) error {
	quit := rc.root.Run != nil && rc.root.Run.QuitByUs
	rc.root.Run = nil
	if err := rc.o.commit(rc.root); err != nil {
		return err
	}
	rc.note(note)
	if quit && rc.located {
		if rc.o.relaunch(ctx, rc.root, i18n.Tx(i18n.ProgressP11RelaunchedClaude)) {
			rc.note(i18n.Tx(i18n.RecoverRelaunched))
		}
	}
	return nil
}

// recoverSwitch implements the RunSwitch table.
func (rc *recovery) recoverSwitch(ctx context.Context) error {
	run := rc.root.Run
	name, done := lastIntent(run)
	switch {
	case name == "" || name == state.IntentQuit || name == state.IntentPlanned:
		return rc.closeRun(ctx, i18n.Tx(i18n.RecoverClosedSwitch))
	case name == state.IntentBackedUp && !done:
		return rc.dropIncomplete(ctx)
	}
	if err := rc.requireStopped(ctx); err != nil {
		return err
	}
	f, err := rc.scan(ctx)
	if err != nil {
		return err
	}
	s, t := run.From, run.To
	sParked, tParked := rc.parked(s), rc.parked(t)
	switch {
	case f.claudeAlias == s && name != state.IntentParked && rc.holds(f, tParked, t):
		return rc.rollbackTarget(ctx, t, rc.o.folderPath(tParked), false, i18n.Tx(i18n.RecoverRolledBack, t))
	case f.claudeAlias == t && (name == state.IntentSwapping || name == state.IntentSwapped || name == state.IntentSwapBack):
		return rc.parkAndFinish(ctx, f, tParked, sParked)
	case f.claudeAlias == t && (name == state.IntentFallbackTPlaced || name == state.IntentParked):
		if name == state.IntentParked && rc.holds(f, tParked, s) {
			return rc.parkAndFinish(ctx, f, tParked, sParked)
		}
		return rc.finishP10(ctx)
	case f.claude == nil && (name == state.IntentFallbackSParked || (name == state.IntentFallbackTPlaced && !done)) &&
		rc.holds(f, sParked, s) && rc.holds(f, tParked, t):
		return rc.completeFallback(ctx, sParked, tParked)
	}
	return rc.contradiction(ctx, f, s, t)
}

// parkAndFinish completes a switch whose swap is done: S, still at T's
// parked name, gets its own parked name (P9), then P10. When S's parked
// name is already taken, recover cannot finish the switch; with
// --rollback it undoes the switch instead.
func (rc *recovery) parkAndFinish(ctx context.Context, f facts, tParked, sParked string) error {
	if rc.nameTaken(f, sParked) {
		run := rc.root.Run
		if rc.opts.Rollback && rc.holds(f, tParked, run.From) {
			return rc.undoSwap(ctx, tParked)
		}
		return pderr.NewT(pderr.NeedsRecover, "recover", i18n.ErrRecoverParkTaken, sParked, describe(rc.root, f))
	}
	if err := rc.parkS(f, tParked, sParked); err != nil {
		return err
	}
	return rc.finishP10(ctx)
}

// nameTaken reports whether a folder name (or a case variant of it, on a
// case-sensitive volume) already exists in the data root.
func (rc *recovery) nameTaken(f facts, name string) bool {
	if _, err := os.Lstat(rc.o.folderPath(name)); err == nil {
		return true
	}
	for _, x := range f.folders {
		if strings.EqualFold(x.Name, name) {
			return true
		}
	}
	return false
}

// undoSwap swaps Claude/ (T) back with T's parked folder (S) and rolls T
// back from the generation.
func (rc *recovery) undoSwap(ctx context.Context, tParked string) error {
	t := rc.root.Run.To
	tPath := rc.o.folderPath(tParked)
	if err := rc.o.d.FS.SwapDirs(rc.o.claudePath(), tPath); err != nil {
		return pderr.Wrap(pderr.NeedsRecover, "recover", err)
	}
	_ = rc.o.d.FS.FullSync(rc.o.d.DataRoot)
	rc.note(i18n.Tx(i18n.RecoverPutBack))
	return rc.rollbackTarget(ctx, t, tPath, false, i18n.Tx(i18n.RecoverRolledBack, t))
}

// dropIncomplete handles an interrupted P6.
func (rc *recovery) dropIncomplete(ctx context.Context) error {
	gone, err := rc.o.d.Gens.DeleteIncomplete(rc.root.Run.Generation)
	if err != nil {
		return pderr.Wrap(pderr.Internal, "recover", err)
	}
	if gone {
		rc.note(i18n.Tx(i18n.RecoverRemovedGeneration))
	}
	return rc.closeRun(ctx, i18n.Tx(i18n.RecoverClosedRunUnchanged))
}

// parkS is P9 during recover.
func (rc *recovery) parkS(f facts, from, to string) error {
	d := rc.o.d
	if err := d.FS.RenameExcl(rc.o.folderPath(from), rc.o.folderPath(to)); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return pderr.NewT(pderr.NeedsRecover, "recover", i18n.ErrRecoverNameTaken, to, describe(rc.root, f))
		}
		return pderr.Wrap(pderr.NeedsRecover, "recover", err)
	}
	rc.note(i18n.Tx(i18n.RecoverMovedLeaving, to))
	return nil
}

// completeFallback finishes (or, with --rollback, undoes) an interrupted
// two-rename fallback.
func (rc *recovery) completeFallback(ctx context.Context, sParked, tParked string) error {
	d := rc.o.d
	claude := rc.o.claudePath()
	if rc.opts.Rollback {
		if err := d.FS.RenameExcl(rc.o.folderPath(sParked), claude); err != nil {
			return pderr.Wrap(pderr.NeedsRecover, "recover", err)
		}
		rc.note(i18n.Tx(i18n.RecoverPutBack))
		t := rc.root.Run.To
		return rc.rollbackTarget(ctx, t, rc.o.folderPath(tParked), false, i18n.Tx(i18n.RecoverRolledBack, t))
	}
	if err := d.FS.RenameExcl(rc.o.folderPath(tParked), claude); err != nil {
		return pderr.Wrap(pderr.NeedsRecover, "recover", err)
	}
	rc.note(i18n.Tx(i18n.RecoverMovedEntering))
	return rc.finishP10(ctx)
}

// rollbackTarget rolls a folder back from the run's generation, drops the
// bases of conflicted rows, and closes the run. live marks Claude/, the
// folder the app uses: it only gets the conditional per-entry restore,
// never the LevelDB clone, because the app may have written any Local
// Storage key there since the crash. A parked folder gets the clone back
// first when apply may have opened its Local Storage.
func (rc *recovery) rollbackTarget(ctx context.Context, a ids.Alias, folder string, live bool, note i18n.Text) error {
	run := rc.root.Run
	_, applied := intentData(run, state.IntentApplied)
	withLS := false
	if run.Generation != 0 && !live {
		if m, err := rc.o.d.Gens.Load(run.Generation); err == nil {
			withLS = applied && m.LevelDB != ""
		}
	}
	rep, err := rc.o.restoreFolder(ctx, run.Generation, folder, withLS, run.ID, scratchRootOf(rc.manifest))
	if err != nil {
		return err
	}
	if ps := rc.root.Profiles[a]; ps != nil {
		rc.o.invalidateConflicted(rc.root, a, ps.Replica, rep.Conflicts)
	}
	rc.o.restoreMarker(rc.root, a, folder)
	if len(rep.Conflicts) > 0 {
		rc.note(i18n.Tx(i18n.RecoverConflictsLeft, len(rep.Conflicts)))
	}
	return rc.closeRun(ctx, note)
}

// finishP10 completes a switch or sync whose folders are in place: T's
// bases come from a re-read only if every post-hash matches.
func (rc *recovery) finishP10(ctx context.Context) error {
	d := rc.o.d
	root := rc.root
	run := root.Run
	t := run.To
	ps := root.Profiles[t]
	if ps == nil {
		ps = &state.ProfileState{Alias: t}
		root.Profiles[t] = ps
	}
	claude := rc.o.claudePath()
	data, _ := intentData(run, state.IntentPlanned)
	rows := splitRows(data["rows"])
	ok, scratchOK := rc.recheckScratch(ctx, plan.Target{Alias: t, Replica: ps.Replica, Folder: claude}, claude)
	refs := scratchRefsFromIntent(data)
	if !scratchOK {
		// Only T's scratch base is dropped: without refs the row is not
		// re-read.
		refs = nil
	}
	var reread map[model.RowID]*model.RowSnapshot
	if ok && rc.manifest != nil {
		var err error
		pr := rc.o.pinsRereadFacts(root, t, ps.Replica)
		if reread, err = rc.o.rereadRowsPins(ctx, rc.manifest, claude, ps.Replica, rows, refs, pr); err != nil {
			ok = false
		}
	}
	for _, r := range rows {
		k := state.BaseKey{Alias: t, Replica: ps.Replica, Row: r}.String()
		snap, have := reread[r]
		if !ok || !have {
			dropBase(root, k)
			continue
		}
		ref, err := d.State.PutSnapshot(snap)
		if err != nil {
			return pderr.Wrap(pderr.Internal, "state", err)
		}
		root.Bases[k] = ref
	}
	if !ok {
		rc.note(i18n.Tx(i18n.RecoverEnteringBasesInvalid))
	}
	if _, backed := intentData(run, state.IntentBackedUp); backed && run.Generation != 0 {
		root.Generations = append(root.Generations, state.GenerationRef{N: run.Generation, RunID: run.ID, Target: t, At: rc.o.now()})
		if ok {
			ps.MarkerBase = run.PlanHash
		}
	}
	if id, err := d.FS.ID(claude); err == nil {
		ps.FolderID = id
	}
	ps.SeededFrom = ""
	if run.Kind == state.RunSwitch {
		if s := root.Profiles[run.From]; s != nil {
			s.SeededFrom = ""
			s.LiveEpoch = root.Epoch
			if id, err := d.FS.ID(rc.o.folderPath(rc.parked(run.From))); err == nil {
				s.FolderID = id
			}
		}
		root.Epoch++
		ps.LiveEpoch = root.Epoch
		root.Active, root.Previous = t, run.From
	}
	if rc.republishOnline(ctx) {
		rc.note(i18n.Tx(i18n.RecoverOnlineRequeued))
	}
	if err := rc.closeRun(ctx, i18n.Tx(i18n.RecoverCompleted, t)); err != nil {
		return err
	}
	// Recover stands in for P10: due.json for T and the new epoch
	// (spec-artifacts 4.6). Best effort, as at P10.
	rc.o.writeArtifactsDue(ctx, rc.manifest, t, ps.Replica, root.Epoch)
	return nil
}

// recoverSync implements the RunSync table.
func (rc *recovery) recoverSync(ctx context.Context) error {
	run := rc.root.Run
	name, done := lastIntent(run)
	switch {
	case name == "" || name == state.IntentQuit || name == state.IntentPlanned:
		return rc.closeRun(ctx, i18n.Tx(i18n.RecoverClosedSync))
	case name == state.IntentBackedUp && !done:
		return rc.dropIncomplete(ctx)
	}
	if err := rc.requireStopped(ctx); err != nil {
		return err
	}
	f, err := rc.scan(ctx)
	if err != nil {
		return err
	}
	if f.claudeAlias != run.To {
		return rc.contradiction(ctx, f, run.To)
	}
	claude := rc.o.claudePath()
	ps := rc.root.Profiles[run.To]
	var rep model.Replica
	if ps != nil {
		rep = ps.Replica
	}
	if ok, _ := rc.recheckScratch(ctx, plan.Target{Alias: run.To, Replica: rep, Folder: claude}, claude); ok {
		return rc.finishP10(ctx)
	}
	return rc.rollbackTarget(ctx, run.To, claude, true, i18n.Tx(i18n.RecoverRestoredActive))
}

// recoverRollback implements the RunRollback table.
func (rc *recovery) recoverRollback(ctx context.Context) error {
	run := rc.root.Run
	name, _ := lastIntent(run)
	if name != state.IntentRestoring {
		return rc.closeRun(ctx, i18n.Tx(i18n.RecoverClosedRollbackUnrestored))
	}
	if err := rc.requireStopped(ctx); err != nil {
		return err
	}
	data, _ := intentData(run, state.IntentRestoring)
	n, _ := strconv.ParseUint(data["n"], 10, 64)
	if n == 0 {
		n = run.Generation
	}
	propagate := data["propagate"] == "true"
	rep, err := rc.o.finishRollback(ctx, rc.root, rc.manifest, n, run.To, propagate, run.ID)
	if err != nil {
		return err
	}
	rc.note(i18n.Tx(i18n.RecoverRollbackFinished, n, len(rep.Restored), len(rep.Conflicts)))
	return rc.closeRun(ctx, i18n.Tx(i18n.RecoverClosedRollback))
}

// recoverAdd implements the RunAdd table.
func (rc *recovery) recoverAdd(ctx context.Context) error {
	d := rc.o.d
	run := rc.root.Run
	name, done := lastIntent(run)
	if name != state.IntentAddParked {
		return rc.closeRun(ctx, i18n.Tx(i18n.RecoverClosedAddUnmoved))
	}
	if err := rc.requireStopped(ctx); err != nil {
		return err
	}
	f, err := rc.scan(ctx)
	if err != nil {
		return err
	}
	s := run.From
	sParked := rc.parked(s)
	switch {
	case f.claudeAlias == s:
		return rc.closeRun(ctx, i18n.Tx(i18n.RecoverClosedAddUnmoved))
	case f.claude == nil && rc.holds(f, sParked, s):
		if err := d.FS.RenameExcl(rc.o.folderPath(sParked), rc.o.claudePath()); err != nil {
			return pderr.Wrap(pderr.NeedsRecover, "recover", err)
		}
		rc.note(i18n.Tx(i18n.RecoverPutBackAdd, s))
		rc.root.PendingAdd = nil
		return rc.closeRun(ctx, i18n.Tx(i18n.RecoverClosedAdd))
	case f.claude != nil && f.claude.Identity != nil && f.claudeAlias == "":
		rc.setPendingAdd(run)
		return rc.pendingAdd(ctx)
	case done && f.claude != nil && f.claude.Identity == nil:
		rc.setPendingAdd(run)
		rc.root.Run = nil
		if err := rc.o.commit(rc.root); err != nil {
			return err
		}
		rc.note(i18n.Tx(i18n.RecoverAddWaits, rc.root.PendingAdd.Alias))
		if rc.located && rc.o.d.App.Launch(ctx, d.Config.App.BundleID) == nil {
			rc.note(i18n.Tx(i18n.RecoverLaunchedForSignIn))
		}
		return nil
	}
	return rc.contradiction(ctx, f, s, run.To)
}

func (rc *recovery) setPendingAdd(run *state.Run) {
	if rc.root.PendingAdd != nil {
		return
	}
	data, _ := intentData(run, state.IntentAddParked)
	rc.root.PendingAdd = &state.PendingAdd{Alias: run.To, Managed: data["managed"] == "true", ParkedFrom: run.From, Started: run.Started}
}

// pendingAdd binds a newly signed-in account to the pending alias after
// confirmation.
func (rc *recovery) pendingAdd(ctx context.Context) error {
	pa := rc.root.PendingAdd
	f, err := rc.scan(ctx)
	if err != nil {
		return err
	}
	if f.claude == nil || f.claude.Identity == nil {
		rc.note(i18n.Tx(i18n.RecoverAddWaits, pa.Alias))
		return nil
	}
	if f.claudeAlias != "" {
		return rc.dropPendingAdd(f.claudeAlias)
	}
	c := Confirmation{Kind: ConfirmAddBind, Lines: []i18n.Text{
		i18n.Tx(i18n.ConfirmAddBindLine, ids.Mask(string(f.claude.Identity.Account)), pa.Alias)}}
	if !rc.opts.Yes {
		if err := rc.o.ask(ctx, rc.o.interactive(false), c); err != nil {
			if pderr.CodeOf(err) == pderr.Declined {
				rc.note(i18n.Tx(i18n.RecoverLeftPendingAdd))
				return nil
			}
			return err
		}
	}
	return rc.bind(ctx, f.claude, pa)
}

// dropPendingAdd clears a pending profile add when Claude/ already holds a
// registered profile (the account was registered some other way, or the
// parked folder was put back). No folder is moved.
func (rc *recovery) dropPendingAdd(holder ids.Alias) error {
	pa := rc.root.PendingAdd
	rc.root.PendingAdd = nil
	if rc.root.Active != holder {
		rc.root.Previous, rc.root.Active = rc.root.Active, holder
	}
	if err := rc.o.commit(rc.root); err != nil {
		return err
	}
	rc.note(i18n.Tx(i18n.RecoverAddDropped, holder, pa.Alias))
	return nil
}

// bind registers Claude/'s account as the pending profile.
func (rc *recovery) bind(ctx context.Context, folder *profile.Folder, pa *state.PendingAdd) error {
	cfg := rc.o.d.Config
	if _, ok := cfg.Profile(pa.Alias); !ok {
		cfg.Profiles = append(cfg.Profiles, config.Profile{Alias: pa.Alias, ManagedByOrg: pa.Managed})
	}
	if err := rc.o.saveConfig(); err != nil {
		return err
	}
	rc.root.Profiles[pa.Alias] = newProfileState(pa.Alias, folder)
	rc.root.Previous, rc.root.Active = pa.ParkedFrom, pa.Alias
	rc.root.PendingAdd = nil
	rc.root.Run = nil
	rc.root.LastCommitAt = rc.o.now()
	if err := rc.o.commit(rc.root); err != nil {
		return err
	}
	rc.note(i18n.Tx(i18n.RecoverRegistered, pa.Alias))
	return nil
}

// newProfileState builds the state of a newly registered folder: its
// replica is the account's only session directory, else the account with
// an unknown org.
func newProfileState(a ids.Alias, f *profile.Folder) *state.ProfileState {
	var rep model.Replica
	if f.Identity != nil {
		rep.Account = f.Identity.Account
	}
	var own []model.Replica
	for _, r := range f.Replicas {
		if r.Account == rep.Account {
			own = appendReplica(own, r)
		}
	}
	if len(own) == 1 {
		rep = own[0]
	}
	return newProfileStateFor(a, f, rep)
}

// newProfileStateFor builds the state of a newly registered folder with a
// given replica (profile adopt --from-legacy records the resolver's).
func newProfileStateFor(a ids.Alias, f *profile.Folder, rep model.Replica) *state.ProfileState {
	ps := &state.ProfileState{Alias: a, FolderID: f.ID, Replica: rep}
	if f.Identity != nil {
		ps.AccountHistory = []ids.AccountID{f.Identity.Account}
	}
	for _, r := range f.Replicas {
		if r.Account == rep.Account {
			ps.Replicas = appendReplica(ps.Replicas, r)
		}
	}
	return ps
}

// contradiction stops when the folders do not match the journal. When
// Claude/ is missing it proposes moving a profile's folder into place,
// which needs ConfirmRecover.
func (rc *recovery) contradiction(ctx context.Context, f facts, prefer ...ids.Alias) error {
	lines := []i18n.Text{i18n.Tx(i18n.ConfirmFoldersMismatch, describe(rc.root, f))}
	if f.claude != nil {
		return pderr.NewT(pderr.NeedsRecover, "recover", i18n.ErrRecoverMismatchClaude, describe(rc.root, f))
	}
	for _, a := range prefer {
		name := rc.parked(a)
		if !rc.holds(f, name, a) {
			continue
		}
		c := Confirmation{Kind: ConfirmRecover,
			Lines: append(lines, i18n.Tx(i18n.ConfirmMoveFolder, name, a))}
		if !rc.opts.Yes {
			if err := rc.o.ask(ctx, rc.o.interactive(false), c); err != nil {
				return err
			}
		}
		if err := rc.o.d.FS.RenameExcl(rc.o.folderPath(name), rc.o.claudePath()); err != nil {
			return pderr.Wrap(pderr.NeedsRecover, "recover", err)
		}
		rc.note(i18n.Tx(i18n.RecoverMoved, name))
		if run := rc.root.Run; run != nil && run.To != a {
			dropAliasKeys(rc.root, run.To)
		}
		rc.root.Active = a
		return rc.closeRun(ctx, i18n.Tx(i18n.RecoverClosedRun))
	}
	return pderr.NewT(pderr.NeedsRecover, "recover", i18n.ErrRecoverMismatchNoMove, describe(rc.root, f))
}

// describe lists folders with masked identities and owners.
func describe(root *state.Root, f facts) i18n.Text {
	var parts i18n.CommaList
	for _, x := range f.folders {
		switch {
		case x.Identity == nil:
			parts = append(parts, i18n.Tx(i18n.RecoverFolderUnidentified, x.Name))
		case aliasOf(root, x.Identity.Account) != "":
			parts = append(parts, i18n.Tx(i18n.RecoverFolderAccount, x.Name, ids.Mask(string(x.Identity.Account)), aliasOf(root, x.Identity.Account)))
		default:
			parts = append(parts, i18n.Tx(i18n.RecoverFolderUnregistered, x.Name, ids.Mask(string(x.Identity.Account))))
		}
	}
	if len(parts) == 0 {
		return i18n.Tx(i18n.RecoverNoFolders)
	}
	return i18n.Tx(i18n.WordList, parts)
}

// withoutJournal handles a missing or unreadable state: identities only.
func (rc *recovery) withoutJournal(ctx context.Context) error {
	rc.root = state.NewRoot(rc.o.d.DataRoot)
	rc.locate(ctx)
	folders, err := rc.o.d.Folders.Scan(ctx)
	if err != nil {
		return pderr.Wrap(pderr.Internal, "recover", err)
	}
	f := facts{folders: folders, claude: facts{folders: folders}.find(ids.DefaultFolder)}
	if f.claude != nil {
		rc.note(i18n.Tx(i18n.RecoverNoJournal))
		return nil
	}
	if err := rc.requireStopped(ctx); err != nil {
		return err
	}
	best, bestTime := "", int64(-1)
	for _, p := range rc.o.d.Config.Profiles {
		name := p.ParkedFolder()
		x := f.find(name)
		if x == nil || x.Identity == nil {
			continue
		}
		fi, err := os.Stat(x.Path)
		if err == nil && fi.ModTime().UnixNano() > bestTime {
			best, bestTime = name, fi.ModTime().UnixNano()
		}
	}
	if best == "" {
		return pderr.NewT(pderr.NeedsRecover, "recover", i18n.ErrRecoverNoJournal)
	}
	c := Confirmation{Kind: ConfirmRecover, Lines: []i18n.Text{
		i18n.Tx(i18n.ConfirmNoJournal), i18n.Tx(i18n.ConfirmMoveRecent, best)}}
	if !rc.opts.Yes {
		if err := rc.o.ask(ctx, rc.o.interactive(false), c); err != nil {
			return err
		}
	}
	if err := rc.o.d.FS.RenameExcl(rc.o.folderPath(best), rc.o.claudePath()); err != nil {
		return pderr.Wrap(pderr.NeedsRecover, "recover", err)
	}
	rc.note(i18n.Tx(i18n.RecoverMoved, best))
	return nil
}

// saveConfig writes the config file. Without a config path (tests) the
// config lives in memory only.
func (o *Orchestrator) saveConfig() error {
	if o.d.Paths.ConfigFile == "" {
		return nil
	}
	if err := o.d.Config.Save(o.d.Paths.ConfigFile); err != nil {
		return pderr.Wrap(pderr.Internal, "config", err)
	}
	return nil
}
