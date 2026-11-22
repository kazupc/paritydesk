package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/app"
	"github.com/kazupc/paritydesk/internal/apply"
	"github.com/kazupc/paritydesk/internal/clock"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/gen"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/profile"
	"github.com/kazupc/paritydesk/internal/rows"
	"github.com/kazupc/paritydesk/internal/runner"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/sources/all"
	"github.com/kazupc/paritydesk/internal/sources/configjson"
	"github.com/kazupc/paritydesk/internal/sources/prefs"
	"github.com/kazupc/paritydesk/internal/sources/worktrees"
	"github.com/kazupc/paritydesk/internal/state"
	"github.com/kazupc/paritydesk/internal/store/kv/leveldb"
	"github.com/kazupc/paritydesk/internal/testkit"
)

// This file runs the orchestrator over the real implementations of every
// dependency except the app itself (stubApp): real folders built by
// testkit, the embedded compat manifest, real Local Storage through
// goleveldb, the real state store, generations, adapters, merge rows,
// plan composition and validation, and apply. Everything lives in
// t.TempDir().

// stubApp stands in for the app: it quits on request, never vetoes, and
// on launch creates an empty Claude folder when none exists.
type stubApp struct {
	dataRoot string
	running  bool
	launches int
	quits    int
	dirty    map[string]bool // worktree paths git status reports dirty
}

var _ AppControl = (*stubApp)(nil)

const stubVersion = "6.28899.2"

func (a *stubApp) Locate(context.Context, string) (app.BundleIdentity, error) {
	return app.BundleIdentity{BundleID: config.DefaultBundleID, Path: "/Apps/Claude.app", Version: stubVersion, Build: "1"}, nil
}
func (a *stubApp) UpdateStaged(context.Context, string) (bool, error) { return false, nil }
func (a *stubApp) Running(_ context.Context, path string) ([]app.Process, error) {
	if !a.running {
		return nil, nil
	}
	return []app.Process{{PID: 100, Exe: path + "/Contents/MacOS/Claude"}}, nil
}
func (a *stubApp) CheckQuiescence(context.Context, app.QuiescenceInput) (app.QuiescenceReport, error) {
	return app.QuiescenceReport{Quiet: !a.running}, nil
}
func (a *stubApp) QuitAndWait(context.Context, string, string, compat.Signals, time.Duration, func()) (app.QuitResult, error) {
	a.quits++
	a.running = false
	return app.QuitResult{Exited: true}, nil
}
func (a *stubApp) WaitExited(context.Context, string, time.Duration) (bool, error) {
	return !a.running, nil
}
func (a *stubApp) WaitTurnEnded(context.Context, int64, compat.Signals, ids.SessionID, time.Duration) error {
	return nil
}
func (a *stubApp) BusySessions(context.Context, int64, compat.Signals) ([]ids.SessionID, error) {
	return nil, nil
}
func (a *stubApp) WaitUpdateInstalled(context.Context, string, time.Duration) error { return nil }
func (a *stubApp) ActiveOrg(context.Context, compat.Signals, ids.AccountID) (ids.OrgID, bool, error) {
	return "", false, app.ErrSignalUnknown
}
func (a *stubApp) Launch(context.Context, string) error {
	a.launches++
	a.running = true
	return os.MkdirAll(filepath.Join(a.dataRoot, ids.DefaultFolder), 0o700)
}
func (a *stubApp) OpenSession(context.Context, ids.SessionID) error { return nil }
func (a *stubApp) ProbeAutomation(context.Context, string) error    { return nil }
func (a *stubApp) Notify(context.Context, string, string) error     { return nil }
func (a *stubApp) LogSize() (int64, error)                          { return 0, nil }
func (a *stubApp) LogEvents(context.Context, int64, compat.Signals, ids.SessionID) ([]app.LogEvent, int64, error) {
	return nil, 0, nil
}
func (a *stubApp) GitDirty(_ context.Context, p string) (bool, error) { return a.dirty[p], nil }
func (a *stubApp) LockHeld(string) (bool, error)                      { return false, nil }

// realWorld is a data root with two profiles and the real dependencies.
type realWorld struct {
	t     *testing.T
	data  *testkit.DataRoot
	home  string
	paths config.Paths
	cfg   *config.Config
	app   *stubApp
	st    *state.Store
	ui    *fakeUI
	clk   *clock.Fake
}

// newRealWorld builds profile a (replica 1, active in Claude) and b
// (replica 2, parked in Claude-b), both from testkit.Rich.
func newRealWorld(t *testing.T) *realWorld {
	return newRealWorldWith(t, nil)
}

// newRealWorldWith is newRealWorld with a hook that may change each
// folder's spec (variant 1 is a, 2 is b) before it is written.
func newRealWorldWith(t *testing.T, edit func(variant int, spec *testkit.FolderSpec)) *realWorld {
	t.Helper()
	w := &realWorld{t: t, data: testkit.NewDataRoot(t), home: filepath.Join(t.TempDir(), "home"),
		clk: clock.NewFake(time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)), ui: &fakeUI{answers: map[ConfirmKind]bool{}}}
	for i, name := range []string{ids.DefaultFolder, "Claude-b"} {
		spec := testkit.Rich(name, i+1)
		if edit != nil {
			edit(i+1, &spec)
		}
		w.data.AddFolder(t, spec)
	}
	w.paths = config.ResolvePaths(w.home, func(string) string { return "" }, config.Overrides{})
	w.cfg = config.Default()
	w.cfg.App.DataRoot = w.data.Dir
	w.cfg.Profiles = []config.Profile{{Alias: "a"}, {Alias: "b"}}
	w.app = &stubApp{dataRoot: w.data.Dir, running: true}
	st, err := state.Open(w.paths.StateDir, state.LockFile(w.paths.SupportDir, w.data.Dir), fsx.Default(), w.clk)
	if err != nil {
		t.Fatal(err)
	}
	w.st = st
	root := state.NewRoot(w.data.Dir)
	root.Profiles["a"] = &state.ProfileState{Alias: "a", Replica: testkit.Replica(1)}
	root.Profiles["b"] = &state.ProfileState{Alias: "b", Replica: testkit.Replica(2)}
	root.Active, root.Previous = "a", "b"
	if err := st.Commit(root); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *realWorld) deps() Deps {
	w.t.Helper()
	ops := fsx.Default()
	manifests, err := compat.Load(filepath.Join(w.paths.SupportDir, "compat.d"))
	if err != nil {
		w.t.Fatal(err)
	}
	opener, err := leveldb.New(filepath.Join(w.data.Dir, ".paritydesk-tmp"), ops)
	if err != nil {
		w.t.Fatal(err)
	}
	return Deps{
		Config: w.cfg, Paths: w.paths, Home: w.home, DataRoot: w.data.Dir, Clock: w.clk, FS: ops,
		State: w.st,
		Gens:  gen.Open(w.paths.GenDir, ops, w.clk),
		Folders: profile.Scanner{FS: ops, DataRoot: w.data.Dir,
			ReadIdentity: configjson.ReadIdentity, ListReplicas: sources.DiscoverReplicas},
		Resolve: profile.Resolve, ReadMarker: profile.ReadMarker, WriteMarker: profile.WriteMarker,
		App: w.app, Compat: compat.Gate{Manifests: manifests}, KV: opener,
		Adapters: all.Registry(), Rows: rows.Registry(), PostMerge: rows.PostMerge,
		Compose: plan.Compose, Validate: plan.Validate, PermissionAcks: prefs.PermissionAcks,
		Applier: &apply.Applier{FS: ops, KV: opener,
			Holders: func(context.Context, string) (bool, string, error) { return false, "", nil }},
		Sessions: sources.Directory{}, Worktrees: worktrees.Guard{},
		Detach: &fakeDetacher{}, Plans: runner.PlanStore{Dir: w.paths.RunDir}, UI: w.ui,
	}
}

// accountIn returns the account recorded in a folder's config.json.
func (w *realWorld) accountIn(folder string) ids.AccountID {
	w.t.Helper()
	id, err := configjson.ReadIdentity(filepath.Join(w.data.Dir, folder))
	if err != nil {
		w.t.Fatalf("identity of %s: %v", folder, err)
	}
	return id.Account
}

// sessionKeys lists the session records of replica r in folder.
func (w *realWorld) sessionKeys(folder string, r model.Replica) []string {
	w.t.Helper()
	entries, err := os.ReadDir(filepath.Join(w.data.Dir, folder, sources.SessionRel(r, "")))
	if err != nil {
		w.t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if k, ok := strings.CutSuffix(e.Name(), sources.RecordSuffix); ok && strings.HasPrefix(k, sources.RecordPrefix) {
			out = append(out, k)
		}
	}
	return out
}

func TestIntegrationSwitchRoundTrip(t *testing.T) {
	w := newRealWorld(t)
	ctx := context.Background()

	res, err := New(w.deps()).Switch(ctx, yes("b"))
	if err != nil {
		t.Fatalf("switch to b: %v (events %v)", err, w.ui.events)
	}
	if res.Code != pderr.OK || res.Mode != compat.ModeFull {
		t.Fatalf("switch to b: code %d mode %s", res.Code, res.Mode)
	}
	if res.Plan == nil || len(res.Plan.Ops) == 0 {
		t.Fatal("switch to b applied no ops")
	}
	if got := w.accountIn(ids.DefaultFolder); got != testkit.Replica(2).Account {
		t.Fatalf("Claude holds %s after switching to b", got)
	}
	if got := w.accountIn("Claude-a"); got != testkit.Replica(1).Account {
		t.Fatalf("Claude-a holds %s", got)
	}
	// Session 11 exists only in a; the first sync carries it to b.
	only := testkit.FakeSession(11)
	if keys := w.sessionKeys(ids.DefaultFolder, testkit.Replica(2)); !containsKey(keys, only) {
		t.Fatalf("session only in a missing from b: %v", keys)
	}
	// The copy new to b carries none of a's security grants.
	rec, err := os.ReadFile(filepath.Join(w.data.Dir, ids.DefaultFolder, sources.SessionRel(testkit.Replica(2), only+sources.RecordSuffix)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"chromePermissionMode", "alwaysAllowedReasons", "sessionPermissionUpdates"} {
		if strings.Contains(string(rec), `"`+f+`"`) {
			t.Errorf("the copy new to b carries %s", f)
		}
	}
	if w.app.quits != 1 || w.app.launches != 1 {
		t.Fatalf("quits %d launches %d", w.app.quits, w.app.launches)
	}

	w.app.running = true
	res, err = New(w.deps()).Switch(ctx, yes("a"))
	if err != nil || res.Code != pderr.OK {
		t.Fatalf("switch back to a: %v code %v", err, res)
	}
	if got := w.accountIn(ids.DefaultFolder); got != testkit.Replica(1).Account {
		t.Fatalf("Claude holds %s after switching back", got)
	}
	// Session 12 exists only in b; it reaches a on the way back.
	if keys := w.sessionKeys(ids.DefaultFolder, testkit.Replica(1)); !containsKey(keys, testkit.FakeSession(12)) {
		t.Fatalf("session only in b missing from a: %v", keys)
	}

	if _, err := New(w.deps()).Recover(ctx, RecoverOptions{Yes: true}); err != nil {
		t.Fatalf("recover after clean runs: %v", err)
	}
	root, err := w.st.Load()
	if err != nil || root.Run != nil || root.Active != "a" {
		t.Fatalf("root after round trip: %v %+v", err, root)
	}
	gens, err := gen.Open(w.paths.GenDir, fsx.Default(), w.clk).List()
	if err != nil || len(gens) != 2 {
		t.Fatalf("generations: %v %d", err, len(gens))
	}
}

func containsKey(keys []string, k string) bool {
	for _, x := range keys {
		if x == k {
			return true
		}
	}
	return false
}

// TestIntegrationOtherAccountsAndBackupUntouched: each folder also holds
// another account's session directory and the app's dframe-store backup.
// A round trip of switches leaves both byte-identical in both folders.
func TestIntegrationOtherAccountsAndBackupUntouched(t *testing.T) {
	w := newRealWorld(t)
	ctx := context.Background()
	type watched struct {
		dir    string
		before map[string]string
		backup string
	}
	watch := func(folder string, n int) watched {
		dir := filepath.Join(w.data.Dir, folder, sources.SessionRel(testkit.DecoyReplica(n), ""))
		return watched{dir: dir, before: testkit.TreeOf(t, dir),
			backup: testkit.ReadLS(t, filepath.Join(w.data.Dir, folder))[testkit.StoreBackupKey]}
	}
	a, b := watch(ids.DefaultFolder, 1), watch("Claude-b", 2)
	if len(a.before) == 0 || a.backup == "" {
		t.Fatal("fixture lacks the decoy or the backup")
	}
	if _, err := New(w.deps()).Switch(ctx, yes("b")); err != nil {
		t.Fatalf("switch to b: %v", err)
	}
	w.app.running = true
	if _, err := New(w.deps()).Switch(ctx, yes("a")); err != nil {
		t.Fatalf("switch back to a: %v", err)
	}
	// a is back in Claude, b is parked in Claude-b.
	for _, c := range []struct {
		folder string
		w      watched
		n      int
	}{{ids.DefaultFolder, a, 1}, {"Claude-b", b, 2}} {
		dir := filepath.Join(w.data.Dir, c.folder, sources.SessionRel(testkit.DecoyReplica(c.n), ""))
		after := testkit.TreeOf(t, dir)
		if len(after) != len(c.w.before) {
			t.Fatalf("%s: decoy directory changed", c.folder)
		}
		for k, v := range c.w.before {
			if after[k] != v {
				t.Fatalf("%s: decoy file %s changed", c.folder, k)
			}
		}
		if got := testkit.ReadLS(t, filepath.Join(w.data.Dir, c.folder))[testkit.StoreBackupKey]; got != c.w.backup {
			t.Fatalf("%s: dframe-store backup changed", c.folder)
		}
	}
}

// TestIntegrationVerifyReportsRestoredStore: after a switch, Claude
// restores the sidebar from its dframe-store backup and the written pin
// order is gone from the config mirror. Verify reports drift, not
// success.
func TestIntegrationVerifyReportsRestoredStore(t *testing.T) {
	w := newRealWorld(t)
	ctx := context.Background()
	if _, err := New(w.deps()).Switch(ctx, yes("b")); err != nil {
		t.Fatalf("switch to b: %v", err)
	}
	if _, err := New(w.deps()).Verify(ctx); err != nil {
		t.Fatalf("verify right after the switch: %v", err)
	}
	cfgPath := filepath.Join(w.data.Dir, ids.DefaultFolder, sources.DesktopConfigFile)
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	ep := cfg["preferences"].(map[string]any)["epitaxyPrefs"].(map[string]any)
	delete(ep["dframe-local-slice"].(map[string]any), "pinnedOrder")
	out, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, out, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := New(w.deps()).Verify(ctx)
	if pderr.CodeOf(err) != pderr.VerifyDrift {
		t.Fatalf("verify after the restore: %v", err)
	}
	if !slices.Contains(res.Drift, model.RowPinnedOrder) {
		t.Fatalf("drift %v", res.Drift)
	}
}

// TestIntegrationRepeatedSwitchesKeepBases: after both profiles have been
// entered once, every later switch has valid bases for every row, so it
// needs no first-sync confirmation and prints no read-back warning. A
// scripted switch (--yes without --confirm-first-sync) then succeeds.
func TestIntegrationRepeatedSwitchesKeepBases(t *testing.T) {
	w := newRealWorld(t)
	ctx := context.Background()
	seq := []ids.Alias{"b", "a", "b", "a"}
	for i, to := range seq {
		w.app.running = true
		res, err := New(w.deps()).Switch(ctx, yes(to))
		if err != nil {
			t.Fatalf("switch %d to %s: %v", i+1, to, err)
		}
		for _, warn := range res.Warnings {
			if strings.Contains(warn, "does not read back") {
				t.Errorf("switch %d to %s: %s", i+1, to, warn)
			}
		}
		if i >= 2 && res.Plan != nil && res.Plan.NeedsConfirmation {
			t.Errorf("switch %d to %s still needs first-sync confirmation", i+1, to)
		}
	}
	w.app.running = true
	opts := SwitchOptions{To: "b", Yes: true, LogOffset: -1}
	res, err := New(w.deps()).Switch(ctx, opts)
	if err != nil {
		t.Fatalf("scripted switch: %v", err)
	}
	if res.Code != pderr.OK {
		t.Fatalf("scripted switch: code %d", res.Code)
	}
}

// TestIntegrationOtherOrgStartedKept: the started-environments map is one
// Local Storage item per folder, so it also holds entries of sessions in
// another account's directory (the decoy). Switches never delete them.
func TestIntegrationOtherOrgStartedKept(t *testing.T) {
	decoy := func(v int) string { return testkit.FakeSession(70 + v) }
	w := newRealWorldWith(t, func(v int, spec *testkit.FolderSpec) {
		spec.StartedEnvironments[decoy(v)] = "env-a"
		spec.StartedEnvironments["session_01ABCDEF"] = "env-a"
	})
	ctx := context.Background()
	for i, to := range []ids.Alias{"b", "a", "b"} {
		w.app.running = true
		res, err := New(w.deps()).Switch(ctx, yes(to))
		if err != nil {
			t.Fatalf("switch %d to %s: %v", i+1, to, err)
		}
		for _, c := range res.Plan.Changes {
			if c.Row == model.RowEnvironments && c.Kind == model.ChangeDeleted &&
				(strings.Contains(c.Key, decoy(1)) || strings.Contains(c.Key, decoy(2)) || strings.Contains(c.Key, "session_")) {
				t.Fatalf("switch %d plans deleting %s", i+1, c.Key)
			}
		}
	}
	for _, c := range []struct {
		folder string
		v      int
	}{{ids.DefaultFolder, 2}, {"Claude-a", 1}} {
		ls := testkit.ReadLS(t, filepath.Join(w.data.Dir, c.folder))["persisted.ccd-started-environments"]
		for _, id := range []string{decoy(c.v), "session_01ABCDEF"} {
			if !strings.Contains(ls, id) {
				t.Errorf("%s: started entry %s lost", c.folder, id)
			}
		}
	}
}

// pinsIn returns the pinnedOrder of a folder's config mirror.
func (w *realWorld) pinsIn(folder string) []any {
	w.t.Helper()
	b, err := os.ReadFile(filepath.Join(w.data.Dir, folder, sources.DesktopConfigFile))
	if err != nil {
		w.t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		w.t.Fatal(err)
	}
	ep := cfg["preferences"].(map[string]any)["epitaxyPrefs"].(map[string]any)
	pins, _ := ep["dframe-local-slice"].(map[string]any)["pinnedOrder"].([]any)
	return pins
}

// setPins rewrites the pinnedOrder of a folder's config mirror, as the
// app does when the user reorders the pins.
func (w *realWorld) setPins(folder string, pins []any) {
	w.t.Helper()
	p := filepath.Join(w.data.Dir, folder, sources.DesktopConfigFile)
	b, err := os.ReadFile(p)
	if err != nil {
		w.t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		w.t.Fatal(err)
	}
	ep := cfg["preferences"].(map[string]any)["epitaxyPrefs"].(map[string]any)
	ep["dframe-local-slice"].(map[string]any)["pinnedOrder"] = pins
	out, err := json.Marshal(cfg)
	if err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(p, out, 0o600); err != nil {
		w.t.Fatal(err)
	}
}

// TestIntegrationLateVerifyKeepsUserEdit: the user reorders the pins
// after the run's own verify, then runs paritydesk verify again. That
// verify reports the difference but records nothing, so the edit still
// reaches the other profile and survives the switch back.
func TestIntegrationLateVerifyKeepsUserEdit(t *testing.T) {
	w := newRealWorld(t)
	ctx := context.Background()
	for _, to := range []ids.Alias{"b", "a", "b"} {
		w.app.running = true
		if _, err := New(w.deps()).Switch(ctx, yes(to)); err != nil {
			t.Fatalf("switch to %s: %v", to, err)
		}
	}
	if _, err := New(w.deps()).Verify(ctx); err != nil {
		t.Fatalf("verify: %v", err)
	}
	pins := w.pinsIn(ids.DefaultFolder)
	if len(pins) < 2 {
		t.Fatalf("fixture has pins %v", pins)
	}
	edited := slices.Clone(pins)
	slices.Reverse(edited)
	w.setPins(ids.DefaultFolder, edited)
	if _, err := New(w.deps()).Verify(ctx); pderr.CodeOf(err) != pderr.VerifyDrift {
		t.Fatalf("verify after the edit: %v", err)
	}
	for _, to := range []ids.Alias{"a", "b"} {
		w.app.running = true
		if _, err := New(w.deps()).Switch(ctx, yes(to)); err != nil {
			t.Fatalf("switch to %s: %v", to, err)
		}
		if got := w.pinsIn(ids.DefaultFolder); !slices.Equal(got, edited) {
			t.Fatalf("after switching to %s the pins are %v, want %v", to, got, edited)
		}
	}
}

// TestIntegrationWorktreeRegistryDeletionGuarded: the leaving profile
// deleted a worktree registration that the entering folder still holds,
// and the worktree directory has uncommitted work. The worktrees row
// would drop the registration, so the switch stops with exit 23.
func TestIntegrationWorktreeRegistryDeletionGuarded(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keep-me")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	w := newRealWorldWith(t, func(_ int, spec *testkit.FolderSpec) {
		spec.Worktrees["keep-me"] = map[string]any{"name": "keep-me", "path": dir, "branch": "wt/keep-me",
			"sourceBranch": "main", "createdAt": 5}
	})
	w.app.dirty = map[string]bool{dir: true}
	ctx := context.Background()
	for _, to := range []ids.Alias{"b", "a"} {
		w.app.running = true
		if _, err := New(w.deps()).Switch(ctx, yes(to)); err != nil {
			t.Fatalf("switch to %s: %v", to, err)
		}
	}
	// a is active: remove the registration there, as the app would.
	p := filepath.Join(w.data.Dir, ids.DefaultFolder, worktrees.FileName)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var reg map[string]any
	if err := json.Unmarshal(b, &reg); err != nil {
		t.Fatal(err)
	}
	delete(reg["worktrees"].(map[string]any), "keep-me")
	out, _ := json.Marshal(reg)
	if err := os.WriteFile(p, out, 0o600); err != nil {
		t.Fatal(err)
	}
	w.app.running = true
	_, err = New(w.deps()).Switch(ctx, yes("b"))
	if pderr.CodeOf(err) != pderr.WorktreeRisk {
		t.Fatalf("switch with a dirty worktree losing its registration: %v", err)
	}
	opts := yes("b")
	opts.AcceptWorktreeRisk = true
	w.app.running = true
	if _, err := New(w.deps()).Switch(ctx, opts); err != nil {
		t.Fatalf("switch accepting the risk: %v", err)
	}
}
