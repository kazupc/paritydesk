package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/online"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/state"
)

// fakeArtifacts is the archive view.
type fakeArtifacts struct {
	due      map[ids.Alias]int
	epochs   map[ids.Alias]uint64
	writes   []string
	renames  [][2]ids.Alias
	removes  []ids.Alias
	writeErr error
	// committedEpoch is root's epoch when WriteDue ran (after the commit).
	st             *fakeState
	committedEpoch uint64
	doctorErr      error
}

func (f *fakeArtifacts) Due(a ids.Alias, _ model.Replica) (int, error) { return f.due[a], nil }
func (f *fakeArtifacts) LastSyncEpoch(a ids.Alias) (uint64, bool, error) {
	e, ok := f.epochs[a]
	return e, ok, nil
}
func (f *fakeArtifacts) WriteDue(_ context.Context, a ids.Alias, _ model.Replica, epoch uint64) error {
	f.writes = append(f.writes, string(a))
	if f.st != nil {
		if r, err := f.st.Load(); err == nil && r != nil {
			f.committedEpoch = r.Epoch
		}
	}
	_ = epoch
	return f.writeErr
}
func (f *fakeArtifacts) RenameProfile(_ context.Context, from, to ids.Alias) error {
	f.renames = append(f.renames, [2]ids.Alias{from, to})
	return nil
}
func (f *fakeArtifacts) RemoveProfile(_ context.Context, a ids.Alias) error {
	f.removes = append(f.removes, a)
	return nil
}
func (f *fakeArtifacts) Doctor() (int, string, error) { return 3, "off (capture only)", f.doctorErr }

func artifactsEnv(t *testing.T) (*env, *fakeArtifacts) {
	t.Helper()
	e := newEnv(t)
	e.compat.m.Artifacts.Enabled, e.compat.m.Online.Enabled = true, true
	f := &fakeArtifacts{due: map[ids.Alias]int{"b": 2}, epochs: map[ids.Alias]uint64{"a": 7}, st: e.st}
	e.customize = func(d *Deps) { d.Artifacts = f }
	return e, f
}

// TestArtifactsPlanLines: P2 shows the leaving side's unsynced line and
// the entering side's due count; neither is part of the plan hash.
func TestArtifactsPlanLines(t *testing.T) {
	e, f := artifactsEnv(t)
	res, err := e.orch().Preview(context.Background(), PreviewOptions{To: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Artifacts == nil || res.Artifacts.Unsynced != "a" || res.Artifacts.Due != 2 || res.Artifacts.To != "b" {
		t.Fatalf("%+v", res.Artifacts)
	}
	f.due, f.epochs = map[ids.Alias]int{}, map[ids.Alias]uint64{}
	res2, err := e.orch().Preview(context.Background(), PreviewOptions{To: "b"})
	if err != nil || res2.Artifacts != nil {
		t.Fatalf("%+v %v", res2.Artifacts, err)
	}
	if res.PlanHash != res2.PlanHash {
		t.Fatal("artifact lines changed the plan hash")
	}
	// Synced while active: no unsynced line.
	root := e.st.root(t)
	f.epochs = map[ids.Alias]uint64{"a": root.Epoch}
	res3, _ := e.orch().Preview(context.Background(), PreviewOptions{To: "b"})
	if res3.Artifacts != nil {
		t.Fatalf("%+v", res3.Artifacts)
	}
	// The online layer off (online.reconcile = "off"): nothing.
	f.due = map[ids.Alias]int{"b": 1}
	e.cfg.Online.Reconcile = config.OnlineReconcileOff
	if res4, _ := e.orch().Preview(context.Background(), PreviewOptions{To: "b"}); res4.Artifacts != nil {
		t.Fatal("lines with the online layer off")
	}
	e.cfg.Online.Reconcile = config.OnlineReconcilePrompt
	// Off in the manifest: nothing.
	e.compat.m.Artifacts.Enabled = false
	f.due = map[ids.Alias]int{"b": 1}
	if res4, _ := e.orch().Preview(context.Background(), PreviewOptions{To: "b"}); res4.Artifacts != nil {
		t.Fatal("lines with artifact sync off")
	}
}

// TestArtifactsDueAtP10: due.json is written for T after the commit; a
// busy archive lock does not fail the run.
func TestArtifactsDueAtP10(t *testing.T) {
	e, f := artifactsEnv(t)
	res, err := e.orch().Switch(context.Background(), yes("b"))
	if err != nil || res.Code != pderr.OK {
		t.Fatalf("%v %+v", err, res)
	}
	root := e.st.root(t)
	if len(f.writes) != 1 || f.writes[0] != "b" || f.committedEpoch != root.Epoch || root.Active != "b" {
		t.Fatalf("writes %v at epoch %d (root %d)", f.writes, f.committedEpoch, root.Epoch)
	}
	f.writeErr = pderr.New(pderr.RunLockBusy, "artifacts", "busy")
	res, err = e.orch().Switch(context.Background(), yes("a"))
	if err != nil || res.Code != pderr.OK {
		t.Fatalf("busy archive failed the run: %v %+v", err, res)
	}
}

func TestArtifactsProfileRenameRemove(t *testing.T) {
	e, f := artifactsEnv(t)
	// Rename and remove reach the archive also while artifact sync is
	// off, so its copies never stay under an old alias.
	e.cfg.Sync.Artifacts = false
	e.cfg.Online.Reconcile = config.OnlineReconcileOff
	if err := e.orch().ProfileRename(context.Background(), "b", "c"); err != nil {
		t.Fatal(err)
	}
	if len(f.renames) != 1 || f.renames[0] != [2]ids.Alias{"b", "c"} {
		t.Fatalf("%v", f.renames)
	}
	if err := e.orch().ProfileRemove(context.Background(), "c"); err != nil {
		t.Fatal(err)
	}
	if len(f.removes) != 1 || f.removes[0] != "c" {
		t.Fatalf("%v", f.removes)
	}
}

func TestDoctorArtifacts(t *testing.T) {
	e, f := artifactsEnv(t)
	checks, err := e.orch().Doctor(context.Background(), DoctorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	last := checks[len(checks)-1]
	if last.ID != "artifacts" || last.Status != "ok" {
		t.Fatalf("%+v", last)
	}
	// Artifact sync off in the config: "off", whatever the archive says.
	e.cfg.Sync.Artifacts = false
	f.doctorErr = errors.New("manifest.json is not valid")
	checks, _ = e.orch().Doctor(context.Background(), DoctorOptions{})
	if last := checks[len(checks)-1]; last.Status != "ok" {
		t.Fatalf("sync off: %+v", last)
	}
	e.cfg.Sync.Artifacts = true
	checks, _ = e.orch().Doctor(context.Background(), DoctorOptions{})
	if last := checks[len(checks)-1]; last.Status != "fail" {
		t.Fatalf("%+v", last)
	}
}

// TestArtifactsRound: a round is published under the run lock and
// recorded as an artifacts queue; closing marks it complete; an open
// run refuses.
func TestArtifactsRound(t *testing.T) {
	w := newOnlineWorld(t)
	o := w.orch()
	root := w.root()
	d := online.Draft{Steps: []online.Step{online.ProbeStep()}}
	_ = d
	sha, err := o.ArtifactsRound(context.Background(), func(r *state.Root) (*online.Draft, *online.QueueHeader, error) {
		return nil, nil, nil
	})
	if err != nil || sha != "" || root.Online != nil {
		t.Fatalf("no draft: %q %v", sha, err)
	}
	r := w.root()
	r.Run = &state.Run{ID: "run-1"}
	if err := w.st.Commit(r); err != nil {
		t.Fatal(err)
	}
	_, err = o.ArtifactsRound(context.Background(), func(*state.Root) (*online.Draft, *online.QueueHeader, error) { return nil, nil, nil })
	if pderr.CodeOf(err) != pderr.NeedsRecover {
		t.Fatalf("open run: %v", err)
	}
	r.Run = nil
	_ = w.st.Commit(r)
	if closed, err := o.CloseArtifactsQueue(context.Background(), "art-20260102T030405Z-00000001-1"); err != nil || !closed {
		t.Fatalf("close without a record: %v %v", closed, err)
	}
}

// TestSwitchSupersedesArtifactsQueue: a switch breaks an artifacts
// round's lease at P0 and replaces the round's record at P10.
func TestSwitchSupersedesArtifactsQueue(t *testing.T) {
	w := newOnlineWorld(t)
	o := w.orch()
	root := w.root()
	steps := []online.Step{online.ProbeStep()}
	d := online.Draft{Steps: steps}
	id := "art-20260102T030405Z-0a0b0c0d-1"
	if _, err := o.ArtifactsRound(context.Background(), func(r *state.Root) (*online.Draft, *online.QueueHeader, error) {
		return &d, &online.QueueHeader{ID: id, Profile: r.Active, From: r.Active, Replica: r.Profiles[r.Active].Replica, Epoch: r.Epoch}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if rec := w.root().Online; rec == nil || rec.Kind != "artifacts" || rec.Queue != id {
		t.Fatalf("record %+v", rec)
	}
	other := ids.Alias("b")
	for a := range root.Profiles {
		if a != root.Active {
			other = a
		}
	}
	w.switchTo(SwitchOptions{To: other, Yes: true, ConfirmFirstSync: true, LogOffset: -1})
	if rec := w.root().Online; rec != nil && rec.Queue == id {
		t.Fatal("the artifacts round survived the switch")
	}
	if l, _ := w.svc.Store.ReadLease(); l != nil && l.Queue == id {
		t.Fatal("lease of the round kept")
	}
}

// TestBusyArtifactsDoneClosedByVerify: when `artifacts done` found the run
// lock busy, the next run-lock holder (here verify) records the round's
// queue complete in root.Online (spec-artifacts 6.4).
func TestBusyArtifactsDoneClosedByVerify(t *testing.T) {
	w := newOnlineWorld(t)
	o := w.orch()
	d := online.Draft{Steps: []online.Step{online.ProbeStep()}}
	id := "art-20260102T030405Z-0a0b0c0d-1"
	if _, err := o.ArtifactsRound(context.Background(), func(r *state.Root) (*online.Draft, *online.QueueHeader, error) {
		return &d, &online.QueueHeader{ID: id, Profile: r.Active, From: r.Active, Replica: r.Profiles[r.Active].Replica, Epoch: r.Epoch}, nil
	}); err != nil {
		t.Fatal(err)
	}
	q, err := w.svc.Store.ReadQueue(online.PhaseReconcile)
	if err != nil || q == nil || q.ID != id {
		t.Fatalf("queue %+v %v", q, err)
	}
	q.State = online.QueueComplete
	if err := w.svc.Store.WriteQueue(q); err != nil {
		t.Fatal(err)
	}
	if rec := w.root().Online; rec.State != string(online.QueuePending) {
		t.Fatalf("record %+v", rec)
	}
	w.app.running = true
	if _, err := w.orch().Verify(context.Background()); err != nil && pderr.CodeOf(err) != pderr.VerifyDrift {
		t.Fatal(err)
	}
	if rec := w.root().Online; rec == nil || rec.State != string(online.QueueComplete) || rec.Kind != "artifacts" {
		t.Fatalf("verify did not close the artifacts record: %+v", rec)
	}
}
