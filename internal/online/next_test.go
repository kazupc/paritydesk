package online

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/kazupc/paritydesk/internal/clock"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/state"
)

// nh drives one queue through `online next` with the real store.
type nh struct {
	t       testing.TB
	s       *Service
	rec     *state.OnlineRecord
	token   string
	replica ReplicaObs
	answers int
}

func (h *nh) clk() *clock.Fake { return h.s.Clock.(*clock.Fake) }
func (h *nh) mono() *FakeMono  { return h.s.Mono.(*FakeMono) }
func (h *nh) advance(d time.Duration) {
	h.clk().Advance(d)
	h.mono().Advance(int64(d))
}

func (h *nh) next(c Caller, unavailable ...string) Batch {
	h.t.Helper()
	if c.Token == "" {
		c.Token = h.token
	}
	b, err := h.s.Next(context.Background(), NextCall{Caller: c, Root: rootInfo(h.rec), Unavailable: unavailable,
		Replica: func(context.Context, time.Time) ReplicaObs { return h.replica }})
	if err != nil {
		h.t.Fatal(err)
	}
	if b.Token != "" {
		h.token = b.Token
	}
	h.answers++
	return b
}

func (h *nh) queue() *Queue {
	h.t.Helper()
	q, err := h.s.Store.ReadQueue(PhaseReconcile)
	if err != nil {
		h.t.Fatal(err)
	}
	return q
}

// record writes a result for a step as PostToolUse would.
func (h *nh) record(id StepID, parsed string) {
	h.t.Helper()
	q := h.queue()
	s := q.Step(id)
	r := result(q.ID, *s, tSession, parsed)
	r.At = h.clk().Now()
	if err := h.s.Store.CreateResult(r); err != nil {
		h.t.Fatal(err)
	}
}

// recordResp recognises a raw response for a step.
func (h *nh) recordResp(id StepID, resp json.RawMessage) {
	h.t.Helper()
	q := h.queue()
	s := q.Step(id)
	r := result(q.ID, *s, tSession, "")
	r.At = h.clk().Now()
	r.Outcome, r.Verdict, r.Parsed = Recognize(RecordInput{Step: *s, ToolsOff: q.Guard.ToolsOff}, resp)
	if err := h.s.Store.CreateResult(r); err != nil {
		h.t.Fatal(err)
	}
}

// marker writes an in-flight marker as PreToolUse would.
func (h *nh) marker(id StepID, n int) {
	h.t.Helper()
	m := h.mono()
	now, _ := m.Now()
	if err := h.s.Store.CreateInflight(tQueue, id, Marker{N: n, At: h.clk().Now(), MonoNS: now.NS, BootID: now.BootID}); err != nil {
		h.t.Fatal(err)
	}
}

func probeOK() string { return `{"id":"` + tLocal + `"}` }

// pinQueue publishes the probe and n pin steps (no cards when card is
// false).
func pinQueue(t testing.TB, n int, card bool, question bool) *nh {
	t.Helper()
	s := testService(t, append(baseRegistry(), testPins{question: question})...)
	steps := []Step{ProbeStep()}
	for i := 0; i < n; i++ {
		in := fmt.Sprintf(`{"pinned":true,"session_id":"local_00000000-0000-4000-8000-%012d"}`, i+1)
		p := pinStep(StepID(fmt.Sprintf("p%03d", i)), in, StatusPending)
		p.Card = card
		if !question {
			p.Needs = []string{"read:r000"}
		}
		steps = append(steps, p)
	}
	rec := publish(t, s, Draft{Steps: steps, Counts: Counts{PinsAdd: n}})
	return &nh{t: t, s: s, rec: rec, replica: ReplicaObs{Known: true, Replica: tReplica}}
}

// sidebarQueue publishes a whole-list group-by change.
func sidebarQueue(t testing.TB) *nh {
	t.Helper()
	s := testService(t)
	d := Build(sidebarInput(t,
		sectionsSnap(t, `{"id":"sessions","kind":"sessions","groupBy":"status"}`),
		sectionsSnap(t, `{"id":"sessions","kind":"sessions","groupBy":"date"}`)), s.Providers)
	if d.Empty() {
		t.Fatal("empty draft")
	}
	rec := publish(t, s, d)
	return &nh{t: t, s: s, rec: rec, replica: ReplicaObs{Known: true, Replica: tReplica}}
}

const sessionsLive = `{"sections":[{"id":"sessions","kind":"sessions","group_by":"date","sort_by":"recency"}]}`

func callIDs(b Batch) []StepID {
	var out []StepID
	for _, c := range b.Calls {
		out = append(out, c.ID)
	}
	return out
}

func TestNextProbeQuestionCallsComplete(t *testing.T) {
	h := pinQueue(t, 2, true, true)
	me := Caller{Session: tSession}
	b := h.next(me)
	if b.State != StateCalls || fmt.Sprint(callIDs(b)) != "[r000]" || b.Token == "" || fmt.Sprint(b.Tools) != "["+ToolGetSession+"]" {
		t.Fatalf("first %+v", b)
	}
	first := b.RemainingBatches
	h.record(ProbeStepID, probeOK())
	b = h.next(me)
	if b.State != StateQuestion || b.Question == nil || b.Question.ID != "pins" {
		t.Fatalf("second %+v", b)
	}
	if err := h.s.Store.Locked(context.Background(), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if v, err := h.s.Decide(context.Background(), Caller{Session: tSession, Token: h.token}, rootInfo(h.rec), "pins", "apply"); err != nil || v.State != StateRecorded {
		t.Fatalf("decide %+v %v", v, err)
	}
	b = h.next(me)
	if b.State != StateCalls || fmt.Sprint(callIDs(b)) != "[p000 p001]" {
		t.Fatalf("third %+v", b)
	}
	h.record("p000", `{"matched":0}`)
	h.record("p001", `{"matched":0}`)
	b = h.next(me)
	if b.State != StateComplete || b.Counts.Done != 2 || h.queue().State != QueueComplete {
		t.Fatalf("fourth %+v", b)
	}
	if h.answers > first+4 {
		t.Fatalf("%d answers, bound %d", h.answers, first+4)
	}
	if l, _ := h.s.Store.ReadLease(); l != nil {
		t.Fatal("lease kept after complete")
	}
}

func TestNextNoHooks(t *testing.T) {
	h := pinQueue(t, 1, false, false)
	me := Caller{Session: tSession}
	if b := h.next(me); fmt.Sprint(callIDs(b)) != "[r000]" {
		t.Fatalf("first %+v", b)
	}
	if b := h.next(me); fmt.Sprint(callIDs(b)) != "[r000]" {
		t.Fatalf("re-offer %+v", b)
	}
	b := h.next(me)
	if b.State != StateNoHooks || len(b.Calls) != 0 {
		t.Fatalf("third %+v", b)
	}
	if q := h.queue(); q.State != QueuePending || q.Step("p000").Status != StatusPending || q.Step(ProbeStepID).Status != StatusPending {
		t.Fatalf("queue %+v", q)
	}
	if l, _ := h.s.Store.ReadLease(); l != nil || h.s.Store.exists(FileArmed) {
		t.Fatal("lease or armed kept after no_hooks")
	}
}

func TestNextMarkerWithoutResult(t *testing.T) {
	// The probe: a marker without a result after the grace is no_hooks.
	h := pinQueue(t, 1, false, false)
	me := Caller{Session: tSession}
	h.next(me)
	h.marker(ProbeStepID, 1)
	if b := h.next(me); b.State != StateWait || b.RetryAfterMS <= 0 {
		t.Fatalf("within grace %+v", b)
	}
	h.advance(11 * time.Minute)
	if b := h.next(Caller{Session: tSession}); b.State != StateNoHooks {
		t.Fatalf("past grace %+v", b)
	}
	// A write: failed(no_result), then a late result resolves it.
	h = pinQueue(t, 1, false, false)
	h.next(me)
	h.record(ProbeStepID, probeOK())
	if b := h.next(me); fmt.Sprint(callIDs(b)) != "[p000]" {
		t.Fatalf("write %+v", b)
	}
	h.marker("p000", 1)
	h.advance(11 * time.Minute)
	b := h.next(me)
	if s := h.queue().Step("p000"); s.Status != StatusFailed || s.Reason != ReasonNoResult || !s.EverUnknown || b.State != StateComplete {
		t.Fatalf("no_result %+v %+v", s, b)
	}
	// The queue is complete; a late result still resolves the step for
	// the confirmation (online done, verify, P0).
	h.record("p000", `{"matched":0}`)
	steps, err := h.s.Resolved(context.Background(), tQueue)
	if err != nil || len(steps) != 2 || steps[1].Status != StatusDone || !steps[1].EverUnknown {
		t.Fatalf("late result %+v %v", steps, err)
	}
}

func TestNextNotCalledAndRetry(t *testing.T) {
	h := pinQueue(t, 1, false, false)
	me := Caller{Session: tSession}
	h.next(me)
	h.record(ProbeStepID, probeOK())
	h.next(me) // offers p000
	h.next(me) // not called: offered again
	b := h.next(me)
	if s := h.queue().Step("p000"); s.Status != StatusFailed || s.Reason != ReasonNotCalled || b.State != StateComplete {
		t.Fatalf("not_called %+v %+v", s, b)
	}
	v, err := h.s.Retry(context.Background(), Caller{Session: tSession, Token: h.token}, rootInfo(h.rec))
	if err != nil || v.Count != 1 {
		t.Fatalf("retry %+v %v", v, err)
	}
	// The lease went with the complete answer: a new one proves the hooks
	// again with the probe, then the step is offered once more.
	if b := h.next(me); fmt.Sprint(callIDs(b)) != "[r000]" {
		t.Fatalf("after retry %+v", b)
	}
	h.record(ProbeStepID, probeOK())
	if b := h.next(me); fmt.Sprint(callIDs(b)) != "[p000]" {
		t.Fatalf("after retry %+v", b)
	}
	if q := h.queue(); q.Step("p000").Retries != 1 {
		t.Fatal("retries not recorded")
	}
	// A second retry is not granted.
	h.next(me)
	h.next(me)
	if v, _ := h.s.Retry(context.Background(), Caller{Session: tSession, Token: h.token}, rootInfo(h.rec)); v.Count != 0 {
		t.Fatalf("second retry %+v", v)
	}
}

func TestNextSkipAndUnavailable(t *testing.T) {
	h := pinQueue(t, 2, true, true)
	me := Caller{Session: tSession}
	h.next(me)
	h.record(ProbeStepID, probeOK())
	h.next(me)
	if _, err := h.s.Decide(context.Background(), Caller{Session: tSession, Token: h.token}, rootInfo(h.rec), "pins", "skip"); err != nil {
		t.Fatal(err)
	}
	b := h.next(me)
	q := h.queue()
	if b.State != StateComplete || q.Step("p000").Reason != ReasonDecisionSkip || q.Step("p001").Reason != ReasonDecisionSkip {
		t.Fatalf("skip %+v %+v", b, q.Steps)
	}
	// An unknown question or option is a usage error.
	h = pinQueue(t, 1, true, true)
	h.next(me)
	h.record(ProbeStepID, probeOK())
	h.next(me)
	_, err := h.s.Decide(context.Background(), Caller{Session: tSession, Token: h.token}, rootInfo(h.rec), "pins", "maybe")
	if pderr.CodeOf(err) != pderr.Usage {
		t.Fatalf("unknown option: %v", err)
	}
	// --unavailable fails the steps of that tool.
	h = pinQueue(t, 1, false, false)
	h.next(me)
	h.record(ProbeStepID, probeOK())
	h.next(me)
	b = h.next(me, toolSetPinnedFull)
	if s := h.queue().Step("p000"); s.Status != StatusFailed || s.Reason != ReasonToolsUnavailable || b.State != StateComplete {
		t.Fatalf("unavailable %+v %+v", s, b)
	}
}

func TestNextBreakerOptions(t *testing.T) {
	h := pinQueue(t, 1, true, true)
	me := Caller{Session: tSession}
	h.next(me)
	h.record(ProbeStepID, probeOK())
	b := h.next(me)
	if b.Question == nil || len(b.Question.Options) != 3 || b.Question.Options[1].ID != "add_only" {
		t.Fatalf("question %+v", b.Question)
	}
	if _, err := h.s.Decide(context.Background(), Caller{Session: tSession, Token: h.token}, rootInfo(h.rec), "pins", "add_only"); err != nil {
		t.Fatal(err)
	}
	if b := h.next(me); fmt.Sprint(callIDs(b)) != "[p000]" || h.queue().Decisions["pins"] != "add_only" {
		t.Fatalf("after add_only %+v", b)
	}
}

// snapshotFiles returns every file under dir with its content.
func snapshotFiles(t testing.TB, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() {
			b, _ := os.ReadFile(p)
			out[p] = string(b)
		}
		return nil
	})
	return out
}

func TestNextLeaseContention(t *testing.T) {
	h := pinQueue(t, 1, false, false)
	h.next(Caller{Session: tSession})
	h.record(ProbeStepID, probeOK())
	before := snapshotFiles(t, h.s.Store.Dir)
	b, err := h.s.Next(context.Background(), NextCall{Caller: Caller{Session: tOther}, Root: rootInfo(h.rec),
		Replica: func(context.Context, time.Time) ReplicaObs { return h.replica }})
	if err != nil || b.State != StateBusyElsewhere || b.Token != "" {
		t.Fatalf("other caller %+v %v", b, err)
	}
	after := snapshotFiles(t, h.s.Store.Dir)
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatal("busy_elsewhere changed files")
	}
	// The lease's own session without the token gets a new token.
	old := h.token
	b, _ = h.s.Next(context.Background(), NextCall{Caller: Caller{Session: tSession}, Root: rootInfo(h.rec),
		Replica: func(context.Context, time.Time) ReplicaObs { return h.replica }})
	if b.Token == "" || b.Token == old || b.State == StateBusyElsewhere {
		t.Fatalf("same session %+v", b)
	}
	// The old token no longer works for another session.
	b, _ = h.s.Next(context.Background(), NextCall{Caller: Caller{Session: tOther, Token: old}, Root: rootInfo(h.rec)})
	if b.State != StateBusyElsewhere {
		t.Fatalf("old token %+v", b)
	}
}

func TestNextUnboundLeaseBindsOnProbe(t *testing.T) {
	h := pinQueue(t, 1, false, false)
	h.next(Caller{})
	l, _ := h.s.Store.ReadLease()
	if l == nil || l.Session != "" || l.ExpiresAt.Sub(t0) != 3*time.Minute {
		t.Fatalf("unbound lease %+v", l)
	}
	h.record(ProbeStepID, probeOK())
	h.next(Caller{})
	l, _ = h.s.Store.ReadLease()
	if l == nil || l.Session != tSession || l.ExpiresAt.Sub(t0) != 20*time.Minute {
		t.Fatalf("bound lease %+v", l)
	}
}

func TestNextSidebarWaitsForSettle(t *testing.T) {
	h := sidebarQueue(t)
	me := Caller{Session: tSession}
	h.next(me)
	h.record(ProbeStepID, probeOK())
	b := h.next(me)
	if b.State != StateWait || b.RetryAfterMS != 45000 {
		t.Fatalf("wait %+v", b)
	}
	h.advance(45 * time.Second)
	b = h.next(me)
	if fmt.Sprint(callIDs(b)) != "[r001]" {
		t.Fatalf("read %+v", b)
	}
	h.record("r001", sessionsLive)
	b = h.next(me)
	if len(b.Calls) != 1 || b.Calls[0].Tool != ToolSetView || string(b.Calls[0].Input) != `{"group_by":"state"}` {
		t.Fatalf("set_view %+v", b)
	}
	h.recordResp(b.Calls[0].ID, textResp(`{"changed":1,"view":{"group_by":"state","status":"active","sort_by":"recency","last_activity":0}}`, false))
	if b := h.next(me); b.State != StateComplete || b.Counts.Done != 1 {
		t.Fatalf("complete %+v", b)
	}
}

func TestNextReplica(t *testing.T) {
	// Another org after the launch: stale, nothing offered, queue gone.
	h := sidebarQueue(t)
	h.replica = ReplicaObs{Known: true, Replica: tReplica}
	h.next(Caller{Session: tSession})
	h.record(ProbeStepID, probeOK())
	h.replica = ReplicaObs{Known: true, Replica: tReplica}
	h.replica.Replica.Org = "00000000-0000-4000-8000-000000000009"
	b := h.next(Caller{Session: tSession})
	if b.State != StateStale || len(b.Calls) != 0 || h.queue() != nil {
		t.Fatalf("replica changed %+v", b)
	}
	// Unknown: server-synced steps wait, then are skipped; pins proceed.
	s := testService(t, append(baseRegistry(), testPins{})...)
	d := Build(sidebarInput(t,
		sectionsSnap(t, `{"id":"sessions","kind":"sessions","groupBy":"status"}`),
		sectionsSnap(t, `{"id":"sessions","kind":"sessions","groupBy":"date"}`)), Registry())
	p := pinStep("p000", pinInput, StatusPending)
	p.Needs, p.Card = []string{"read:r000"}, false
	d.Steps = append(d.Steps, p)
	h = &nh{t: t, s: s, rec: publish(t, s, d)}
	h.next(Caller{Session: tSession})
	h.record(ProbeStepID, probeOK())
	h.advance(time.Minute)
	b = h.next(Caller{Session: tSession})
	if fmt.Sprint(callIDs(b)) != "[r001]" {
		t.Fatalf("unknown replica, read %+v", b)
	}
	h.record("r001", sessionsLive)
	b = h.next(Caller{Session: tSession})
	if fmt.Sprint(callIDs(b)) != "[p000]" {
		t.Fatalf("pins proceed %+v", b)
	}
	h.record("p000", `{"matched":0}`)
	if b = h.next(Caller{Session: tSession}); b.State != StateWait {
		t.Fatalf("sidebar waits %+v", b)
	}
	h.advance(2 * time.Minute)
	b = h.next(Caller{Session: tSession})
	sv := h.queue().Steps[2]
	if b.State != StateComplete || sv.Kind != KindSidebar || sv.Reason != ReasonReplicaUnknown {
		t.Fatalf("replica unknown %+v %+v", b, sv)
	}
}

func TestNextTamperedAndStale(t *testing.T) {
	h := pinQueue(t, 1, false, false)
	q := h.queue()
	q.Steps[1].Tool = "mcp__ccd_sidebar__set_unread"
	if err := h.s.Store.WriteQueue(q); err != nil {
		t.Fatal(err)
	}
	_, err := h.s.Next(context.Background(), NextCall{Caller: Caller{Session: tSession}, Root: rootInfo(h.rec)})
	if pderr.CodeOf(err) != pderr.Internal {
		t.Fatalf("tool outside the policy: %v", err)
	}
	h = pinQueue(t, 1, false, false)
	q = h.queue()
	q.Steps[1].Input = json.RawMessage(`{"pinned":false,"session_id":"local_00000000-0000-4000-8000-000000000001"}`)
	if err := h.s.Store.WriteQueue(q); err != nil {
		t.Fatal(err)
	}
	if b := h.next(Caller{Session: tSession}); b.State != StateStale || h.queue() != nil {
		t.Fatalf("tampered %+v", b)
	}
	// Epoch or profile mismatch and expiry are stale too.
	h = pinQueue(t, 1, false, false)
	h.rec.Epoch = 5
	if b := h.next(Caller{Session: tSession}); b.State != StateStale {
		t.Fatalf("epoch %+v", b)
	}
	h = pinQueue(t, 1, false, false)
	h.advance(337 * time.Hour)
	if b := h.next(Caller{Session: tSession}); b.State != StateStale {
		t.Fatalf("expired %+v", b)
	}
	// A run is open: exit 41.
	h = pinQueue(t, 1, false, false)
	_, err = h.s.Next(context.Background(), NextCall{Root: RootInfo{Record: h.rec, Epoch: 4, Active: "home", RunOpen: true}})
	if pderr.CodeOf(err) != pderr.NeedsRecover {
		t.Fatalf("run open: %v", err)
	}
}

// loopModel runs the skill's loop. With deliver set every call returns a
// recognised result; otherwise each call is randomly not made, made with
// its result lost, or answered. Questions get a random option, waits
// sleep the time asked. It returns the first answer's bound, the answers
// and the terminal state.
func loopModel(rt *rapid.T, h *nh, deliver bool, max int) (first, answers int, terminal string) {
	me := Caller{Session: tSession}
	for i := 0; i < max; i++ {
		b := h.next(me)
		if i == 0 {
			first = b.RemainingBatches
		}
		switch b.State {
		case StateComplete, StateNoHooks, StateStale:
			return first, h.answers, b.State
		case StateQuestion:
			opt := rapid.SampledFrom(b.Question.Options).Draw(rt, "option").ID
			if _, err := h.s.Decide(context.Background(), Caller{Session: tSession, Token: h.token}, rootInfo(h.rec), b.Question.ID, opt); err != nil {
				rt.Fatal(err)
			}
		case StateWait:
			h.advance(time.Duration(b.RetryAfterMS) * time.Millisecond)
		case StateCalls:
			for _, c := range b.Calls {
				outcome := 2
				if !deliver {
					outcome = rapid.IntRange(0, 2).Draw(rt, "outcome")
				}
				if outcome == 0 {
					continue // not called
				}
				h.marker(c.ID, 1+h.queue().Step(c.ID).Retries)
				if outcome == 1 {
					continue // result lost
				}
				parsed := `{"matched":0}`
				switch c.Tool {
				case ToolGetSession:
					parsed = probeOK()
				case ToolListSections:
					parsed = sessionsLive
				case ToolSetView:
					parsed = `{"changed":1,"group_by":"state"}`
				}
				h.record(c.ID, parsed)
			}
		}
	}
	return first, h.answers, "none"
}

func randomQueue(rt *rapid.T, t *testing.T) *nh {
	if rapid.Bool().Draw(rt, "sidebar") {
		return sidebarQueue(t)
	}
	return pinQueue(t, rapid.SampledFrom([]int{1, 3, 200}).Draw(rt, "pins"), rapid.Bool().Draw(rt, "card"), rapid.Bool().Draw(rt, "question"))
}

// TestNextTermination (property 4): when the calls return their results,
// every run completes within the first answer's remaining_batches + 4
// answers (the skill's bound), 200 pin steps included; with calls not
// made or results lost, every run still reaches complete, no_hooks or
// stale (the skill stops at its bound and the queue stays for the next
// session).
func TestNextTermination(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		h := randomQueue(rt, t)
		first, answers, terminal := loopModel(rt, h, true, 60)
		if terminal != StateComplete || answers > first+4 {
			rt.Fatalf("%s after %d answers, first bound %d", terminal, answers, first)
		}
	})
	rapid.Check(t, func(rt *rapid.T) {
		h := randomQueue(rt, t)
		if _, answers, terminal := loopModel(rt, h, false, 2000); terminal == "none" {
			rt.Fatalf("no terminal state after %d answers", answers)
		}
	})
}

func TestNoWriteWithoutProbe(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		h := pinQueue(t, rapid.IntRange(1, 5).Draw(rt, "pins"), false, false)
		for i := 0; i < rapid.IntRange(1, 6).Draw(rt, "answers"); i++ {
			b := h.next(Caller{Session: tSession})
			for _, c := range b.Calls {
				if c.ID != ProbeStepID {
					rt.Fatalf("write %s offered without the probe's result", c.ID)
				}
				if rapid.Bool().Draw(rt, "marker") {
					h.marker(c.ID, 1)
				}
			}
			h.advance(time.Duration(rapid.IntRange(0, 15).Draw(rt, "minutes")) * time.Minute)
		}
	})
}

// TestNextIdempotent (property 3): two calls without new results offer
// the same calls and never a done, skipped or failed step.
func TestNextIdempotent(t *testing.T) {
	q := &Queue{V: 1, ID: tQueue, Phase: PhaseReconcile, State: QueueActive, Profile: "home", Epoch: 4, Replica: tReplica,
		FirstNextAt: t0, Steps: []Step{ProbeStep(), pinStep("p000", pinInput, StatusPending)}}
	q.Steps[0].Status = StatusDone
	q.Steps[1].Needs = []string{"read:r000"}
	rec := &state.OnlineRecord{Queue: tQueue, Profile: "home", Epoch: 4, Replica: tReplica, StepsSHA: StepsSHA(q.Steps)}
	lease := &Lease{V: 1, Queue: tQueue, Session: tSession, TokenSHA256: hashHex([]byte("tok")), BootID: tBoot,
		MonoDeadlineNS: 1e12, ExpiresAt: t0.Add(time.Hour), Since: t0.Add(-time.Minute)}
	in := NextInput{Queue: q, Record: rec, RootEpoch: 4, RootActive: "home", Now: t0, Mono: MonoNow{BootID: tBoot, NS: 1, OK: true},
		Lease: lease, Caller: Caller{Session: tSession, Token: "tok"},
		Providers: append(baseRegistry(), testPins{}), Settings: NewSettings(spec7(t), testService(t).Config), NewToken: "t",
		Results: map[StepID]Result{ProbeStepID: result(tQueue, q.Steps[0], tSession, probeOK())},
		Replica: ReplicaObs{Known: true, Replica: tReplica}}
	a, err := Next(in)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Next(in)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(a.Batch.Calls) != fmt.Sprint(b.Batch.Calls) || fmt.Sprint(callIDs(a.Batch)) != "[p000]" {
		t.Fatalf("not idempotent: %v / %v", a.Batch.Calls, b.Batch.Calls)
	}
}

// TestRemainingBatchesBound: the first answer's bound covers the answers
// the loop needs when every call succeeds.
func TestRemainingBatchesBound(t *testing.T) {
	for _, n := range []int{1, 20, 21, 45, 200} {
		h := pinQueue(t, n, false, true)
		me := Caller{Session: tSession}
		first := h.next(me).RemainingBatches
		h.record(ProbeStepID, probeOK())
		for i := 0; i < 50; i++ {
			b := h.next(me)
			if b.State == StateComplete {
				break
			}
			if b.State == StateQuestion {
				if _, err := h.s.Decide(context.Background(), Caller{Session: tSession, Token: h.token}, rootInfo(h.rec), "pins", "apply"); err != nil {
					t.Fatal(err)
				}
				continue
			}
			ids := callIDs(b)
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
			for _, id := range ids {
				h.record(id, `{"matched":0}`)
			}
		}
		if h.answers-1 > first {
			t.Errorf("%d pins: %d further answers, first bound %d", n, h.answers-1, first)
		}
	}
}

// TestBatchFieldOrderNoV: CLI documents get "v" from render.Document,
// never from the batch itself.
func TestBatchFieldOrderNoV(t *testing.T) {
	b, err := json.Marshal(newBatch(StateNone))
	if err != nil || bytes.Contains(b, []byte(`"v"`)) {
		t.Fatalf("%s %v", b, err)
	}
	_ = model.Replica{}
}

// driveTo answers `online next` until step id is offered: the probe and
// the sidebar read get their results, waits are slept through.
func (h *nh) driveTo(me Caller, id StepID) Batch {
	h.t.Helper()
	for i := 0; i < 10; i++ {
		b := h.next(me)
		for _, c := range b.Calls {
			switch c.ID {
			case id:
				return b
			case ProbeStepID:
				h.record(ProbeStepID, probeOK())
			case "r001":
				h.record("r001", sessionsLive)
			}
		}
		if b.State == StateWait {
			h.advance(time.Duration(b.RetryAfterMS) * time.Millisecond)
		}
	}
	h.t.Fatalf("%s never offered", id)
	return Batch{}
}

func (h *nh) retry() DecideView {
	h.t.Helper()
	v, err := h.s.Retry(context.Background(), Caller{Session: tSession, Token: h.token}, rootInfo(h.rec))
	if err != nil {
		h.t.Fatal(err)
	}
	return v
}

func sidebarStepID(t *testing.T, q *Queue) StepID {
	t.Helper()
	for _, s := range q.Steps {
		if s.Kind == KindSidebar {
			return s.ID
		}
	}
	t.Fatal("no sidebar step")
	return ""
}

// TestNextRetryAfterResult: a step that failed with a recorded result
// (app_refused, mismatch) and is retried ends done when the new attempt
// succeeds: the consumed result is removed, so the new one is recorded
// and used.
func TestNextRetryAfterResult(t *testing.T) {
	me := Caller{Session: tSession}
	t.Run("app_refused", func(t *testing.T) {
		h := pinQueue(t, 1, false, false)
		h.driveTo(me, "p000")
		h.marker("p000", 1)
		h.recordResp("p000", textResp("something went wrong", true))
		h.next(me)
		if s := h.queue().Step("p000"); s.Status != StatusFailed || s.Reason != ReasonAppRefused {
			t.Fatalf("first attempt %+v", s)
		}
		if v := h.retry(); v.Count != 1 {
			t.Fatalf("retry %+v", v)
		}
		if s := h.queue().Step("p000"); s.RetryBase != 1 || s.MaxAttempts() != 2 {
			t.Fatalf("retried step %+v", s)
		}
		h.driveTo(me, "p000")
		h.marker("p000", 2)
		h.record("p000", `{"matched":0}`)
		if b := h.next(me); b.State != StateComplete || b.Counts.Done != 1 || b.Counts.Failed != 0 {
			t.Fatalf("after the second attempt %+v", b)
		}
	})
	t.Run("mismatch", func(t *testing.T) {
		h := sidebarQueue(t)
		sid := sidebarStepID(t, h.queue())
		h.driveTo(me, sid)
		h.marker(sid, 1)
		h.recordResp(sid, textResp(`{"changed":1,"view":{"group_by":"date","sort_by":"recency"}}`, false))
		h.next(me)
		if s := h.queue().Step(sid); s.Status != StatusFailed || s.Reason != ReasonMismatch {
			t.Fatalf("first attempt %+v", s)
		}
		if v := h.retry(); v.Count != 1 {
			t.Fatalf("retry %+v", v)
		}
		h.driveTo(me, sid)
		h.marker(sid, 2)
		h.recordResp(sid, textResp(`{"changed":1,"view":{"group_by":"state","sort_by":"recency"}}`, false))
		if b := h.next(me); b.State != StateComplete || b.Counts.Done != 1 || b.Counts.Failed != 0 {
			t.Fatalf("after the second attempt %+v", b)
		}
	})
}

// TestNextRetryOpenCard: after a retry, the new attempt's marker counts
// as an open call within result_grace (state wait), whether the step
// was never called before (not_called: its first marker is the new
// attempt) or called without a result (no_result: only marker 2 is new).
func TestNextRetryOpenCard(t *testing.T) {
	me := Caller{Session: tSession}
	t.Run("not_called", func(t *testing.T) {
		h := pinQueue(t, 1, false, false)
		h.driveTo(me, "p000")
		h.next(me) // not called: offered again
		h.next(me) // failed(not_called)
		if s := h.queue().Step("p000"); s.Reason != ReasonNotCalled {
			t.Fatalf("first attempt %+v", s)
		}
		h.retry()
		if s := h.queue().Step("p000"); s.RetryBase != 0 || s.MaxAttempts() != 1 {
			t.Fatalf("retried step %+v", s)
		}
		h.driveTo(me, "p000")
		h.marker("p000", 1)
		h.advance(time.Second)
		if b := h.next(me); b.State != StateWait || len(b.Calls) != 0 {
			t.Fatalf("card open %+v", b)
		}
		h.record("p000", `{"matched":0}`)
		if b := h.next(me); b.State != StateComplete || b.Counts.Done != 1 {
			t.Fatalf("done %+v", b)
		}
	})
	t.Run("no_result", func(t *testing.T) {
		h := pinQueue(t, 1, false, false)
		h.driveTo(me, "p000")
		h.marker("p000", 1)
		h.advance(11 * time.Minute)
		h.next(me)
		if s := h.queue().Step("p000"); s.Reason != ReasonNoResult {
			t.Fatalf("first attempt %+v", s)
		}
		h.retry()
		h.driveTo(me, "p000")
		h.advance(time.Second)
		// Only the old marker: not called yet, offered again.
		if b := h.next(me); fmt.Sprint(callIDs(b)) != "[p000]" {
			t.Fatalf("old marker only %+v", b)
		}
		h.marker("p000", 2)
		h.advance(time.Second)
		if b := h.next(me); b.State != StateWait || len(b.Calls) != 0 {
			t.Fatalf("card open %+v", b)
		}
	})
}

// TestNextUnboundLeaseNotExtended: an unbound lease keeps the deadline it
// got when it was created (unbound_lease_ttl) however many answers it
// gets; once the probe binds it, it is extended to lease_ttl.
func TestNextUnboundLeaseNotExtended(t *testing.T) {
	h := pinQueue(t, 1, false, false)
	anon := Caller{}
	if b := h.next(anon); b.State != StateCalls {
		t.Fatalf("%+v", b)
	}
	created := h.clk().Now()
	l0, _ := h.s.Store.ReadLease()
	if l0 == nil || l0.Session != "" || !l0.ExpiresAt.Equal(created.Add(3*time.Minute)) {
		t.Fatalf("unbound lease %+v", l0)
	}
	h.marker(ProbeStepID, 1) // PreToolUse passed; no result yet
	for i := 0; i < 2; i++ {
		h.advance(time.Minute)
		if b := h.next(Caller{Token: h.token}); b.State != StateWait {
			t.Fatalf("answer %d %+v", i, b)
		}
		l, _ := h.s.Store.ReadLease()
		if l == nil || !l.ExpiresAt.Equal(l0.ExpiresAt) || l.MonoDeadlineNS != l0.MonoDeadlineNS {
			t.Fatalf("answer %d: lease %+v, want the deadlines of %+v", i, l, l0)
		}
	}
	h.record(ProbeStepID, probeOK())
	h.next(Caller{Token: h.token})
	l, _ := h.s.Store.ReadLease()
	if l == nil || l.Session != tSession || !l.ExpiresAt.Equal(h.clk().Now().Add(20*time.Minute)) {
		t.Fatalf("bound lease %+v", l)
	}
}
