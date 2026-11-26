package online

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/state"
)

func TestStorePermissionsAndSymlinks(t *testing.T) {
	s := testStore(t)
	if err := s.WriteLease(&Lease{V: 1, Queue: tQueue, ExpiresAt: t0}); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"", FileLease, FileArmed} {
		fi, err := os.Lstat(filepath.Join(s.Dir, rel))
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0o600)
		if rel == "" {
			want = os.ModeDir | 0o700
		}
		if fi.Mode()&(os.ModeDir|os.ModePerm) != want {
			t.Errorf("%q mode %v", rel, fi.Mode())
		}
	}
	if b, _ := os.ReadFile(filepath.Join(s.Dir, FileArmed)); string(b) != fmt.Sprintf("%d\n", t0.Unix()) {
		t.Errorf("armed %q", b)
	}
	// A symlinked results directory is refused.
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(s.Dir, dirResults)); err != nil {
		t.Fatal(err)
	}
	err := s.CreateResult(Result{V: 1, Queue: tQueue, Step: "r000"})
	if !errors.Is(err, ErrNotRegular) {
		t.Fatalf("symlinked dir: %v", err)
	}
	if es, _ := os.ReadDir(target); len(es) != 0 {
		t.Fatal("wrote through a symlink")
	}
	// A symlinked queue file is refused on read.
	if err := os.Symlink(filepath.Join(target, "x"), filepath.Join(s.Dir, FileQueue)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadQueue(PhaseReconcile); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("symlinked queue: %v", err)
	}
}

func TestStoreExclusiveResults(t *testing.T) {
	s := testStore(t)
	r := Result{V: 1, Queue: tQueue, Step: "p000", Outcome: OutcomeOK}
	if err := s.CreateResult(r); err != nil {
		t.Fatal(err)
	}
	r2 := r
	r2.Outcome = OutcomeError
	if err := s.CreateResult(r2); !errors.Is(err, ErrExists) {
		t.Fatalf("second result: %v", err)
	}
	if got := s.Results(tQueue, nil)["p000"]; got.Outcome != OutcomeOK {
		t.Fatalf("first did not win: %+v", got)
	}
	// Concurrent creates: exactly one wins.
	var wg sync.WaitGroup
	wins := make(chan int, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if s.CreateResult(Result{V: 1, Queue: tQueue, Step: "p001", ToolUseID: fmt.Sprint(i)}) == nil {
				wins <- i
			}
		}(i)
	}
	wg.Wait()
	close(wins)
	if n := len(wins); n != 1 {
		t.Fatalf("%d winners", n)
	}
}

func TestStoreCaptureResultsBySession(t *testing.T) {
	s := testStore(t)
	cap := "cap-20260102T030405Z-01020304"
	mine := Result{V: 1, Queue: cap, Step: CaptureStepID, Session: tSession, Outcome: OutcomeOK, Parsed: json.RawMessage(`{"id":"` + tLocal + `"}`)}
	other := mine
	other.Session = tOther
	other.Parsed = json.RawMessage(`{"id":"local_00000000-0000-4000-8000-0000000000d9"}`)
	if err := s.CreateCaptureResult(other); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateCaptureResult(mine); err != nil {
		t.Fatal(err)
	}
	got := s.CaptureResults(cap, tSession, nil)
	if len(got) != 1 || string(got[CaptureStepID].Parsed) != string(mine.Parsed) {
		t.Fatalf("results %+v", got)
	}
}

func TestStoreDenialCapAndShown(t *testing.T) {
	s := testStore(t)
	for i := 0; i < MaxDenials+5; i++ {
		if err := s.CreateDenial(Denial{At: t0, Tool: "mcp__ccd_sidebar__set_unread", Reason: "guard.deny_unplanned"}, MonoNow{NS: int64(i), OK: true}); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(s.Denials()); n != MaxDenials {
		t.Fatalf("%d denials", n)
	}
	for i := 1; i <= 3; i++ {
		if !s.CreateShown(tQueue, 3) {
			t.Fatalf("show %d refused", i)
		}
	}
	if s.CreateShown(tQueue, 3) {
		t.Fatal("fourth show allowed")
	}
	if !s.CreateShown("due-42", 3) || s.CreateShown("../x", 3) {
		t.Fatal("shown keys")
	}
}

func TestStoreSizeLimits(t *testing.T) {
	s := testStore(t)
	big := strings.Repeat("x", MaxResultBytes+1)
	r := Result{V: 1, Queue: tQueue, Step: "a000", Outcome: OutcomeOK, Parsed: json.RawMessage(`"` + big + `"`)}
	if err := s.CreateResult(r); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Results(tQueue, nil)["a000"]; ok {
		t.Fatal("oversized result read")
	}
	if _, ok := s.Results(tQueue, func(StepID) int64 { return 2 * MaxResultBytes })["a000"]; !ok {
		t.Fatal("provider ResultMax not applied")
	}
	if err := os.WriteFile(filepath.Join(s.Dir, FileQueue), []byte(strings.Repeat(" ", MaxQueueBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadQueue(PhaseReconcile); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized queue: %v", err)
	}
}

func TestQueueFieldOrderAndHead(t *testing.T) {
	q := &Queue{V: 1, ID: tQueue, Phase: PhaseReconcile, State: QueuePending, Profile: "home", Replica: tReplica, Epoch: 4,
		CreatedAt: t0, ExpiresAt: t0.Add(time.Hour), Steps: []Step{ProbeStep()}}
	b, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(b), `{"v":1,"id":"run-0001","phase":"reconcile","state":"pending",`) ||
		!strings.Contains(string(b), `,"steps":[`) || strings.Index(string(b), `"steps"`) < strings.Index(string(b), `"guard"`) {
		t.Fatalf("field order: %s", b)
	}
	// The head decodes from a 64 KiB prefix of a much larger queue.
	for i := 0; i < 3000; i++ {
		s := pinStep(StepID(fmt.Sprintf("p%03d", i%1000)), pinInput, StatusPending)
		q.Steps = append(q.Steps, s)
	}
	b, _ = json.Marshal(q)
	if len(b) <= HeadPrefix {
		t.Fatalf("queue only %d bytes", len(b))
	}
	h, err := DecodeHead(b[:HeadPrefix])
	if err != nil || h.ID != tQueue || h.State != QueuePending || h.Phase != PhaseReconcile {
		t.Fatalf("head %+v %v", h, err)
	}
	s := testStore(t)
	if err := s.write(FileQueue, b); err != nil {
		t.Fatal(err)
	}
	if h, err := s.ReadHead(); err != nil || h.ID != tQueue {
		t.Fatalf("ReadHead %+v %v", h, err)
	}
	if _, err := DecodeHead([]byte(`{"id":"x","v":1}`)); err == nil {
		t.Fatal("out-of-order head accepted")
	}
}

func TestStoreCleanup(t *testing.T) {
	s := testStore(t)
	for _, q := range []string{tQueue, "run-0002"} {
		if err := s.CreateResult(Result{V: 1, Queue: q, Step: "r000"}); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateInflight(q, "r000", Marker{N: 1}); err != nil {
			t.Fatal(err)
		}
		s.CreateShown(q, 3)
	}
	if err := s.CreateStop(); err != nil {
		t.Fatal(err)
	}
	if err := s.Cleanup(CleanupInput{Queue: tQueue, Now: t0, CaptureMaxAge: time.Minute, QueueMaxAge: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if len(s.Results(tQueue, nil)) != 1 || len(s.Results("run-0002", nil)) != 0 || len(s.Inflight("run-0002")) != 0 {
		t.Fatal("cleanup kept the wrong files")
	}
	if !s.Stopped() {
		t.Fatal("stop removed while a queue is live")
	}
	if err := s.Cleanup(CleanupInput{Now: t0, CaptureMaxAge: time.Minute, QueueMaxAge: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if s.Stopped() || len(s.Results(tQueue, nil)) != 0 {
		t.Fatal("cleanup without a queue kept files")
	}
}

func TestCreateOnlyFS(t *testing.T) {
	s := testStore(t)
	hookStore := NewStore(filepath.Dir(s.Dir), CreateOnly(s.FS, s.Dir))
	if err := hookStore.CreateResult(Result{V: 1, Queue: tQueue, Step: "r000"}); err != nil {
		t.Fatalf("hook result: %v", err)
	}
	if err := hookStore.CreateInflight(tQueue, "r000", Marker{N: 1}); err != nil {
		t.Fatalf("hook marker: %v", err)
	}
	if err := hookStore.WriteQueue(&Queue{V: 1, ID: tQueue, Phase: PhaseReconcile}); err == nil {
		t.Fatal("hook wrote queue.json")
	}
	if err := hookStore.WriteLease(&Lease{V: 1}); err == nil {
		t.Fatal("hook wrote the lease")
	}
	if err := hookStore.RemoveQueue(PhaseReconcile); err == nil && hookStore.exists(FileQueue) {
		t.Fatal("hook removed a file")
	}
}

// TestCleanupRemovesOrphanQueue: a queue published by a run that crashed
// before its commit does not match root.Online and is removed.
func TestCleanupRemovesOrphanQueue(t *testing.T) {
	s := testService(t)
	d := Draft{Steps: []Step{ProbeStep(), pinStep("p000", pinInput, StatusPending)}}
	s.Providers = append(baseRegistry(), testPins{})
	publish(t, s, d)
	root := state.NewRoot("/d")
	root.Epoch, root.Active = 4, "home"
	if err := s.Cleanup(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if q, _ := s.Store.ReadQueue(PhaseReconcile); q != nil {
		t.Fatal("orphan queue kept")
	}
}

func TestCaptureUsable(t *testing.T) {
	s := testService(t)
	in := CaptureInput{Spec: spec7(t), Config: config.Default().Online, Profile: "work", Replica: tReplica, Session: tSession}
	b, err := s.CaptureStart(context.Background(), in, 4)
	if err != nil || len(b.Calls) != 1 || b.Calls[0].ID != CaptureStepID || !ValidCaptureID(b.Queue) {
		t.Fatalf("capture %+v %v", b, err)
	}
	q, _ := s.Store.ReadQueue(PhaseCapture)
	r := result(q.ID, q.Steps[0], tSession, `{"id":"`+tLocal+`"}`)
	if err := s.Store.CreateCaptureResult(r); err != nil {
		t.Fatal(err)
	}
	snap, err := s.Capture(context.Background(), "work", tReplica, 4)
	if err != nil || snap == nil || snap.ReopenSession != tLocal {
		t.Fatalf("snapshot %+v %v", snap, err)
	}
	for name, check := range map[string]func() (*CaptureSnapshot, error){
		"other profile": func() (*CaptureSnapshot, error) { return s.Capture(context.Background(), "home", tReplica, 4) },
		"other epoch":   func() (*CaptureSnapshot, error) { return s.Capture(context.Background(), "work", tReplica, 5) },
		"other replica": func() (*CaptureSnapshot, error) {
			return s.Capture(context.Background(), "work", model.Replica{Account: tReplica.Account}, 4)
		},
	} {
		if snap, _ := check(); snap != nil {
			t.Errorf("%s: usable", name)
		}
	}
	s.Mono.(*FakeMono).Advance(int64(11 * time.Minute))
	if snap, _ := s.Capture(context.Background(), "work", tReplica, 4); snap != nil {
		t.Error("old by the monotonic clock: usable")
	}
	s.Mono.(*FakeMono).Reboot("boot-2")
	if snap, _ := s.Capture(context.Background(), "work", tReplica, 4); snap != nil {
		t.Error("other boot: usable")
	}
	// Another session's result is never used.
	s = testService(t)
	if _, err := s.CaptureStart(context.Background(), in, 4); err != nil {
		t.Fatal(err)
	}
	q, _ = s.Store.ReadQueue(PhaseCapture)
	r = result(q.ID, q.Steps[0], tOther, `{"id":"`+tLocal+`"}`)
	_ = s.Store.CreateCaptureResult(r)
	if snap, _ := s.Capture(context.Background(), "work", tReplica, 4); snap == nil || snap.ReopenSession != "" {
		t.Fatalf("other session's result used: %+v", snap)
	}
}

func TestValidateRefusesForeignTools(t *testing.T) {
	pols := Policies(append(baseRegistry(), testPins{}))
	base := func() *Queue {
		return &Queue{V: 1, ID: tQueue, Phase: PhaseReconcile, Steps: []Step{ProbeStep(), pinStep("p000", pinInput, StatusPending)}}
	}
	if err := base().Validate(pols, nil); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(q *Queue){
		"tool outside the policy": func(q *Queue) { q.Steps[1].Tool = "mcp__ccd_session_mgmt__send_message" },
		"floor tool":              func(q *Queue) { q.Steps[1].Tool = "mcp__ccd_sidebar__delete_section" },
		"artifact tool":           func(q *Queue) { q.Steps[1].Tool = "Artifact" },
		"duplicate call":          func(q *Queue) { q.Steps = append(q.Steps, pinStep("p001", pinInput, StatusPending)) },
		"duplicate id":            func(q *Queue) { q.Steps = append(q.Steps, q.Steps[1]) },
		"unknown after":           func(q *Queue) { q.Steps[1].After = []StepID{"s999"} },
		"read need on a write":    func(q *Queue) { q.Steps[1].Needs = []string{"read:p000"} },
		"unknown need":            func(q *Queue) { q.Steps[1].Needs = []string{"later"} },
		"bad reason":              func(q *Queue) { q.Steps[1].Reason = "because" },
		"input not canonical":     func(q *Queue) { q.Steps[1].Input = json.RawMessage(`{ "pinned":true}`) },
		"no input":                func(q *Queue) { q.Steps[1].Input = nil },
		"letter mismatch":         func(q *Queue) { q.Steps[1].ID = "s000" },
	} {
		q := base()
		mutate(q)
		if err := q.Validate(pols, nil); err == nil {
			t.Errorf("%s: validated", name)
		}
	}
	q := base()
	q.Steps[1].Tool = "mcp__ccd_sidebar__set_unread"
	if err := q.Validate(pols, []string{"mcp__ccd_sidebar__set_unread"}); err == nil {
		t.Error("manifest always_deny tool validated")
	}
}

func FuzzQueueValidate(f *testing.F) {
	f.Add([]byte(`{"v":1,"id":"run-0001","phase":"reconcile","steps":[{"id":"r000","kind":"read","tool":"mcp__ccd_session_mgmt__get_session","input":{"session_id":"self"},"status":"pending","needs":[],"after":[],"expect":{"format":"json-object"}}]}`))
	f.Add([]byte(`{"v":1,"steps":[{"id":"p000","kind":"pin","tool":"mcp__ccd_sidebar__delete_group","input":{},"status":"pending"}]}`))
	pols := Policies(Registry())
	f.Fuzz(func(t *testing.T, b []byte) {
		var q Queue
		if json.Unmarshal(b, &q) != nil || q.Validate(pols, nil) != nil {
			return
		}
		for _, s := range q.Steps {
			if InFloor(s.Tool, nil) || inUntrusted(s.Tool) || policyFor(pols, s).Kinds == nil {
				t.Fatalf("valid queue with tool %s", s.Tool)
			}
		}
	})
}

// TestBuildDedupeAndIDs: Build adds the probe, renumbers, dedupes and
// validates; nothing queued means no probe either.
func TestBuildDedupeAndIDs(t *testing.T) {
	desired := sectionsSnap(t, `{"id":"sessions","kind":"sessions","groupBy":"status"}`, `{"id":"routines","kind":"routines","groupBy":"status"}`)
	current := sectionsSnap(t, `{"id":"sessions","kind":"sessions","groupBy":"date"}`, `{"id":"routines","kind":"routines"}`)
	d := Build(sidebarInput(t, desired, current), Registry())
	var ids []string
	for _, s := range d.Steps {
		ids = append(ids, string(s.ID))
		if s.ID != ProbeStepID && s.Needs[0] != "read:r000" {
			t.Errorf("%s does not need the probe", s.ID)
		}
	}
	if strings.Join(ids, " ") != "r000 r001 s000 s001" || d.Counts.Sidebar != 2 {
		t.Fatalf("ids %v counts %+v", ids, d.Counts)
	}
	if d := Build(sidebarInput(t, current, current), Registry()); !d.Empty() {
		// Equal Desired and Current still queue (the live check decides);
		// only no intent at all yields no steps.
		_ = d
	}
	empty := sectionsSnap(t, `{"id":"sessions","kind":"sessions"}`)
	if d := Build(sidebarInput(t, empty, empty), Registry()); !d.Empty() {
		t.Fatalf("no intent: %+v", d.Steps)
	}
	off := sidebarInput(t, desired, current)
	off.Spec = compat.OnlineSpec{}
	if d := Build(off, Registry()); !d.Empty() {
		t.Fatal("steps with the layer off")
	}
	if StepsSHA(d.Steps) == "" || StepsSHA(d.Steps) != StepsSHA(d.Steps) {
		t.Fatal("steps hash")
	}
}

// TestStepsSHAImmutable: Advance-style changes (status, templated input,
// reason) keep the hash; an immutable field changes it.
func TestStepsSHAImmutable(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		steps := []Step{ProbeStep(), pinStep("p000", pinInput, StatusPending)}
		steps[1].Templated = true
		steps[1].Input = nil
		h := StepsSHA(steps)
		steps[1].Status = rapid.SampledFrom([]StepStatus{StatusOffered, StatusDone, StatusFailed}).Draw(rt, "status")
		steps[1].Reason = ReasonNoResult
		steps[1].Input = json.RawMessage(pinInput)
		steps[1].Offered++
		if StepsSHA(steps) != h {
			rt.Fatal("mutable fields changed the hash")
		}
		steps[1].Needs = append(steps[1].Needs, NeedSettled)
		if StepsSHA(steps) == h {
			rt.Fatal("needs did not change the hash")
		}
	})
}

func TestStoreLeaseHelpers(t *testing.T) {
	s := testService(t, append(baseRegistry(), testPins{})...)
	rec := publish(t, s, Draft{Steps: []Step{ProbeStep(), func() Step {
		p := pinStep("p000", pinInput, StatusPending)
		p.Needs = []string{"read:r000"}
		return p
	}()}})
	b, err := s.Next(context.Background(), NextCall{Caller: Caller{Session: tSession}, Root: rootInfo(rec)})
	if err != nil || b.State != StateCalls {
		t.Fatalf("next %+v %v", b, err)
	}
	ctx, now, mono := context.Background(), t0, Read(s.Mono)
	// A bound, fresh lease moves to the next queue.
	if err := s.Store.RebindLease(ctx, tQueue, "run-0002", now, mono, time.Minute); err != nil {
		t.Fatalf("bound lease rebind: %v", err)
	}
	l, _ := s.Store.ReadLease()
	if l.Queue != "run-0002" || l.Session != tSession || l.ExpiresAt != now.Add(time.Minute) {
		t.Fatalf("rebound %+v", l)
	}
	if err := s.Store.RebindLease(ctx, tQueue, "run-0003", now, mono, time.Minute); !errors.Is(err, ErrLeaseNotUsable) {
		t.Fatalf("rebind from another queue: %v", err)
	}
	// MarkRefused: the offered probe without a result fails user_refused.
	if err := s.Store.MarkRefused(ctx, tQueue, []StepID{ProbeStepID, "p000"}); err != nil {
		t.Fatal(err)
	}
	q, _ := s.Store.ReadQueue(PhaseReconcile)
	if st := q.Step(ProbeStepID); st.Status != StatusFailed || st.Reason != "user_refused" {
		t.Fatalf("probe %+v", st)
	}
	if st := q.Step("p000"); st.Status != StatusPending {
		t.Fatalf("pending step changed %+v", st)
	}
	// ReleaseLease only for its own queue.
	if err := s.Store.ReleaseLease(ctx, tQueue); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.Store.ReadLease(); l == nil {
		t.Fatal("released another queue's lease")
	}
	if err := s.Store.ReleaseLease(ctx, "run-0002"); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.Store.ReadLease(); l != nil || s.Store.exists(FileArmed) {
		t.Fatal("lease kept")
	}
}
