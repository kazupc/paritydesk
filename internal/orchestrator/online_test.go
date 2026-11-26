package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/fsx/fsxtest"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/online"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/state"
)

// The online layer over the real dependencies with the 7.x manifest: the
// Rich fixture's whole-list sort is a writable sidebar intent, so every
// switch queues the probe, one list_sections read and one set_view.

// onlineWorld adds an online service (fsxtest store under the support
// directory, fake monotonic clock) to a real world.
type onlineWorld struct {
	*realWorld
	svc *online.Service
	// state replaces Deps.State when set (fault injection).
	state StateStore
}

func newOnlineWorld(t *testing.T) *onlineWorld {
	t.Helper()
	w := newRealWorld(t)
	store := online.NewStore(w.paths.SupportDir, fsxtest.New(t))
	var mu sync.Mutex
	store.Lock = func(context.Context, time.Duration) (func(), error) { mu.Lock(); return mu.Unlock, nil }
	svc := &online.Service{Store: store, Config: w.cfg.Online, Clock: w.clk, Mono: online.NewFakeMono("boot-1", 1e12),
		Providers: online.Registry()}
	return &onlineWorld{realWorld: w, svc: svc}
}

// alwaysPin is a test provider standing in for the real pins provider: it
// queues one pin step on every build, for runs whose sidebar has nothing
// to change (a sidebar field whose prior already equals the intent gets
// no step).
type alwaysPin struct{}

func (alwaysPin) Name() string                              { return "pins-test" }
func (alwaysPin) Kinds() []online.Kind                      { return []online.Kind{online.KindPin} }
func (alwaysPin) Questions(*online.Queue) []online.Question { return nil }
func (alwaysPin) Advance(*online.Queue, map[online.StepID]online.Result, time.Time) error {
	return nil
}
func (alwaysPin) Interpret(s online.Step, r online.Result) (online.StepStatus, string) {
	return online.InterpretBuiltin(s, r)
}
func (alwaysPin) Tools() online.ToolPolicy {
	return online.ToolPolicy{Kinds: []online.Kind{online.KindPin}, Exact: []string{"mcp__ccd_sidebar__set_pinned"}}
}
func (alwaysPin) Build(online.BuildInput) ([]online.Step, []online.Warning, error) {
	return []online.Step{{ID: "p000", Kind: online.KindPin, Tool: "mcp__ccd_sidebar__set_pinned",
		Input:  json.RawMessage(`{"pinned":true,"session_id":"local_00000000-0000-4000-8000-0000000000e1"}`),
		Status: online.StatusPending, Needs: []string{}, After: []online.StepID{},
		Expect: online.Expect{Format: online.FormatText, OK: []string{"Session pinned."}}}}, nil, nil
}
func (alwaysPin) CountSteps(steps []online.Step, c *online.Counts) {
	for _, s := range steps {
		if s.Kind == online.KindPin {
			c.PinsAdd++
		}
	}
}

func (w *onlineWorld) deps() Deps {
	d := w.deps7()
	d.Online = w.svc
	if w.state != nil {
		d.State = w.state
	}
	return d
}

func (w *onlineWorld) orch() *Orchestrator { return New(w.deps()) }

func (w *onlineWorld) switchTo(opts SwitchOptions) *Result {
	w.t.Helper()
	w.app.running = true
	res, err := w.orch().Switch(context.Background(), opts)
	if err != nil || res == nil || res.Code != pderr.OK {
		w.t.Fatalf("switch to %s: %v %+v", opts.To, err, res)
	}
	return res
}

func (w *onlineWorld) root() *state.Root {
	w.t.Helper()
	r, err := w.st.Load()
	if err != nil {
		w.t.Fatal(err)
	}
	return r
}

func (w *onlineWorld) queue() *online.Queue {
	w.t.Helper()
	q, err := w.svc.Store.ReadQueue(online.PhaseReconcile)
	if err != nil {
		w.t.Fatal(err)
	}
	return q
}

// checkPublished checks the queue against root.Online: same id, epoch
// equal to the root's, steps hash, profile.
func (w *onlineWorld) checkPublished(res *Result, profile string) *online.Queue {
	w.t.Helper()
	root := w.root()
	rec := root.Online
	q := w.queue()
	if rec == nil || q == nil {
		w.t.Fatalf("nothing published: record %+v queue %+v", rec, q)
	}
	if rec.Queue != res.RunID || q.ID != rec.Queue || rec.Epoch != root.Epoch || q.Epoch != root.Epoch ||
		string(rec.Profile) != profile || rec.Profile != root.Active || online.StepsSHA(q.Steps) != rec.StepsSHA {
		w.t.Fatalf("record %+v vs queue %s/%d (root epoch %d, active %s)", rec, q.ID, q.Epoch, root.Epoch, root.Active)
	}
	if online.Valid(q, rec, root.Epoch, root.Active, w.clk.Now()) != "" {
		w.t.Fatalf("published queue invalid: %s", online.Valid(q, rec, root.Epoch, root.Active, w.clk.Now()))
	}
	if res.Online == nil || !res.Online.Queued || !res.Online.Any() {
		w.t.Fatalf("result online %+v", res.Online)
	}
	return q
}

func TestOnlineSwitchPublishes(t *testing.T) {
	w := newOnlineWorld(t)
	res := w.switchTo(yes("b"))
	q := w.checkPublished(res, "b")
	if q.Step(online.ProbeStepID) == nil || q.LaunchedAt.IsZero() || res.Online.Sidebar == 0 {
		t.Fatalf("queue %+v, online %+v", q, res.Online)
	}
	if root := w.root(); root.Run != nil || root.Online.State != string(online.QueuePending) {
		t.Fatalf("root %+v", root.Online)
	}
	// Back to a, the sidebar sort a had is already a's: no sidebar step.
	// A test pin step keeps the next queue non-empty.
	w.svc.Providers = []online.Provider{online.ProbeProvider(), online.SidebarProvider(), alwaysPin{}}
	// The P2 preview of the next switch counts without publishing.
	prev, err := w.orch().Preview(context.Background(), PreviewOptions{To: "a"})
	if err != nil || prev.Online == nil || prev.Online.Queued || prev.Online.Sidebar != 0 || prev.Online.PinsAdd != 1 {
		t.Fatalf("preview online %+v %v", prev.Online, err)
	}
	if w.queue().ID != res.RunID {
		t.Fatal("the preview replaced the queue")
	}
	// The next switch supersedes the queue (and breaks its lease at P0).
	if _, err := w.svc.Next(context.Background(), online.NextCall{Caller: online.Caller{Session: "00000000-0000-4000-8000-0000000000c1"},
		Root: online.RootInfoOf(w.root())}); err != nil {
		t.Fatal(err)
	}
	if l, _ := w.svc.Store.ReadLease(); l == nil {
		t.Fatal("no lease to break")
	}
	res2 := w.switchTo(yes("a"))
	w.checkPublished(res2, "a")
	if l, _ := w.svc.Store.ReadLease(); l != nil {
		t.Fatal("P0 did not break the lease")
	}
}

func TestOnlineSyncKeepsEpoch(t *testing.T) {
	w := newOnlineWorld(t)
	w.switchTo(yes("b"))
	epoch := w.root().Epoch
	// A sync changes no sidebar value (prior equals intent), so a test pin
	// step gives it something to queue.
	w.svc.Providers = []online.Provider{online.ProbeProvider(), online.SidebarProvider(), alwaysPin{}}
	w.app.running = true
	res, err := w.orch().Sync(context.Background(), SyncOptions{Yes: true})
	if err != nil || res.Code != pderr.OK {
		t.Fatalf("sync: %v %+v", err, res)
	}
	q := w.checkPublished(res, "b")
	if w.root().Epoch != epoch || q.Epoch != epoch {
		t.Fatalf("sync moved the epoch: %d -> %d (queue %d)", epoch, w.root().Epoch, q.Epoch)
	}
}

func TestOnlineOffBuildsNothing(t *testing.T) {
	w := newOnlineWorld(t)
	w.cfg.Online.Reconcile = "off"
	res := w.switchTo(yes("b"))
	if res.Online != nil || w.root().Online != nil || w.queue() != nil {
		t.Fatalf("online off: %+v %+v", res.Online, w.root().Online)
	}
}

// reconcile drives the queue as the skill would, answering list_sections
// with the active folder's whole-list values and set_view as asked.
func (w *onlineWorld) reconcile(liveSort string) string {
	w.t.Helper()
	ctx := context.Background()
	me := online.Caller{Session: "00000000-0000-4000-8000-0000000000c1"}
	call := online.NextCall{Caller: me, Replica: func(context.Context, time.Time) online.ReplicaObs {
		r := w.root()
		return online.ReplicaObs{Known: true, Replica: r.Online.Replica}
	}}
	for i := 0; i < 20; i++ {
		call.Root = online.RootInfoOf(w.root())
		b, err := w.svc.Next(ctx, call)
		if err != nil {
			w.t.Fatal(err)
		}
		if b.Token != "" {
			call.Caller.Token = b.Token
		}
		switch b.State {
		case online.StateComplete:
			return call.Caller.Token
		case online.StateWait:
			w.clk.Advance(time.Duration(b.RetryAfterMS) * time.Millisecond)
			continue
		case online.StateCalls:
		default:
			w.t.Fatalf("state %s %v", b.State, b.Lines)
		}
		q := w.queue()
		for _, c := range b.Calls {
			s := q.Step(c.ID)
			text := ""
			switch c.Tool {
			case online.ToolGetSession:
				text = `{"sessionId":"local_00000000-0000-4000-8000-0000000000d1"}`
			case online.ToolListSections:
				text = `[{"id":"sessions","kind":"sessions","group_by":"project","sort_by":"` + liveSort + `"}]`
			case online.ToolSetView:
				var in map[string]string
				_ = json.Unmarshal(c.Input, &in)
				text = `{"changed":1,"view":{"group_by":"project","sort_by":"` + in["sort_by"] + `"}}`
			}
			resp, _ := json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": text}}})
			r := online.Result{V: 1, Queue: q.ID, Step: s.ID, Tool: s.Tool, Session: me.Session, At: w.clk.Now(), MonoNS: 1e12, BootID: "boot-1"}
			r.Outcome, r.Verdict, r.Parsed = online.Recognize(online.RecordInput{Step: *s}, resp)
			if err := w.svc.Store.CreateResult(r); err != nil {
				w.t.Fatal(err)
			}
		}
	}
	w.t.Fatal("reconcile did not complete")
	return ""
}

func TestOnlineConfirmAfterReconcile(t *testing.T) {
	w := newOnlineWorld(t)
	res := w.switchTo(yes("b"))
	q := w.checkPublished(res, "b")
	sv := q.Steps[len(q.Steps)-1]
	var data online.SidebarData
	if err := json.Unmarshal(sv.Data, &data); err != nil || data.Intended.SortBy == nil {
		t.Fatalf("sidebar step %+v", sv)
	}
	// The server put the other sort back after the launch; the step
	// writes the intended one.
	other := "alpha"
	if *data.Intended.SortBy == "alpha" {
		other = "recency"
	}
	if data.Prior.SortBy == nil || *data.Prior.SortBy != other {
		t.Skipf("fixture: prior %+v", data.Prior)
	}
	token := w.reconcile(other)
	out, err := w.orch().OnlineConfirm(context.Background(), OnlineConfirmInput{Queue: q.ID,
		Caller: online.Caller{Session: "00000000-0000-4000-8000-0000000000c1", Token: token}})
	if err != nil || out.State != string(online.QueueComplete) || out.RootBusy {
		t.Fatalf("done %+v %v", out, err)
	}
	root := w.root()
	if root.Online.State != string(online.QueueComplete) || !slices.Contains(root.Online.Confirmed, string(sv.ID)) {
		t.Fatalf("record %+v", root.Online)
	}
	// Confirming again changes nothing.
	again, err := w.orch().OnlineConfirm(context.Background(), OnlineConfirmInput{Queue: q.ID})
	if err != nil || again.BasesAdvanced != 0 {
		t.Fatalf("second done %+v %v", again, err)
	}
}

// TestOnlineDoneWithBusyRunLock: the queue is completed at once; the next
// verify applies the confirmation and records the queue complete.
func TestOnlineDoneWithBusyRunLock(t *testing.T) {
	w := newOnlineWorld(t)
	res := w.switchTo(yes("b"))
	q := w.checkPublished(res, "b")
	token := w.reconcile(func() string {
		var d online.SidebarData
		_ = json.Unmarshal(q.Steps[len(q.Steps)-1].Data, &d)
		if d.Prior.SortBy != nil {
			return *d.Prior.SortBy
		}
		return "recency"
	}())
	l, err := w.st.Lock(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	out, err := w.orch().OnlineConfirm(context.Background(), OnlineConfirmInput{Queue: q.ID,
		Caller: online.Caller{Session: "00000000-0000-4000-8000-0000000000c1", Token: token}})
	_ = l.Release()
	if err != nil || !out.RootBusy {
		t.Fatalf("done with the lock held: %+v %v", out, err)
	}
	if h, _ := w.svc.ReadHead(); h.State != online.QueueComplete || w.root().Online.State != string(online.QueuePending) {
		t.Fatalf("head %+v record %+v", h, w.root().Online)
	}
	w.app.running = true
	if _, err := w.orch().Verify(context.Background()); err != nil && pderr.CodeOf(err) != pderr.VerifyDrift {
		t.Fatal(err)
	}
	if rec := w.root().Online; rec.State != string(online.QueueComplete) {
		t.Fatalf("verify did not apply the confirmation: %+v", rec)
	}
}

// failOnce fails the first Commit after armed is set.
type failOnce struct {
	StateStore
	armed, failed bool
}

func (f *failOnce) Commit(r *state.Root) error {
	if f.armed && !f.failed && r.Run == nil && r.Online != nil {
		f.failed = true
		return errors.New("disk full")
	}
	return f.StateStore.Commit(r)
}

// TestOnlinePublishThenCrash: a run that published its queue but could
// not commit leaves an orphan queue.json; the draft survives GC in
// Run.Online, and recover (which completes a run past the park) publishes
// the same queue again and records it. (An orphan whose record never
// comes is removed by Cleanup: online's TestCleanupRemovesOrphanQueue.)
func TestOnlinePublishThenCrash(t *testing.T) {
	w := newOnlineWorld(t)
	f := &failOnce{StateStore: w.st, armed: true}
	w.state = f
	w.app.running = true
	res, err := w.orch().Switch(context.Background(), yes("b"))
	if err == nil || !f.failed {
		t.Fatalf("the commit did not fail: %v", err)
	}
	root := w.root()
	if root.Run == nil || root.Run.Online == "" || w.queue() == nil {
		t.Fatalf("no open run with a draft: %+v", root.Run)
	}
	if root.Online != nil && root.Online.Queue == res.RunID {
		t.Fatal("the failed commit's record was kept")
	}
	if err := w.st.GC(root); err != nil {
		t.Fatal(err)
	}
	var d online.Draft
	if err := w.st.GetObject(root.Run.Online, onlineDraftKind, &d); err != nil || d.Empty() {
		t.Fatalf("GC removed the draft: %v", err)
	}
	w.state = nil
	w.app.running = false
	if _, err := w.orch().Recover(context.Background(), RecoverOptions{Yes: true}); err != nil {
		t.Fatalf("recover: %v", err)
	}
	root = w.root()
	if root.Online == nil || root.Online.Queue != res.RunID {
		t.Fatalf("not republished: %+v", root.Online)
	}
	w.checkPublished(&Result{RunID: res.RunID, Online: &online.Summary{Queued: true, Sidebar: 1}}, "b")
}

func TestOnlineInvalidatedByRebaseRollbackRename(t *testing.T) {
	ctx := context.Background()
	w := newOnlineWorld(t)
	w.switchTo(yes("b"))
	if err := w.orch().Rebase(ctx); err != nil {
		t.Fatal(err)
	}
	if w.root().Online != nil || w.queue() != nil {
		t.Fatal("rebase kept the queue")
	}
	w = newOnlineWorld(t)
	w.switchTo(yes("b"))
	w.app.running = false
	w.ui.interactive = true
	if _, err := w.orch().Rollback(ctx, 0, false); err != nil {
		t.Fatal(err)
	}
	if w.root().Online != nil || w.queue() != nil {
		t.Fatal("rollback kept the queue")
	}
	w = newOnlineWorld(t)
	w.switchTo(yes("b"))
	w.switchTo(yes("a"))
	if err := w.orch().ProfileRename(ctx, "a", "c"); err != nil {
		t.Fatal(err)
	}
	if w.root().Online != nil || w.queue() != nil {
		t.Fatal("rename kept the queue")
	}
}

// TestOnlineStatusAndDoctor: status carries the record; doctor reports
// the queue (and the support directory rule).
func TestOnlineStatusAndDoctor(t *testing.T) {
	w := newOnlineWorld(t)
	w.switchTo(yes("b"))
	st, err := w.orch().Status(context.Background(), true)
	if err != nil || st.Online == nil || st.Online.State != "pending" || st.Online.Pending() == 0 {
		t.Fatalf("status %+v %v", st.Online, err)
	}
	checks, err := w.orch().Doctor(context.Background(), DoctorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var got *Check
	for i := range checks {
		if checks[i].ID == "online" {
			got = &checks[i]
		}
	}
	if got == nil || got.Status != "ok" || got.Detail != "Waiting changes in Claude: pending" {
		t.Fatalf("doctor online %+v", got)
	}
	w.paths.SupportDir = filepath.Join(w.home, "elsewhere")
	checks, _ = w.orch().Doctor(context.Background(), DoctorOptions{})
	for _, c := range checks {
		if c.ID == "online" && c.Status != "warn" {
			t.Fatalf("non-default support dir: %+v", c)
		}
	}
	_ = model.RowSidebarView
}
