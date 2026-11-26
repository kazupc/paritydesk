package sidebar_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/sources/sidebar"
	"github.com/kazupc/paritydesk/internal/testkit"
)

const (
	groupsItem   = "collapsedGroups"
	familiesItem = "collapsedFamilies"
	storeKey     = "dframe-store"
)

// collapseFolder writes a folder whose dframe-store holds the given
// members (and nothing else of the sidebar rows).
func collapseFolder(t *testing.T, spec testkit.FolderSpec) (string, sources.ReadContext) {
	t.Helper()
	if spec.Name == "" {
		spec.Name = "Claude"
	}
	if spec.Replica.Account == "" {
		spec.Replica = rep1
	}
	folder := testkit.NewDataRoot(t).AddFolder(t, spec)
	return folder, rc(t, testkit.Manifest(t), folder, spec.Replica, model.RowSidebarCollapse)
}

// rawStore is a dframe-store document with the given state members.
func rawStore(t *testing.T, version int, state map[string]any) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"version": version, "shared": 2, "state": state})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func setItem(t *testing.T, key string, members ...string) model.Item {
	t.Helper()
	if members == nil {
		members = []string{}
	}
	b, _ := json.Marshal(members)
	it, err := model.NewItem(key, b, b, model.Stamp{}, false)
	if err != nil {
		t.Fatal(err)
	}
	return it
}

// withSet returns s with item key holding members.
func withSet(t *testing.T, s *model.RowSnapshot, key string, members ...string) *model.RowSnapshot {
	t.Helper()
	d := s.Clone()
	if d.Items == nil {
		d.Items = map[string]model.Item{}
	}
	d.Items[key] = setItem(t, key, members...)
	return d
}

// storedMember returns the stored array of dframe-store.state.<member>.
func storedMember(t *testing.T, folder, member string) string {
	t.Helper()
	v, err := jsonx.Get([]byte(testkit.ReadLS(t, folder)[storeKey]), jsonx.Path{"state", member})
	if err != nil {
		t.Fatal(err)
	}
	return string(v)
}

func TestCollapseReadNormalizes(t *testing.T) {
	_, c := collapseFolder(t, testkit.FolderSpec{LS: map[string]string{
		storeKey: rawStore(t, 1, map[string]any{groupsItem: []any{"b", "a", "a", 1, nil}, familiesItem: []any{}}),
	}})
	s := read(t, sidebar.Collapse{}, c)
	it := s.Items[groupsItem]
	if string(it.Value) != `["a","b"]` {
		t.Fatalf("value %s", it.Value)
	}
	if string(it.Raw) != `["b","a","a",1,null]` {
		t.Errorf("raw %s, want the stored bytes", it.Raw)
	}
	for _, v := range []string{`["a","b"]`, `["b","a"]`} {
		_, oc := collapseFolder(t, testkit.FolderSpec{LS: map[string]string{
			storeKey: `{"version":1,"state":{"collapsedGroups":` + v + `}}`}})
		if got := read(t, sidebar.Collapse{}, oc); got.Items[groupsItem].Hash != it.Hash {
			t.Errorf("%s hashes differently", v)
		}
	}
	if len(s.Order) != 0 || len(s.Stale) != 0 {
		t.Errorf("order %v stale %v", s.Order, s.Stale)
	}
}

func TestCollapseReadAbsentStore(t *testing.T) {
	_, c := collapseFolder(t, testkit.FolderSpec{AppPrefs: map[string]any{}})
	s := read(t, sidebar.Collapse{}, c)
	if len(s.Items) != 0 || s.Row != model.RowSidebarCollapse {
		t.Fatalf("absent store gave %+v", s)
	}
	ts, err := sidebar.Collapse{}.TargetState(context.Background(), c)
	if err != nil || !ts.StoreMissing || ts.Seeded {
		t.Fatalf("target state %+v, %v", ts, err)
	}
	_, c = collapseFolder(t, testkit.FolderSpec{CollapsedGroups: []string{"x"}})
	if ts, err := (sidebar.Collapse{}).TargetState(context.Background(), c); err != nil || ts.StoreMissing {
		t.Fatalf("present store: %+v, %v", ts, err)
	}
}

func TestCollapseReadMissingMember(t *testing.T) {
	_, c := collapseFolder(t, testkit.FolderSpec{CollapsedGroups: []string{"recents"}, OmitStore: []string{familiesItem}})
	s := read(t, sidebar.Collapse{}, c)
	if got := s.Keys(); !slices.Equal(got, []string{groupsItem}) {
		t.Fatalf("items %v", got)
	}
}

func TestCollapseReadBadShape(t *testing.T) {
	_, c := collapseFolder(t, testkit.FolderSpec{LS: map[string]string{
		storeKey: rawStore(t, 1, map[string]any{groupsItem: map[string]any{"secret-member": testkit.Canary}}),
	}})
	_, err := sidebar.Collapse{}.Read(context.Background(), c)
	if err == nil || !strings.Contains(err.Error(), storeKey) || strings.Contains(err.Error(), testkit.Canary) ||
		strings.Contains(err.Error(), "secret-member") {
		t.Fatalf("error %v", err)
	}
}

func TestCollapseIgnoresOpenedGroups(t *testing.T) {
	_, a := collapseFolder(t, testkit.FolderSpec{CollapsedGroups: []string{"x"}, OpenedGroups: []string{"y"}})
	_, b := collapseFolder(t, testkit.FolderSpec{CollapsedGroups: []string{"x"}, OpenedGroups: []string{"z", "w"}})
	sa, sb := read(t, sidebar.Collapse{}, a), read(t, sidebar.Collapse{}, b)
	if !sources.SameItems(sa, sb, false) {
		t.Fatal("openedGroups changed an item hash")
	}
	if _, ok := sa.Items["openedGroups"]; ok {
		t.Fatal("openedGroups read as an item")
	}
	res := ops(t, sidebar.Collapse{}, b, sb, withSet(t, sb, groupsItem, "x", "q"))
	for _, e := range res.Edits {
		if slices.Contains(e.Path, "openedGroups") {
			t.Fatalf("edit touches openedGroups: %v", e.Path)
		}
	}
}

func TestCollapseOpsSameIsEmpty(t *testing.T) {
	_, c := collapseFolder(t, testkit.FolderSpec{CollapsedGroups: []string{"b", "a"}})
	cur := read(t, sidebar.Collapse{}, c)
	if res := ops(t, sidebar.Collapse{}, c, cur, cur.Clone()); len(res.Edits) != 0 || len(res.Skipped) != 0 {
		t.Fatalf("Ops(cur, cur) = %+v", res)
	}
	folder, c := collapseFolder(t, testkit.FolderSpec{CollapsedGroups: []string{"a", "c", "a"}})
	cur = read(t, sidebar.Collapse{}, c)
	if res := ops(t, sidebar.Collapse{}, c, cur, withSet(t, cur, groupsItem, "a", "c")); len(res.Edits) != 0 {
		t.Fatalf("an equal normalized set was written: %+v", res.Edits)
	}
	if got := storedMember(t, folder, groupsItem); got != `["a","c","a"]` {
		t.Fatalf("the duplicate was cleaned without a reason: %s", got)
	}
}

func TestCollapseOpsKeepsTargetOrder(t *testing.T) {
	tests := []struct {
		target  []string
		desired []string
		want    string // "" = no edit
	}{
		{[]string{"c", "a"}, []string{"a", "b"}, `["a","b"]`},
		{[]string{"a", "c", "a"}, []string{"a", "b", "c"}, `["a","c","b"]`},
		{[]string{"c", "a"}, []string{"a", "c"}, ""},
	}
	for _, tc := range tests {
		folder, c := collapseFolder(t, testkit.FolderSpec{CollapsedGroups: tc.target})
		cur := read(t, sidebar.Collapse{}, c)
		res := ops(t, sidebar.Collapse{}, c, cur, withSet(t, cur, groupsItem, tc.desired...))
		if tc.want == "" {
			if len(res.Edits) != 0 {
				t.Errorf("%v → %v: edits %+v", tc.target, tc.desired, res.Edits)
			}
			continue
		}
		if len(res.Edits) != 1 || res.Edits[0].Loc.LSKey != storeKey || strings.Join(res.Edits[0].Path, "/") != "state/"+groupsItem {
			t.Fatalf("%v → %v: edits %+v", tc.target, tc.desired, res.Edits)
		}
		testkit.Apply(t, folder, res.Edits)
		if got := storedMember(t, folder, groupsItem); got != tc.want {
			t.Errorf("%v → %v: wrote %s, want %s", tc.target, tc.desired, got, tc.want)
		}
	}
}

func TestCollapseOpsBytesOutsideSpanUnchanged(t *testing.T) {
	doc := `{"state":{"zLast":1,"collapsedGroups":["b"],"openedGroups":["o"],"collapsedFamilies":[],"aFirst":{"k":[1,2]}},"shared":2,"version":1}`
	folder, c := collapseFolder(t, testkit.FolderSpec{StoreShared: 2, LS: map[string]string{storeKey: doc}})
	cur := read(t, sidebar.Collapse{}, c)
	res := ops(t, sidebar.Collapse{}, c, cur, withSet(t, cur, groupsItem, "a", "b"))
	testkit.Apply(t, folder, res.Edits)
	after := testkit.ReadLS(t, folder)[storeKey]
	p := jsonx.Path{"state", groupsItem}
	if string(without(t, []byte(doc), p)) != string(without(t, []byte(after), p)) || after == doc {
		t.Fatalf("bytes outside state.collapsedGroups changed:\n%s\n%s", doc, after)
	}
	if !strings.Contains(after, `"shared":2`) {
		t.Fatal("shared changed")
	}
}

func TestCollapseOpsStoreMissing(t *testing.T) {
	folder, c := collapseFolder(t, testkit.FolderSpec{AppPrefs: map[string]any{}})
	cur := read(t, sidebar.Collapse{}, c)
	res := ops(t, sidebar.Collapse{}, c, cur, withSet(t, cur, groupsItem, "a"))
	if len(res.Edits) != 0 || len(res.Skipped) != 1 || res.Skipped[0].Note != sources.NoteStoreMissing || res.Skipped[0].Key != groupsItem {
		t.Fatalf("res %+v", res)
	}
	if _, ok := testkit.ReadLS(t, folder)[storeKey]; ok {
		t.Fatal("dframe-store created")
	}
}

func TestCollapseOpsVersionMismatch(t *testing.T) {
	_, c := collapseFolder(t, testkit.FolderSpec{LS: map[string]string{
		storeKey: rawStore(t, 2, map[string]any{groupsItem: []string{"a"}})}})
	cur := withSet(t, sources.NewSnapshot(model.RowSidebarCollapse), groupsItem, "a")
	if _, err := (sidebar.Collapse{}).Ops(context.Background(), sources.OpsContext{ReadContext: c, NowMillis: now}, cur,
		withSet(t, cur, groupsItem, "b")); err == nil {
		t.Fatal("a store of another version was written")
	}
	if _, err := (sidebar.Collapse{}).Read(context.Background(), c); err == nil {
		t.Fatal("a store of another version was read")
	}
}

func TestCollapseOpsNeverDeletes(t *testing.T) {
	_, c := collapseFolder(t, testkit.FolderSpec{CollapsedGroups: []string{"a"}, CollapsedFamilies: []string{"code:x"}})
	cur := read(t, sidebar.Collapse{}, c)
	des := cur.Clone()
	delete(des.Items, groupsItem)
	if res := ops(t, sidebar.Collapse{}, c, cur, des); len(res.Edits) != 0 || len(res.Skipped) != 0 {
		t.Fatalf("res %+v", res)
	}
}

func TestCollapseOpsLimits(t *testing.T) {
	many := make([]string, 4097)
	for i := range many {
		many[i] = "project-/tmp/p" + itoa(i)
	}
	long := strings.Repeat("y", 2049)
	for name, des := range map[string][]string{"members": many, "bytes": {"a", long}} {
		t.Run(name, func(t *testing.T) {
			_, c := collapseFolder(t, testkit.FolderSpec{CollapsedGroups: []string{"a"}})
			cur := read(t, sidebar.Collapse{}, c)
			res := ops(t, sidebar.Collapse{}, c, cur, withSet(t, cur, groupsItem, des...))
			if len(res.Edits) != 0 || len(res.Skipped) != 1 || res.Skipped[0].Note != sources.NoteTooLarge {
				t.Fatalf("res %d edits, skipped %+v", len(res.Edits), res.Skipped)
			}
		})
	}
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

func TestCollapseBackupNeverWritten(t *testing.T) {
	folder, c := collapseFolder(t, testkit.FolderSpec{CollapsedGroups: []string{"a"}})
	before, ok := testkit.ReadLS(t, folder)[testkit.StoreBackupKey]
	if !ok {
		t.Fatal("fixture lacks the backup item")
	}
	cur := read(t, sidebar.Collapse{}, c)
	res := ops(t, sidebar.Collapse{}, c, cur, withSet(t, withSet(t, cur, groupsItem, "a", "b"), familiesItem, "code:y"))
	if len(res.Edits) != 2 {
		t.Fatalf("edits %+v", res.Edits)
	}
	for _, e := range res.Edits {
		if e.Loc.LSKey != storeKey || e.Kind != plan.EditSet {
			t.Fatalf("edit on %+v (%s)", e.Loc, e.Kind)
		}
	}
	testkit.Apply(t, folder, res.Edits)
	if after := testkit.ReadLS(t, folder)[testkit.StoreBackupKey]; after != before {
		t.Fatal("the backup item changed")
	}
}

func TestCollapseVerifyItemsEqualsRead(t *testing.T) {
	_, c := collapseFolder(t, testkit.FolderSpec{CollapsedGroups: []string{"b", "a"}, CollapsedFamilies: []string{"code:x"}})
	r := read(t, sidebar.Collapse{}, c)
	v, err := sidebar.Collapse{}.VerifyItems(context.Background(), c)
	if err != nil || !sources.SameItems(r, v, false) || len(v.Items) != 2 {
		t.Fatalf("VerifyItems %v, Read %v, %v", v.Keys(), r.Keys(), err)
	}
}

// TestCollapseReadAfterApply: a written set reads back as desired.
func TestCollapseReadAfterApply(t *testing.T) {
	folder, c := collapseFolder(t, testkit.FolderSpec{CollapsedGroups: []string{"c"}})
	cur := read(t, sidebar.Collapse{}, c)
	des := withSet(t, withSet(t, cur, groupsItem, "a", "c"), familiesItem, "code:"+testkit.FakeSession(4))
	testkit.Apply(t, folder, ops(t, sidebar.Collapse{}, c, cur, des).Edits)
	got := read(t, sidebar.Collapse{}, rc(t, testkit.Manifest(t), folder, rep1, model.RowSidebarCollapse))
	if !sources.SameItems(got, des, false) {
		t.Fatalf("read back %v", got.Items)
	}
}
