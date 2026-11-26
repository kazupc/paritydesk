package online

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/online/appsim"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/rows"
)

func sid(n int) string { return fmt.Sprintf("local_00000000-0000-4000-8000-%012d", n) }

// rec is a session record of the fake id n.
type rec struct {
	n       int
	members map[string]any
}

func recsSnap(t testing.TB, recs ...rec) *model.RowSnapshot {
	t.Helper()
	s := &model.RowSnapshot{Row: model.RowSessions, Schema: 1, Items: map[string]model.Item{}}
	for _, r := range recs {
		m := r.members
		if m == nil {
			m = map[string]any{}
		}
		b, _ := json.Marshal(m)
		it, err := model.NewItem(sid(r.n), b, nil, model.Stamp{}, false)
		if err != nil {
			t.Fatal(err)
		}
		s.Items[it.Key] = it
	}
	return s
}

func pinsSnap(t testing.TB, ns ...int) *model.RowSnapshot {
	t.Helper()
	s := &model.RowSnapshot{Row: model.RowPins, Schema: 1, Items: map[string]model.Item{}}
	for _, n := range ns {
		it, err := model.NewItem(sid(n), json.RawMessage("true"), nil, model.Stamp{}, false)
		if err != nil {
			t.Fatal(err)
		}
		s.Items[it.Key] = it
	}
	return s
}

// pinsIn is a pins BuildInput: D, C (after exclusion), raw C, sessions.
func pinsIn(t testing.TB, d, c []int, raw []int, sessions *model.RowSnapshot) BuildInput {
	t.Helper()
	in := BuildInput{Spec: spec7(t), Config: config.Default().Online, From: "work", To: "home", Replica: tReplica,
		Outputs:    map[model.RowID]*rows.Output{model.RowPins: {Desired: pinsSnap(t, d...)}},
		Current:    map[model.RowID]*model.RowSnapshot{model.RowPins: pinsSnap(t, c...), model.RowSessions: sessions},
		PinsSource: PinsIDB, Phase: PhaseReconcile, Limits: plan.Limits{MaxDeleteFraction: 0.5, MaxDeleteMin: 10}}
	if raw != nil {
		for _, n := range raw {
			in.PinsRaw = append(in.PinsRaw, sid(n))
		}
	}
	return in
}

func pinsDraft(t testing.TB, in BuildInput) Draft {
	t.Helper()
	return Build(in, Registry())
}

func pinSteps(d Draft) []Step {
	var out []Step
	for _, s := range d.Steps {
		if s.Kind == KindPin {
			out = append(out, s)
		}
	}
	return out
}

func stepFor(steps []Step, n int) (Step, PinData, bool) {
	for _, s := range steps {
		d, in, ok := decodePin(s)
		if ok && in.SessionID == sid(n) {
			return s, d, true
		}
	}
	return Step{}, PinData{}, false
}

func hasMsg(ws []Warning, m string) bool {
	for _, w := range ws {
		if w.Msg == m {
			return true
		}
	}
	return false
}

func TestPinsEligibilityAndOrder(t *testing.T) {
	sessions := recsSnap(t, rec{n: 1}, rec{n: 2}, rec{n: 3, members: map[string]any{"isArchived": true}},
		rec{n: 4, members: map[string]any{"scheduledTaskId": "x"}}, rec{n: 5}, rec{n: 6})
	in := pinsIn(t, []int{1, 3, 4, 9}, []int{5, 6}, nil, sessions)
	in.ReopenSession = ids.SessionID(sid(6))
	d := pinsDraft(t, in)
	steps := pinSteps(d)
	var got []string
	for _, s := range steps {
		pd, pi, _ := decodePin(s)
		got = append(got, fmt.Sprintf("%s:%s:%s:%v:%v", s.ID, pd.Op, pi.SessionID[len(pi.SessionID)-1:], s.Status, s.Card))
		if !strings.Contains(strings.Join(s.Needs, ","), "read:r000") || !strings.Contains(strings.Join(s.Needs, ","), "decision:pins") {
			t.Fatalf("needs %v", s.Needs)
		}
	}
	// Removals first (no-card first), then the add; archived, scheduled
	// and unknown ids get no step.
	want := "p000:remove:6:pending:false,p001:remove:5:pending:true,p002:add:1:dormant:true"
	if strings.Join(got, ",") != want {
		t.Fatalf("steps = %s", strings.Join(got, ","))
	}
	if d.Counts.PinsAdd != 1 || d.Counts.PinsRemove != 2 {
		t.Fatalf("counts %+v", d.Counts)
	}
	// Adds are pending when nothing is removed.
	d = pinsDraft(t, pinsIn(t, []int{1}, nil, nil, sessions))
	if s := pinSteps(d); len(s) != 1 || s[0].Status != StatusPending {
		t.Fatalf("lone add: %+v", s)
	}
	// No pins source: no steps.
	in = pinsIn(t, []int{1}, nil, nil, sessions)
	in.PinsSource = PinsNone
	if d := pinsDraft(t, in); !d.Empty() {
		t.Fatal("steps without a pins source")
	}
}

func TestPinsKeepSets(t *testing.T) {
	fam := func(field string, parent int) map[string]any {
		switch field {
		case "lineage":
			return map[string]any{"lineage": map[string]any{"sessionId": sid(parent), "kind": "hand_off", "initiator": "user"}}
		case "display":
			return map[string]any{"displayParentSessionId": sid(parent)}
		case "chain":
			return map[string]any{"startChain": map[string]any{"firstSessionId": sid(parent)}}
		}
		return map[string]any{"forkedFromSessionId": sid(parent)}
	}
	t.Run("hand-off child in D is kept", func(t *testing.T) {
		in := pinsIn(t, []int{2}, []int{1, 2}, nil, recsSnap(t, rec{n: 1}, rec{n: 2, members: fam("lineage", 1)}))
		steps := pinSteps(pinsDraft(t, in))
		_, rd, ok1 := stepFor(steps, 1)
		k, kd, ok2 := stepFor(steps, 2)
		if !ok1 || !ok2 || rd.Op != PinRemove || kd.Op != PinKeep || k.Status != StatusDormant ||
			strings.Join(kd.Of, ",") != sid(1) || strings.Join(rd.Desc, ",") != sid(2) {
			t.Fatalf("steps %+v", steps)
		}
	})
	t.Run("display parent child kept", func(t *testing.T) {
		in := pinsIn(t, []int{2}, []int{1, 2}, nil, recsSnap(t, rec{n: 1}, rec{n: 2, members: fam("display", 1)}))
		if _, kd, ok := stepFor(pinSteps(pinsDraft(t, in)), 2); !ok || kd.Op != PinKeep {
			t.Fatal("no keep")
		}
	})
	t.Run("child not in D in neither keep nor desc", func(t *testing.T) {
		in := pinsIn(t, nil, []int{1, 2}, nil, recsSnap(t, rec{n: 1}, rec{n: 2, members: fam("fork", 1)}))
		steps := pinSteps(pinsDraft(t, in))
		_, rd, _ := stepFor(steps, 1)
		_, kd, _ := stepFor(steps, 2)
		if len(rd.Desc) != 0 || kd.Op != PinRemove {
			t.Fatalf("desc %v, child %+v", rd.Desc, kd)
		}
	})
	t.Run("grandchild through the chain start is kept", func(t *testing.T) {
		in := pinsIn(t, []int{3}, []int{1, 3}, nil, recsSnap(t, rec{n: 1}, rec{n: 2, members: fam("fork", 1)}, rec{n: 3, members: fam("chain", 2)}))
		if _, kd, ok := stepFor(pinSteps(pinsDraft(t, in)), 3); !ok || kd.Op != PinKeep {
			t.Fatal("no keep for the grandchild")
		}
	})
	t.Run("excluded pinned child is kept", func(t *testing.T) {
		in := pinsIn(t, nil, []int{1}, []int{1, 2}, recsSnap(t, rec{n: 1}, rec{n: 2, members: fam("fork", 1)}))
		if _, kd, ok := stepFor(pinSteps(pinsDraft(t, in)), 2); !ok || kd.Op != PinKeep {
			t.Fatal("no keep for the excluded child")
		}
	})
	t.Run("archived pinned child in D blocks the removal", func(t *testing.T) {
		m := fam("fork", 1)
		m["isArchived"] = true
		in := pinsIn(t, []int{2}, []int{1, 2}, nil, recsSnap(t, rec{n: 1}, rec{n: 2, members: m}))
		d := pinsDraft(t, in)
		if len(pinSteps(d)) != 0 || !hasMsg(d.Warnings, "online.warn_pins_family_blocked") || PinsBlocked(in) != 1 {
			t.Fatalf("draft %+v", d)
		}
	})
	t.Run("archived child not in D does not block", func(t *testing.T) {
		m := fam("fork", 1)
		m["isArchived"] = true
		in := pinsIn(t, nil, []int{1, 2}, nil, recsSnap(t, rec{n: 1}, rec{n: 2, members: m}))
		if _, rd, ok := stepFor(pinSteps(pinsDraft(t, in)), 1); !ok || rd.Op != PinRemove {
			t.Fatal("removal blocked")
		}
	})
	t.Run("add in the family is in desc", func(t *testing.T) {
		in := pinsIn(t, []int{2}, []int{1}, nil, recsSnap(t, rec{n: 1}, rec{n: 2, members: fam("fork", 1)}))
		steps := pinSteps(pinsDraft(t, in))
		_, rd, _ := stepFor(steps, 1)
		s, ad, _ := stepFor(steps, 2)
		if strings.Join(rd.Desc, ",") != sid(2) || ad.Op != PinAdd || s.Status != StatusDormant {
			t.Fatalf("desc %v add %+v", rd.Desc, ad)
		}
	})
	t.Run("hidden prediction defers the removal", func(t *testing.T) {
		in := pinsIn(t, nil, []int{1}, nil, recsSnap(t, rec{n: 1}))
		in.Hidden = map[string]bool{sid(1): true}
		d := pinsDraft(t, in)
		if len(pinSteps(d)) != 0 || !hasMsg(d.Warnings, "online.warn_pins_hidden") {
			t.Fatalf("draft %+v", d)
		}
	})
	t.Run("keep of a session that is itself removed", func(t *testing.T) {
		in := pinsIn(t, nil, []int{1, 2}, nil, recsSnap(t, rec{n: 1}, rec{n: 2, members: fam("fork", 1)}))
		for _, s := range pinSteps(pinsDraft(t, in)) {
			if d, _, _ := decodePin(s); d.Op == PinKeep {
				t.Fatal("keep of a removed session")
			}
		}
	})
}

func TestPinsCapAndBreaker(t *testing.T) {
	var recs []rec
	for i := 1; i <= 12; i++ {
		recs = append(recs, rec{n: i})
	}
	// Removal 1 has a keep (2); removal 3 none.
	recs[1].members = map[string]any{"forkedFromSessionId": sid(1)}
	in := pinsIn(t, []int{2, 10, 11}, []int{1, 2, 3}, nil, recsSnap(t, recs...))
	in.Spec.Pins.MaxSteps = 3
	d := pinsDraft(t, in)
	steps := pinSteps(d)
	var ops []string
	for _, s := range steps {
		pd, pi, _ := decodePin(s)
		ops = append(ops, pd.Op+":"+pi.SessionID[len(pi.SessionID)-2:])
		if pd.Op == PinKeep && strings.Join(pd.Of, ",") != sid(1) {
			t.Fatalf("keep of %v", pd.Of)
		}
	}
	if strings.Join(ops, ",") != "remove:01,remove:03,keep:02" || !hasMsg(d.Warnings, "online.warn_pins_capped") {
		t.Fatalf("capped steps %v warnings %v", ops, d.Warnings)
	}
	// Breaker: more removals than max(min, fraction * |C|).
	var c []int
	for i := 1; i <= 12; i++ {
		c = append(c, i)
	}
	in = pinsIn(t, nil, c, nil, recsSnap(t, recs...))
	in.Limits = plan.Limits{MaxDeleteFraction: 0.1, MaxDeleteMin: 2}
	for _, s := range pinSteps(pinsDraft(t, in)) {
		if !strings.Contains(strings.Join(s.Needs, ","), "decision:pins_removals") {
			t.Fatalf("breaker need missing: %v", s.Needs)
		}
	}
}

func TestValidatePinData(t *testing.T) {
	in := pinsIn(t, []int{2}, []int{1, 2}, nil, recsSnap(t, rec{n: 1}, rec{n: 2, members: map[string]any{"forkedFromSessionId": sid(1)}}))
	steps := pinSteps(pinsDraft(t, in))
	for _, s := range steps {
		if err := validatePinData(s); err != nil {
			t.Fatalf("%s: %v", s.ID, err)
		}
	}
	bad := steps[0]
	bad.Data = json.RawMessage(`{"op":"remove","desc":["cloud_1"]}`)
	if validatePinData(bad) == nil {
		t.Fatal("non-pattern id accepted")
	}
	bad.Data = json.RawMessage(`{"op":"add","extra":1}`)
	if validatePinData(bad) == nil {
		t.Fatal("unknown member accepted")
	}
	bad = steps[0]
	bad.Input = json.RawMessage(`{"pinned":true,"session_id":"` + sid(1) + `"}`)
	if validatePinData(bad) == nil {
		t.Fatal("removal that pins accepted")
	}
	q := &Queue{V: 1, ID: tQueue, Phase: PhaseReconcile, Steps: append([]Step{ProbeStep()}, bad)}
	if err := q.Validate(Policies(Registry()), nil); err == nil {
		t.Fatal("queue with bad pin data is valid")
	}
}

// familyQueue is a queue with removal 1 (family: 2 kept), add 3.
func familyQueue(t *testing.T) *Queue {
	t.Helper()
	in := pinsIn(t, []int{2, 3}, []int{1, 2}, nil, recsSnap(t, rec{n: 1}, rec{n: 2, members: map[string]any{"forkedFromSessionId": sid(1)}}, rec{n: 3}))
	d := pinsDraft(t, in)
	return &Queue{V: 1, ID: tQueue, Phase: PhaseReconcile, From: "work", Steps: d.Steps}
}

func setStatus(q *Queue, n int, st StepStatus, reason string) {
	for i := range q.Steps {
		if _, in, ok := decodePin(q.Steps[i]); ok && in.SessionID == sid(n) {
			q.Steps[i].Status, q.Steps[i].Reason = st, reason
		}
	}
}

func statusOf(q *Queue, n int) string {
	for _, s := range q.Steps {
		if _, in, ok := decodePin(s); ok && in.SessionID == sid(n) {
			return string(s.Status) + "/" + s.Reason
		}
	}
	return ""
}

func TestPinsAdvance(t *testing.T) {
	p := pinsProvider{}
	q := familyQueue(t)
	if err := p.ApplyAnswer(q, QuestionPins, OptionSkip); err != nil {
		t.Fatal(err)
	}
	_ = p.Advance(q, nil, t0)
	if statusOf(q, 1) != "skipped/decision_skip" || statusOf(q, 2) != "skipped/decision_skip" || statusOf(q, 3) != "skipped/decision_skip" {
		t.Fatal("skip did not skip every pin step")
	}
	q = familyQueue(t)
	q.Decisions = map[string]string{QuestionPins: OptionApply, QuestionPinsRemovals: OptionSkip}
	_ = p.Advance(q, nil, t0)
	if statusOf(q, 1) != "skipped/decision_skip" || statusOf(q, 2) != "skipped/not_needed" || statusOf(q, 3) != "pending/" {
		t.Fatalf("add_only: %s %s %s", statusOf(q, 1), statusOf(q, 2), statusOf(q, 3))
	}
	for _, tc := range []struct {
		st     StepStatus
		reason string
		keep   string
	}{
		{StatusDone, "", "pending/"},
		{StatusFailed, ReasonNoResult, "pending/"},
		{StatusFailed, ReasonUnknownOutcome, "pending/"},
		{StatusSkipped, ReasonNotWritable, "skipped/not_needed"},
		{StatusFailed, ReasonAppRefused, "skipped/not_needed"},
		{StatusSkipped, ReasonNotFound, "skipped/not_needed"},
	} {
		q := familyQueue(t)
		q.Decisions = map[string]string{QuestionPins: OptionApply}
		_ = p.Advance(q, nil, t0)
		if statusOf(q, 2) != "dormant/" {
			t.Fatal("keep active before the removal settled")
		}
		setStatus(q, 1, tc.st, tc.reason)
		_ = p.Advance(q, nil, t0)
		if statusOf(q, 2) != tc.keep || statusOf(q, 3) != "pending/" {
			t.Fatalf("%s/%s: keep %s add %s", tc.st, tc.reason, statusOf(q, 2), statusOf(q, 3))
		}
		before, _ := json.Marshal(q)
		_ = p.Advance(q, nil, t0)
		if after, _ := json.Marshal(q); string(after) != string(before) {
			t.Fatal("Advance is not idempotent")
		}
	}
}

func TestPinsInterpret(t *testing.T) {
	q := familyQueue(t)
	var rem, add Step
	for _, s := range q.Steps {
		if d, _, ok := decodePin(s); ok && d.Op == PinRemove {
			rem = s
		} else if ok && d.Op == PinAdd {
			add = s
		}
	}
	spec := spec7(t)
	interpret := func(s Step, text string, isErr bool) string {
		resp, _ := json.Marshal(map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": isErr})
		o, v, parsed := Recognize(RecordInput{Step: s, ToolsOff: spec.Responses.ToolsOff}, resp)
		if v != nil {
			return string(v.Status) + "/" + v.Reason
		}
		if o == OutcomeUnrecognized {
			return "unrecognized"
		}
		st, r := pinsProvider{}.Interpret(s, Result{Outcome: o, Parsed: parsed})
		return string(st) + "/" + r
	}
	for _, c := range []struct {
		s     Step
		text  string
		isErr bool
		want  string
	}{
		{add, "Session pinned.", false, "done/"},
		{add, "The session was already pinned.", false, "done/"},
		{rem, "Session unpinned.", false, "done/"},
		{rem, "The session was already unpinned.", false, "done/"},
		{rem, "Session pinned.", false, "unrecognized"},
		{rem, "The app window didn't answer. Try once more. Then tell the user.", true, "failed/unknown_outcome"},
		{add, "The Claude window isn't open, so the sidebar can't be changed right now.", true, "failed/app_refused"},
		{rem, "This session's section is hidden, so unpinning it could take it out of the user's sight.", true, "skipped/not_writable"},
		{add, "Session \"" + sid(3) + "\" is archived; archived sessions aren't shown.", true, "skipped/not_writable"},
		{add, "Session \"" + sid(3) + "\" not found.", true, "skipped/not_found"},
		{add, "Sidebar tools are turned off right now.", true, "failed/tools_off"},
	} {
		if got := interpret(c.s, c.text, c.isErr); got != c.want {
			t.Errorf("%q: %s, want %s", c.text, got, c.want)
		}
	}
	// A recorded direction that contradicts the input is a mismatch.
	if st, r := (pinsProvider{}).Interpret(rem, Result{Outcome: OutcomeOK, Parsed: json.RawMessage(`{"matched":0,"pinned":true}`)}); st != StatusFailed || r != ReasonMismatch {
		t.Fatalf("mismatch: %s %s", st, r)
	}
	if !PinChanged(Result{Outcome: OutcomeOK, Parsed: json.RawMessage(`{"matched":0}`)}) || PinChanged(Result{Outcome: OutcomeOK, Parsed: json.RawMessage(`{"matched":1}`)}) {
		t.Fatal("PinChanged")
	}
}

func TestPinsRetryable(t *testing.T) {
	p := pinsProvider{}
	q := familyQueue(t)
	var rem Step
	for _, s := range q.Steps {
		if d, _, ok := decodePin(s); ok && d.Op == PinRemove {
			rem = s
		}
	}
	if !p.Retryable(q, rem) {
		t.Fatal("removal not retryable while adds and keeps are dormant")
	}
	setStatus(q, 3, StatusPending, "")
	if p.Retryable(q, rem) {
		t.Fatal("removal retryable after an add was activated")
	}
	if p.RetryRefused() != "online.retry_pins_removal_late" {
		t.Fatal("retry message")
	}
}

func TestPinsQuestion(t *testing.T) {
	p := pinsProvider{}
	q := familyQueue(t)
	qs := p.Questions(q)
	if len(qs) != 1 || qs[0].ID != QuestionPins || !strings.Contains(qs[0].Text.English(), "pins 1 of them again") || len(qs[0].Options) != 2 {
		t.Fatalf("question %+v", qs)
	}
	// Every step without a card, no breaker, no union: no question; the
	// decision is taken as apply.
	in := pinsIn(t, []int{3}, nil, nil, recsSnap(t, rec{n: 3}))
	in.ReopenSession = ids.SessionID(sid(3))
	q = &Queue{V: 1, Phase: PhaseReconcile, Steps: pinsDraft(t, in).Steps}
	if len(p.Questions(q)) != 0 {
		t.Fatal("question without cards")
	}
	_ = p.Advance(q, nil, t0)
	if q.Decisions[QuestionPins] != OptionApply {
		t.Fatal("skipped question not applied")
	}
	in.PinsUnion = true
	q = &Queue{V: 1, Phase: PhaseReconcile, Steps: pinsDraft(t, in).Steps}
	if qs := p.Questions(q); len(qs) != 1 || !strings.Contains(qs[0].Text.English(), "combined") {
		t.Fatalf("union question %+v", qs)
	}
}

// TestPinsGoldens pins the queue (two removals, one with a family holding
// a hand-off child kept and an add in the family; adds and a keep) and the
// question text with the union, keep and blocked sentences.
func TestPinsGoldens(t *testing.T) {
	sessions := recsSnap(t,
		rec{n: 1},
		rec{n: 2, members: map[string]any{"lineage": map[string]any{"sessionId": sid(1), "kind": "hand_off", "initiator": "user"}}},
		rec{n: 3, members: map[string]any{"forkedFromSessionId": sid(1)}},
		rec{n: 4},
		rec{n: 5},
		rec{n: 6},
		rec{n: 7, members: map[string]any{"forkedFromSessionId": sid(6)}},
		rec{n: 8, members: map[string]any{"isArchived": true, "forkedFromSessionId": sid(6)}},
	)
	in := pinsIn(t, []int{2, 3, 5, 8}, []int{1, 2, 4, 6, 8}, nil, sessions)
	in.ReopenSession = ids.SessionID(sid(5))
	in.PinsUnion = true
	d := pinsDraft(t, in)
	q := &Queue{V: 1, ID: tQueue, Phase: PhaseReconcile, From: "work", Steps: d.Steps}
	b, err := json.MarshalIndent(q.Steps, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "queue_pins", string(b)+"\n")
	qs := pinsProvider{}.Questions(q)
	if len(qs) != 1 {
		t.Fatal("no question")
	}
	qb, _ := json.MarshalIndent(qs[0], "", "  ")
	golden(t, "next_pins_question", string(qb)+"\n")
}

// ---------------------------------------------------------------- simulation

// behaviour is what happens to one call in the simulation.
type behaviour int

const (
	approve behaviour = iota
	denied            // the card is denied: no call, no result
	lost              // the call runs, its result is never recorded
	timeout           // the window does not answer; the command runs later
)

// runPins runs the queue's pin steps against the app simulator in the
// order `online next` offers them: pending reads first (none here), the
// decision, then writes with card == false first, Advance between rounds.
func runPins(t *rapid.T, q *Queue, app *appsim.App, spec compat.OnlineSpec, pick func(Step) behaviour) {
	p := pinsProvider{}
	_ = p.ApplyAnswer(q, QuestionPins, OptionApply)
	for round := 0; round < 10; round++ {
		_ = p.Advance(q, nil, t0)
		var batch []*Step
		for _, noCard := range []bool{true, false} {
			for i := range q.Steps {
				s := &q.Steps[i]
				if s.Kind == KindPin && s.Status == StatusPending && s.Card != noCard {
					batch = append(batch, s)
				}
			}
		}
		if len(batch) == 0 {
			return
		}
		for _, s := range batch {
			_, in, _ := decodePin(*s)
			switch pick(*s) {
			case denied:
				s.Status, s.Reason, s.EverUnknown = StatusFailed, ReasonNoResult, true
				continue
			case lost:
				app.SetPinned(in.SessionID, in.Pinned, false)
				s.Status, s.Reason, s.EverUnknown = StatusFailed, ReasonNoResult, true
				continue
			}
			text := app.SetPinned(in.SessionID, in.Pinned, pick(*s) == timeout)
			isErr := text != appsim.TextPinned && text != appsim.TextUnpinned && text != appsim.TextAlreadyPinned && text != appsim.TextAlreadyUnpinned
			resp, _ := json.Marshal(map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": isErr})
			o, v, parsed := Recognize(RecordInput{Step: *s, ToolsOff: spec.Responses.ToolsOff}, resp)
			switch {
			case v != nil:
				s.Status, s.Reason = v.Status, v.Reason
			case o == OutcomeOK:
				s.Status, s.Reason = p.Interpret(*s, Result{Outcome: o, Parsed: parsed})
			case o == OutcomeError:
				s.Status, s.Reason = StatusFailed, ReasonAppRefused
			default:
				s.Status, s.Reason = StatusFailed, ReasonNoResult
			}
			if s.Status == StatusFailed && (s.Reason == ReasonNoResult || s.Reason == ReasonUnknownOutcome) {
				s.EverUnknown = true
			}
		}
	}
	t.Fatal("the pin steps did not settle")
}

// world is a random entering side: sessions with family links of every
// kind, flags, the pin list and the desired list.
type world struct {
	recs                     []rec
	sessions                 []appsim.Session
	craw, c, d, excl, hidden map[int]bool
	sc, nested               bool
}

func genWorld(t *rapid.T) world {
	n := rapid.IntRange(1, 8).Draw(t, "n")
	w := world{craw: map[int]bool{}, c: map[int]bool{}, d: map[int]bool{}, excl: map[int]bool{}, hidden: map[int]bool{},
		sc: rapid.Bool().Draw(t, "sc"), nested: rapid.Bool().Draw(t, "nested")}
	kinds := []string{"hand_off", "side_session", "suggested_task", "fork"}
	for i := 1; i <= n; i++ {
		m := map[string]any{}
		if i > 1 && rapid.Bool().Draw(t, fmt.Sprintf("linked%d", i)) {
			parent := sid(rapid.IntRange(1, i-1).Draw(t, fmt.Sprintf("parent%d", i)))
			switch rapid.IntRange(0, 4).Draw(t, fmt.Sprintf("field%d", i)) {
			case 0:
				m["lineage"] = map[string]any{"sessionId": parent, "kind": kinds[rapid.IntRange(0, 3).Draw(t, fmt.Sprintf("kind%d", i))],
					"initiator": "user", "attachesThroughArchived": rapid.Bool().Draw(t, fmt.Sprintf("ata%d", i))}
			case 1:
				m["displayParentSessionId"] = parent
			case 2:
				m["spawnedFrom"] = map[string]any{"sessionId": parent, "sideSession": true, "sharedCheckout": rapid.Bool().Draw(t, fmt.Sprintf("sc%d", i))}
			case 3:
				m["forkedFromSessionId"] = parent
			case 4:
				m["startChain"] = map[string]any{"firstSessionId": parent}
			}
		}
		if rapid.IntRange(0, 5).Draw(t, fmt.Sprintf("arch%d", i)) == 0 {
			m["isArchived"] = true
		}
		if rapid.IntRange(0, 7).Draw(t, fmt.Sprintf("sched%d", i)) == 0 {
			m["scheduledTaskId"] = "task"
		}
		w.recs = append(w.recs, rec{n: i, members: m})
		b, _ := json.Marshal(m)
		w.sessions = append(w.sessions, appsim.FromRecord(sid(i), b))
		if rapid.Bool().Draw(t, fmt.Sprintf("pinned%d", i)) {
			w.craw[i] = true
		}
		if rapid.IntRange(0, 5).Draw(t, fmt.Sprintf("excl%d", i)) == 0 {
			w.excl[i] = true
		}
		if rapid.IntRange(0, 6).Draw(t, fmt.Sprintf("hidden%d", i)) == 0 {
			w.hidden[i] = true
		}
		if !w.excl[i] && rapid.Bool().Draw(t, fmt.Sprintf("desired%d", i)) {
			w.d[i] = true
		}
	}
	for i := range w.craw {
		if !w.excl[i] {
			w.c[i] = true
		}
	}
	return w
}

func keysOf(m map[int]bool) []int {
	var out []int
	for k, v := range m {
		if v {
			out = append(out, k)
		}
	}
	sort.Ints(out)
	return out
}

func (w world) input(t testing.TB, hidden bool) BuildInput {
	in := pinsIn(t, keysOf(w.d), keysOf(w.c), keysOf(w.craw), recsSnap(t, w.recs...))
	if in.PinsRaw == nil {
		in.PinsRaw = []string{}
	}
	in.Spec.Pins.MaxSteps = 1000
	in.Limits = plan.Limits{MaxDeleteFraction: 1, MaxDeleteMin: 1000}
	if hidden {
		in.Hidden = map[string]bool{}
		for i := range w.hidden {
			in.Hidden[sid(i)] = true
		}
	}
	return in
}

func (w world) app() *appsim.App {
	a := &appsim.App{Sessions: append([]appsim.Session(nil), w.sessions...), Pinned: map[string]bool{}, SC: w.sc, Nested: w.nested,
		Hidden: map[string]bool{}}
	for i := range w.craw {
		a.Pinned[sid(i)] = true
	}
	for i := range w.hidden {
		a.Hidden[sid(i)] = true
	}
	return a
}

func (w world) eligible(i int) bool {
	m := w.recs[i-1].members
	return m["isArchived"] != true && m["scheduledTaskId"] == nil
}

// TestPropPinsApply is P-APPLY: every card approved, no timeouts. After
// the queue ran, every eligible desired session is pinned, every queued
// removal is unpinned, nothing outside D or C got pinned, and every
// excluded pinned session is still pinned.
func TestPropPinsApply(t *testing.T) {
	spec := spec7(t)
	rapid.Check(t, func(rt *rapid.T) {
		w := genWorld(rt)
		in := w.input(t, true)
		d := Build(in, Registry())
		q := &Queue{V: 1, ID: tQueue, Phase: PhaseReconcile, From: "work", Steps: d.Steps}
		app := w.app()
		runPins(rt, q, app, spec, func(Step) behaviour { return approve })
		for i := 1; i <= len(w.recs); i++ {
			pinned := app.Pinned[sid(i)]
			if w.excl[i] && w.craw[i] && !pinned {
				rt.Fatalf("excluded pinned session %d was unpinned", i)
			}
			if !w.eligible(i) {
				continue
			}
			if w.d[i] && !pinned {
				rt.Fatalf("desired session %d is not pinned", i)
			}
			if pinned && !w.d[i] && !w.craw[i] {
				rt.Fatalf("session %d pinned from nowhere", i)
			}
		}
		for _, s := range pinSteps(d) {
			if pd, pi, _ := decodePin(s); pd.Op == PinRemove && app.Pinned[pi.SessionID] {
				rt.Fatalf("queued removal %s still pinned", pi.SessionID)
			}
		}
	})
}

// TestPropPinsSound is P-SOUND: with random approvals, denials, lost
// results and timeouts applied late, and hidden sections the prediction
// does not know, T's next harvest against the confirmed base never exports
// an add of an id outside D or a removal of an id in D.
func TestPropPinsSound(t *testing.T) {
	spec := spec7(t)
	rapid.Check(t, func(rt *rapid.T) {
		w := genWorld(rt)
		in := w.input(t, false)
		d := Build(in, Registry())
		q := &Queue{V: 1, ID: tQueue, Phase: PhaseReconcile, From: "work", Steps: d.Steps}
		app := w.app()
		flushAt := rapid.IntRange(0, len(q.Steps)).Draw(rt, "flushAt")
		calls := 0
		runPins(rt, q, app, spec, func(s Step) behaviour {
			calls++
			if calls == flushAt {
				app.Flush()
			}
			return behaviour(rapid.IntRange(0, 3).Draw(rt, "behaviour"+string(s.ID)))
		})
		app.Flush()
		base := pinsSnap(t, keysOf(w.craw)...)
		pc, err := PinsBaseAfter(base, q.Steps, nil, EverUnknown(q.Steps))
		if err != nil {
			rt.Fatal(err)
		}
		after := &model.RowSnapshot{Row: model.RowPins, Schema: 1, Items: map[string]model.Item{}}
		for id := range app.Pinned {
			it, _ := model.NewItem(id, json.RawMessage("true"), nil, model.Stamp{}, false)
			after.Items[id] = it
		}
		strip := func(s *model.RowSnapshot) *model.RowSnapshot {
			c := s.Clone()
			for i := range w.excl {
				delete(c.Items, sid(i))
			}
			return c
		}
		desired := pinsSnap(t, keysOf(w.d)...)
		// The canonical state that produced D: every id it decided to
		// unpin carries the engine's tombstone (spec-pins 7.1).
		canon := desired.Clone()
		canon.Tombstones = map[string]model.Tombstone{}
		for i := range w.craw {
			if !w.d[i] {
				canon.Tombstones[sid(i)] = model.Tombstone{Key: sid(i)}
			}
		}
		mi := rows.Input{Row: model.RowPins,
			Leaving:   rows.Side{Alias: "home", Snapshot: strip(after), Base: strip(pc.Base), BaseValid: true, LiveEpoch: 9},
			Entering:  rows.Side{Alias: "work", Snapshot: desired, Base: desired, BaseValid: true, LiveEpoch: 3},
			Canonical: strip(canon),
			Policy:    rows.Policy{TombstoneMode: "newer-wins", LeavingExports: true, EnteringExports: true, EnteringImports: true}}
		out, err := rows.Registry()[model.RowPins].Merge(mi)
		if err != nil {
			rt.Fatal(err)
		}
		for id := range out.Canonical.Items {
			if _, ok := desired.Items[id]; !ok {
				rt.Fatalf("harvest exported an add of %s, which is not desired", id)
			}
		}
		for id := range desired.Items {
			if _, ok := out.Canonical.Items[id]; !ok {
				rt.Fatalf("harvest exported a removal of desired %s", id)
			}
		}
	})
}

// TestPropPinsIdempotent is P-IDEMP for Advance and PinsBaseAfter.
func TestPropPinsIdempotent(t *testing.T) {
	spec := spec7(t)
	rapid.Check(t, func(rt *rapid.T) {
		w := genWorld(rt)
		d := Build(w.input(t, false), Registry())
		q := &Queue{V: 1, ID: tQueue, Phase: PhaseReconcile, From: "work", Steps: d.Steps}
		runPins(rt, q, w.app(), spec, func(s Step) behaviour {
			return behaviour(rapid.IntRange(0, 3).Draw(rt, "b"+string(s.ID)))
		})
		once, _ := json.Marshal(q)
		_ = pinsProvider{}.Advance(q, nil, t0)
		if twice, _ := json.Marshal(q); string(twice) != string(once) {
			rt.Fatal("Advance changed a settled queue")
		}
		base := pinsSnap(t, keysOf(w.craw)...)
		a, _ := PinsBaseAfter(base, q.Steps, nil, EverUnknown(q.Steps))
		confirmed := map[StepID]bool{}
		for _, id := range a.Confirmed {
			confirmed[id] = true
		}
		b, _ := PinsBaseAfter(a.Base, q.Steps, confirmed, EverUnknown(q.Steps))
		if strings.Join(a.Base.Keys(), ",") != strings.Join(b.Base.Keys(), ",") || b.Changed != 0 {
			rt.Fatalf("second confirmation changed the base: %v -> %v", a.Base.Keys(), b.Base.Keys())
		}
	})
}
