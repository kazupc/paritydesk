package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/gen"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/rows"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/state"
	"github.com/kazupc/paritydesk/internal/store/kv"
)

// Tests for the guards around folder moves and restores: recover's
// contradiction branch, the stopped-app requirement, live-folder
// restores, the P0 parked-name check, the fallback swap's post check,
// pending profile adds and the rollback target.

// openSwitchRun commits an open switch a -> b whose last intent is last
// (not done), with a base for each profile.
func openSwitchRun(t *testing.T, e *env, last state.IntentName) {
	t.Helper()
	root := e.st.root(t)
	root.Run = &state.Run{ID: "run-0001", Kind: state.RunSwitch, From: "a", To: "b", Started: e.clk.Now(),
		Intents: []state.Intent{{Name: state.IntentQuit, Done: true}, {Name: state.IntentPlanned, Done: true}, {Name: last}}}
	for _, k := range []state.BaseKey{
		{Alias: "a", Replica: repA, Row: model.RowSessions},
		{Alias: "b", Replica: repB, Row: model.RowSessions},
	} {
		root.Bases[k.String()] = state.Ref(strings.Repeat("ab", 32))
	}
	if err := e.st.Commit(root); err != nil {
		t.Fatal(err)
	}
	e.app.running = false
}

func (e *env) move(from, to string) {
	e.t.Helper()
	if err := os.Rename(filepath.Join(e.root, from), filepath.Join(e.root, to)); err != nil {
		e.t.Fatal(err)
	}
}

// moveOut moves a data folder out of the data root.
func (e *env) moveOut(name string) {
	e.t.Helper()
	if err := os.Rename(filepath.Join(e.root, name), filepath.Join(e.t.TempDir(), name)); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) exists(name string) bool {
	_, err := os.Lstat(filepath.Join(e.root, name))
	return err == nil
}

func hasBase(root *state.Root, a ids.Alias) bool {
	for k := range root.Bases {
		if strings.HasPrefix(k, string(a)+"/") {
			return true
		}
	}
	return false
}

func TestRecoverContradictionBranch(t *testing.T) {
	tests := []struct {
		name        string
		layout      func(e *env)
		opts        RecoverOptions
		interactive bool
		decline     bool
		wantCode    pderr.Code
		// wantClaude is the account Claude/ must hold afterwards ("" for
		// none); wantActive and wantBBase are checked on success.
		wantClaude ids.AccountID
		wantActive ids.Alias
		wantBBase  bool
	}{
		{
			name:       "claude missing, leaving profile parked",
			layout:     func(e *env) { e.move(ids.DefaultFolder, "Claude-a") },
			opts:       RecoverOptions{Yes: true},
			wantClaude: acctA, wantActive: "a", wantBBase: false,
		},
		{
			name:       "claude missing, only the entering profile parked",
			layout:     func(e *env) { e.moveOut(ids.DefaultFolder) },
			opts:       RecoverOptions{Yes: true},
			wantClaude: acctB, wantActive: "b", wantBBase: true,
		},
		{
			name: "claude missing, foreign and unidentified folders",
			layout: func(e *env) {
				e.moveOut(ids.DefaultFolder)
				e.makeFolder("Claude-a", model.Replica{Account: acctC, Org: orgA}, nil)
				e.makeFolder("Claude-b", model.Replica{}, nil)
			},
			opts:     RecoverOptions{Yes: true},
			wantCode: pderr.NeedsRecover,
		},
		{
			name:     "non-interactive without --yes",
			layout:   func(e *env) { e.move(ids.DefaultFolder, "Claude-a") },
			wantCode: pderr.Declined,
		},
		{
			name:        "declined",
			layout:      func(e *env) { e.move(ids.DefaultFolder, "Claude-a") },
			interactive: true, decline: true,
			wantCode: pderr.Declined,
		},
		{
			name:       "claude present but holding another account",
			layout:     func(e *env) { e.makeFolder(ids.DefaultFolder, model.Replica{Account: acctC, Org: orgA}, nil) },
			opts:       RecoverOptions{Yes: true},
			wantCode:   pderr.NeedsRecover,
			wantClaude: acctC,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			openSwitchRun(t, e, state.IntentSwapping)
			tc.layout(e)
			e.ui.interactive = tc.interactive
			if tc.decline {
				e.ui.answers[ConfirmRecover] = false
			}
			before, _ := os.ReadDir(e.root)
			_, err := e.orch().Recover(context.Background(), tc.opts)
			root := e.st.root(t)
			if got := pderr.CodeOf(err); got != tc.wantCode {
				t.Fatalf("code %d, want %d (%v)", got, tc.wantCode, err)
			}
			if got := e.account(ids.DefaultFolder); got != tc.wantClaude {
				t.Errorf("Claude holds %q, want %q", got, tc.wantClaude)
			}
			if tc.wantCode != pderr.OK {
				after, _ := os.ReadDir(e.root)
				if len(after) != len(before) || root.Run == nil {
					t.Errorf("folders or run changed on refusal: %d -> %d entries, run %v", len(before), len(after), root.Run)
				}
				return
			}
			if root.Run != nil || root.Active != tc.wantActive || hasBase(root, "b") != tc.wantBBase {
				t.Errorf("run %v active %q b base %v", root.Run, root.Active, hasBase(root, "b"))
			}
			if !hasBase(root, "a") {
				t.Error("a's base was dropped")
			}
		})
	}
}

func TestRecoverRequiresStoppedApp(t *testing.T) {
	tests := []struct {
		name  string
		setup func(e *env)
	}{
		{"bundle not found", func(e *env) { e.app.locateErr = errors.New("not found") }},
		{"app running", func(e *env) { e.app.running = true }},
		{"folder held", func(e *env) { e.app.inUse[filepath.Join(e.root, "Claude-b")] = true }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			openSwitchRun(t, e, state.IntentSwapping)
			e.move(ids.DefaultFolder, "Claude-a")
			tc.setup(e)
			_, err := e.orch().Recover(context.Background(), RecoverOptions{Yes: true})
			if pderr.CodeOf(err) != pderr.NeedsRecover {
				t.Fatalf("err = %v", err)
			}
			if e.exists(ids.DefaultFolder) || e.st.root(t).Run == nil {
				t.Error("recover moved a folder or closed the run while the app may run")
			}
		})
	}
}

// crashSync leaves a sync interrupted right after the applied intent,
// with a LevelDB clone in its generation.
func crashSync(t *testing.T, e *env) {
	t.Helper()
	afterSwitch(t, e)
	e.invalid["b"] = true
	e.writeRow(ids.DefaultFolder, model.RowSessions, map[string]any{string(fakeSession(2)): map[string]any{"title": "two"}})
	e.ui.interactive = true
	e.app.running = true
	e.crash = func(p Phase) bool { return p == P7Apply }
	if _, err := e.orch().Sync(context.Background(), SyncOptions{Yes: true}); !errors.Is(err, ErrCrashInjected) {
		t.Fatalf("sync: %v", err)
	}
	e.crash = nil
	root := e.st.root(t)
	if root.Run == nil || root.Run.Generation == 0 {
		t.Fatalf("no interrupted sync: %+v", root.Run)
	}
	e.gens.gens[root.Run.Generation].m.LevelDB = "leveldb"
}

func TestRecoverSyncNeverRestoresLiveLevelDB(t *testing.T) {
	e := newEnv(t)
	crashSync(t, e)
	if _, err := e.orch().Recover(context.Background(), RecoverOptions{Yes: true}); err != nil {
		t.Fatal(err)
	}
	if e.gens.levelRestores != 0 {
		t.Errorf("recover replaced the live folder's Local Storage %d time(s)", e.gens.levelRestores)
	}
	if e.st.root(t).Run != nil {
		t.Error("run still open")
	}
}

func TestRecoverSyncChecksIdentity(t *testing.T) {
	e := newEnv(t)
	crashSync(t, e)
	e.makeFolder(ids.DefaultFolder, model.Replica{Account: acctC, Org: orgA}, nil)
	restores := e.gens.restores
	_, err := e.orch().Recover(context.Background(), RecoverOptions{Yes: true})
	if pderr.CodeOf(err) != pderr.NeedsRecover {
		t.Fatalf("err = %v", err)
	}
	if e.gens.restores != restores || e.st.root(t).Run == nil {
		t.Error("restored into a folder holding another account")
	}
}

func TestRecoverSwitchRollbackChecksIdentity(t *testing.T) {
	e := newEnv(t)
	e.crash = func(p Phase) bool { return p == P7Apply }
	if _, err := e.orch().Switch(context.Background(), yes("b")); !errors.Is(err, ErrCrashInjected) {
		t.Fatal(err)
	}
	e.crash = nil
	e.makeFolder("Claude-b", model.Replica{Account: acctC, Org: orgB}, nil)
	_, err := e.orch().Recover(context.Background(), RecoverOptions{Yes: true})
	if pderr.CodeOf(err) != pderr.NeedsRecover || e.gens.restores != 0 {
		t.Fatalf("err = %v, restores %d", err, e.gens.restores)
	}
}

func TestApplyFailureRestores(t *testing.T) {
	tests := []struct {
		name      string
		failAfter int
		inUse     bool
		wantCode  pderr.Code
		wantOpen  bool
		restores  int
	}{
		{name: "nothing written", failAfter: 0, wantCode: pderr.ApplyRolledBack, restores: 0},
		{name: "partly written", failAfter: 1, wantCode: pderr.ApplyRolledBack, restores: 1},
		{name: "partly written, folder reopened", failAfter: 1, inUse: true, wantCode: pderr.NeedsRecover, wantOpen: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.applier.failAfter = tc.failAfter
			if tc.inUse {
				e.applier.before = func() { e.app.inUse[filepath.Join(e.root, "Claude-b")] = true }
			}
			_, err := e.orch().Switch(context.Background(), yes("b"))
			if got := pderr.CodeOf(err); got != tc.wantCode {
				t.Fatalf("code %d, want %d (%v)", got, tc.wantCode, err)
			}
			if e.gens.restores != tc.restores || e.gens.levelRestores != 0 {
				t.Errorf("restores %d (want %d), LevelDB restores %d", e.gens.restores, tc.restores, e.gens.levelRestores)
			}
			if open := e.st.root(t).Run != nil; open != tc.wantOpen {
				t.Fatalf("run open %v, want %v", open, tc.wantOpen)
			}
			if !tc.wantOpen {
				return
			}
			// Once the holder is gone, recover rolls the folder back.
			e.app.inUse = map[string]bool{}
			e.app.running = false
			if _, err := e.orch().Recover(context.Background(), RecoverOptions{Yes: true}); err != nil {
				t.Fatal(err)
			}
			if e.gens.restores != 1 {
				t.Errorf("recover restores %d", e.gens.restores)
			}
			e.checkInvariants(tc.name)
		})
	}
}

func TestReopenedAppBeforeCommitAborts(t *testing.T) {
	e := newEnv(t)
	e.ui.onEvent = func(ev Event) {
		if ev.Phase == P5Harvest && ev.Status == "start" {
			e.app.running = true
		}
	}
	_, err := e.orch().Switch(context.Background(), yes("b"))
	if pderr.CodeOf(err) != pderr.AppDidNotQuit {
		t.Fatalf("err = %v", err)
	}
	if e.applier.applies != 0 || len(e.gens.gens) != 0 || e.st.root(t).Run != nil {
		t.Errorf("applies %d, generations %d", e.applier.applies, len(e.gens.gens))
	}
}

func TestFallbackSwapIntruderStops(t *testing.T) {
	e := newEnv(t)
	e.fs.NoSwap = true
	e.app.intruder = true
	_, err := e.orch().Switch(context.Background(), yes("b"))
	if pderr.CodeOf(err) != pderr.NeedsRecover {
		t.Fatalf("err = %v", err)
	}
	if e.st.root(t).Run == nil || e.app.launches != 0 {
		t.Fatalf("run closed or app launched after an intruder")
	}
	e.app.intruder, e.app.running = false, false
	if _, err := e.orch().Recover(context.Background(), RecoverOptions{Yes: true}); err != nil {
		t.Fatal(err)
	}
	if root := e.st.root(t); root.Active != "b" || e.account(ids.DefaultFolder) != acctB || e.account("Claude-a") != acctA {
		t.Errorf("active %q, Claude holds %q", root.Active, e.account(ids.DefaultFolder))
	}
	e.checkInvariants("fallback intruder")
}

func TestSwitchRefusesTakenParkedName(t *testing.T) {
	for _, name := range []string{"Claude-a", "claude-A"} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.makeDir(filepath.Join(e.root, name))
			_, err := e.orch().Switch(context.Background(), yes("b"))
			if pderr.CodeOf(err) != pderr.IdentityMismatch {
				t.Fatalf("err = %v", err)
			}
			if e.app.quits != 0 || e.st.root(t).Run != nil {
				t.Errorf("quits %d", e.app.quits)
			}
		})
	}
}

func TestRecoverTakenParkedName(t *testing.T) {
	e := newEnv(t)
	n := 0
	e.crash = func(p Phase) bool {
		if p == P8Swap {
			n++
		}
		return n == 2 // right after the swap
	}
	if _, err := e.orch().Switch(context.Background(), yes("b")); !errors.Is(err, ErrCrashInjected) {
		t.Fatal(err)
	}
	e.crash = nil
	e.app.running = false
	e.makeDir(filepath.Join(e.root, "Claude-a"))
	_, err := e.orch().Recover(context.Background(), RecoverOptions{Yes: true})
	if pderr.CodeOf(err) != pderr.NeedsRecover || !strings.Contains(err.Error(), "--rollback") {
		t.Fatalf("err = %v", err)
	}
	if _, err := e.orch().Recover(context.Background(), RecoverOptions{Yes: true, Rollback: true}); err != nil {
		t.Fatal(err)
	}
	root := e.st.root(t)
	if root.Run != nil || root.Active != "a" || e.account(ids.DefaultFolder) != acctA || e.account("Claude-b") != acctB {
		t.Errorf("run %v active %q", root.Run, root.Active)
	}
	if e.gens.restores == 0 {
		t.Error("the entering folder was not rolled back")
	}
}

func TestPendingAddBlocksProfileCommands(t *testing.T) {
	ctx := context.Background()
	cmds := map[string]func(o *Orchestrator) error{
		"init":   func(o *Orchestrator) error { return o.Init(ctx, InitOptions{Alias: "c2"}) },
		"adopt":  func(o *Orchestrator) error { return o.ProfileAdopt(ctx, "c2", "Claude-a", false) },
		"rename": func(o *Orchestrator) error { return o.ProfileRename(ctx, "a", "a2") },
		"remove": func(o *Orchestrator) error { return o.ProfileRemove(ctx, "b") },
		"add":    func(o *Orchestrator) error { return o.ProfileAdd(ctx, "d", false) },
	}
	for name, cmd := range cmds {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			if err := e.orch().ProfileAdd(ctx, "c", false); err != nil {
				t.Fatal(err)
			}
			e.makeFolder(ids.DefaultFolder, model.Replica{Account: acctC, Org: orgB}, nil)
			if err := cmd(e.orch()); pderr.CodeOf(err) != pderr.NeedsRecover {
				t.Fatalf("err = %v", err)
			}
			if root := e.st.root(t); root.PendingAdd == nil || len(root.Profiles) != 2 {
				t.Errorf("state changed: %+v", root)
			}
		})
	}
}

func TestRecoverDropsPendingAddWhenClaudeIsRegistered(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.orch().ProfileAdd(ctx, "c", false); err != nil {
		t.Fatal(err)
	}
	e.app.running = false
	e.makeFolder(ids.DefaultFolder, model.Replica{Account: acctB, Org: orgB}, nil)
	res, err := e.orch().Recover(ctx, RecoverOptions{Yes: true})
	if err != nil {
		t.Fatal(err)
	}
	root := e.st.root(t)
	if root.PendingAdd != nil || root.Active != "b" || root.Previous != "a" || len(i18n.Strings(res.Actions)) == 0 {
		t.Errorf("pending %+v active %q previous %q", root.PendingAdd, root.Active, root.Previous)
	}
	if _, err := e.orch().Recover(ctx, RecoverOptions{Yes: true}); err != nil {
		t.Fatal(err)
	}
}

func TestRenameAliasRewritesPendingAdd(t *testing.T) {
	root := state.NewRoot("/data")
	root.PendingAdd = &state.PendingAdd{Alias: "c", ParkedFrom: "a"}
	renameAlias(root, "a", "a2")
	if root.PendingAdd.ParkedFrom != "a2" {
		t.Errorf("parked from %q", root.PendingAdd.ParkedFrom)
	}
}

func TestRollbackTarget(t *testing.T) {
	ctx := context.Background()
	t.Run("after a rename", func(t *testing.T) {
		e := newEnv(t)
		afterSwitch(t, e)
		e.ui.interactive = true
		if err := e.orch().ProfileRename(ctx, "b", "b2"); err != nil {
			t.Fatal(err)
		}
		res, err := e.orch().Rollback(ctx, 0, false)
		if err != nil {
			t.Fatal(err)
		}
		if res.Restored == 0 || len(res.Conflicts) != 0 {
			t.Errorf("result = %+v", res)
		}
		if got := e.keys(ids.DefaultFolder, model.RowSessions); !slices.Equal(got, []string{string(fakeSession(2))}) {
			t.Errorf("sessions = %v", got)
		}
	})
	t.Run("folder holds another account", func(t *testing.T) {
		e := newEnv(t)
		afterSwitch(t, e)
		e.ui.interactive = true
		e.makeFolder(ids.DefaultFolder, model.Replica{Account: acctC, Org: orgA}, nil)
		if _, err := e.orch().Rollback(ctx, 0, false); pderr.CodeOf(err) != pderr.IdentityMismatch {
			t.Fatalf("err = %v", err)
		}
		if e.gens.restores != 0 {
			t.Error("restored into another account's folder")
		}
	})
	t.Run("state of another data root", func(t *testing.T) {
		e := newEnv(t)
		afterSwitch(t, e)
		root := e.st.root(t)
		root.DataRoot = "/elsewhere"
		if err := e.st.Commit(root); err != nil {
			t.Fatal(err)
		}
		if _, err := e.orch().Rollback(ctx, 0, false); pderr.CodeOf(err) != pderr.Usage {
			t.Fatalf("rollback: %v", err)
		}
		if _, err := e.orch().Recover(ctx, RecoverOptions{Yes: true}); pderr.CodeOf(err) != pderr.Usage {
			t.Fatalf("recover: %v", err)
		}
	})
}

func TestDropBaseDropsContested(t *testing.T) {
	root := state.NewRoot("/data")
	k := state.BaseKey{Alias: "a", Replica: repA, Row: model.RowAppPrefs}.String()
	other := state.BaseKey{Alias: "a", Replica: repA, Row: model.RowSessions}.String()
	root.Bases[k], root.Bases[other] = "r1", "r2"
	root.Contested = map[string]model.Contested{k + "/theme": {}, other + "/x": {}}
	dropBase(root, k)
	if _, ok := root.Bases[k]; ok {
		t.Error("base kept")
	}
	if _, ok := root.Contested[k+"/theme"]; ok {
		t.Error("contested entry of the dropped base kept")
	}
	if _, ok := root.Contested[other+"/x"]; !ok || root.Bases[other] != "r2" {
		t.Error("another base's entries were dropped")
	}
}

func TestDriftOfSectionOrder(t *testing.T) {
	withOrder := func(row model.RowID, order []string, items map[string]string) *model.RowSnapshot {
		s := &model.RowSnapshot{Row: row, Order: order, Items: map[string]model.Item{}}
		for k, v := range items {
			it, err := model.NewItem(k, []byte(v), nil, model.Stamp{}, false)
			if err != nil {
				t.Fatal(err)
			}
			s.Items[k] = it
		}
		return s
	}
	sv, grp := model.RowSidebarView, model.RowGroupOrder
	tests := []struct {
		name               string
		intended, observed *model.RowSnapshot
		wantKeys           []string
	}{
		{"same order", withOrder(sv, []string{"s1", "s2"}, nil), withOrder(sv, []string{"s1", "s2"}, nil), nil},
		{"sections reordered", withOrder(sv, []string{"s1", "s2"}, nil), withOrder(sv, []string{"s2", "s1"}, nil), []string{"order"}},
		{"field and order drift", withOrder(sv, []string{"s1", "s2"}, map[string]string{"field:x": `1`}),
			withOrder(sv, []string{"s2", "s1"}, map[string]string{"field:x": `2`}), []string{"field:x", "order"}},
		{"order item row counted once", withOrder(grp, []string{"a", "b"}, map[string]string{"order": `["a","b"]`}),
			withOrder(grp, []string{"b", "a"}, map[string]string{"order": `["b","a"]`}), []string{"order"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := driftOf(tc.intended.Row, repA, tc.intended, tc.observed)
			if err != nil {
				t.Fatal(err)
			}
			var keys []string
			for _, c := range got {
				keys = append(keys, c.Key)
			}
			if !slices.Equal(keys, tc.wantKeys) {
				t.Fatalf("keys = %v, want %v", keys, tc.wantKeys)
			}
			for _, c := range got {
				if c.Key == "order" && tc.intended.Row == sv {
					want, _ := rows.OrderHash(tc.observed.Order)
					if c.ServerHash != want {
						t.Errorf("server hash %q, want %q", c.ServerHash, want)
					}
				}
			}
		})
	}
}

func TestExpandExcludes(t *testing.T) {
	got := expandExcludes("/Users/someone", []string{"~/clients/**", "~", "/abs/**", "local_x", "~other/x"})
	want := []string{"/Users/someone/clients/**", "/Users/someone", "/abs/**", "local_x", "~other/x"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
}

// TestRollbackWaitsForQuiescence: a rollback restores only when no process
// uses the target folder. A held LOCK, a write holder or a copy of the app
// started with --user-data-dir on the folder (all reported by the
// quiescence check) make it refuse with exit 41 and restore nothing.
func TestRollbackWaitsForQuiescence(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	afterSwitch(t, e)
	e.ui.interactive = true
	if e.app.inUse == nil {
		e.app.inUse = map[string]bool{}
	}
	// Generation 0 belongs to b, which is in Claude/ now.
	e.app.inUse[filepath.Join(e.root, ids.DefaultFolder)] = true
	launches := e.app.launches
	_, err := e.orch().Rollback(ctx, 0, false)
	if pderr.CodeOf(err) != pderr.AppDidNotQuit {
		t.Fatalf("err = %v", err)
	}
	if e.gens.restores != 0 {
		t.Error("restored while the folder was in use")
	}
	if root := e.st.root(t); root.Run != nil {
		t.Errorf("run left open: %+v", root.Run)
	}
	if e.app.quits > 0 && e.app.launches == launches {
		t.Error("Claude was quit for the rollback and not relaunched")
	}
	// Once the folder is free, the rollback restores.
	e.app.inUse = nil
	if _, err := e.orch().Rollback(ctx, 0, false); err != nil {
		t.Fatalf("rollback when quiet: %v", err)
	}
	if e.gens.restores == 0 {
		t.Error("nothing restored when quiet")
	}
}

// TestRollbackLocalStorageLockedNeedsRecover: when another process holds
// the Local Storage lock as a rollback opens it for its restore, the
// writable open refuses with nothing written. That is not an internal
// error: the run stays open with exit 41 and nothing restored, and
// recover finishes the rollback once the folder is free.
func TestRollbackLocalStorageLockedNeedsRecover(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	afterSwitch(t, e)
	e.ui.interactive = true
	var n uint64
	for k := range e.gens.gens {
		n = max(n, k)
	}
	g := e.gens.gens[n]
	g.m.Entries = append(g.m.Entries, gen.Entry{Kind: gen.EntryLSValue, Row: model.RowSessions, Key: "dframe-store", Present: true})
	e.mem.WritableErr = fmt.Errorf("leveldb: lock database: %w", kv.ErrLocked)
	_, err := e.orch().Rollback(ctx, n, false)
	if pderr.CodeOf(err) != pderr.NeedsRecover || !errors.Is(err, kv.ErrLocked) {
		t.Fatalf("err = %v, want exit 41 wrapping kv.ErrLocked", err)
	}
	if e.gens.restores != 0 {
		t.Error("restored although Local Storage could not be opened")
	}
	if root := e.st.root(t); root.Run == nil {
		t.Fatal("the rollback run was closed; recover could not finish it")
	}
	e.mem.WritableErr = nil
	e.mem.Put(sources.LevelDBPath(filepath.Join(e.root, ids.DefaultFolder)), kv.NewMem())
	if _, err := e.orch().Recover(ctx, RecoverOptions{Yes: true}); err != nil {
		t.Fatalf("recover once the folder is free: %v", err)
	}
	if e.gens.restores != 1 {
		t.Errorf("restores = %d, want 1", e.gens.restores)
	}
	if root := e.st.root(t); root.Run != nil {
		t.Errorf("run left open: %+v", root.Run)
	}
}

func TestLSOpenErr(t *testing.T) {
	if got := pderr.CodeOf(lsOpenErr("restore", fmt.Errorf("x: %w", kv.ErrLocked))); got != pderr.NeedsRecover {
		t.Errorf("locked: code %d, want %d", got, pderr.NeedsRecover)
	}
	if got := pderr.CodeOf(lsOpenErr("restore", errors.New("disk"))); got != pderr.Internal {
		t.Errorf("other: code %d, want %d", got, pderr.Internal)
	}
}
