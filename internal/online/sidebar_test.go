package online

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/kazupc/paritydesk/internal/model"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/online -update)", err)
	}
	if string(want) != got {
		t.Fatalf("%s differs:\n--- got\n%s\n--- want\n%s", p, got, want)
	}
}

func TestSidebarMapGolden(t *testing.T) {
	golden(t, "sidebar_map", SidebarMap(spec7(t).Sidebar))
}

// sidebarSteps builds the draft and returns its sidebar steps.
func sidebarSteps(t *testing.T, desired, current *model.RowSnapshot, create bool) ([]Step, []Warning) {
	t.Helper()
	in := sidebarInput(t, desired, current)
	in.Config.CreateSections = create
	d := Build(in, Registry())
	var out []Step
	for _, s := range d.Steps {
		if s.Kind == KindSidebar || s.Kind == KindSectionCreate {
			out = append(out, s)
		}
	}
	return out, d.Warnings
}

func sbData(t *testing.T, s Step) SidebarData {
	t.Helper()
	var d SidebarData
	if err := json.Unmarshal(s.Data, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSidebarBuildIntent(t *testing.T) {
	tests := []struct {
		name         string
		desired, cur string
		wantSteps    int
		wantGroupBy  string // intended, "" = none
		wantPrior    string // prior group_by, "-" = unknown
		wantWarning  bool
	}{
		{"absent groupBy: no intent", `{"id":"sessions","kind":"sessions","sortBy":"recency"}`, `{"id":"sessions","kind":"sessions","sortBy":"alpha"}`, 1, "", "-", false},
		{"prior equals intent: no step", `{"id":"sessions","kind":"sessions","groupBy":"status","sortBy":"recency"}`, `{"id":"sessions","kind":"sessions","groupBy":"status","sortBy":"recency"}`, 0, "", "", false},
		{"manual absent in T and none intended: no step", `{"id":"g1","kind":"manual","name":"Work","groupBy":"none"}`, `{"id":"g9","kind":"manual","name":"Work"}`, 0, "", "", false},
		{"folder -> project for sessions", `{"id":"sessions","kind":"sessions","groupBy":"folder"}`, `{"id":"sessions","kind":"sessions","groupBy":"date"}`, 1, "project", "date", false},
		{"status -> state", `{"id":"sessions","kind":"sessions","groupBy":"status"}`, `{"id":"sessions","kind":"sessions"}`, 1, "state", "-", false},
		{"savedEnvironment not writable", `{"id":"sessions","kind":"sessions","groupBy":"savedEnvironment"}`, `{"id":"sessions","kind":"sessions"}`, 0, "", "", true},
		{"project refused for manual", `{"id":"g1","kind":"manual","name":"Work","groupBy":"folder"}`, `{"id":"g9","kind":"manual","name":"Work"}`, 0, "", "", true},
		{"manual absent in T reports none", `{"id":"g1","kind":"manual","name":"Work","groupBy":"status"}`, `{"id":"g9","kind":"manual","name":"Work"}`, 1, "state", "none", false},
		{"unknown stored value", `{"id":"sessions","kind":"sessions","groupBy":"weekday"}`, `{"id":"sessions","kind":"sessions"}`, 0, "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			steps, warns := sidebarSteps(t, sectionsSnap(t, tc.desired), sectionsSnap(t, tc.cur), false)
			if len(steps) != tc.wantSteps || (len(warns) > 0) != tc.wantWarning {
				t.Fatalf("steps %d warnings %v", len(steps), warns)
			}
			if len(steps) == 0 {
				return
			}
			d := sbData(t, steps[0])
			switch {
			case tc.wantGroupBy == "" && d.Intended.GroupBy != nil:
				t.Fatalf("intended %v", *d.Intended.GroupBy)
			case tc.wantGroupBy != "" && (d.Intended.GroupBy == nil || *d.Intended.GroupBy != tc.wantGroupBy):
				t.Fatalf("intended %+v", d.Intended)
			}
			if tc.wantGroupBy != "" {
				if (tc.wantPrior == "-") != (d.Prior.GroupBy == nil) || (d.Prior.GroupBy != nil && *d.Prior.GroupBy != tc.wantPrior) {
					t.Fatalf("prior %+v", d.Prior)
				}
			}
			if !steps[0].Templated || !slices.Contains(steps[0].Needs, NeedReplica) || !slices.Contains(steps[0].Needs, NeedSettled) {
				t.Fatalf("step %+v", steps[0])
			}
		})
	}
}

// advanceWith runs the sidebar provider's Advance with a live
// list_sections answer and returns the sidebar step.
func advanceWith(t *testing.T, desired, cur, live string, create bool, createResult string) (*Queue, *Step) {
	t.Helper()
	in := sidebarInput(t, sectionsSnap(t, desired), sectionsSnap(t, cur))
	in.Config.CreateSections = create
	d := Build(in, Registry())
	if d.Empty() {
		t.Fatal("empty draft")
	}
	q := &Queue{V: 1, ID: tQueue, Phase: PhaseReconcile, Steps: d.Steps}
	read := sidebarRead(q)
	read.Status = StatusDone
	results := map[StepID]Result{read.ID: result(tQueue, *read, tSession, live)}
	if err := SidebarProvider().Advance(q, results, t0); err != nil {
		t.Fatal(err)
	}
	if createResult != "" {
		for i := range q.Steps {
			if q.Steps[i].Kind == KindSectionCreate && q.Steps[i].Status == StatusPending {
				q.Steps[i].Status = StatusDone
				results[q.Steps[i].ID] = result(tQueue, q.Steps[i], tSession, createResult)
				if createResult == "fail" {
					q.Steps[i].Status, q.Steps[i].Reason = StatusFailed, ReasonAppRefused
				}
			}
		}
		if err := SidebarProvider().Advance(q, results, t0); err != nil {
			t.Fatal(err)
		}
	}
	for i := range q.Steps {
		if q.Steps[i].Kind == KindSidebar {
			return q, &q.Steps[i]
		}
	}
	t.Fatal("no sidebar step")
	return nil, nil
}

// live is the parsed form of a list_sections answer (names hashed).
func live(sections ...string) string {
	p, ok := parseListSections("[" + strings.Join(sections, ",") + "]")
	if !ok {
		panic("live sections not parsed")
	}
	return string(p)
}

func TestSidebarLiveCheck(t *testing.T) {
	sessions := `{"id":"sessions","kind":"sessions","groupBy":"status","sortBy":"alpha"}`
	cur := `{"id":"sessions","kind":"sessions","groupBy":"date","sortBy":"recency"}`
	tests := []struct {
		name, desired, cur, live string
		status                   StepStatus
		reason, input            string
	}{
		{"V==I done", sessions, cur, live(`{"id":"sessions","kind":"sessions","group_by":"state","sort_by":"alpha"}`), StatusDone, "", ""},
		{"V==P write without section_id", sessions, cur, live(`{"id":"sessions","kind":"sessions","group_by":"date","sort_by":"recency"}`),
			StatusPending, "", `{"group_by":"state","sort_by":"alpha"}`},
		{"P unknown write", `{"id":"sessions","kind":"sessions","groupBy":"status"}`, `{"id":"sessions","kind":"sessions"}`,
			live(`{"id":"sessions","kind":"sessions","group_by":"project","sort_by":"recency"}`), StatusPending, "", `{"group_by":"state"}`},
		{"third value skipped", sessions, cur, live(`{"id":"sessions","kind":"sessions","group_by":"project","sort_by":"created"}`),
			StatusSkipped, ReasonChangedElsewhere, ""},
		{"environment live, I project, P state", `{"id":"sessions","kind":"sessions","groupBy":"folder"}`, `{"id":"sessions","kind":"sessions","groupBy":"status"}`,
			live(`{"id":"sessions","kind":"sessions","group_by":"environment","sort_by":"recency"}`), StatusSkipped, ReasonChangedElsewhere, ""},
		{"user_order not writable", `{"id":"sessions","kind":"sessions","sortBy":"alpha"}`, `{"id":"sessions","kind":"sessions","sortBy":"recency"}`,
			live(`{"id":"sessions","kind":"sessions","group_by":"date","sort_by":"user_order"}`), StatusSkipped, ReasonNotWritable, ""},
		{"routines with section_id", `{"id":"routines","kind":"routines","groupBy":"status"}`, `{"id":"routines","kind":"routines"}`,
			live(`{"id":"routines","kind":"routines","group_by":"none","sort_by":"recency"}`), StatusPending, "", `{"group_by":"state","section_id":"routines"}`},
		{"manual by name, live id", `{"id":"g1","kind":"manual","name":"Work","groupBy":"status"}`, `{"id":"g9","kind":"manual","name":"Work"}`,
			live(`{"id":"g7","kind":"manual","name":"Work","group_by":"none","sort_by":"recency"}`), StatusPending, "", `{"group_by":"state","section_id":"g7"}`},
		{"ambiguous live name", `{"id":"g1","kind":"manual","name":"Work","groupBy":"status"}`, `{"id":"g9","kind":"manual","name":"Work"}`,
			live(`{"id":"g7","kind":"manual","name":"Work","group_by":"none"}`, `{"id":"g8","kind":"manual","name":" Work ","group_by":"none"}`), StatusSkipped, ReasonAmbiguousName, ""},
		{"manual not found, no create", `{"id":"g1","kind":"manual","name":"Work","groupBy":"status"}`, `{"id":"g9","kind":"manual","name":"Work"}`,
			live(`{"id":"sessions","kind":"sessions","group_by":"date"}`), StatusSkipped, ReasonNotFound, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, s := advanceWith(t, tc.desired, tc.cur, tc.live, false, "")
			if s.Status != tc.status || s.Reason != tc.reason || string(s.Input) != tc.input {
				t.Fatalf("step %s %s %s", s.Status, s.Reason, s.Input)
			}
		})
	}
}

func TestSidebarCreateSection(t *testing.T) {
	desired := `{"id":"g1","kind":"manual","name":"Work","groupBy":"status"}`
	cur := `{"id":"g9","kind":"manual","name":"Work"}`
	none := live(`{"id":"sessions","kind":"sessions","group_by":"date"}`)
	// Not found: the dormant create step is activated.
	q, s := advanceWith(t, desired, cur, none, true, "")
	var create *Step
	for i := range q.Steps {
		if q.Steps[i].Kind == KindSectionCreate {
			create = &q.Steps[i]
		}
	}
	if create == nil || create.Status != StatusPending || string(create.Input) != `{"name":"Work"}` || s.Status != StatusPending || len(s.Input) != 0 {
		t.Fatalf("create %+v step %+v", create, s)
	}
	// After the create: the dependent set_view gets the new id.
	_, s = advanceWith(t, desired, cur, none, true, `{"id":"g42"}`)
	if s.Status != StatusPending || string(s.Input) != `{"group_by":"state","section_id":"g42"}` {
		t.Fatalf("after create %+v", s)
	}
	// A failed create: the dependent step is skipped not_found.
	q, s = advanceWith(t, desired, cur, none, true, "fail")
	blockDependents(q)
	if s.Status != StatusSkipped || s.Reason != ReasonNotFound {
		t.Fatalf("failed create %+v", s)
	}
	// Found: the dormant create step is retired.
	q, _ = advanceWith(t, desired, cur, live(`{"id":"g7","kind":"manual","name":"Work","group_by":"none"}`), true, "")
	for _, st := range q.Steps {
		if st.Kind == KindSectionCreate && (st.Status != StatusSkipped || st.Reason != "") {
			t.Fatalf("create not retired %+v", st)
		}
	}
}

func TestSectionNames(t *testing.T) {
	long := strings.Repeat("a", 201)
	for _, tc := range []struct {
		name string
		ok   bool
	}{
		{"Work", true},
		{"  Work  ", true},
		{"", false},
		{long, false},
		{strings.Repeat("\U0001F600", 100), true},  // 200 UTF-16 units
		{strings.Repeat("\U0001F600", 101), false}, // 202 UTF-16 units
		{"a\u0007b", false},
		{"a\u200eb", false}, // bidi mark (Cf)
		{"a\u202eb", false}, // bidi override
		{"a\u2028b", false},
		{"e\u0301", true}, // NFC composes it
	} {
		if _, ok := ValidSectionName(tc.name, 200); ok != tc.ok {
			t.Errorf("%q: ok %v", tc.name, ok)
		}
	}
	if n, _ := ValidSectionName("e\u0301", 200); n != "\u00e9" {
		t.Errorf("not NFC: %q", n)
	}
	// A name that fails gives no create step.
	steps, warns := sidebarSteps(t, sectionsSnap(t, `{"id":"g1","kind":"manual","name":"a\u202eb","groupBy":"status"}`),
		sectionsSnap(t, `{"id":"g9","kind":"manual","name":"x"}`), true)
	for _, s := range steps {
		if s.Kind == KindSectionCreate {
			t.Fatal("create step for an invalid name")
		}
	}
	if len(warns) == 0 {
		t.Fatal("no warning for an invalid name")
	}
	if len(steps) != 1 || !sbData(t, steps[0]).NameInvalid {
		t.Fatalf("sidebar step without name_invalid: %+v", steps)
	}
	// Missing live, it is skipped not_writable (it could not be created),
	// not not_found; without create_sections it is not_found.
	bad := "{\"id\":\"g1\",\"kind\":\"manual\",\"name\":\"a\u202eb\",\"groupBy\":\"status\"}"
	none := live(`{"id":"sessions","kind":"sessions","group_by":"date"}`)
	if _, s := advanceWith(t, bad, `{"id":"g9","kind":"manual","name":"x"}`, none, true, ""); s.Status != StatusSkipped || s.Reason != ReasonNotWritable {
		t.Fatalf("invalid name, create on: %s %s", s.Status, s.Reason)
	}
	if _, s := advanceWith(t, bad, `{"id":"g9","kind":"manual","name":"x"}`, none, false, ""); s.Status != StatusSkipped || s.Reason != ReasonNotFound {
		t.Fatalf("invalid name, create off: %s %s", s.Status, s.Reason)
	}
}

func TestSidebarRefusalAndExpect(t *testing.T) {
	_, s := advanceWith(t, `{"id":"sessions","kind":"sessions","groupBy":"status"}`, `{"id":"sessions","kind":"sessions","groupBy":"date"}`,
		live(`{"id":"sessions","kind":"sessions","group_by":"date","sort_by":"recency"}`), false, "")
	out, v, _ := Recognize(RecordInput{Step: *s}, textResp(`Grouping by "state" isn't available for this account.`, true))
	if out != OutcomeError || v == nil || v.Status != StatusSkipped || v.Reason != ReasonNotWritable {
		t.Fatalf("refusal %v %+v", out, v)
	}
	ok := result(tQueue, *s, tSession, `{"changed":1,"group_by":"state"}`)
	if st, r := InterpretBuiltin(*s, ok); st != StatusDone || r != "" {
		t.Fatalf("expected fields: %s %s", st, r)
	}
	bad := result(tQueue, *s, tSession, `{"changed":1,"group_by":"date"}`)
	if st, r := InterpretBuiltin(*s, bad); st != StatusFailed || r != ReasonMismatch {
		t.Fatalf("mismatch: %s %s", st, r)
	}
}

// TestSidebarSpellingSafety (property 2) and write safety (property 6):
// no set_view input ever holds a value outside the tool's enum or a
// stored-only spelling, and a field is written only when the live value
// equals the prior or the prior is unknown.
func TestSidebarSpellingSafety(t *testing.T) {
	stored := []string{"none", "date", "folder", "status", "savedEnvironment", "weird", ""}
	reported := []string{"date", "project", "environment", "state", "custom", "none", ""}
	sorts := []string{"recency", "alpha", "created", "user_order", ""}
	obj := func(id, kind, name, gb, sb string) string {
		m := map[string]string{"id": id, "kind": kind}
		if name != "" {
			m["name"] = name
		}
		if gb != "" {
			m["groupBy"] = gb
		}
		if sb != "" {
			m["sortBy"] = sb
		}
		b, _ := json.Marshal(m)
		return string(b)
	}
	rapid.Check(t, func(rt *rapid.T) {
		class := rapid.SampledFrom([]string{"sessions", "routines", "pinned", "manual"}).Draw(rt, "class")
		id, name := class, ""
		if class == "manual" {
			id, name = "g1", "Work"
		}
		desired := obj(id, class, name, rapid.SampledFrom(stored).Draw(rt, "dg"), rapid.SampledFrom(sorts[:3]).Draw(rt, "ds"))
		cur := obj(id, class, name, rapid.SampledFrom(stored).Draw(rt, "cg"), rapid.SampledFrom(sorts).Draw(rt, "cs"))
		lg, ls := rapid.SampledFrom(reported).Draw(rt, "lg"), rapid.SampledFrom(sorts).Draw(rt, "ls")
		lv := map[string]any{"id": id, "kind": class, "name": "Work"}
		if lg != "" {
			lv["group_by"] = lg
		}
		if ls != "" {
			lv["sort_by"] = ls
		}
		lb, _ := json.Marshal(lv)
		in := sidebarInput(t, sectionsSnap(t, desired), sectionsSnap(t, cur))
		d := Build(in, Registry())
		if d.Empty() {
			return
		}
		q := &Queue{V: 1, ID: tQueue, Phase: PhaseReconcile, Steps: d.Steps}
		read := sidebarRead(q)
		read.Status = StatusDone
		parsed, ok := parseListSections("[" + string(lb) + "]")
		if !ok {
			rt.Fatal("live answer not parsed")
		}
		if err := SidebarProvider().Advance(q, map[StepID]Result{read.ID: result(tQueue, *read, tSession, string(parsed))}, t0); err != nil {
			rt.Fatal(err)
		}
		for _, s := range q.Steps {
			if s.Kind != KindSidebar || len(s.Input) == 0 {
				continue
			}
			var input map[string]string
			if err := json.Unmarshal(s.Input, &input); err != nil {
				rt.Fatal(err)
			}
			var sd SidebarData
			_ = json.Unmarshal(s.Data, &sd)
			for field, v := range input {
				switch field {
				case "group_by":
					if !slices.Contains(toolGroupBy, v) || v == "custom" {
						rt.Fatalf("group_by %q outside the enum", v)
					}
					if (sd.Prior.GroupBy != nil) && lg != *sd.Prior.GroupBy {
						rt.Fatalf("group_by written: live %q, prior %q", lg, *sd.Prior.GroupBy)
					}
				case "sort_by":
					if !slices.Contains(toolSortBy, v) {
						rt.Fatalf("sort_by %q outside the enum", v)
					}
					if (sd.Prior.SortBy != nil) && ls != *sd.Prior.SortBy {
						rt.Fatalf("sort_by written: live %q, prior %q", ls, *sd.Prior.SortBy)
					}
				case "section_id":
				default:
					rt.Fatalf("unexpected field %s", field)
				}
				for _, bad := range []string{"folder", "status", "savedEnvironment"} {
					if v == bad {
						rt.Fatalf("stored spelling %q written", v)
					}
				}
			}
		}
	})
	_ = time.Second
}
