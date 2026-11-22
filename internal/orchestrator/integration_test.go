package orchestrator

import (
	"context"
	"os"
	"path/filepath"
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
func (a *stubApp) ActiveOrg(context.Context, compat.Signals) (ids.OrgID, bool, error) {
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
func (a *stubApp) GitDirty(context.Context, string) (bool, error) { return false, nil }
func (a *stubApp) LockHeld(string) (bool, error)                  { return false, nil }

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
	t.Helper()
	w := &realWorld{t: t, data: testkit.NewDataRoot(t), home: filepath.Join(t.TempDir(), "home"),
		clk: clock.NewFake(time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)), ui: &fakeUI{answers: map[ConfirmKind]bool{}}}
	w.data.AddFolder(t, testkit.Rich(ids.DefaultFolder, 1))
	w.data.AddFolder(t, testkit.Rich("Claude-b", 2))
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
