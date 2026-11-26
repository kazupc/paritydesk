package rows

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/model"
)

func marshalAny(v any) (json.RawMessage, error) { return jsonx.Marshal(v) }

func unmarshalAny(b []byte, v any) error { return json.Unmarshal(b, v) }

func splitDots(p string) jsonx.Path { return jsonx.Path(strings.Split(p, ".")) }

// viewPolicy is the sidebar-view field lists of the manifests.
func viewPolicy() Policy {
	p := openPolicy()
	p.Fields = map[string][]string{
		RolePerField: {"collapsed", "name", "shown", "show", "groupBy", "order", "members"},
		RoleGroup:    {"sortBy", "ascending"},
	}
	return p
}

const sessionsSection = `{"id":"sessions","kind":"sessions","name":"Sessions","order":0,"shown":true,"collapsed":false,` +
	`"groupBy":"project","sortBy":"recency","ascending":false,"v":1,"show":{"emptyGroups":false,"x":false}}`

func viewOf(t fatalHelper, sec string) *model.RowSnapshot {
	t.Helper()
	s := snap(model.RowSidebarView, mk(t, sectionPrefix+"sessions", sec, 0))
	s.Order = []string{"sessions"}
	return s
}

// edited returns sessionsSection with the given members replaced.
func edited(t fatalHelper, kv map[string]any) string {
	t.Helper()
	it := mk(t, "x", sessionsSection, 0)
	d := newEditDoc(it)
	for k, v := range kv {
		b, err := marshalAny(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := d.set(splitDots(k), b); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}
	out, err := d.item("x", model.Stamp{}, false)
	if err != nil {
		t.Fatalf("item: %v", err)
	}
	return string(out.Value)
}

func sectionField(t fatalHelper, out Output, path string) any {
	t.Helper()
	v, ok := getAt(out.Desired.Items[sectionPrefix+"sessions"].Value, splitDots(path))
	if !ok {
		t.Fatalf("%s missing", path)
	}
	var x any
	if err := unmarshalAny(v, &x); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return x
}

func TestSidebarViewMembersMergeOnTheirOwn(t *testing.T) {
	row := model.RowSidebarView
	base := viewOf(t, sessionsSection)
	sortAlpha := viewOf(t, edited(t, map[string]any{"sortBy": "alpha"}))
	folded := viewOf(t, edited(t, map[string]any{"collapsed": true}))
	ascending := viewOf(t, edited(t, map[string]any{"ascending": true}))

	// leaving changed sortBy, entering changed collapsed.
	out := mustMerge(t, row, input(row, sortAlpha, base, folded, base, base, viewPolicy()))
	if sectionField(t, out, "sortBy") != "alpha" || sectionField(t, out, "collapsed") != true || len(out.Conflicts) != 0 {
		t.Fatalf("leaving sortBy + entering collapsed: conflicts %+v", out.Conflicts)
	}
	// leaving changed collapsed, entering changed sortBy and wins by epoch.
	in := input(row, folded, base, sortAlpha, base, base, viewPolicy())
	in.Entering.LiveEpoch = 9
	out = mustMerge(t, row, in)
	if sectionField(t, out, "sortBy") != "alpha" || sectionField(t, out, "collapsed") != true || len(out.Conflicts) != 0 {
		t.Fatalf("leaving collapsed + entering sortBy: conflicts %+v", out.Conflicts)
	}
	// leaving changed sortBy, entering changed ascending: the pair comes
	// whole from the Prefer winner (leaving, higher epoch), with a conflict.
	out = mustMerge(t, row, input(row, sortAlpha, base, ascending, base, base, viewPolicy()))
	if sectionField(t, out, "sortBy") != "alpha" || sectionField(t, out, "ascending") != false || len(out.Conflicts) != 1 {
		t.Fatalf("sort pair: sortBy %v ascending %v conflicts %+v", sectionField(t, out, "sortBy"),
			sectionField(t, out, "ascending"), out.Conflicts)
	}
	// show.emptyGroups on one side, show.x on the other: both.
	empty := viewOf(t, edited(t, map[string]any{"show.emptyGroups": true}))
	other := viewOf(t, edited(t, map[string]any{"show.x": true}))
	for _, swap := range []bool{false, true} {
		l, e := empty, other
		if swap {
			l, e = other, empty
		}
		out = mustMerge(t, row, input(row, l, base, e, base, base, viewPolicy()))
		if sectionField(t, out, "show.emptyGroups") != true || sectionField(t, out, "show.x") != true || len(out.Conflicts) != 0 {
			t.Fatalf("show members (swap %v): conflicts %+v", swap, out.Conflicts)
		}
	}
}
