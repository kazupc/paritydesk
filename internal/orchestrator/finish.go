package orchestrator

import (
	"context"
	"fmt"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/gen"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/state"
)

// rereadRows reads rows of the folder now holding a profile, over a fresh
// snapshot (the app is stopped).
func (o *Orchestrator) rereadRows(ctx context.Context, m *compat.Manifest, folder string, rep model.Replica, rs []model.RowID) (map[model.RowID]*model.RowSnapshot, error) {
	out := map[model.RowID]*model.RowSnapshot{}
	if m == nil || len(rs) == 0 {
		return out, nil
	}
	ls, err := o.lsReader(ctx, folder)
	if err != nil {
		return nil, err
	}
	defer closeReader(ls)
	for _, r := range rs {
		ad := o.d.Adapters[r]
		spec, ok := o.rowSpec(m, r)
		if ad == nil || !ok {
			continue
		}
		snap, err := ad.Read(ctx, sources.ReadContext{Folder: folder, Replica: rep, LS: ls, Spec: spec.Expand(rep), Manifest: m})
		if err != nil {
			return nil, pderr.Wrap(pderr.Internal, fmt.Sprintf("re-read %s", r), err)
		}
		out[r] = snap
	}
	return out, nil
}

// commitP10 re-reads T, sets its bases from the re-read (only where it
// equals Desired), and closes the run in one commit.
func (r *run) commitP10(ctx context.Context) error {
	defer r.phase(P10Commit, "recording")()
	d := r.d()
	root := r.root
	reread, err := r.o.rereadRows(ctx, r.manifest, r.tPath, r.T.Replica, r.p5.Rows)
	if err != nil {
		r.warn(P10Commit, "the entering folder could not be re-read; its bases are invalid until the next run")
		reread = nil
	}
	for _, row := range r.p5.Rows {
		w := r.p5.Work[row]
		tk := w.TKey.String()
		snap, ok := reread[row]
		if !ok || !sameItems(snap, w.Out.Desired) {
			dropBase(root, tk)
			if reread != nil {
				r.warn(P10Commit, fmt.Sprintf("%s: the entering folder does not read back as planned; its base is invalid", row))
			}
			continue
		}
		ref, err := d.State.PutSnapshot(snap)
		if err != nil {
			return pderr.Wrap(pderr.Internal, "state", err)
		}
		root.Bases[tk] = ref
		if row == model.RowSessions {
			r.tSess = snap
		}
		if len(w.Stamps) > 0 {
			if root.ToolStamps == nil {
				root.ToolStamps = map[string]map[string]int64{}
			}
			if root.ToolStamps[tk] == nil {
				root.ToolStamps[tk] = map[string]int64{}
			}
			for k, v := range w.Stamps {
				root.ToolStamps[tk][k] = v
			}
		}
	}
	if r.tSess == nil && r.invoking != "" {
		if snaps, err := r.o.rereadRows(ctx, r.manifest, r.tPath, r.T.Replica, []model.RowID{model.RowSessions}); err == nil {
			r.tSess = snaps[model.RowSessions]
		}
	}
	r.recordProfiles()
	if r.gen != 0 {
		root.Generations = append(root.Generations, state.GenerationRef{N: r.gen, RunID: r.res.RunID, Target: r.T.Profile.Alias, At: r.o.now()})
	}
	r.quitByUs = root.Run.QuitByUs
	root.Run = nil
	if err := r.o.commit(root); err != nil {
		return err
	}
	if err := d.State.GC(root); err != nil {
		r.warn(P10Commit, "unreferenced state objects could not be removed")
	}
	if err := d.Gens.Prune(r.o.prunePolicy()); err != nil {
		r.warn(P10Commit, "old backup generations could not be pruned")
	}
	return nil
}

// recordProfiles updates both profiles' state: replicas, folder ids,
// marker base, epoch, active and previous.
func (r *run) recordProfiles() {
	root := r.root
	t := r.profileState(r.T.Profile.Alias)
	t.Replica = r.T.Replica
	t.Replicas = appendReplica(t.Replicas, r.T.Replica)
	t.AccountHistory = appendAccount(t.AccountHistory, r.T.Replica.Account)
	if r.gen != 0 {
		t.MarkerBase = r.p5.Plan.Hash()
	}
	if id, err := r.d().FS.ID(r.tPath); err == nil {
		t.FolderID = id
	}
	if r.kind == state.RunSync {
		t.LiveEpoch = root.Epoch
		return
	}
	s := r.profileState(r.S.Profile.Alias)
	s.LiveEpoch = root.Epoch
	if id, err := r.d().FS.ID(r.sPath); err == nil {
		s.FolderID = id
	}
	root.Epoch++
	t.LiveEpoch = root.Epoch
	root.Active, root.Previous = r.T.Profile.Alias, r.S.Profile.Alias
}

// profileState returns (creating) a profile's state.
func (r *run) profileState(a ids.Alias) *state.ProfileState {
	ps := r.root.Profiles[a]
	if ps == nil {
		ps = &state.ProfileState{Alias: a}
		r.root.Profiles[a] = ps
	}
	return ps
}

// prunePolicy reads the backup retention settings.
func (o *Orchestrator) prunePolicy() gen.PrunePolicy {
	b := o.d.Config.Backups
	return gen.PrunePolicy{Keep: b.Keep, LevelDBClones: b.LevelDBClonesKeep, MaxBytes: parseSize(b.MaxSize)}
}

// launch is P11: launch only when I4 holds, then reopen the invoking
// session if T has it.
func (r *run) launch(ctx context.Context) {
	defer r.phase(P11Launch, "launching")()
	d := r.d()
	if r.opts.NoLaunch {
		r.o.progress(P11Launch, "ok", "not launched (--no-launch)")
		return
	}
	if !r.quitByUs {
		r.o.progress(P11Launch, "ok", "Claude was not running before; not launched")
		return
	}
	if !r.o.claudeRegistered(ctx, r.root) {
		r.warn(P11Launch, "Claude was not launched: the active folder holds no registered profile")
		return
	}
	if n, err := d.App.LogSize(); err == nil {
		r.launchAt = n
	}
	if err := d.App.Launch(ctx, d.Config.App.BundleID); err != nil {
		r.warn(P11Launch, "Claude could not be launched; open it manually")
		return
	}
	r.launched = true
	if r.opts.NoReopen || r.invoking == "" {
		return
	}
	if r.tSess == nil || !hasItem(r.tSess, string(r.invoking)) {
		r.warn(P11Launch, "the invoking session is not in the entering profile; it was not reopened")
		return
	}
	if err := d.App.OpenSession(ctx, r.invoking); err != nil {
		r.warn(P11Launch, "the invoking session could not be reopened")
	}
}

func hasItem(s *model.RowSnapshot, key string) bool {
	if s == nil {
		return false
	}
	_, ok := s.Items[key]
	return ok
}

// verifyP12 runs P12 after a launch, for detached and non-interactive
// runs (an interactive terminal is told to run paritydesk verify).
func (r *run) verifyP12(ctx context.Context) error {
	if !r.launched {
		return nil
	}
	if r.interactive {
		r.o.progress(P12Verify, "ok", "run paritydesk verify to confirm the values after launch")
		return nil
	}
	defer r.phase(P12Verify, "verifying")()
	_, err := r.o.verifyCore(ctx, verifyIn{
		root: r.root, alias: r.T.Profile.Alias, replica: r.T.Replica, manifest: r.manifest, sig: r.sig,
		from: r.launchAt, session: r.invoking, runID: r.res.RunID, rows: r.p5.Rows,
	})
	return err
}

// finalCode combines the verify outcome and the mode into the result.
func (r *run) finalCode(verifyErr error) error {
	if verifyErr != nil {
		r.res.Code = pderr.CodeOf(verifyErr)
		return verifyErr
	}
	switch r.decision.Mode {
	case compat.ModeSwapOnly:
		r.res.Code = pderr.SwapOnly
		return pderr.New(pderr.SwapOnly, "switch", "switched without syncing (swap-only mode)")
	case compat.ModeDegraded:
		r.res.Code = pderr.Degraded
		return pderr.New(pderr.Degraded, "switch", "some rows were skipped")
	}
	r.res.Code = pderr.OK
	return nil
}
