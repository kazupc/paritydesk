package online

import (
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/state"
)

// confirmQueue: removal 1 (desc 2, 3), keep 2, add 3, removal 4 (no desc),
// add 5.
func confirmQueue(t *testing.T) []Step {
	t.Helper()
	in := pinsIn(t, []int{2, 3, 5}, []int{1, 2, 4}, nil, recsSnap(t,
		rec{n: 1}, rec{n: 2, members: map[string]any{"forkedFromSessionId": sid(1)}},
		rec{n: 3, members: map[string]any{"forkedFromSessionId": sid(1)}}, rec{n: 4}, rec{n: 5}))
	return pinsDraft(t, in).Steps
}

func set(steps []Step, n int, st StepStatus, reason string, ever bool) {
	for i := range steps {
		if _, in, ok := decodePin(steps[i]); ok && in.SessionID == sid(n) {
			steps[i].Status, steps[i].Reason, steps[i].EverUnknown = st, reason, ever
		}
	}
}

func baseKeys(s *model.RowSnapshot) string {
	var out []string
	for _, k := range s.Keys() {
		out = append(out, k[len(k)-1:])
	}
	return strings.Join(out, ",")
}

func TestPinsBaseAfter(t *testing.T) {
	base := pinsSnap(t, 1, 2, 4)
	for _, tc := range []struct {
		name string
		mod  func([]Step)
		want string
		n    int
	}{
		{"nothing resolved", func([]Step) {}, "1,2,4", 0},
		{"applied once", func(s []Step) {
			set(s, 1, StatusDone, "", false)
			set(s, 2, StatusDone, "", false)
			set(s, 3, StatusDone, "", false)
			set(s, 4, StatusDone, "", false)
			set(s, 5, StatusDone, "", false)
		}, "2,3,5", 4},
		{"definite removal, keep not done: desc dropped", func(s []Step) {
			set(s, 1, StatusDone, "", false)
			set(s, 2, StatusFailed, ReasonAppRefused, false)
			set(s, 3, StatusSkipped, ReasonDecisionSkip, false)
		}, "4", 1},
		{"unknown removal drops desc even with keep done", func(s []Step) {
			set(s, 1, StatusFailed, ReasonUnknownOutcome, true)
			set(s, 2, StatusDone, "", false)
			set(s, 3, StatusDone, "", false)
		}, "1,4", 1},
		{"late done after no_result stays unknown", func(s []Step) {
			set(s, 1, StatusDone, "", true)
			set(s, 2, StatusDone, "", false)
		}, "4", 1},
		{"no_result removal", func(s []Step) {
			set(s, 1, StatusFailed, ReasonNoResult, true)
		}, "1,4", 0},
		{"refused removal drops nothing", func(s []Step) {
			set(s, 1, StatusSkipped, ReasonNotWritable, false)
			set(s, 2, StatusSkipped, ReasonNotNeeded, false)
		}, "1,2,4", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps := confirmQueue(t)
			tc.mod(steps)
			pc, err := PinsBaseAfter(base, steps, nil, EverUnknown(steps))
			if err != nil || baseKeys(pc.Base) != tc.want || pc.Changed != tc.n {
				t.Fatalf("base %s changed %d (err %v), want %s %d", baseKeys(pc.Base), pc.Changed, err, tc.want, tc.n)
			}
			confirmed := map[StepID]bool{}
			for _, id := range pc.Confirmed {
				confirmed[id] = true
			}
			for _, s := range steps {
				if s.Kind == KindPin && s.Status.Terminal() != confirmed[s.ID] {
					t.Fatalf("%s terminal %v confirmed %v", s.ID, s.Status.Terminal(), confirmed[s.ID])
				}
			}
			again, _ := PinsBaseAfter(pc.Base, steps, confirmed, EverUnknown(steps))
			if baseKeys(again.Base) != tc.want || again.Changed != 0 || len(again.Confirmed) != 0 {
				t.Fatalf("not idempotent: %s", baseKeys(again.Base))
			}
		})
	}
	if pc, err := PinsBaseAfter(nil, confirmQueue(t), nil, nil); err != nil || pc.Base != nil || len(pc.Confirmed) != 0 {
		t.Fatalf("nil base: %+v %v", pc, err)
	}
	// Ids outside desc are never dropped.
	steps := confirmQueue(t)
	set(steps, 4, StatusFailed, ReasonUnknownOutcome, true)
	if pc, _ := PinsBaseAfter(base, steps, nil, EverUnknown(steps)); baseKeys(pc.Base) != "1,2,4" {
		t.Fatalf("dropped outside desc: %s", baseKeys(pc.Base))
	}
}

func TestConfirmPlanCarriesPins(t *testing.T) {
	steps := confirmQueue(t)
	set(steps, 4, StatusDone, "", false)
	rec := &state.OnlineRecord{Queue: tQueue}
	e := ConfirmPlan(rec, steps, ConfirmBases{PinsBase: pinsSnap(t, 1, 2, 4)})
	if baseKeys(e.Pins.Base) != "1,2" || e.Pins.Changed != 1 || len(e.Confirmed) != 1 {
		t.Fatalf("edits %+v", e)
	}
	rec.Confirmed = e.Confirmed
	e = ConfirmPlan(rec, steps, ConfirmBases{PinsBase: e.Pins.Base})
	if e.Pins.Changed != 0 || len(e.Confirmed) != 0 || e.Pins.Moved {
		t.Fatalf("second plan %+v", e)
	}
}
