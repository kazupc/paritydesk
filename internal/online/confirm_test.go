package online

import (
	"encoding/json"
	"testing"

	"pgregory.net/rapid"

	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/state"
)

// doneSidebarStep returns a done sidebar step for the whole list.
func doneSidebarStep(t testing.TB, id StepID, gb, sb string) Step {
	t.Helper()
	d := SidebarData{Key: "builtin:sessions", Class: ClassSessions}
	if gb != "" {
		d.Intended.GroupBy = &gb
	}
	if sb != "" {
		d.Intended.SortBy = &sb
	}
	b, _ := json.Marshal(d)
	return Step{ID: id, Kind: KindSidebar, Tool: ToolSetView, Status: StatusDone, Data: b}
}

func TestConfirmPlanSidebar(t *testing.T) {
	sp := spec7(t).Sidebar
	base := sectionsSnap(t, `{"id":"sessions","kind":"sessions","groupBy":"date","sortBy":"recency","name":"Sessions","collapsed":false}`)
	observed := sectionsSnap(t, `{"id":"sessions","kind":"sessions","groupBy":"status","sortBy":"recency","name":"Sessions","collapsed":true}`)
	rec := &state.OnlineRecord{Queue: tQueue}
	contested := map[string]model.Contested{"section:sessions": {Key: "section:sessions", IntendedHash: observed.Items["section:sessions"].Hash, ServerHash: "old"}}
	steps := []Step{doneSidebarStep(t, "s000", "state", "")}
	e := ConfirmPlan(rec, steps, ConfirmBases{SidebarBase: base, Observed: observed, Contested: contested, Spec: sp})
	if e.SidebarBase == nil || e.BasesAdvanced != 1 || len(e.Confirmed) != 1 || e.ContestedCleared != 1 {
		t.Fatalf("edits %+v", e)
	}
	var got map[string]any
	_ = json.Unmarshal(e.SidebarBase.Items["section:sessions"].Value, &got)
	// Only the confirmed member moved, in the stored spelling; the
	// unrelated collapsed member stays as the base had it.
	if got["groupBy"] != "status" || got["collapsed"] != false || got["sortBy"] != "recency" {
		t.Fatalf("base item %v", got)
	}
	if base.Items["section:sessions"].Hash == e.SidebarBase.Items["section:sessions"].Hash {
		t.Fatal("hash not recomputed")
	}
	// Observed differs from the intent: no base move; the contested entry
	// follows the live state.
	contested["section:sessions"] = model.Contested{Key: "section:sessions", IntendedHash: "other", ServerHash: "old"}
	e = ConfirmPlan(rec, []Step{doneSidebarStep(t, "s000", "project", "")}, ConfirmBases{SidebarBase: base, Observed: observed, Contested: contested, Spec: sp})
	if e.SidebarBase != nil || e.ContestedUpdate["section:sessions"].ServerHash != observed.Items["section:sessions"].Hash {
		t.Fatalf("edits %+v", e)
	}
	// Already confirmed ids are skipped; without the mirror nothing is
	// confirmed (the next holder retries).
	rec.Confirmed = []string{"s000"}
	if e := ConfirmPlan(rec, steps, ConfirmBases{SidebarBase: base, Observed: observed, Spec: sp}); len(e.Confirmed) != 0 || e.SidebarBase != nil {
		t.Fatalf("repeat %+v", e)
	}
	rec.Confirmed = nil
	if e := ConfirmPlan(rec, steps, ConfirmBases{SidebarBase: base, Spec: sp}); len(e.Confirmed) != 0 {
		t.Fatalf("no mirror %+v", e)
	}
	// Skipped and failed steps are not confirmed.
	f := doneSidebarStep(t, "s000", "state", "")
	f.Status = StatusFailed
	if e := ConfirmPlan(rec, []Step{f}, ConfirmBases{SidebarBase: base, Observed: observed, Spec: sp}); len(e.Confirmed) != 0 {
		t.Fatalf("failed step confirmed %+v", e)
	}
}

// TestConfirmMonotonic (property 7): applying a confirmation twice
// changes nothing the second time.
func TestConfirmMonotonic(t *testing.T) {
	sp := spec7(t).Sidebar
	values := []string{"date", "status", "folder", "none"}
	sorts := []string{"recency", "alpha", "created"}
	rapid.Check(t, func(rt *rapid.T) {
		obj := func(gb, sb string) string {
			b, _ := json.Marshal(map[string]string{"id": "sessions", "kind": "sessions", "groupBy": gb, "sortBy": sb})
			return string(b)
		}
		base := sectionsSnap(t, obj(rapid.SampledFrom(values).Draw(rt, "bg"), rapid.SampledFrom(sorts).Draw(rt, "bs")))
		observed := sectionsSnap(t, obj(rapid.SampledFrom(values).Draw(rt, "og"), rapid.SampledFrom(sorts).Draw(rt, "os")))
		gb := sp.GroupByReport[rapid.SampledFrom(values).Draw(rt, "ig")]
		steps := []Step{doneSidebarStep(t, "s000", gb, rapid.SampledFrom(sorts).Draw(rt, "is"))}
		rec := &state.OnlineRecord{Queue: tQueue}
		e1 := ConfirmPlan(rec, steps, ConfirmBases{SidebarBase: base, Observed: observed, Spec: sp})
		next := base
		if e1.SidebarBase != nil {
			next = e1.SidebarBase
		}
		rec.Confirmed = append(rec.Confirmed, e1.Confirmed...)
		e2 := ConfirmPlan(rec, steps, ConfirmBases{SidebarBase: next, Observed: observed, Spec: sp})
		if e2.SidebarBase != nil || len(e2.Confirmed) != 0 || e2.BasesAdvanced != 0 {
			rt.Fatalf("second confirmation changed %+v", e2)
		}
		// Without the record, a second pass over the moved base moves
		// nothing either (the values already equal the observed ones).
		e3 := ConfirmPlan(&state.OnlineRecord{Queue: tQueue}, steps, ConfirmBases{SidebarBase: next, Observed: observed, Spec: sp})
		if e3.BasesAdvanced != 0 {
			rt.Fatalf("re-applied %+v", e3)
		}
	})
}
