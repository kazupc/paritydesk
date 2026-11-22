package sidebar_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/sources/sidebar"
	"github.com/kazupc/paritydesk/internal/testkit"
)

const now int64 = 1800000000000

var (
	rep1 = testkit.Replica(1)
	rep2 = testkit.Replica(2)
)

type mirrored interface {
	sources.Adapter
	sources.MirrorReader
}

var adapters = []mirrored{sidebar.GroupOrder{}, sidebar.PinnedOrder{}, sidebar.SidebarView{}}

func rc(t *testing.T, m *compat.Manifest, folder string, r model.Replica, row model.RowID) sources.ReadContext {
	t.Helper()
	return sources.ReadContext{Folder: folder, Replica: r, LS: testkit.LS(t, folder), Spec: testkit.Spec(t, m, row, r), Manifest: m}
}

func read(t *testing.T, a sources.Adapter, c sources.ReadContext) *model.RowSnapshot {
	t.Helper()
	s, err := a.Read(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func envelope(t *testing.T, value any, tab string, ts int64) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"value": value, "tabId": tab, "timestamp": ts})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func acct(r model.Replica) string  { return string(r.Account) }
func scope(r model.Replica) string { return string(r.Account) + "/" + string(r.Org) }

func TestAgreeingCopiesStampFromEnvelope(t *testing.T) {
	m := testkit.Manifest(t)
	folder := testkit.NewDataRoot(t).AddFolder(t, testkit.Rich("Claude", 1))
	for _, a := range adapters {
		s := read(t, a, rc(t, m, folder, rep1, a.Row()))
		if len(s.Stale) != 0 || len(s.Items) == 0 {
			t.Fatalf("%s: stale %v, %d items", a.Row(), s.Stale, len(s.Items))
		}
		for k, it := range s.Items {
			if it.Stamp.AppTime != testkit.LSTimestamp {
				t.Errorf("%s/%s: AppTime %d, want the envelope timestamp", a.Row(), k, it.Stamp.AppTime)
			}
		}
	}
	if s := read(t, sidebar.GroupOrder{}, rc(t, m, folder, rep1, model.RowGroupOrder)); len(s.Order) != 3 {
		t.Errorf("order elements: %v", s.Order)
	}
}

func TestMirrorWinsOverDifferingLS(t *testing.T) {
	m := testkit.Manifest(t)
	spec := testkit.Rich("Claude", 1)
	spec.LS = map[string]string{
		"LSS-persisted.code-projects-order." + acct(rep1): envelope(t, []string{"/tmp/stale"}, "t", 5),
		"LSS-persisted.dframe-local-slice":                envelope(t, map[string]any{"pinnedOrder": []string{}}, "t", 5),
		"LSS-persisted.dframe-code-sections": envelope(t, map[string]any{scope(rep1): map[string]any{
			"groupBy": "stale", "sections": spec.SidebarView["sections"]}}, "t", 5),
	}
	folder := testkit.NewDataRoot(t).AddFolder(t, spec)
	tests := []struct {
		a         mirrored
		stale     []string
		mirrorKey string
		want      any
	}{
		{sidebar.GroupOrder{}, []string{sidebar.OrderItem}, sidebar.OrderItem, spec.GroupOrder},
		{sidebar.PinnedOrder{}, []string{sidebar.OrderItem}, sidebar.OrderItem, spec.PinnedOrder},
		{sidebar.SidebarView{}, []string{"field:groupBy"}, "field:groupBy", spec.SidebarView["groupBy"]},
	}
	for _, tc := range tests {
		t.Run(string(tc.a.Row()), func(t *testing.T) {
			s := read(t, tc.a, rc(t, m, folder, rep1, tc.a.Row()))
			if strings.Join(s.Stale, ",") != strings.Join(tc.stale, ",") {
				t.Fatalf("stale %v, want %v", s.Stale, tc.stale)
			}
			it := s.Items[tc.mirrorKey]
			want, _ := json.Marshal(tc.want)
			if h, _ := model.ItemHash(want); h != it.Hash {
				t.Errorf("value is not the mirror's")
			}
			if it.Stamp.AppTime != 0 {
				t.Errorf("stale item AppTime %d, want 0", it.Stamp.AppTime)
			}
		})
	}
	view := read(t, sidebar.SidebarView{}, rc(t, m, folder, rep1, model.RowSidebarView))
	if view.Items["section:sessions"].Stamp.AppTime != 5 {
		t.Errorf("agreeing section should keep the envelope timestamp")
	}
}

func TestEnvelopeMetadataDoesNotChangeHash(t *testing.T) {
	m := testkit.Manifest(t)
	root := testkit.NewDataRoot(t)
	a := root.AddFolder(t, testkit.FolderSpec{Name: "Claude-a", Replica: rep1,
		LS: map[string]string{"LSS-persisted.code-projects-order." + acct(rep1): envelope(t, []string{"x", "y"}, "tab-a", 1)}})
	b := root.AddFolder(t, testkit.FolderSpec{Name: "Claude-b", Replica: rep1,
		LS: map[string]string{"LSS-persisted.code-projects-order." + acct(rep1): `{"timestamp":99,"value":[ "x" , "y" ],"tabId":"tab-b"}`}})
	sa := read(t, sidebar.GroupOrder{}, rc(t, m, a, rep1, model.RowGroupOrder))
	sb := read(t, sidebar.GroupOrder{}, rc(t, m, b, rep1, model.RowGroupOrder))
	if sa.Items[sidebar.OrderItem].Hash == "" || sa.Items[sidebar.OrderItem].Hash != sb.Items[sidebar.OrderItem].Hash {
		t.Fatal("envelope metadata or spacing changed the hash")
	}
}

func TestMirrorItemsMatchRead(t *testing.T) {
	m := testkit.Manifest(t)
	folder := testkit.NewDataRoot(t).AddFolder(t, testkit.Rich("Claude", 2))
	mirror, err := os.ReadFile(filepath.Join(folder, sources.DesktopConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range adapters {
		s := read(t, a, rc(t, m, folder, rep2, a.Row()))
		mi, err := a.MirrorItems(testkit.Spec(t, m, a.Row(), rep2), mirror)
		if err != nil {
			t.Fatal(err)
		}
		if !sources.SameItems(s, mi, false) || len(mi.Items) == 0 {
			t.Errorf("%s: MirrorItems %v differ from Read %v", a.Row(), mi.Keys(), s.Keys())
		}
	}
}

// change returns desired = current with the order item (or a view field)
// replaced by value.
func change(t *testing.T, s *model.RowSnapshot, key string, value any) *model.RowSnapshot {
	t.Helper()
	d := s.Clone()
	b, _ := json.Marshal(value)
	it, err := model.NewItem(key, b, b, model.Stamp{}, false)
	if err != nil {
		t.Fatal(err)
	}
	d.Items[key] = it
	return d
}

func ops(t *testing.T, a sources.Adapter, c sources.ReadContext, cur, des *model.RowSnapshot) sources.OpsResult {
	t.Helper()
	res, err := a.Ops(context.Background(), sources.OpsContext{ReadContext: c, NowMillis: now}, cur, des)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestMirroredRowsEditBothCopies(t *testing.T) {
	m := testkit.Manifest(t)
	tests := []struct {
		a     mirrored
		key   string
		value any
		locs  []string
	}{
		{sidebar.GroupOrder{}, sidebar.OrderItem, []string{"/tmp/z"}, []string{sources.DesktopConfigFile, "LSS-persisted.code-projects-order." + acct(rep1)}},
		{sidebar.PinnedOrder{}, sidebar.OrderItem, []string{testkit.FakeSession(9)}, []string{sources.DesktopConfigFile, "LSS-persisted.dframe-local-slice", "dframe-store"}},
		{sidebar.SidebarView{}, "field:groupBy", "none", []string{sources.DesktopConfigFile, "LSS-persisted.dframe-code-sections", "dframe-store"}},
	}
	for _, tc := range tests {
		t.Run(string(tc.a.Row()), func(t *testing.T) {
			folder := testkit.NewDataRoot(t).AddFolder(t, testkit.Rich("Claude", 1))
			c := rc(t, m, folder, rep1, tc.a.Row())
			cur := read(t, tc.a, c)
			res := ops(t, tc.a, c, cur, change(t, cur, tc.key, tc.value))
			got := map[string]bool{}
			stamped := false
			for _, e := range res.Edits {
				got[e.Loc.File+e.Loc.LSKey] = true
				if e.Kind != plan.EditSet {
					t.Errorf("edit kind %s, want set", e.Kind)
				}
				if e.Loc.LSKey != "" && strings.HasPrefix(e.Loc.LSKey, "LSS-") && strings.Join(e.Path, "/") == "timestamp" {
					stamped = true
				}
			}
			for _, l := range tc.locs {
				if !got[l] {
					t.Errorf("no edit on %s (got %v)", l, got)
				}
			}
			if len(got) != len(tc.locs) {
				t.Errorf("edits on %v, want only %v", got, tc.locs)
			}
			if !stamped || res.WrittenStamps[tc.key] != now {
				t.Errorf("envelope not stamped or stamp not reported: %v", res.WrittenStamps)
			}
			testkit.Apply(t, folder, res.Edits)
			after := read(t, tc.a, rc(t, m, folder, rep1, tc.a.Row()))
			if after.Items[tc.key].Hash != change(t, cur, tc.key, tc.value).Items[tc.key].Hash || len(after.Stale) != 0 {
				t.Errorf("read after apply differs (stale %v)", after.Stale)
			}
		})
	}
}

// without returns doc with path deleted (absent paths are fine).
func without(t *testing.T, doc []byte, paths ...jsonx.Path) []byte {
	t.Helper()
	for _, p := range paths {
		out, err := jsonx.Delete(doc, p)
		if errors.Is(err, jsonx.ErrNotFound) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		doc = out
	}
	return doc
}

func TestEditsChangeOnlyIntendedSpans(t *testing.T) {
	m := testkit.Manifest(t)
	folder := testkit.NewDataRoot(t).AddFolder(t, testkit.Rich("Claude", 1))
	cfg := filepath.Join(folder, sources.DesktopConfigFile)
	before, _ := os.ReadFile(cfg)
	lsBefore := testkit.ReadLS(t, folder)
	c := rc(t, m, folder, rep1, model.RowSidebarView)
	cur := read(t, sidebar.SidebarView{}, c)
	res := ops(t, sidebar.SidebarView{}, c, cur, change(t, cur, "field:groupBy", "none"))
	testkit.Apply(t, folder, res.Edits)
	after, _ := os.ReadFile(cfg)
	lsAfter := testkit.ReadLS(t, folder)

	mp := jsonx.Path{"preferences", "epitaxyPrefs", "dframe-code-sections", scope(rep1), "groupBy"}
	if string(without(t, before, mp)) != string(without(t, after, mp)) || string(before) == string(after) {
		t.Fatal("config bytes outside the edited member changed")
	}
	lss := "LSS-persisted.dframe-code-sections"
	lp := []jsonx.Path{{"value", scope(rep1), "groupBy"}, {"timestamp"}}
	if string(without(t, []byte(lsBefore[lss]), lp...)) != string(without(t, []byte(lsAfter[lss]), lp...)) {
		t.Fatal("LSS bytes outside the edited members changed")
	}
	sp := jsonx.Path{"state", "codeSidebarByScope", scope(rep1), "groupBy"}
	if string(without(t, []byte(lsBefore["dframe-store"]), sp)) != string(without(t, []byte(lsAfter["dframe-store"]), sp)) {
		t.Fatal("dframe-store bytes outside the edited member changed")
	}
	if !strings.Contains(lsAfter["dframe-store"], `"prefsSeeded":true`) {
		t.Fatal("prefsSeeded not preserved")
	}
	for name, v := range lsBefore {
		if name != lss && name != "dframe-store" && lsAfter[name] != v {
			t.Errorf("LS item %s changed", name)
		}
	}
}

func TestSidebarViewSubPathsAndScopeKey(t *testing.T) {
	m := testkit.Manifest(t)
	root := testkit.NewDataRoot(t)
	src := root.AddFolder(t, testkit.Rich("Claude-s", 1))
	dst := root.AddFolder(t, testkit.Rich("Claude", 2))
	srcRC := rc(t, m, src, rep1, model.RowSidebarView)
	desired := read(t, sidebar.SidebarView{}, srcRC)
	c := rc(t, m, dst, rep2, model.RowSidebarView)
	res, err := sidebar.SidebarView{}.Ops(context.Background(), sources.OpsContext{ReadContext: c, NowMillis: now, Source: &srcRC}, read(t, sidebar.SidebarView{}, c), desired)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"groupBy": true, "sections": true}
	for _, e := range res.Edits {
		p := e.Path
		switch {
		case e.Loc.File != "":
			p = p[4:]
		case e.Loc.LSKey == "dframe-store":
			p = p[3:]
		case len(p) == 1 && p[0] == "timestamp":
			continue
		default:
			p = p[2:]
		}
		if len(p) != 1 || !allowed[p[0]] {
			t.Errorf("edit outside declared sub-paths: %+v %v", e.Loc, e.Path)
		}
		if strings.Contains(strings.Join(e.Path, "/"), acct(rep1)) {
			t.Errorf("edit addresses the source scope: %v", e.Path)
		}
	}
	testkit.Apply(t, dst, res.Edits)
	cfg, _ := os.ReadFile(filepath.Join(dst, sources.DesktopConfigFile))
	if strings.Contains(string(cfg), acct(rep1)) {
		t.Fatal("source account id leaked into the target's mirror")
	}
}

func TestPersistVersionChecked(t *testing.T) {
	m := testkit.Manifest(t)
	folder := testkit.NewDataRoot(t).AddFolder(t, testkit.Rich("Claude", 1))
	for _, tc := range []struct {
		version int
		ok      bool
	}{{0, true}, {testkit.StoreVersion, true}, {testkit.StoreVersion + 1, false}} {
		c := rc(t, m, folder, rep1, model.RowPinnedOrder)
		c.Spec.PersistVersion = tc.version
		cur := read(t, sidebar.PinnedOrder{}, c)
		_, err := sidebar.PinnedOrder{}.Ops(context.Background(), sources.OpsContext{ReadContext: c, NowMillis: now}, cur, change(t, cur, sidebar.OrderItem, []string{}))
		if (err == nil) != tc.ok {
			t.Errorf("version %d: err %v", tc.version, err)
		}
		if err != nil && strings.Contains(err.Error(), testkit.FakeUUID(1)) {
			t.Errorf("error carries content: %v", err)
		}
	}
}

func TestMissingMirrorContainerSkipsRow(t *testing.T) {
	m := testkit.Manifest(t)
	folder := testkit.NewDataRoot(t).AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep1})
	if err := os.WriteFile(filepath.Join(folder, sources.DesktopConfigFile), []byte(`{"preferences":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c := rc(t, m, folder, rep1, model.RowGroupOrder)
	cur := read(t, sidebar.GroupOrder{}, c)
	res := ops(t, sidebar.GroupOrder{}, c, cur, change(t, cur, sidebar.OrderItem, []string{"a"}))
	if len(res.Edits) != 0 || len(res.Skipped) != 1 || res.Skipped[0].Kind != model.ChangeSkipped {
		t.Fatalf("got %d edits, skipped %+v", len(res.Edits), res.Skipped)
	}
}

func TestMissingLSCreatesEnvelope(t *testing.T) {
	m := testkit.Manifest(t)
	root := testkit.NewDataRoot(t)
	folder := root.AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep1, AppPrefs: map[string]any{}})
	for _, a := range adapters {
		c := rc(t, m, folder, rep1, a.Row())
		cur := read(t, a, c)
		var des *model.RowSnapshot
		if a.Row() == model.RowSidebarView {
			des = change(t, cur, "field:groupBy", "project")
		} else {
			des = change(t, cur, sidebar.OrderItem, []string{"a", "b"})
		}
		res := ops(t, a, c, cur, des)
		var replaced bool
		for _, e := range res.Edits {
			if e.Loc.LSKey != "" && e.Kind == plan.EditReplace {
				replaced = true
			}
			if e.Loc.LSKey == "dframe-store" {
				t.Errorf("%s: absent dframe-store must be left alone", a.Row())
			}
		}
		if !replaced {
			t.Errorf("%s: no fresh envelope", a.Row())
		}
		testkit.Apply(t, folder, res.Edits)
		if got := read(t, a, rc(t, m, folder, rep1, a.Row())); !sources.SameItems(got, des, false) || len(got.Stale) != 0 {
			t.Errorf("%s: read after apply differs", a.Row())
		}
	}
}

func TestMalformedMirrorNamesFileOnly(t *testing.T) {
	m := testkit.Manifest(t)
	folder := testkit.NewDataRoot(t).AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep1})
	bad := `{"preferences":{"epitaxyPrefs":{"code-projects-order.` + acct(rep1) + `":"` + testkit.Canary + `"}}}`
	if err := os.WriteFile(filepath.Join(folder, sources.DesktopConfigFile), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := sidebar.GroupOrder{}.Read(context.Background(), rc(t, m, folder, rep1, model.RowGroupOrder))
	if err == nil || !strings.Contains(err.Error(), sources.DesktopConfigFile) || strings.Contains(err.Error(), testkit.Canary) {
		t.Fatalf("error %v", err)
	}
}
