package sidebar_test

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/localstorage"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/sources/sidebar"
	"github.com/kazupc/paritydesk/internal/testkit"
)

const (
	sortKey  = "code-sessions-sort-ascending"
	emptyKey = "code-sessions-show-empty-projects"
)

// seedView is a sidebar scope object; seeded sets prefsSeeded.
func seedView(seeded any) map[string]any {
	v := map[string]any{"sections": []any{map[string]any{"id": "sessions", "kind": "sessions", "ascending": false}}}
	if seeded != nil {
		v["prefsSeeded"] = seeded
	}
	return v
}

// seedFolder writes a folder and returns its read context for the seed
// row under the 7.x manifest.
func seedFolder(t *testing.T, spec testkit.FolderSpec) (string, sources.ReadContext) {
	t.Helper()
	if spec.Name == "" {
		spec.Name = "Claude"
	}
	if spec.Replica.Account == "" {
		spec.Replica = rep1
	}
	folder := testkit.NewDataRoot(t).AddFolder(t, spec)
	return folder, rc(t, testkit.Manifest7(t), folder, spec.Replica, model.RowSidebarSeed)
}

func boolItem(t *testing.T, key string, v bool) model.Item {
	t.Helper()
	b, _ := json.Marshal(v)
	it, err := model.NewItem(key, b, b, model.Stamp{}, false)
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func withBools(t *testing.T, s *model.RowSnapshot, kv map[string]bool) *model.RowSnapshot {
	t.Helper()
	d := s.Clone()
	if d.Items == nil {
		d.Items = map[string]model.Item{}
	}
	for k, v := range kv {
		d.Items[k] = boolItem(t, k, v)
	}
	return d
}

func lssKey(name string, r model.Replica) string { return "LSS-persisted." + name + "." + acct(r) }

func TestSeedReadMirrorAuthoritative(t *testing.T) {
	_, c := seedFolder(t, testkit.FolderSpec{SidebarView: seedView(true),
		SeedPrefs: map[string]any{sortKey: true, emptyKey: false},
		LS:        map[string]string{lssKey(sortKey, rep1): envelope(t, false, "t", 5)}})
	s := read(t, sidebar.Seed{}, c)
	if string(s.Items[sortKey].Value) != "true" || s.Items[sortKey].Stamp.AppTime != 0 {
		t.Fatalf("sort item %+v", s.Items[sortKey])
	}
	if strings.Join(s.Stale, ",") != sortKey {
		t.Fatalf("stale %v", s.Stale)
	}
	if it := s.Items[emptyKey]; string(it.Value) != "false" || it.Stamp.AppTime != testkit.LSTimestamp {
		t.Fatalf("agreeing copies: %+v", it)
	}
	// An LSS copy alone is used with its timestamp.
	_, c = seedFolder(t, testkit.FolderSpec{AppPrefs: map[string]any{},
		LS: map[string]string{lssKey(emptyKey, rep1): envelope(t, true, "t", 9)}})
	s = read(t, sidebar.Seed{}, c)
	if it := s.Items[emptyKey]; string(it.Value) != "true" || it.Stamp.AppTime != 9 || len(s.Items) != 1 {
		t.Fatalf("LSS only: %+v", s.Items)
	}
}

func TestSeedReadNonBool(t *testing.T) {
	_, c := seedFolder(t, testkit.FolderSpec{SeedPrefs: map[string]any{sortKey: testkit.Canary}})
	_, err := sidebar.Seed{}.Read(context.Background(), c)
	if err == nil || !strings.Contains(err.Error(), sources.DesktopConfigFile) || strings.Contains(err.Error(), testkit.Canary) {
		t.Fatalf("error %v", err)
	}
	_, c = seedFolder(t, testkit.FolderSpec{AppPrefs: map[string]any{},
		LS: map[string]string{lssKey(emptyKey, rep1): envelope(t, 1, "t", 9)}})
	if _, err := (sidebar.Seed{}).Read(context.Background(), c); err == nil || !strings.Contains(err.Error(), lssKey(emptyKey, rep1)) {
		t.Fatalf("LSS error %v", err)
	}
}

func TestSeedTargetState(t *testing.T) {
	scopeEnv := func(v any) string { return envelope(t, map[string]any{scope(rep1): v}, "t", 1) }
	store := func(v any) string {
		b, _ := json.Marshal(map[string]any{"version": 1, "shared": 2, "state": map[string]any{
			"codeSidebarByScope": map[string]any{scope(rep1): v}}})
		return string(b)
	}
	tests := []struct {
		name   string
		spec   testkit.FolderSpec
		noView bool
		want   bool
	}{
		{"store scope seeded, no mirror", testkit.FolderSpec{LS: map[string]string{"dframe-store": store(seedView(true))}}, false, true},
		{"mirror true only", testkit.FolderSpec{AppPrefs: map[string]any{}, LS: map[string]string{}}, false, true},
		{"LSS true only", testkit.FolderSpec{LS: map[string]string{"LSS-persisted.dframe-code-sections": scopeEnv(seedView(true))}}, false, true},
		{"all copies absent", testkit.FolderSpec{AppPrefs: map[string]any{}}, false, false},
		{"present without the flag", testkit.FolderSpec{SidebarView: seedView(nil)}, false, false},
		{"flag false", testkit.FolderSpec{SidebarView: seedView(false)}, false, false},
		{"scope value not an object", testkit.FolderSpec{LS: map[string]string{"LSS-persisted.dframe-code-sections": scopeEnv("x")}}, false, true},
		{"flag not a boolean", testkit.FolderSpec{SidebarView: seedView("yes")}, false, true},
		{"manifest without sidebar-view", testkit.FolderSpec{SidebarView: seedView(nil)}, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			folder, c := seedFolder(t, tc.spec)
			if tc.name == "mirror true only" {
				cfg := `{"preferences":{"epitaxyPrefs":{"dframe-code-sections":{"` + scope(rep1) + `":{"prefsSeeded":true}}}}}`
				if err := os.WriteFile(filepath.Join(folder, sources.DesktopConfigFile), []byte(cfg), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.noView {
				m := *c.Manifest
				m.Rows = nil
				for _, r := range c.Manifest.Rows {
					if r.Row != model.RowSidebarView {
						m.Rows = append(m.Rows, r)
					}
				}
				c.Manifest = &m
			}
			ts, err := sidebar.Seed{}.TargetState(context.Background(), c)
			if err != nil || ts.Seeded != tc.want || ts.StoreMissing {
				t.Fatalf("target state %+v, %v; want seeded %v", ts, err, tc.want)
			}
		})
	}
}

func TestSeedOpsUnseededWritesBothCopies(t *testing.T) {
	folder, c := seedFolder(t, testkit.FolderSpec{SidebarView: seedView(nil)})
	cur := read(t, sidebar.Seed{}, c)
	if len(cur.Items) != 0 {
		t.Fatalf("fixture has seed keys: %v", cur.Keys())
	}
	des := withBools(t, cur, map[string]bool{sortKey: true, emptyKey: false})
	res := ops(t, sidebar.Seed{}, c, cur, des)
	if len(res.Skipped) != 0 {
		t.Fatalf("skipped %+v", res.Skipped)
	}
	created := map[string]bool{}
	for _, e := range res.Edits {
		where := e.Loc.File + e.Loc.LSKey + "/" + strings.Join(e.Path, "/")
		for _, bad := range []string{"dframe-code-sections", "dframe-store", "prefsSeeded"} {
			if strings.Contains(where, bad) {
				t.Errorf("edit touches %s: %s", bad, where)
			}
		}
		if e.Loc.LSKey != "" && e.Kind == plan.EditReplace {
			created[e.Loc.LSKey] = true
		}
		if e.Loc.File == sources.DesktopConfigFile && e.Kind == plan.EditSet {
			want := []string{"preferences", "epitaxyPrefs", ""}
			if len(e.Path) != 3 || e.Path[0] != want[0] || e.Path[1] != want[1] || !strings.HasSuffix(e.Path[2], "."+acct(rep1)) {
				t.Errorf("mirror edit at %v", e.Path)
			}
		}
	}
	if !created[lssKey(sortKey, rep1)] || !created[lssKey(emptyKey, rep1)] {
		t.Fatalf("LSS envelopes not created: %v", created)
	}
	if res.WrittenStamps[sortKey] != now || res.WrittenStamps[emptyKey] != now {
		t.Fatalf("stamps %v", res.WrittenStamps)
	}
	testkit.Apply(t, folder, res.Edits)
	got := read(t, sidebar.Seed{}, rc(t, c.Manifest, folder, rep1, model.RowSidebarSeed))
	if !sources.SameItems(got, des, false) || len(got.Stale) != 0 {
		t.Fatalf("read back %v (stale %v)", got.Items, got.Stale)
	}
	cfg, _ := os.ReadFile(filepath.Join(folder, sources.DesktopConfigFile))
	v, err := jsonx.Get(cfg, jsonx.Path{"preferences", "epitaxyPrefs", sortKey + "." + acct(rep1)})
	if err != nil || string(v) != "true" {
		t.Fatalf("mirror value %s, %v", v, err)
	}
}

func TestSeedOpsSeededSkipsSortOnly(t *testing.T) {
	_, c := seedFolder(t, testkit.FolderSpec{SidebarView: seedView(true), SeedPrefs: map[string]any{sortKey: false, emptyKey: false}})
	cur := read(t, sidebar.Seed{}, c)
	res := ops(t, sidebar.Seed{}, c, cur, withBools(t, cur, map[string]bool{sortKey: true, emptyKey: true}))
	if len(res.Skipped) != 1 || res.Skipped[0].Key != sortKey || res.Skipped[0].Note != sources.NoteSeeded {
		t.Fatalf("skipped %+v", res.Skipped)
	}
	for _, e := range res.Edits {
		if e.Item != emptyKey {
			t.Fatalf("edit of %s on %+v", e.Item, e.Loc)
		}
	}
	if len(res.Edits) == 0 {
		t.Fatal("show-empty key not written")
	}
}

// folderSource reads a folder's files and Local Storage for plan.Compose.
func folderSource(t *testing.T, folder string) plan.Source {
	t.Helper()
	ls := testkit.LS(t, folder)
	return func(loc plan.Location) ([]byte, bool, error) {
		if loc.LSKey != "" {
			if ls == nil {
				return nil, false, nil
			}
			k, err := localstorage.Key(loc.LSKey)
			if err != nil {
				return nil, false, err
			}
			return ls.Get(k)
		}
		b, err := os.ReadFile(filepath.Join(folder, filepath.FromSlash(loc.File)))
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return b, err == nil, err
	}
}

func TestSeedOpsBothKeysOneFile(t *testing.T) {
	folder, c := seedFolder(t, testkit.FolderSpec{SidebarView: seedView(nil), SeedPrefs: map[string]any{sortKey: false, emptyKey: true}})
	cur := read(t, sidebar.Seed{}, c)
	res := ops(t, sidebar.Seed{}, c, cur, withBools(t, cur, map[string]bool{sortKey: true, emptyKey: false}))
	ops, err := plan.Compose(res.Edits, folderSource(t, folder))
	if err != nil {
		t.Fatal(err)
	}
	count := map[string]int{}
	for _, op := range ops {
		count[op.Path+op.Key]++
	}
	want := map[string]int{sources.DesktopConfigFile: 1, lssKey(sortKey, rep1): 1, lssKey(emptyKey, rep1): 1}
	if len(count) != len(want) {
		t.Fatalf("ops %v", count)
	}
	for k, n := range want {
		if count[k] != n {
			t.Errorf("%s: %d ops", k, count[k])
		}
	}
}

func TestSeedOpsMirrorMissing(t *testing.T) {
	folder, c := seedFolder(t, testkit.FolderSpec{AppPrefs: map[string]any{}})
	if err := os.WriteFile(filepath.Join(folder, sources.DesktopConfigFile), []byte(`{"preferences":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cur := read(t, sidebar.Seed{}, c)
	res := ops(t, sidebar.Seed{}, c, cur, withBools(t, cur, map[string]bool{emptyKey: true}))
	if len(res.Edits) != 0 || len(res.Skipped) != 1 || res.Skipped[0].Note != sources.NoteMirrorMissing {
		t.Fatalf("res %+v", res)
	}
}

func TestSeedOpsNeverDeletes(t *testing.T) {
	_, c := seedFolder(t, testkit.FolderSpec{SidebarView: seedView(nil), SeedPrefs: map[string]any{sortKey: true, emptyKey: true}})
	cur := read(t, sidebar.Seed{}, c)
	if res := ops(t, sidebar.Seed{}, c, cur, sources.NewSnapshot(model.RowSidebarSeed)); len(res.Edits) != 0 || len(res.Skipped) != 0 {
		t.Fatalf("res %+v", res)
	}
}

func TestSeedMirrorItemsMatchRead(t *testing.T) {
	folder, c := seedFolder(t, testkit.FolderSpec{SidebarView: seedView(true), SeedPrefs: map[string]any{sortKey: true, emptyKey: false}})
	mirror, err := os.ReadFile(filepath.Join(folder, sources.DesktopConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	s := read(t, sidebar.Seed{}, c)
	mi, err := sidebar.Seed{}.MirrorItems(c.Spec, mirror)
	if err != nil || !sources.SameItems(s, mi, false) || len(mi.Items) != 2 {
		t.Fatalf("MirrorItems %v, Read %v, %v", mi.Keys(), s.Keys(), err)
	}
}

// TestSeedNotCarriedOn6x: the 6.x manifest declares the row without keys;
// the adapter then has nothing to read or write.
func TestSeedNotCarriedOn6x(t *testing.T) {
	folder := testkit.NewDataRoot(t).AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep1,
		SeedPrefs: map[string]any{sortKey: true}})
	c := rc(t, testkit.Manifest(t), folder, rep1, model.RowSidebarSeed)
	s := read(t, sidebar.Seed{}, c)
	if len(s.Items) != 0 {
		t.Fatalf("6.x read %v", s.Keys())
	}
	if res := ops(t, sidebar.Seed{}, c, s, withBools(t, s, map[string]bool{sortKey: false})); len(res.Edits) != 0 {
		t.Fatalf("6.x ops %+v", res)
	}
}

// TestSidebarOfflineEditsChangeOnlyIntendedSpans: like
// TestEditsChangeOnlyIntendedSpans for the two new rows: the collapse row
// edits only state.<member> of dframe-store, the seed row only its own
// mirror member and its own LSS items; every other byte stays.
func TestSidebarOfflineEditsChangeOnlyIntendedSpans(t *testing.T) {
	spec := testkit.Rich("Claude", 1)
	spec.CollapsedGroups = []string{"recents"}
	spec.SeedPrefs = map[string]any{sortKey: false, emptyKey: false}
	folder := testkit.NewDataRoot(t).AddFolder(t, spec)
	m := testkit.Manifest7(t)
	cfgPath := filepath.Join(folder, sources.DesktopConfigFile)
	cfgBefore, _ := os.ReadFile(cfgPath)
	lsBefore := testkit.ReadLS(t, folder)

	cc := rc(t, m, folder, rep1, model.RowSidebarCollapse)
	ccur := read(t, sidebar.Collapse{}, cc)
	edits := ops(t, sidebar.Collapse{}, cc, ccur, withSet(t, ccur, groupsItem, "recents", "pinned")).Edits
	sc := rc(t, m, folder, rep1, model.RowSidebarSeed)
	scur := read(t, sidebar.Seed{}, sc)
	// The fixture's scope is seeded: only the show-empty key is written.
	edits = append(edits, ops(t, sidebar.Seed{}, sc, scur, withBools(t, scur, map[string]bool{sortKey: true, emptyKey: true})).Edits...)
	testkit.Apply(t, folder, edits)
	cfgAfter, _ := os.ReadFile(cfgPath)
	lsAfter := testkit.ReadLS(t, folder)

	mp := jsonx.Path{"preferences", "epitaxyPrefs", emptyKey + "." + acct(rep1)}
	if string(without(t, cfgBefore, mp)) != string(without(t, cfgAfter, mp)) || string(cfgBefore) == string(cfgAfter) {
		t.Fatal("config bytes outside the show-empty member changed")
	}
	sp := jsonx.Path{"state", groupsItem}
	if string(without(t, []byte(lsBefore[storeKey]), sp)) != string(without(t, []byte(lsAfter[storeKey]), sp)) ||
		lsBefore[storeKey] == lsAfter[storeKey] {
		t.Fatal("dframe-store bytes outside state.collapsedGroups changed")
	}
	ek := lssKey(emptyKey, rep1)
	lp := []jsonx.Path{{"value"}, {"timestamp"}}
	if string(without(t, []byte(lsBefore[ek]), lp...)) != string(without(t, []byte(lsAfter[ek]), lp...)) {
		t.Fatal("LSS bytes outside value and timestamp changed")
	}
	for name, v := range lsBefore {
		if name != storeKey && name != ek && lsAfter[name] != v {
			t.Errorf("LS item %s changed", name)
		}
	}
}
