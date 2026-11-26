package orchestrator

import (
	"context"
	"fmt"
	"strconv"

	"github.com/kazupc/paritydesk/internal/app"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/gen"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/runner"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/state"
)

// RollbackResult reports a rollback.
type RollbackResult struct {
	Generation uint64   `json:"generation"`
	Restored   int      `json:"restored"`
	Conflicts  []string `json:"conflicts,omitempty"`
}

// Rollback restores generation n conditionally (app stopped), records an
// epoch event, and sets bases according to propagate (M6). n == 0 means
// the latest generation.
func (o *Orchestrator) Rollback(ctx context.Context, n uint64, propagate bool) (*RollbackResult, error) {
	d := o.d
	if d.ReadOnly {
		return nil, pderr.New(pderr.Usage, "rollback", "rollback writes; it cannot run in read-only mode")
	}
	l, err := d.State.Lock(ctx, 0)
	if err != nil {
		return nil, err
	}
	defer releaseLock(l)
	root, err := o.loadRoot()
	if err != nil {
		return nil, err
	}
	if err := o.checkBinding(root); err != nil {
		return nil, err
	}
	if root.Run != nil || root.PendingAdd != nil {
		return nil, pderr.New(pderr.NeedsRecover, "rollback", "a run is open; run paritydesk recover first")
	}
	if n == 0 {
		n = latestGeneration(root)
	}
	if n == 0 {
		return nil, pderr.New(pderr.Usage, "rollback", "there is no generation to roll back")
	}
	man, err := d.Gens.Load(n)
	if err != nil {
		return nil, pderr.Wrap(pderr.Usage, "rollback", err)
	}
	target, err := o.rollbackTarget(ctx, root, n, man)
	if err != nil {
		return nil, err
	}
	quarantine := isQuarantine(man)
	c := Confirmation{Kind: ConfirmRollback, Lines: []string{
		fmt.Sprintf("Restore generation %d of %s (%d item(s)); items changed since are left as they are.", n, target, len(man.Entries)),
		fmt.Sprintf("Propagate the restored values to other profiles: %v.", propagate)}}
	if quarantine {
		// A quarantine restore touches no row: propagate means nothing.
		propagate = false
		c.Lines = []string{i18n.T(i18n.RollbackQuarantineLine, len(man.Entries), parkedOrActive(o.d, root, target))}
	}
	if err := o.ask(ctx, o.interactive(false), c); err != nil {
		return nil, err
	}
	b, err := d.App.Locate(ctx, d.Config.App.BundleID)
	if err != nil {
		return nil, pderr.Wrap(pderr.CompatRefused, "rollback", err)
	}
	m, _, _ := d.Compat.Manifest(ctx, b.Version)
	runID, err := runner.NewRunID(o.now())
	if err != nil {
		return nil, err
	}
	root.Run = &state.Run{ID: runID, Kind: state.RunRollback, From: target, To: target, Generation: n, Started: o.now()}
	bundlePath := b.Path
	if quarantine && target != root.Active {
		// Moving directories back into a parked folder: Claude keeps
		// running; only processes using that folder count.
		bundlePath = ""
	} else if err := o.stopForRollback(ctx, root, b); err != nil {
		return nil, err
	}
	if err := o.quietForRollback(ctx, root, bundlePath, o.rollbackFolder(root, target)); err != nil {
		return nil, err
	}
	cctx, cancel := uncancel(ctx)
	defer cancel()
	data := map[string]string{"n": strconv.FormatUint(n, 10), "propagate": strconv.FormatBool(propagate)}
	if err := o.begin(root, P7Apply, state.IntentRestoring, data); err != nil {
		return nil, err
	}
	rep, err := o.finishRollback(cctx, root, m, n, target, propagate, runID)
	if err != nil {
		return nil, err
	}
	if err := o.done(root, P7Apply, state.IntentRestoring); err != nil {
		return nil, err
	}
	quit := root.Run.QuitByUs
	root.Run = nil
	if err := o.commit(root); err != nil {
		return nil, err
	}
	if quit {
		o.relaunch(cctx, root, "relaunched Claude")
	}
	out := &RollbackResult{Generation: n, Restored: len(rep.Restored)}
	for _, e := range rep.Conflicts {
		out.Conflicts = append(out.Conflicts, conflictName(e))
	}
	return out, nil
}

// rollbackTarget returns the profile generation n belongs to: the state's
// record of it (renameAlias keeps that current), else the manifest's
// alias. The profile's folder must hold the account the generation was
// taken from, so a renamed or reused alias never restores into another
// account's folder.
func (o *Orchestrator) rollbackTarget(ctx context.Context, root *state.Root, n uint64, man *gen.Manifest) (ids.Alias, error) {
	target := man.Alias
	for _, g := range root.Generations {
		if g.N == n && g.Target != "" {
			target = g.Target
		}
	}
	ps := root.Profiles[target]
	if ps == nil {
		return "", pderr.New(pderr.Usage, "rollback", fmt.Sprintf("generation %d belongs to no registered profile", n))
	}
	if man.Replica.Account != "" && man.Replica.Account != ps.Replica.Account {
		return "", pderr.New(pderr.IdentityMismatch, "rollback", fmt.Sprintf("generation %d was taken from another account than %s holds now", n, target))
	}
	name := parkedName(o.d, target)
	if target == root.Active {
		name = ids.DefaultFolder
	}
	f, _, err := o.scanFolder(ctx, name)
	if err != nil {
		return "", err
	}
	if f == nil || f.Identity == nil || f.Identity.Account != ps.Replica.Account {
		return "", pderr.New(pderr.IdentityMismatch, "rollback", fmt.Sprintf("%s does not hold the account of %s; nothing was restored", name, target))
	}
	return target, nil
}

func conflictName(e gen.Entry) string {
	if e.Path != "" {
		return string(e.Row) + ": " + e.Path
	}
	return string(e.Row) + ": " + e.Key
}

// stopForRollback quits the app (intent quit, QuitByUs) when it runs.
func (o *Orchestrator) stopForRollback(ctx context.Context, root *state.Root, b app.BundleIdentity) error {
	d := o.d
	procs, err := d.App.Running(ctx, b.Path)
	if err != nil {
		return pderr.Wrap(pderr.Internal, "rollback", err)
	}
	if len(procs) == 0 {
		return nil
	}
	root.Run.QuitByUs = true
	if err := o.begin(root, P3Quit, state.IntentQuit, nil); err != nil {
		return err
	}
	qr, err := d.App.QuitAndWait(ctx, d.Config.App.BundleID, b.Path, compat.Signals{}, o.quitTimeout(), nil)
	if err != nil || !qr.Exited {
		root.Run = nil
		if cerr := o.commit(root); cerr != nil {
			return cerr
		}
		return pderr.New(pderr.AppDidNotQuit, "rollback", "Claude did not quit; nothing was restored")
	}
	return o.done(root, P3Quit, state.IntentQuit)
}

// rollbackFolder is the folder that holds profile a now.
func (o *Orchestrator) rollbackFolder(root *state.Root, a ids.Alias) string {
	if a == root.Active {
		return o.claudePath()
	}
	return o.folderPath(parkedName(o.d, a))
}

// quietForRollback waits, as P4 does, until no process uses folder: no
// app process, updater, held LevelDB LOCK or write holder (which also
// covers a copy started with --user-data-dir on the folder). When it
// stays busy, the run is closed, Claude is relaunched if paritydesk quit
// it, and nothing is restored.
func (o *Orchestrator) quietForRollback(ctx context.Context, root *state.Root, bundlePath, folder string) error {
	d := o.d
	in := o.quiescenceInput(bundlePath, folder)
	deadline := o.now().Add(quiesceTimeout)
	for {
		rep, err := d.App.CheckQuiescence(ctx, in)
		if err == nil && rep.Quiet {
			return nil
		}
		if err != nil || !o.now().Before(deadline) {
			break
		}
		if err := d.Clock.Sleep(ctx, quiescePoll); err != nil {
			break
		}
	}
	quit := root.Run.QuitByUs
	root.Run = nil
	if err := o.commit(root); err != nil {
		return err
	}
	if quit {
		o.relaunch(context.WithoutCancel(ctx), root, "relaunched Claude")
	}
	return pderr.New(pderr.AppDidNotQuit, "rollback",
		"a process still uses the data folder; quit Claude (and any copy started with --user-data-dir), then run the rollback again; nothing was restored")
}

// finishRollback re-runs the conditional restore (idempotent), records
// the epoch event and sets the profile's bases per propagate. The caller
// closes the run.
func (o *Orchestrator) finishRollback(ctx context.Context, root *state.Root, m *compat.Manifest, n uint64, a ids.Alias, propagate bool, runID string) (gen.RestoreReport, error) {
	d := o.d
	man, err := d.Gens.Load(n)
	if err != nil {
		return gen.RestoreReport{}, pderr.Wrap(pderr.Internal, "rollback", err)
	}
	folder := o.rollbackFolder(root, a)
	in := gen.RestoreInput{N: n, Folder: folder, TmpDir: o.tmpDir(runID), ScratchRoot: scratchRootOf(m)}
	if hasLSEntries(man) {
		w, err := d.KV.OpenWritable(ctx, sources.LevelDBPath(folder))
		if err != nil {
			return gen.RestoreReport{}, lsOpenErr("rollback", err)
		}
		defer closeReader(w)
		in.LS = w
	}
	rep, err := d.Gens.Restore(ctx, in)
	if err != nil {
		return rep, pderr.Wrap(pderr.Internal, "rollback", err)
	}
	if isQuarantine(man) {
		// Leftovers moved back: no row changed, so no epoch event, no
		// live epoch and no base change.
		return rep, nil
	}
	if markEpochRecorded(root.Run) {
		root.Epoch++
	}
	ps := root.Profiles[a]
	if ps != nil && a == root.Active {
		ps.LiveEpoch = root.Epoch
	}
	if !propagate && ps != nil {
		o.rebaseRestored(ctx, root, m, folder, a, ps.Replica, man, rep)
	}
	return rep, nil
}

// rebaseRestored keeps a rollback local: the restored rows' bases become
// the restored state, so the next run does not carry it to other
// profiles. Rows with conflicts lose their base instead.
func (o *Orchestrator) rebaseRestored(ctx context.Context, root *state.Root, m *compat.Manifest, folder string, a ids.Alias, rep model.Replica, man *gen.Manifest, r gen.RestoreReport) {
	conflicted := map[model.RowID]bool{}
	for _, e := range r.Conflicts {
		conflicted[e.Row] = true
	}
	var rows []model.RowID
	seen := map[model.RowID]bool{}
	for _, e := range man.Entries {
		if !seen[e.Row] && e.Kind != gen.EntryConflict {
			seen[e.Row] = true
			rows = append(rows, e.Row)
		}
	}
	snaps, err := o.rereadRows(ctx, m, folder, rep, rows, nil)
	for _, row := range rows {
		k := state.BaseKey{Alias: a, Replica: rep, Row: row}.String()
		snap, ok := snaps[row]
		if err != nil || !ok || conflicted[row] {
			dropBase(root, k)
			continue
		}
		if ref, err := o.d.State.PutSnapshot(snap); err == nil {
			root.Bases[k] = ref
		} else {
			dropBase(root, k)
		}
	}
}

// markEpochRecorded flags the restoring intent so a re-run restore (recover)
// records the epoch event only once. It reports whether to record it now.
func markEpochRecorded(run *state.Run) bool {
	if run == nil {
		return true
	}
	for i := len(run.Intents) - 1; i >= 0; i-- {
		in := &run.Intents[i]
		if in.Name != state.IntentRestoring {
			continue
		}
		if in.Data["epoch"] == "recorded" {
			return false
		}
		data := map[string]string{"epoch": "recorded"}
		for k, v := range in.Data {
			if k != "epoch" {
				data[k] = v
			}
		}
		in.Data = data
		return true
	}
	return true
}

func hasLSEntries(m *gen.Manifest) bool {
	for _, e := range m.Entries {
		if e.Kind == gen.EntryLSValue {
			return true
		}
	}
	return false
}

func parkedName(d Deps, a ids.Alias) string {
	if p, ok := d.Config.Profile(a); ok {
		return p.ParkedFolder()
	}
	return ids.DefaultParkedFolder(a)
}

// Rebase drops every base; the next run is a first sync (M14).
func (o *Orchestrator) Rebase(ctx context.Context) error {
	if o.d.ReadOnly {
		return pderr.New(pderr.Usage, "rebase", "rebase writes; it cannot run in read-only mode")
	}
	l, err := o.d.State.Lock(ctx, 0)
	if err != nil {
		return err
	}
	defer releaseLock(l)
	root, err := o.loadRoot()
	if err != nil {
		return err
	}
	if root.Run != nil {
		return pderr.New(pderr.NeedsRecover, "rebase", "a run is open; run paritydesk recover first")
	}
	root.Bases = map[string]state.Ref{}
	root.Contested = nil
	for _, ps := range root.Profiles {
		ps.SeededFrom = ""
	}
	if err := o.commit(root); err != nil {
		return err
	}
	if err := o.d.State.GC(root); err != nil {
		o.progress(P10Commit, "warn", "unreferenced state objects could not be removed")
	}
	return nil
}

// isQuarantine reports whether a generation holds only directories that
// profile adopt --from-legacy moved aside.
func isQuarantine(m *gen.Manifest) bool {
	if m == nil || m.Purpose != gen.PurposeLegacyQuarantine || len(m.Entries) == 0 {
		return false
	}
	for _, e := range m.Entries {
		if e.Kind != gen.EntryQuarantineDir {
			return false
		}
	}
	return true
}

// parkedOrActive is the folder name that holds profile a now.
func parkedOrActive(d Deps, root *state.Root, a ids.Alias) string {
	if a == root.Active {
		return ids.DefaultFolder
	}
	return parkedName(d, a)
}
