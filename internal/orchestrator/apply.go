package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"

	"github.com/kazupc/paritydesk/internal/app"
	"github.com/kazupc/paritydesk/internal/apply"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/gen"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/localstorage"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/profile"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/state"
	"github.com/kazupc/paritydesk/internal/store/kv"
)

// harvest is P5 up to the commit: identity re-check, final plan,
// validation and the comparison with the preview.
func (r *run) harvest(ctx context.Context) error {
	defer r.phase(P5Harvest, "final plan")()
	d := r.d()
	b, err := d.App.Locate(ctx, d.Config.App.BundleID)
	if err != nil || !sameBundle(b, r.bundle) {
		return r.abort(ctx, pderr.New(pderr.CompatRefused, "harvest", "Claude changed during the run (an update installed?)"))
	}
	if r.swapOnly() {
		r.p5 = r.p2
		return nil
	}
	closeReader(r.sLS)
	closeReader(r.tLS)
	r.sLS, r.tLS = nil, nil
	if r.sLS, err = r.o.lsReader(ctx, r.sPath); err != nil {
		return r.abort(ctx, err)
	}
	if r.kind == state.RunSwitch {
		if r.tLS, err = r.o.lsReader(ctx, r.tPath); err != nil {
			return r.abort(ctx, err)
		}
	}
	pl, err := r.o.buildPlan(ctx, planInput{S: r.sideS(), T: r.sideT(), Manifest: r.manifest, Root: r.root,
		Rows: r.rows, Skipped: r.skipped, RunID: r.res.RunID})
	if err != nil {
		return r.abort(ctx, err)
	}
	r.p5 = pl
	r.setPlan(pl.Plan)
	r.onlineHarvest()
	if err := r.validate(ctx); err != nil {
		return r.abort(ctx, err)
	}
	if nd := plan.NewDestructive(r.p2.Plan, pl.Plan); len(nd) > 0 {
		if !r.interactive {
			return r.abort(ctx, pderr.New(pderr.Declined, "harvest", fmt.Sprintf("the final plan deletes %d item(s) the preview did not; nothing was changed", len(nd))))
		}
		c := Confirmation{Kind: ConfirmNewDestructive, Plan: pl.Plan, Summary: r.res.Summary}
		for _, ch := range nd {
			c.Lines = append(c.Lines, fmt.Sprintf("%s: %s", ch.Row, ids.Mask(ch.Key)))
		}
		if err := r.o.ask(ctx, true, c); err != nil {
			return r.abort(ctx, err)
		}
	}
	return r.stillQuiet(ctx)
}

// stillQuiet re-checks P4's quiescence once, right before the commit:
// the user may have reopened Claude while the final plan was built or a
// confirmation was open, and nothing may be written under a running app.
func (r *run) stillQuiet(ctx context.Context) error {
	folders := []string{r.sPath}
	if r.kind == state.RunSwitch {
		folders = append(folders, r.tPath)
	}
	rep, err := r.d().App.CheckQuiescence(ctx, r.o.quiescenceInput(r.bundle.Path, folders...))
	if err != nil {
		return r.abort(ctx, pderr.Wrap(pderr.Internal, "quiescence", err))
	}
	if !rep.Quiet {
		return r.abort(ctx, pderr.New(pderr.AppDidNotQuit, "quiescence", "Claude or another process opened the data folders during the run; nothing was changed"))
	}
	return nil
}

// allowedPaths returns the manifest's synced globs expanded for the
// entering replica. File ops outside them are refused, so an entry the
// manifest does not classify is never touched, and no op reaches another
// account's session directory. An empty list would allow everything, so
// a manifest without synced globs allows nothing.
func allowedPaths(m *compat.Manifest, rep model.Replica) []string {
	out := m.SyncedFor(rep)
	if len(out) == 0 {
		return []string{nothingAllowed}
	}
	return out
}

// validate runs plan.Validate with the manifest's lists and the worktree
// guard's risks.
func (r *run) validate(ctx context.Context) error {
	d := r.d()
	if d.Validate == nil {
		return nil
	}
	p := r.p5.Plan
	cfg := d.Config
	mayCross, allowed := r.o.secrets(r.S.Profile.ManagedByOrg, r.T.Profile.ManagedByOrg)
	allowDeletions := r.opts.AllowDeletions
	if r.confirmedByPerson {
		allowDeletions = math.MaxInt
	}
	in := plan.ValidateInput{
		Plan:   p,
		Before: beforeCounts(r.p5),
		Limits: plan.Limits{MaxDeleteFraction: cfg.Limits.MaxDeleteFraction, MaxDeleteMin: cfg.Limits.MaxDeleteMin,
			AllowDeletions: allowDeletions},
		DenyPaths:          append(append([]string(nil), r.manifest.Classification.NeverTouched...), r.manifest.Classification.NeverUntilClassified...),
		DenyLSKeys:         r.manifest.Classification.LSNeverTouched,
		AllowPaths:         allowedPaths(r.manifest, r.T.Replica),
		AllowLSKeys:        expandedLSKeys(r.manifest, r.rows, r.T.Replica),
		TmpRecoveryGlobs:   r.manifest.TmpRecoveryGlobs,
		SecretsMayCross:    mayCross,
		SecretsAllowed:     allowed,
		AcceptWorktreeRisk: r.opts.AcceptWorktreeRisk,
	}
	risks, err := r.worktreeRisks(ctx)
	if err != nil {
		return err
	}
	in.Worktrees = risks
	if err := d.Validate(in); err != nil {
		if pderr.CodeOf(err) == pderr.Internal {
			return pderr.Wrap(pderr.Internal, "validate", err)
		}
		return err
	}
	return nil
}

// worktreeRisks lists worktree directories that the entering folder will
// no longer register after apply, with their dirty state: worktrees the
// leaving sessions reference that the entering sessions will not, and
// registry entries of the entering folder that the worktrees row deletes
// or replaces with another directory. The second part runs even when the
// sessions row is skipped. A worktree directory that no longer exists
// while its parent does holds nothing to lose and is left out; any other
// path that cannot be checked counts as dirty.
func (r *run) worktreeRisks(ctx context.Context) ([]plan.WorktreeRisk, error) {
	d := r.d()
	if d.Worktrees == nil || r.kind != state.RunSwitch {
		return nil, nil
	}
	var lost []string
	if w := r.p5.Work[model.RowSessions]; w != nil {
		var fields []string
		if spec, ok := r.manifest.Row(model.RowSessions); ok {
			fields = spec.Fields[sources.RoleWorktreePath]
		}
		l, err := d.Worktrees.LostRegistrations(w.S, w.Out.Desired, fields)
		if err != nil {
			return nil, pderr.Wrap(pderr.Internal, "worktree guard", err)
		}
		lost = append(lost, l...)
	}
	if w := r.p5.Work[model.RowWorktrees]; w != nil && w.T != nil && w.Out.Desired != nil {
		var fields []string
		if spec, ok := r.manifest.Row(model.RowWorktrees); ok {
			fields = spec.Fields[sources.RoleWorktreePath]
		}
		l, err := d.Worktrees.LostEntries(w.T, w.Out.Desired, fields)
		if err != nil {
			return nil, pderr.Wrap(pderr.Internal, "worktree guard", err)
		}
		lost = append(lost, l...)
	}
	sort.Strings(lost)
	lost = slices.Compact(lost)
	var out []plan.WorktreeRisk
	for _, p := range lost {
		if worktreeGone(p) {
			continue
		}
		dirty, err := d.App.GitDirty(ctx, p)
		out = append(out, plan.WorktreeRisk{Path: p, Dirty: dirty || err != nil})
	}
	return out, nil
}

// worktreeGone reports whether an absolute worktree path does not exist
// while its parent directory does. Only a definite "does not exist"
// counts. A path whose parent is missing too (for example on an external
// volume that is not mounted now) stays in the guard, as does a path
// that cannot be checked.
func worktreeGone(p string) bool {
	if !filepath.IsAbs(p) {
		return false
	}
	if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	fi, err := os.Stat(filepath.Dir(p))
	return err == nil && fi.IsDir()
}

// commitP5 records the harvest: objects, canonical, both overlays, S's
// bases for harvested rows, the post-hashes and the planned intent. From
// here on the run no longer reacts to cancellation.
func (r *run) commitP5() error {
	d := r.d()
	root := r.root
	for _, row := range r.p5.Rows {
		w := r.p5.Work[row]
		if err := r.putRef(func(ref state.Ref) { root.Canonical[row] = ref }, func() { delete(root.Canonical, row) }, w.Out.Canonical); err != nil {
			return err
		}
		sk, tk := w.SKey.String(), w.TKey.String()
		if sk != tk {
			if err := r.putRef(func(ref state.Ref) { root.Overlays[sk] = ref }, func() { delete(root.Overlays, sk) }, w.Out.LeavingOverlay); err != nil {
				return err
			}
		}
		if err := r.putRef(func(ref state.Ref) { root.Overlays[tk] = ref }, func() { delete(root.Overlays, tk) }, w.Out.EnteringOverlay); err != nil {
			return err
		}
		if w.S != nil {
			ref, err := d.State.PutSnapshot(w.S)
			if err != nil {
				return pderr.Wrap(pderr.Internal, "state", err)
			}
			root.Bases[sk] = ref
		}
	}
	p := r.p5.Plan
	root.Run.PlanHash = p.Hash()
	root.Run.PostHashes = postHashes(p)
	root.Run.ConfirmedPlan = r.opts.ConfirmedPlan
	if err := r.storeOnlineDraft(); err != nil {
		return err
	}
	data := map[string]string{"hash": root.Run.PlanHash, "rows": joinRows(r.p5.Rows)}
	if err := r.o.begin(root, P5Harvest, state.IntentPlanned, data); err != nil {
		return err
	}
	return r.o.done(root, P5Harvest, state.IntentPlanned)
}

// putRef stores snap and calls set with its ref, or calls clear for nil.
func (r *run) putRef(set func(state.Ref), clear func(), snap *model.RowSnapshot) error {
	if snap == nil {
		clear()
		return nil
	}
	ref, err := r.d().State.PutSnapshot(snap)
	if err != nil {
		return pderr.Wrap(pderr.Internal, "state", err)
	}
	set(ref)
	return nil
}

// nextGeneration returns an unused generation number.
func (o *Orchestrator) nextGeneration(root *state.Root) uint64 {
	n := latestGeneration(root)
	if list, err := o.d.Gens.List(); err == nil {
		for _, g := range list {
			n = max(n, g.N)
		}
	}
	return n + 1
}

// backup is P6: a complete generation of everything the plan touches.
func (r *run) backup(ctx context.Context) error {
	defer r.phase(P6Backup, "backing up")()
	d := r.d()
	p := r.p5.Plan
	r.gen = r.o.nextGeneration(r.root)
	r.root.Run.Generation = r.gen
	if err := r.o.begin(r.root, P6Backup, state.IntentBackedUp, map[string]string{"n": strconv.FormatUint(r.gen, 10)}); err != nil {
		return err
	}
	w, err := d.Gens.Begin(r.gen, r.res.RunID, r.T.Profile.Alias, r.T.Replica)
	if err != nil {
		return r.abort(ctx, pderr.Wrap(pderr.Internal, "generation", err))
	}
	if err := r.fillGeneration(w, p); err != nil {
		_ = w.Abort()
		return r.abort(ctx, pderr.Wrap(pderr.Internal, "generation", err))
	}
	if _, err := w.Finish(); err != nil {
		_ = w.Abort()
		return r.abort(ctx, pderr.Wrap(pderr.Internal, "generation", err))
	}
	r.res.Generation = r.gen
	r.genWriter = w
	return r.o.done(r.root, P6Backup, state.IntentBackedUp)
}

// fillGeneration saves every target's previous bytes, conflict losers,
// and a LevelDB clone when Local Storage is written.
func (r *run) fillGeneration(w gen.Writer, p *plan.Plan) error {
	for _, op := range p.Ops {
		switch op.Kind {
		case plan.OpWriteFile, plan.OpQuarantine:
			if err := w.SaveFile(op.Row, r.tPath, op.Path, op.PostHash); err != nil {
				return err
			}
		case plan.OpLSPut, plan.OpLSDelete:
			raw, present, err := r.lsValue(op.Key)
			if err != nil {
				return err
			}
			if err := w.SaveLSValue(op.Row, op.Key, raw, present, op.PostHash); err != nil {
				return err
			}
		}
	}
	for _, c := range p.Conflicts {
		if raw := r.loserBytes(c); raw != nil {
			if err := w.SaveConflict(c.Row, c.Key, raw); err != nil {
				return err
			}
		}
	}
	if hasLSOps(p) {
		r.genLS = true
		return w.CloneLevelDB(sources.LevelDBPath(r.tPath))
	}
	return nil
}

// lsValue reads an LS item's current encoded bytes from T's snapshot.
func (r *run) lsValue(name string) ([]byte, bool, error) {
	ls := r.sideT().LS
	if ls == nil {
		return nil, false, nil
	}
	k, err := localstorage.Key(name)
	if err != nil {
		return nil, false, err
	}
	v, found, err := ls.Get(k)
	return append([]byte(nil), v...), found, err
}

// loserBytes returns the bytes of a merge loser, from whichever side lost.
func (r *run) loserBytes(c model.Conflict) []byte {
	w := r.p5.Work[c.Row]
	if w == nil {
		return nil
	}
	for _, snap := range []*model.RowSnapshot{w.S, w.T} {
		if snap == nil {
			continue
		}
		if it, ok := snap.Items[c.Key]; ok && it.Hash == c.LoserHash {
			if it.Raw != nil {
				return it.Raw
			}
			return it.Value
		}
	}
	return nil
}

// applyPlan is P7: apply on the (parked) entering folder, then the marker.
func (r *run) applyPlan(ctx context.Context) error {
	defer r.phase(P7Apply, "applying")()
	d := r.d()
	if err := r.o.begin(r.root, P7Apply, state.IntentApplied, nil); err != nil {
		return err
	}
	in := apply.Input{Plan: r.p5.Plan, Folder: r.tPath, TmpDir: r.o.tmpDir(r.res.RunID), QuarantineDir: r.genWriter.QuarantineDir()}
	res, err := d.Applier.Apply(ctx, in)
	r.touched, r.lsOpened = res.Touched, res.LSOpened
	if err != nil && !res.Touched {
		// Nothing in the folder changed: there is nothing to restore.
		return r.abort(ctx, pderr.Wrap(pderr.ApplyRolledBack, "apply", err))
	}
	if err == nil {
		err = checkApplied(r.p5.Plan, res)
	}
	if err == nil {
		m := model.Marker{Version: 1, Alias: r.T.Profile.Alias, Replica: r.T.Replica, Generation: r.gen, BaseHash: r.p5.Plan.Hash()}
		err = d.WriteMarker(d.FS, r.tPath, m)
	}
	if err != nil {
		return r.rollbackT(ctx, pderr.Wrap(pderr.ApplyRolledBack, "apply", err))
	}
	r.markLeaving()
	return r.o.done(r.root, P7Apply, state.IntentApplied)
}

// checkApplied compares the hashes apply read back with the plan's.
func checkApplied(p *plan.Plan, res apply.Result) error {
	if res.PostHashes == nil {
		return nil
	}
	for _, op := range p.Ops {
		if got, ok := res.PostHashes[postHashKey(op)]; ok && got != op.PostHash {
			return errors.New("orchestrator: a written target does not read back as planned")
		}
	}
	return nil
}

func (o *Orchestrator) tmpDir(runID string) string {
	return filepath.Join(o.d.DataRoot, ".paritydesk-tmp", runID)
}

// rollbackT restores the entering folder from the generation and aborts
// the run (relaunching S): the P7 failure path and the P8 swap-back path.
// It restores nothing when apply left the folder untouched, restores the
// LevelDB clone only when apply opened Local Storage, and leaves the run
// open (41) instead of restoring under a process that uses the folder.
func (r *run) rollbackT(ctx context.Context, cause error) error {
	cctx, cancel := uncancel(ctx)
	defer cancel()
	if errors.Is(cause, ErrCrashInjected) {
		return cause
	}
	if !r.touched {
		return r.abort(cctx, cause)
	}
	live := r.tPath == r.o.claudePath()
	if busy, err := r.o.folderInUse(cctx, r.bundle.Path, r.tPath, live); err != nil || busy {
		r.warn(P7Apply, "the entering folder is in use, so it was not restored; quit Claude, then run paritydesk recover")
		return &pderr.Error{Code: pderr.NeedsRecover, Op: "apply", Msg: "the entering folder is in use; run paritydesk recover", Err: cause}
	}
	rep, err := r.o.restoreFolder(cctx, r.gen, r.tPath, r.genLS && r.lsOpened, r.res.RunID)
	if err != nil {
		r.warn(P7Apply, "the entering folder could not be fully restored; run paritydesk recover")
		return &pderr.Error{Code: pderr.NeedsRecover, Op: "apply", Msg: "the entering folder could not be fully restored; run paritydesk recover", Err: errors.Join(cause, err)}
	}
	r.o.invalidateConflicted(r.root, r.T.Profile.Alias, r.T.Replica, rep.Conflicts)
	r.o.restoreMarker(r.root, r.T.Profile.Alias, r.tPath)
	return r.abort(cctx, cause)
}

// restoreFolder rolls a folder back from generation n: the LevelDB clone
// first when apply may have opened Local Storage for writing (cloneLS),
// then the conditional restore of every entry.
func (o *Orchestrator) restoreFolder(ctx context.Context, n uint64, folder string, cloneLS bool, runID string) (gen.RestoreReport, error) {
	d := o.d
	if n == 0 {
		return gen.RestoreReport{}, nil
	}
	if cloneLS {
		if err := d.Gens.RestoreLevelDB(ctx, n, folder); err != nil {
			return gen.RestoreReport{}, pderr.Wrap(pderr.Internal, "restore", err)
		}
	}
	in := gen.RestoreInput{N: n, Folder: folder, TmpDir: o.tmpDir(runID)}
	if m, err := d.Gens.Load(n); err == nil && hasLSEntries(m) {
		w, err := d.KV.OpenWritable(ctx, sources.LevelDBPath(folder))
		if err != nil {
			return gen.RestoreReport{}, lsOpenErr("restore", err)
		}
		defer closeReader(w)
		in.LS = w
	}
	rep, err := d.Gens.Restore(ctx, in)
	if err != nil {
		return rep, pderr.Wrap(pderr.Internal, "restore", err)
	}
	return rep, nil
}

// lsOpenErr maps a failed writable open of Local Storage during a
// restore or rollback. Another process holding its lock is not an
// internal error: the run stays open, nothing was written to Local
// Storage, and recover finishes once the folder is free (41).
func lsOpenErr(op string, err error) error {
	if errors.Is(err, kv.ErrLocked) {
		return &pderr.Error{Code: pderr.NeedsRecover, Op: op, Msg: "another process holds the Local Storage lock; quit Claude (and any copy started with --user-data-dir), then run paritydesk recover", Err: err}
	}
	return pderr.Wrap(pderr.Internal, op, err)
}

// invalidateConflicted drops a profile's bases of rows with conflicts.
func (o *Orchestrator) invalidateConflicted(root *state.Root, a ids.Alias, rep model.Replica, conflicts []gen.Entry) {
	for _, e := range conflicts {
		dropBase(root, state.BaseKey{Alias: a, Replica: rep, Row: e.Row}.String())
	}
}

// restoreMarker puts back the marker state recorded for a profile, so a
// rolled-back folder still matches its old bases.
func (o *Orchestrator) restoreMarker(root *state.Root, a ids.Alias, folder string) {
	ps := root.Profiles[a]
	if ps == nil || ps.MarkerBase == "" {
		return
	}
	var n uint64
	for _, g := range root.Generations {
		if g.Target == a {
			n = max(n, g.N)
		}
	}
	m := model.Marker{Version: 1, Alias: a, Replica: ps.Replica, Generation: n, BaseHash: ps.MarkerBase}
	_ = o.d.WriteMarker(o.d.FS, folder, m)
}

// swap is P8 (switch only): swap Claude/ with the entering folder, or the
// journaled two-rename fallback, then the post-swap check.
func (r *run) swap(ctx context.Context) error {
	defer r.phase(P8Swap, "swapping folders")()
	d := r.d()
	claude := r.o.claudePath()
	sParked := r.o.folderPath(r.S.Profile.ParkedFolder())
	if _, err := r.quiesce(ctx, claude, r.tPath); err != nil {
		return r.rollbackT(ctx, err)
	}
	if err := r.o.begin(r.root, P8Swap, state.IntentSwapping, nil); err != nil {
		return err
	}
	err := d.FS.SwapDirs(claude, r.tPath)
	switch {
	case errors.Is(err, fsx.ErrSwapUnsupported):
		if err := r.fallbackSwap(ctx, claude, sParked); err != nil {
			return err
		}
		return r.fallbackPostCheck(ctx)
	case err != nil:
		return r.rollbackT(ctx, pderr.Wrap(pderr.ApplyRolledBack, "swap", err))
	}
	// S now sits at T's parked name; T is in Claude/.
	r.sPath, r.tPath = r.tPath, claude
	_ = d.FS.FullSync(d.DataRoot)
	if err := r.o.done(r.root, P8Swap, state.IntentSwapping); err != nil {
		return err
	}
	if err := r.o.begin(r.root, P8Swap, state.IntentSwapped, nil); err != nil {
		return err
	}
	if err := r.o.done(r.root, P8Swap, state.IntentSwapped); err != nil {
		return err
	}
	return r.postSwapCheck(ctx)
}

// fallbackSwap is the journaled two-rename fallback; it leaves S parked.
func (r *run) fallbackSwap(ctx context.Context, claude, sParked string) error {
	d := r.d()
	r.fallback = true
	if err := r.o.begin(r.root, P8Swap, state.IntentFallbackSParked, nil); err != nil {
		return err
	}
	if err := d.FS.RenameExcl(claude, sParked); err != nil {
		return r.rollbackT(ctx, pderr.Wrap(pderr.ApplyRolledBack, "swap", err))
	}
	r.sPath = sParked
	if err := r.o.done(r.root, P8Swap, state.IntentFallbackSParked); err != nil {
		return err
	}
	if err := r.o.begin(r.root, P8Swap, state.IntentFallbackTPlaced, nil); err != nil {
		return err
	}
	if err := d.FS.RenameExcl(r.tPath, claude); err != nil {
		// Put S back so Claude/ is never left empty.
		if berr := d.FS.RenameExcl(sParked, claude); berr != nil {
			return pderr.New(pderr.NeedsRecover, "swap", "the folder swap was interrupted; run paritydesk recover")
		}
		r.sPath = claude
		return r.rollbackT(ctx, pderr.Wrap(pderr.ApplyRolledBack, "swap", err))
	}
	r.tPath = claude
	_ = d.FS.FullSync(d.DataRoot)
	return r.o.done(r.root, P8Swap, state.IntentFallbackTPlaced)
}

// fallbackPostCheck is the post-swap check of the two-rename fallback.
// Undoing two renames under a process that just started is not atomic,
// so when anything started since P4 or holds the moved folders the run
// stops (41) with T in place; recover completes it once that process
// has quit.
func (r *run) fallbackPostCheck(ctx context.Context) error {
	d := r.d()
	in := app.QuiescenceInput{BundlePath: r.bundle.Path, BundleID: d.Config.App.BundleID, DataRoot: d.DataRoot,
		Folders: []string{r.tPath, r.sPath}, After: r.t4}
	rep, err := d.App.CheckQuiescence(ctx, in)
	if err != nil {
		r.warn(P8Swap, "the post-swap check could not run")
		return nil
	}
	if len(rep.StartedAfter) == 0 && len(rep.WriteHolders) == 0 {
		return nil
	}
	return pderr.New(pderr.NeedsRecover, "swap", "a process started during the folder swap; quit Claude, then run paritydesk recover")
}

// postSwapCheck looks for an app process or holder that appeared since
// P4; it swaps back when nothing holds the moved folders, else stops (41).
func (r *run) postSwapCheck(ctx context.Context) error {
	d := r.d()
	in := app.QuiescenceInput{BundlePath: r.bundle.Path, BundleID: d.Config.App.BundleID, DataRoot: d.DataRoot,
		Folders: []string{r.tPath, r.sPath}, After: r.t4}
	rep, err := d.App.CheckQuiescence(ctx, in)
	if err != nil {
		r.warn(P8Swap, "the post-swap check could not run")
		return nil
	}
	if len(rep.StartedAfter) == 0 && len(rep.WriteHolders) == 0 {
		return nil
	}
	if err := r.o.begin(r.root, P8Swap, state.IntentSwapBack, nil); err != nil {
		return err
	}
	if len(rep.WriteHolders)+len(rep.ReadOnlyHolders) > 0 {
		return pderr.New(pderr.NeedsRecover, "swap", "a process opened the data folders during the swap; quit it, then run paritydesk recover")
	}
	claude := r.o.claudePath()
	if err := d.FS.SwapDirs(claude, r.sPath); err != nil {
		return pderr.New(pderr.NeedsRecover, "swap", "swapping back failed; run paritydesk recover")
	}
	r.sPath, r.tPath = claude, r.sPath
	_ = d.FS.FullSync(d.DataRoot)
	return r.rollbackT(ctx, pderr.New(pderr.ApplyRolledBack, "swap", "a process started during the swap; the switch was undone"))
}

// park is P9: give S its own parked name.
func (r *run) park(ctx context.Context) error {
	defer r.phase(P9Park, "parking")()
	d := r.d()
	sParked := r.o.folderPath(r.S.Profile.ParkedFolder())
	if err := r.o.begin(r.root, P9Park, state.IntentParked, nil); err != nil {
		return err
	}
	if err := d.FS.RenameExcl(r.sPath, sParked); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return pderr.New(pderr.NeedsRecover, "park", fmt.Sprintf("%s already exists; folders hold accounts %s and %s",
				filepath.Base(sParked), maskedAccount(r.S), maskedAccount(r.T)))
		}
		return pderr.Wrap(pderr.NeedsRecover, "park", err)
	}
	r.sPath = sParked
	return r.o.done(r.root, P9Park, state.IntentParked)
}

// maskedAccount shows a binding's account masked.
func maskedAccount(b *profile.Binding) string { return ids.Mask(string(b.Replica.Account)) }

// markLeaving writes the leaving folder's marker. P5 recorded that
// folder's harvested rows as its bases, and nothing writes to it after
// the quit, so the marker vouches for them: the next switch back merges
// with valid bases instead of a second first-sync union. A failed write
// only leaves those bases invalid (a union next time); the run goes on.
func (r *run) markLeaving() {
	if r.kind == state.RunSync || r.gen == 0 || r.sPath == r.tPath {
		return
	}
	d := r.d()
	m := model.Marker{Version: 1, Alias: r.S.Profile.Alias, Replica: r.S.Replica, Generation: r.gen, BaseHash: r.p5.Plan.Hash()}
	if err := d.WriteMarker(d.FS, r.sPath, m); err != nil {
		r.warn(P7Apply, "the leaving folder's marker could not be written; the next switch back merges without deleting")
		return
	}
	r.sMarked = true
}
