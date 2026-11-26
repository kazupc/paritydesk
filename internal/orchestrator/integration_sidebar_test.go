package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/app"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/sources/sidebar"
	"github.com/kazupc/paritydesk/internal/state"
	"github.com/kazupc/paritydesk/internal/store/kv"
	"github.com/kazupc/paritydesk/internal/testkit"
)

// The sidebar-offline rows over the real dependencies (as in
// integration_test.go), with the app reporting a tested 7.x version so the
// 7.x manifest (which carries sidebar-seed) applies.

const (
	seedSortKey  = "code-sessions-sort-ascending"
	seedEmptyKey = "code-sessions-show-empty-projects"
)

// stub7App is stubApp reporting the tested 7.x version.
type stub7App struct{ *stubApp }

func (a stub7App) Locate(context.Context, string) (app.BundleIdentity, error) {
	return app.BundleIdentity{BundleID: config.DefaultBundleID, Path: "/Apps/Claude.app", Version: testkit.TestedVersion7, Build: "1"}, nil
}

func (w *realWorld) deps7() Deps {
	d := w.deps()
	d.App = stub7App{w.app}
	return d
}

// switch7 switches to alias with the 7.x deps.
func (w *realWorld) switch7(opts SwitchOptions) *Result {
	w.t.Helper()
	return w.switch7Code(opts, pderr.OK)
}

// switch7Code is switch7 expecting exit code want (OK, or Degraded when a
// probe skips a row).
func (w *realWorld) switch7Code(opts SwitchOptions, want pderr.Code) *Result {
	w.t.Helper()
	w.app.running = true
	res, err := New(w.deps7()).Switch(context.Background(), opts)
	if pderr.CodeOf(err) != want || res == nil {
		w.t.Fatalf("switch to %s: %v, want code %d (events %v)", opts.To, err, want, w.ui.events)
	}
	if res.Code != want {
		w.t.Fatalf("switch to %s: code %d", opts.To, res.Code)
	}
	for _, warn := range i18n.Strings(i18n.English, (res.Warnings)) {
		if strings.Contains(warn, "does not read back") {
			w.t.Errorf("switch to %s: %s", opts.To, warn)
		}
	}
	return res
}

func (w *realWorld) folder(name string) string { return filepath.Join(w.data.Dir, name) }

// collapsed returns dframe-store.state.<member> of a folder (nil when the
// store or member is absent).
func (w *realWorld) collapsed(folder, member string) []string {
	w.t.Helper()
	doc, ok := testkit.ReadLS(w.t, w.folder(folder))["dframe-store"]
	if !ok {
		return nil
	}
	v, err := jsonx.Get([]byte(doc), jsonx.Path{"state", member})
	if errors.Is(err, jsonx.ErrNotFound) {
		return nil
	}
	if err != nil {
		w.t.Fatal(err)
	}
	var out []string
	if err := json.Unmarshal(v, &out); err != nil {
		w.t.Fatal(err)
	}
	slices.Sort(out)
	return out
}

// setCollapsed rewrites dframe-store.state.collapsedGroups of a folder, as
// the app does when the person folds a group.
func (w *realWorld) setCollapsed(folder string, members ...string) {
	w.t.Helper()
	if members == nil {
		members = []string{}
	}
	doc := testkit.ReadLS(w.t, w.folder(folder))["dframe-store"]
	v, _ := json.Marshal(members)
	out, err := jsonx.Set([]byte(doc), jsonx.Path{"state", "collapsedGroups"}, v)
	if err != nil {
		w.t.Fatal(err)
	}
	testkit.WriteLS(w.t, w.folder(folder), map[string]string{"dframe-store": string(out)})
}

// seedValues returns a seed key's value in the config mirror and in its
// LSS envelope of a folder ("" when absent).
func (w *realWorld) seedValues(folder, name string, r model.Replica) (mirror, lss string) {
	w.t.Helper()
	cfg, err := os.ReadFile(filepath.Join(w.folder(folder), sources.DesktopConfigFile))
	if err != nil {
		w.t.Fatal(err)
	}
	if v, err := jsonx.Get(cfg, jsonx.Path{"preferences", "epitaxyPrefs", name + "." + string(r.Account)}); err == nil {
		mirror = string(v)
	}
	if env, ok := testkit.ReadLS(w.t, w.folder(folder))["LSS-persisted."+name+"."+string(r.Account)]; ok {
		if v, err := jsonx.Get([]byte(env), jsonx.Path{"value"}); err == nil {
			lss = string(v)
		}
	}
	return mirror, lss
}

func rowChanges(res *Result, r model.RowID) []model.Change {
	var out []model.Change
	for _, c := range res.Plan.Changes {
		if c.Row == r {
			out = append(out, c)
		}
	}
	return out
}

// TestIntegrationCollapseCarried: scenarios 1 and 2 of the sidebar-offline
// spec: a collapsed set reaches the other folder, a switch without edits
// plans nothing for the row, and concurrent edits on both sides (valid
// bases) both survive.
func TestIntegrationCollapseCarried(t *testing.T) {
	w := newRealWorld(t)
	w.switch7(yes("b"))
	w.switch7(yes("a"))
	// 1. a (active in Claude) folds two groups.
	w.setCollapsed(ids.DefaultFolder, "project-/tmp/alpha", "recents")
	w.switch7(yes("b"))
	if got := w.collapsed(ids.DefaultFolder, "collapsedGroups"); !slices.Equal(got, []string{"project-/tmp/alpha", "recents"}) {
		t.Fatalf("b holds %v", got)
	}
	res := w.switch7(yes("a"))
	if ch := rowChanges(res, model.RowSidebarCollapse); len(ch) != 0 {
		t.Fatalf("switch without edits plans %+v", ch)
	}
	// 2. b (parked) unfolds recents; a folds beta.
	w.setCollapsed("Claude-b", "project-/tmp/alpha")
	w.setCollapsed(ids.DefaultFolder, "project-/tmp/alpha", "recents", "project-/tmp/beta")
	w.switch7(SwitchOptions{To: "b", Yes: true, LogOffset: -1})
	w.switch7(SwitchOptions{To: "a", Yes: true, LogOffset: -1})
	want := []string{"project-/tmp/alpha", "project-/tmp/beta"}
	for _, f := range []string{ids.DefaultFolder, "Claude-b"} {
		if got := w.collapsed(f, "collapsedGroups"); !slices.Equal(got, want) {
			t.Errorf("%s holds %v, want %v", f, got, want)
		}
	}
}

// TestIntegrationCollapseFirstV02Switch: scenario 3: the first switch with
// the new row (no bases for it, bases for every v0.1 row) merges as a
// union and needs no confirmation.
func TestIntegrationCollapseFirstV02Switch(t *testing.T) {
	w := newRealWorld(t)
	w.cfg.Sync.CollapsedGroups = false
	w.switch7(yes("b"))
	w.switch7(yes("a"))
	w.cfg.Sync.CollapsedGroups = true
	w.setCollapsed(ids.DefaultFolder, "x")
	w.setCollapsed("Claude-b", "y")
	res := w.switch7(SwitchOptions{To: "b", Yes: true, LogOffset: -1})
	if res.Plan.NeedsConfirmation {
		t.Fatal("the first switch with the new row needs confirmation")
	}
	if got := w.collapsed(ids.DefaultFolder, "collapsedGroups"); !slices.Equal(got, []string{"x", "y"}) {
		t.Fatalf("b holds %v", got)
	}
}

// TestIntegrationSeedUnseededAndSeeded: scenario 4: an unseeded target
// gets the carried Sessions sort direction and empty-group setting; a
// seeded target keeps its sort key byte for byte and still gets the
// show-empty value.
func TestIntegrationSeedUnseededAndSeeded(t *testing.T) {
	sessionsAsc := func(spec *testkit.FolderSpec) {
		secs := spec.SidebarView["sections"].([]any)
		s := secs[0].(map[string]any)
		s["ascending"] = true
		s["show"] = map[string]any{"emptyGroups": false, "metadata": true}
	}
	t.Run("unseeded", func(t *testing.T) {
		w := newRealWorldWith(t, func(v int, spec *testkit.FolderSpec) {
			if v == 1 {
				sessionsAsc(spec)
				return
			}
			delete(spec.SidebarView, "prefsSeeded")
		})
		w.switch7(yes("b"))
		rb := testkit.Replica(2)
		if m, l := w.seedValues(ids.DefaultFolder, seedSortKey, rb); m != "true" || l != "true" {
			t.Errorf("sort key: mirror %q LSS %q", m, l)
		}
		if m, l := w.seedValues(ids.DefaultFolder, seedEmptyKey, rb); m != "false" || l != "false" {
			t.Errorf("show-empty key: mirror %q LSS %q", m, l)
		}
	})
	t.Run("seeded", func(t *testing.T) {
		w := newRealWorldWith(t, func(v int, spec *testkit.FolderSpec) {
			if v == 1 {
				sessionsAsc(spec)
				return
			}
			spec.SeedPrefs = map[string]any{seedSortKey: false, seedEmptyKey: true}
		})
		rb := testkit.Replica(2)
		sortLSS := "LSS-persisted." + seedSortKey + "." + string(rb.Account)
		before := testkit.ReadLS(t, w.folder("Claude-b"))[sortLSS]
		res := w.switch7(yes("b"))
		if m, _ := w.seedValues(ids.DefaultFolder, seedSortKey, rb); m != "false" {
			t.Errorf("sort key written to a seeded folder: %q", m)
		}
		if after := testkit.ReadLS(t, w.folder(ids.DefaultFolder))[sortLSS]; after != before {
			t.Errorf("sort key LSS item changed")
		}
		if m, l := w.seedValues(ids.DefaultFolder, seedEmptyKey, rb); m != "false" || l != "false" {
			t.Errorf("show-empty key: mirror %q LSS %q", m, l)
		}
		for _, c := range rowChanges(res, model.RowSidebarSeed) {
			if c.Key == seedSortKey {
				t.Errorf("sort key change planned: %+v", c)
			}
		}
	})
}

// TestIntegrationSidebarOneOpPerTarget: sidebar-view, pinned-order and
// sidebar-collapse all edit dframe-store, sidebar-view and sidebar-seed
// both edit the config mirror: the plan holds one op per target.
func TestIntegrationSidebarOneOpPerTarget(t *testing.T) {
	w := newRealWorldWith(t, func(v int, spec *testkit.FolderSpec) {
		spec.CollapsedGroups = []string{fmt.Sprintf("project-/tmp/v%d", v)}
		if v == 2 {
			delete(spec.SidebarView, "prefsSeeded")
		}
	})
	res := w.switch7(yes("b"))
	count := map[string]int{}
	var store, mirror []model.RowID
	for _, op := range res.Plan.Ops {
		count[op.Path+"|"+op.Key]++
		switch {
		case op.Key == "dframe-store":
			store = op.Rows
		case op.Path == sources.DesktopConfigFile:
			mirror = op.Rows
		}
	}
	for k, n := range count {
		if n != 1 {
			t.Errorf("%d ops for %s", n, k)
		}
	}
	for _, r := range []model.RowID{model.RowSidebarView, model.RowPinnedOrder, model.RowSidebarCollapse} {
		if !slices.Contains(store, r) {
			t.Errorf("dframe-store op rows %v lack %s", store, r)
		}
	}
	for _, r := range []model.RowID{model.RowSidebarView, model.RowSidebarSeed} {
		if !slices.Contains(mirror, r) {
			t.Errorf("config mirror op rows %v lack %s", mirror, r)
		}
	}
}

// TestIntegrationSidebarSkipsKeepBases: a target without dframe-store, a
// merged set over max_members, and a seeded target with a differing sort
// key are all left as they are, and P10 keeps a valid base for the row.
func TestIntegrationSidebarSkipsKeepBases(t *testing.T) {
	many := make([]string, 4097)
	for i := range many {
		many[i] = fmt.Sprintf("project-/tmp/p%d", i)
	}
	cases := []struct {
		name string
		row  model.RowID
		// code: a folder without dframe-store also fails the sidebar-view
		// row's store probe, so that row is skipped (degraded).
		code pderr.Code
		edit func(v int, spec *testkit.FolderSpec)
	}{
		{"no store", model.RowSidebarCollapse, pderr.Degraded, func(v int, spec *testkit.FolderSpec) {
			if v == 2 {
				spec.PinnedOrder, spec.SidebarView = nil, nil
				spec.AppPrefs = map[string]any{}
			} else {
				spec.CollapsedGroups = []string{"recents"}
			}
		}},
		{"over max_members", model.RowSidebarCollapse, pderr.OK, func(v int, spec *testkit.FolderSpec) {
			if v == 1 {
				spec.CollapsedGroups = many
			} else {
				spec.CollapsedGroups = []string{"recents"}
			}
		}},
		{"seeded sort key", model.RowSidebarSeed, pderr.OK, func(v int, spec *testkit.FolderSpec) {
			spec.SeedPrefs = map[string]any{seedSortKey: v == 1, seedEmptyKey: false}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newRealWorldWith(t, tc.edit)
			res := w.switch7Code(yes("b"), tc.code)
			skipped := false
			for _, c := range rowChanges(res, tc.row) {
				if c.Kind == model.ChangeSkipped {
					skipped = true
				} else if tc.row == model.RowSidebarCollapse || c.Key == seedSortKey {
					t.Errorf("change %+v", c)
				}
			}
			// The collapse merge reports what it kept; a seeded folder's
			// sort key is simply the target's own (no change at all).
			if skipped != (tc.row == model.RowSidebarCollapse) {
				t.Errorf("skipped changes: %+v", rowChanges(res, tc.row))
			}
			if tc.row == model.RowSidebarSeed {
				if m, _ := w.seedValues(ids.DefaultFolder, seedSortKey, testkit.Replica(2)); m != "false" {
					t.Errorf("seeded sort key now %q", m)
				}
			}
			root, err := w.st.Load()
			if err != nil {
				t.Fatal(err)
			}
			key := state.BaseKey{Alias: "b", Replica: testkit.Replica(2), Row: tc.row}
			if _, ok := root.Bases[key.String()]; !ok {
				t.Fatalf("no base for %s after the skip", key)
			}
			// The next switch sees a valid base: no read-back warning.
			w.switch7Code(yes("a"), tc.code)
		})
	}
}

// TestIntegrationCollapseKeepsOwnPrivateMembers: with an exclude glob set,
// the entering folder keeps its own private collapsed groups and fork
// families on every path that keeps its value: a profile that does not
// import the sidebar category, and a merged set over max_members. Both
// members are left byte for byte and the plan lists no write.
func TestIntegrationCollapseKeepsOwnPrivateMembers(t *testing.T) {
	many := make([]string, 4097)
	for i := range many {
		many[i] = fmt.Sprintf("project-/tmp/p%d", i)
	}
	family := "code:" + testkit.FakeSession(1)
	cases := []struct {
		name    string
		imports map[string][]string
		a       []string
		skip    bool
	}{
		{"no import", map[string][]string{"b": {"sessions"}}, []string{"project-/tmp/alpha", "recents"}, false},
		{"over max_members", nil, many, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newRealWorldWith(t, func(v int, spec *testkit.FolderSpec) {
				if v == 2 {
					spec.CollapsedGroups = []string{"project-/tmp/client/y", "project-example/repo", "recents"}
					spec.CollapsedFamilies = []string{family}
				} else {
					spec.CollapsedGroups = tc.a
					spec.CollapsedFamilies = []string{}
				}
			})
			w.cfg.Sync.Exclude = []string{"/tmp/client/**"}
			w.cfg.Sync.Import = tc.imports
			spans := func(folder string) string {
				doc := testkit.ReadLS(t, w.folder(folder))["dframe-store"]
				var out string
				for _, m := range []string{"collapsedGroups", "collapsedFamilies"} {
					v, err := jsonx.Get([]byte(doc), jsonx.Path{"state", m})
					if err != nil {
						t.Fatal(err)
					}
					out += string(v) + "\n"
				}
				return out
			}
			before := spans("Claude-b")
			res := w.switch7(yes("b"))
			for _, c := range rowChanges(res, model.RowSidebarCollapse) {
				if c.Kind != model.ChangeSkipped || !tc.skip || c.Key != "collapsedGroups" {
					t.Errorf("change %+v", c)
				}
			}
			if got := w.collapsed(ids.DefaultFolder, "collapsedGroups"); !slices.Equal(got, []string{"project-/tmp/client/y", "project-example/repo", "recents"}) {
				t.Errorf("b's collapsedGroups now %v", got)
			}
			if got := w.collapsed(ids.DefaultFolder, "collapsedFamilies"); !slices.Equal(got, []string{family}) {
				t.Errorf("b's collapsedFamilies now %v", got)
			}
			if after := spans(ids.DefaultFolder); after != before {
				t.Errorf("b's collapse members were rewritten:\n%s\nwas\n%s", after, before)
			}
			// The next switch sees a valid base: no read-back warning.
			w.switch7(SwitchOptions{To: "a", Yes: true, LogOffset: -1})
		})
	}
}

// failingSnapshots makes every Local Storage snapshot fail.
type failingSnapshots struct{ kv.Opener }

func (failingSnapshots) OpenSnapshot(context.Context, string) (kv.Reader, error) {
	return nil, errors.New("snapshot failed")
}

// TestIntegrationVerifyCollapseFromLocalStorage: P12 reads the collapse
// row back from a Local Storage snapshot: equal → nothing; a change after
// the launch → replaced (exit 0), contested entry stored, base :=
// observed; a snapshot that cannot be opened → unverified, exit 0.
func TestIntegrationVerifyCollapseFromLocalStorage(t *testing.T) {
	w := newRealWorldWith(t, func(v int, spec *testkit.FolderSpec) {
		spec.CollapsedGroups = []string{fmt.Sprintf("project-/tmp/v%d", v)}
	})
	ctx := context.Background()
	w.switch7(yes("b"))
	res, err := New(w.deps7()).Verify(ctx)
	if err != nil || slices.Contains(res.Replaced, model.RowSidebarCollapse) || len(res.Unverified) != 0 {
		t.Fatalf("verify right after the switch: %+v, %v", res, err)
	}
	// Opener failure: unverified, never drift.
	d := w.deps7()
	d.KV = failingSnapshots{d.KV}
	res, err = New(d).Verify(ctx)
	if err != nil || !slices.Equal(res.Unverified, []model.RowID{model.RowSidebarCollapse}) || len(res.Drift) != 0 {
		t.Fatalf("verify with a failing snapshot: %+v, %v", res, err)
	}
	// The person folds a group right after launch; the next verify (still
	// recording) reports it as replaced and moves the base.
	root, _ := w.st.Load()
	root.LastVerify = nil
	if err := w.st.Commit(root); err != nil {
		t.Fatal(err)
	}
	w.setCollapsed(ids.DefaultFolder, "project-/tmp/v1", "project-/tmp/v2", "recents")
	res, err = New(w.deps7()).Verify(ctx)
	if err != nil || !slices.Contains(res.Replaced, model.RowSidebarCollapse) || len(res.Drift) != 0 {
		t.Fatalf("verify after a fold: %+v, %v", res, err)
	}
	root, err = w.st.Load()
	if err != nil {
		t.Fatal(err)
	}
	key := state.BaseKey{Alias: "b", Replica: testkit.Replica(2), Row: model.RowSidebarCollapse}
	if _, ok := root.Contested[state.ContestedKey(key, "collapsedGroups")]; !ok {
		t.Fatalf("no contested entry: %v", root.Contested)
	}
	base, err := w.st.GetSnapshot(root.Bases[key.String()])
	if err != nil {
		t.Fatal(err)
	}
	rc := sources.ReadContext{Folder: w.folder(ids.DefaultFolder), Replica: testkit.Replica(2),
		LS: testkit.LS(t, w.folder(ids.DefaultFolder)), Spec: testkit.Spec(t, testkit.Manifest7(t), model.RowSidebarCollapse, testkit.Replica(2))}
	observed, err := sidebar.Collapse{}.Read(ctx, rc)
	if err != nil {
		t.Fatal(err)
	}
	if !sameItems(base, observed) {
		t.Fatal("base is not what was observed")
	}
}
