package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/state"
)

// crashCase is one run kind driven through every crash point.
type crashCase struct {
	name  string
	setup func(t *testing.T, e *env)
	run   func(o *Orchestrator) error
	// after checks the recovered state (optional).
	after func(t *testing.T, e *env, k int)
}

func runCrashMatrix(t *testing.T, c crashCase) {
	ctx := context.Background()
	for k := 1; ; k++ {
		if k > 40 {
			t.Fatal("no end of crash points")
		}
		e := newEnv(t)
		if c.setup != nil {
			c.setup(t, e)
		}
		e.markBases()
		e.app.badLaunch = 0
		n := 0
		e.crash = func(Phase) bool { n++; return n == k }
		err := c.run(e.orch())
		if !errors.Is(err, ErrCrashInjected) {
			if k == 1 {
				t.Fatalf("%s: no crash point (err %v)", c.name, err)
			}
			if err != nil {
				t.Fatalf("%s: run without a crash failed: %v", c.name, err)
			}
			t.Logf("%s: %d crash points recovered", c.name, k-1)
			return
		}
		e.crash = nil
		label := fmt.Sprintf("%s, crash %d (%v)", c.name, k, e.st.log)
		res, err := e.orch().Recover(ctx, RecoverOptions{Yes: true})
		if err != nil {
			t.Errorf("%s: recover: %v (%v)", label, err, res)
			continue
		}
		e.checkInvariants(label)
		if again, err := e.orch().Recover(ctx, RecoverOptions{Yes: true}); err != nil || !slices.Contains(again.Actions, "nothing to recover") && e.st.root(t).PendingAdd == nil {
			t.Errorf("%s: second recover: %v %v", label, again, err)
		}
		if c.after != nil {
			c.after(t, e, k)
		}
	}
}

func TestCrashRecoverSwitch(t *testing.T) {
	sw := func(o *Orchestrator) error {
		_, err := o.Switch(context.Background(), yes("b"))
		return err
	}
	cases := []crashCase{
		{name: "switch", run: sw},
		{name: "switch with fallback", setup: func(_ *testing.T, e *env) { e.fs.NoSwap = true }, run: sw},
		{name: "switch with sidebar-offline rows", setup: func(_ *testing.T, e *env) {
			withSidebarRows(e, fileAdapter{model.RowSidebarCollapse}, nil)
		}, run: sw},
		{name: "switch with swap back", setup: func(_ *testing.T, e *env) { e.app.intruder = true }, run: func(o *Orchestrator) error {
			_, err := o.Switch(context.Background(), yes("b"))
			if pderr.CodeOf(err) == pderr.ApplyRolledBack {
				return nil
			}
			return err
		}},
		{name: "switch with update", setup: func(_ *testing.T, e *env) { e.app.staged = true }, run: func(o *Orchestrator) error {
			_, err := o.Switch(context.Background(), yes("b"))
			return err
		}},
		{name: "switch with apply failure", setup: func(_ *testing.T, e *env) { e.applier.failAfter = 1 }, run: func(o *Orchestrator) error {
			_, err := o.Switch(context.Background(), yes("b"))
			if pderr.CodeOf(err) == pderr.ApplyRolledBack {
				return nil
			}
			return err
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCrashMatrix(t, c) })
	}
}

// afterSwitch puts the env after a completed switch to b.
func afterSwitch(t *testing.T, e *env) {
	t.Helper()
	if _, err := e.orch().Switch(context.Background(), yes("b")); err != nil {
		t.Fatal(err)
	}
	e.st.log = nil
}

func TestCrashRecoverSync(t *testing.T) {
	runCrashMatrix(t, crashCase{
		name: "sync",
		setup: func(t *testing.T, e *env) {
			afterSwitch(t, e)
			e.invalid["b"] = true
			e.writeRow(ids.DefaultFolder, model.RowSessions, map[string]any{string(fakeSession(2)): map[string]any{"title": "two"}})
			e.ui.interactive = true
		},
		run: func(o *Orchestrator) error {
			_, err := o.Sync(context.Background(), SyncOptions{Yes: true})
			return err
		},
	})
}

func TestCrashRecoverRollback(t *testing.T) {
	runCrashMatrix(t, crashCase{
		name: "rollback",
		setup: func(t *testing.T, e *env) {
			afterSwitch(t, e)
			e.ui.interactive = true
		},
		run: func(o *Orchestrator) error {
			_, err := o.Rollback(context.Background(), 0, false)
			return err
		},
		after: func(t *testing.T, e *env, k int) {
			// Once the restoring intent was recorded, recover finishes the
			// restore idempotently.
			if k >= 3 && len(e.keys(ids.DefaultFolder, model.RowSessions)) != 1 {
				t.Errorf("crash %d: rollback not finished: %v", k, e.keys(ids.DefaultFolder, model.RowSessions))
			}
		},
	})
}

func TestCrashRecoverProfileAdd(t *testing.T) {
	runCrashMatrix(t, crashCase{
		name: "profile add",
		run:  func(o *Orchestrator) error { return o.ProfileAdd(context.Background(), "c", true) },
		after: func(t *testing.T, e *env, k int) {
			root := e.st.root(t)
			if root.PendingAdd == nil && e.account(ids.DefaultFolder) != acctA {
				t.Errorf("crash %d: a is not back in Claude", k)
			}
		},
	})
}

func TestProfileAddThenBind(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.orch().ProfileAdd(ctx, "c", true); err != nil {
		t.Fatal(err)
	}
	root := e.st.root(t)
	if root.PendingAdd == nil || root.PendingAdd.Alias != "c" || root.Run != nil || e.account("Claude-a") != acctA || e.app.launches != 1 {
		t.Fatalf("after add: pending %+v", root.PendingAdd)
	}
	if !slices.ContainsFunc(e.ui.events, func(ev Event) bool { return ev.Message == chromeNote }) {
		t.Error("no Chrome profile explanation")
	}
	if _, err := e.orch().Switch(ctx, yes("b")); codeOf(t, err) != pderr.NeedsRecover {
		t.Fatalf("switch with a pending add: %v", err)
	}
	st, _ := e.orch().Status(ctx, true)
	if !st.NeedsRecover || len(st.Warnings) == 0 {
		t.Errorf("status = %+v", st)
	}
	// The user signs in: Claude now holds a new account.
	e.makeFolder(ids.DefaultFolder, model.Replica{Account: acctC, Org: orgB}, nil)
	res, err := e.orch().Recover(ctx, RecoverOptions{Yes: true})
	if err != nil {
		t.Fatalf("bind: %v %v", err, res)
	}
	root = e.st.root(t)
	if root.PendingAdd != nil || root.Active != "c" || root.Previous != "a" || root.Profiles["c"].Replica.Account != acctC {
		t.Errorf("after bind: %+v", root)
	}
	if p, ok := e.cfg.Profile("c"); !ok || !p.ManagedByOrg {
		t.Error("config not updated")
	}
	e.checkInvariants("bind")
}

func TestRecoverDeclinedBindLeavesPending(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if err := e.orch().ProfileAdd(ctx, "c", false); err != nil {
		t.Fatal(err)
	}
	e.makeFolder(ids.DefaultFolder, model.Replica{Account: acctC, Org: orgB}, nil)
	e.ui.interactive = true
	e.ui.answers[ConfirmAddBind] = false
	if _, err := e.orch().Recover(ctx, RecoverOptions{}); err != nil {
		t.Fatal(err)
	}
	if e.st.root(t).PendingAdd == nil {
		t.Error("declined bind cleared the pending add")
	}
}

func TestRollbackConflictsAreKept(t *testing.T) {
	e := newEnv(t)
	afterSwitch(t, e)
	e.ui.interactive = true
	envFile := filepath.Join(e.root, ids.DefaultFolder, rowFile(model.RowEnvironments))
	edited := []byte(`{"preset/p9":{"name":"edited after the switch"}}`)
	if err := os.WriteFile(envFile, edited, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := e.orch().Rollback(context.Background(), 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Generation != 1 || res.Restored != 1 || len(res.Conflicts) != 1 {
		t.Fatalf("result = %+v", res)
	}
	if b, _ := os.ReadFile(envFile); string(b) != string(edited) {
		t.Error("a conflicting file was overwritten")
	}
	if got := e.keys(ids.DefaultFolder, model.RowSessions); !slices.Equal(got, []string{string(fakeSession(2))}) {
		t.Errorf("sessions not restored: %v", got)
	}
	root := e.st.root(t)
	if root.Epoch != 2 || root.Run != nil || !slices.Contains(e.ui.asked, ConfirmRollback) {
		t.Errorf("epoch %d", root.Epoch)
	}
	if _, ok := root.Bases[state.BaseKey{Alias: "b", Replica: repB, Row: model.RowEnvironments}.String()]; ok {
		t.Error("conflicted row kept its base")
	}
}

func TestInterruptedRollbackRecoversIdempotently(t *testing.T) {
	for _, k := range []int{3, 4} { // after begin restoring, after done restoring
		e := newEnv(t)
		afterSwitch(t, e)
		e.ui.interactive = true
		n := 0
		e.crash = func(Phase) bool { n++; return n == k }
		if _, err := e.orch().Rollback(context.Background(), 1, true); !errors.Is(err, ErrCrashInjected) {
			t.Fatalf("crash %d: %v", k, err)
		}
		e.crash = nil
		restores := e.gens.restores
		if _, err := e.orch().Recover(context.Background(), RecoverOptions{Yes: true}); err != nil {
			t.Fatal(err)
		}
		if e.gens.restores != restores+1 {
			t.Errorf("crash %d: restore not re-run", k)
		}
		if got := e.keys(ids.DefaultFolder, model.RowSessions); !slices.Equal(got, []string{string(fakeSession(2))}) {
			t.Errorf("crash %d: sessions = %v", k, got)
		}
		root := e.st.root(t)
		if root.Run != nil || root.Epoch != 2 {
			t.Errorf("crash %d: run %v epoch %d", k, root.Run, root.Epoch)
		}
	}
}

func TestRecoverWithoutJournal(t *testing.T) {
	e := newEnv(t)
	e.st.committed = nil
	e.app.running = false
	if err := os.Rename(filepath.Join(e.root, "Claude"), filepath.Join(e.root, "Claude-a")); err != nil {
		t.Fatal(err)
	}
	e.ui.interactive = true
	res, err := e.orch().Recover(context.Background(), RecoverOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if e.account(ids.DefaultFolder) == "" || !slices.Contains(e.ui.asked, ConfirmRecover) || len(res.Actions) == 0 {
		t.Errorf("nothing moved: %+v", res)
	}
	// With Claude in place there is nothing to move.
	res, err = e.orch().Recover(context.Background(), RecoverOptions{})
	if err != nil || len(res.Actions) != 1 {
		t.Errorf("%v %v", res, err)
	}
}

func TestRecoverContradictionStops(t *testing.T) {
	e := newEnv(t)
	n := 0
	e.crash = func(p Phase) bool { n++; return p == P8Swap }
	if _, err := e.orch().Switch(context.Background(), yes("b")); !errors.Is(err, ErrCrashInjected) {
		t.Fatal(err)
	}
	// Someone put an unknown account into Claude by hand.
	e.makeFolder(ids.DefaultFolder, model.Replica{Account: acctC, Org: orgA}, nil)
	e.app.running = false
	_, err := (&env{}).orchOf(e).Recover(context.Background(), RecoverOptions{Yes: true})
	if pderr.CodeOf(err) != pderr.NeedsRecover {
		t.Fatalf("err = %v", err)
	}
	if e.st.root(t).Run == nil {
		t.Error("run closed despite the contradiction")
	}
}

// orchOf returns an orchestrator over e without the crash hook.
func (*env) orchOf(e *env) *Orchestrator {
	e.crash = nil
	return e.orch()
}
