package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/gen"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/rows"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/sources/pins"
	"github.com/kazupc/paritydesk/internal/state"
	"github.com/kazupc/paritydesk/internal/store/idb"
	"github.com/kazupc/paritydesk/internal/store/kv"
)

// rereadRows reads rows of the folder now holding a profile, over a fresh
// snapshot (the app is stopped). It opens one Local Storage reader and
// reads every row with readRowLS. A sources.RefReader row is read with
// ReadRefs(refs[r]) when refs has an entry for it, else it is left out of
// the result (never Read). A row whose source is unavailable
// (sources.ErrSourceUnavailable) is left out and the others are read; any
// other error fails the call. Row pins is read without side facts (the
// strictest rules); rereadRowsPins gives it the facts of a run.
func (o *Orchestrator) rereadRows(ctx context.Context, m *compat.Manifest, folder string, rep model.Replica, rs []model.RowID, refs map[model.RowID][]string) (map[model.RowID]*model.RowSnapshot, error) {
	return o.rereadRowsPins(ctx, m, folder, rep, rs, refs, nil)
}

// pinsReread gives the pins read of a re-read the side facts a run's read
// had (spec-pins 6.4 items 5 to 7, 9.3: the probe is checked at P10 and
// at recover too). When Facts.StarredIDs is nil it is filled from the
// sessions snapshot of the same re-read, or from a read of the sessions
// adapter when that row is not re-read. Report, when not nil, receives
// the read's report (ProbePassed decides whether the probe is cleared).
type pinsReread struct {
	Facts  sources.SideFacts
	Report *sources.ReadReport
}

// rereadRowsPins is rereadRows with side facts for row pins (nil: none).
func (o *Orchestrator) rereadRowsPins(ctx context.Context, m *compat.Manifest, folder string, rep model.Replica, rs []model.RowID, refs map[model.RowID][]string, pr *pinsReread) (map[model.RowID]*model.RowSnapshot, error) {
	out := map[model.RowID]*model.RowSnapshot{}
	if m == nil || len(rs) == 0 {
		return out, nil
	}
	ls, err := o.lsReader(ctx, folder)
	if err != nil {
		return nil, err
	}
	defer closeReader(ls)
	if _, ok := refs[model.RowScratch]; ok {
		o.scratchAdapter() // the same limits and excludes as the run's read
	}
	for _, r := range rs {
		rowRefs, have := refs[r]
		var snap *model.RowSnapshot
		var err error
		if r == model.RowPins && pr != nil {
			snap, err = o.rereadPinsRow(ctx, m, folder, rep, ls, out[model.RowSessions], pr)
		} else {
			snap, err = o.readRowLS(ctx, m, folder, rep, r, ls, rowRefs, have)
		}
		switch {
		case errors.Is(err, sources.ErrSourceUnavailable):
			continue
		case err != nil:
			return nil, pderr.Wrap(pderr.Internal, fmt.Sprintf("re-read %s", r), err)
		case snap == nil:
			continue // no adapter, no spec, or a RefReader row without refs
		}
		out[r] = snap
	}
	return out, nil
}

// rereadPinsRow reads row pins of folder with the side facts of pr.
// sessions is the sessions snapshot of the same re-read (nil: the sessions
// adapter is read here, read only). When no sessions snapshot can be had
// and the facts carry no isStarred evidence, the row is unavailable (left
// out), as a sessions read error is fatal to a run's pins read.
func (o *Orchestrator) rereadPinsRow(ctx context.Context, m *compat.Manifest, folder string, rep model.Replica, ls kv.Reader, sessions *model.RowSnapshot, pr *pinsReread) (*model.RowSnapshot, error) {
	ad := o.d.Adapters[model.RowPins]
	spec, ok := o.rowSpec(m, model.RowPins)
	if ad == nil || !ok {
		return nil, nil
	}
	facts := pr.Facts
	facts.StarredIDs = maps.Clone(facts.StarredIDs)
	if facts.StarredIDs == nil {
		if sessions == nil {
			s, err := o.readRowLS(ctx, m, folder, rep, model.RowSessions, ls, nil, false)
			if err != nil || s == nil {
				return nil, &pins.Unavailable{Reason: idb.ReasonOther, Cause: err}
			}
			sessions = s
		}
		facts.StarredIDs = starredIDs(sessions)
	}
	rc := sources.ReadContext{Folder: folder, Replica: rep, LS: ls, Spec: spec.Expand(rep), Manifest: m, Side: &facts, Report: pr.Report}
	return ad.Read(ctx, rc)
}

// readRowLS reads row r of folder over ls. A sources.RefReader row is read
// with ReadRefs(refs) when haveRefs is true; without refs it is not read
// at all and (nil, nil) is returned. A row without an adapter or without a
// manifest spec also returns (nil, nil). Adapter errors are returned
// unwrapped, so callers can test for sources.ErrSourceUnavailable.
func (o *Orchestrator) readRowLS(ctx context.Context, m *compat.Manifest, folder string, rep model.Replica, r model.RowID, ls kv.Reader, refs []string, haveRefs bool) (*model.RowSnapshot, error) {
	ad := o.d.Adapters[r]
	spec, ok := o.rowSpec(m, r)
	if ad == nil || !ok {
		return nil, nil
	}
	rc := sources.ReadContext{Folder: folder, Replica: rep, LS: ls, Spec: spec.Expand(rep), Manifest: m}
	if _, isRef := ad.(sources.RefReader); isRef && !haveRefs {
		return nil, nil
	}
	return readAdapter(ctx, ad, rc, refs)
}

// commitP10 re-reads T, sets its bases from the re-read (only where it
// equals Desired), and closes the run in one commit.
func (r *run) commitP10(ctx context.Context) error {
	defer r.phase(P10Commit, "recording")()
	d := r.d()
	root := r.root
	// T's pins re-read gets the side facts of its P5 read: the probe is
	// checked at P10 too (spec-pins 9.3), and only a read that passed it
	// clears it.
	var pr *pinsReread
	if r.p5.Pins != nil && r.p5.Pins.TFacts != nil {
		pr = &pinsReread{Facts: *r.p5.Pins.TFacts, Report: &sources.ReadReport{}}
	}
	reread, err := r.o.rereadRowsPins(ctx, r.manifest, r.tPath, r.T.Replica, r.p5.Rows, r.scratchRereadRefs(), pr)
	if err != nil {
		r.warn(P10Commit, "the entering folder could not be re-read; its bases are invalid until the next run")
		reread = nil
	}
	for _, row := range r.p5.Rows {
		w := r.p5.Work[row]
		tk := w.TKey.String()
		snap, ok := reread[row]
		want := w.Out.Desired
		if row == model.RowScratch {
			want = r.scratchExpected(w)
		}
		probePassed := false
		if onRow(r.manifest, row) {
			// Nothing was applied offline: the base is T's re-read (or its
			// P5 read when the re-read left the row out); the online
			// changes move it once they are confirmed. The probe goes only
			// with the read that becomes the base and passed it.
			if !ok {
				snap = w.T
				probePassed = r.p5.Pins != nil && r.p5.Pins.TProbePassed
			} else if pr != nil && row == model.RowPins {
				probePassed = pr.Report.ProbePassed
			}
			if snap == nil {
				dropBase(root, tk)
				continue
			}
		} else if !ok || !sameRead(snap, want, rows.CarriesOrder(row)) {
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
		if row == model.RowPins && probePassed {
			clearPinsProbe(root, r.T.Profile.Alias)
		}
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
		if snaps, err := r.o.rereadRows(ctx, r.manifest, r.tPath, r.T.Replica, []model.RowID{model.RowSessions}, nil); err == nil {
			r.tSess = snaps[model.RowSessions]
		}
	}
	r.recordProfiles()
	prevOnline := root.Online
	published := publishOnline(ctx, r.o, root, r.manifest, r.draft, r.res.RunID, r.T.Profile.Alias, r.S.Profile.Alias, r.T.Replica)
	if published {
		prev := r.res.Online
		r.res.Online = onlineSummary(r.draft, true)
		keepPinsSummary(r.res.Online, prev)
	}
	if r.bundle.Version != "" {
		root.LastAppVersion = r.bundle.Version
	}
	root.LastCommitAt = r.o.now()
	if r.gen != 0 {
		root.Generations = append(root.Generations, state.GenerationRef{N: r.gen, RunID: r.res.RunID, Target: r.T.Profile.Alias, At: r.o.now()})
	}
	r.quitByUs = root.Run.QuitByUs
	run := root.Run
	root.Run = nil
	if err := r.o.commit(root); err != nil {
		// The run stays open: the queue just published is an orphan
		// (recover republishes it from Run.Online or removes it).
		root.Run, root.Online = run, prevOnline
		return err
	}
	r.o.afterPublish(ctx, published)
	if err := d.State.GC(root); err != nil {
		r.warn(P10Commit, "unreferenced state objects could not be removed")
	}
	r.saveScratchCache()
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
	// The leaving replica's org was known for this run (from the log while
	// it was active); a parked folder of an account in several orgs
	// cannot tell it, so it is kept here.
	s.Replica = r.S.Replica
	s.Replicas = appendReplica(s.Replicas, r.S.Replica)
	if r.sMarked {
		s.MarkerBase = r.p5.Plan.Hash()
	}
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
	t0 := r.o.now()
	if err := d.App.Launch(ctx, d.Config.App.BundleID); err != nil {
		r.warn(P11Launch, "Claude could not be launched; open it manually")
		return
	}
	r.launched = true
	r.onlineLaunched(ctx, t0)
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

// verifyP12 runs P12 after a launch. A terminal run hands the check to a
// detached job, so the terminal is free as soon as Claude is up; the
// SessionStart hook reports what the job finds. If the job cannot be
// started, the run checks here.
func (r *run) verifyP12(ctx context.Context) error {
	if !r.launched {
		return nil
	}
	if r.interactive && !r.opts.FromRunner && r.o.d.Detach != nil {
		g := JobGlobals{Config: r.o.d.Paths.ConfigFile, StateDir: r.o.d.Paths.StateDir, AppSupportDir: r.o.d.Paths.SupportDir}
		if _, err := r.o.d.Detach.Submit(ctx, VerifyJobArgs(r.res.RunID, g)); err == nil {
			r.o.progress(P12Verify, "ok", "Claude is up; the values are checked in the background (paritydesk verify shows the result)")
			return nil
		}
	}
	defer r.phase(P12Verify, "verifying")()
	_, err := r.o.verifyCore(ctx, verifyIn{
		root: r.root, alias: r.T.Profile.Alias, replica: r.T.Replica, manifest: r.manifest, sig: r.sig,
		from: r.launchAt, session: r.invoking, runID: r.res.RunID, rows: r.p5.Rows, record: true,
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
