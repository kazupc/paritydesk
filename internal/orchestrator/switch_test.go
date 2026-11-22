package orchestrator

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/app"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/state"
	"github.com/kazupc/paritydesk/internal/store/kv"
)

func TestSwitchHappyPath(t *testing.T) {
	e := newEnv(t)
	e.ui.interactive = true
	res, err := e.orch().Switch(context.Background(), SwitchOptions{To: "b", LogOffset: -1})
	if err != nil {
		t.Fatalf("switch: %v", err)
	}
	if res.Code != pderr.OK || res.From != "a" || res.To != "b" || res.Generation != 1 {
		t.Fatalf("result = %+v", res)
	}
	wantPhases := []Phase{P0Preflight, P2Preview, P3Quit, P4Quiesce, P5Harvest, P6Backup, P7Apply, P8Swap, P9Park, P10Commit, P11Launch}
	if got := e.ui.phases(); !slices.Equal(got, wantPhases) {
		t.Errorf("phases = %v, want %v", got, wantPhases)
	}
	wantIntents := []string{"begin:quit", "done:quit", "begin:planned", "done:planned", "begin:backed_up", "done:backed_up",
		"begin:applied", "done:applied", "begin:swapping", "done:swapping", "begin:swapped", "done:swapped", "begin:parked", "done:parked"}
	if got := e.st.log; !slices.Equal(got, wantIntents) {
		t.Errorf("intents = %v", got)
	}
	if e.account(ids.DefaultFolder) != acctB || e.account("Claude-a") != acctA || e.account("Claude-b") != "" {
		t.Fatal("folders not swapped")
	}
	want := []string{string(fakeSession(1)), string(fakeSession(2))}
	if got := e.keys(ids.DefaultFolder, model.RowSessions); !slices.Equal(got, want) {
		t.Errorf("sessions in b = %v", got)
	}
	root := e.st.root(t)
	if root.Active != "b" || root.Previous != "a" || root.Epoch != 1 || len(root.Generations) != 1 {
		t.Errorf("root = active %s previous %s epoch %d gens %d", root.Active, root.Previous, root.Epoch, len(root.Generations))
	}
	if !slices.Contains(e.ui.asked, ConfirmPlan) || !slices.Contains(e.ui.asked, ConfirmBaseInvalid) {
		t.Errorf("asked = %v", e.ui.asked)
	}
	if e.app.quits != 1 || e.app.launches != 1 {
		t.Errorf("quits %d launches %d", e.app.quits, e.app.launches)
	}
	m, err := readMarker(e.fs, e.root+"/Claude")
	if err != nil || m == nil || m.Alias != "b" || m.Generation != 1 {
		t.Errorf("marker = %+v, %v", m, err)
	}
	e.checkInvariants("happy")
}

func TestSwitchEmptyPlanSkipsBackup(t *testing.T) {
	e := newEnv(t)
	same := map[string]any{"k": "v"}
	for _, f := range []string{"Claude", "Claude-b"} {
		e.writeRow(f, model.RowSessions, same)
		e.writeRow(f, model.RowEnvironments, same)
	}
	res, err := e.orch().Switch(context.Background(), yes("b"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Plan.Empty() || res.Generation != 0 {
		t.Fatalf("plan not empty: %+v", res.Plan.Ops)
	}
	for _, p := range e.ui.phases() {
		if p == P6Backup || p == P7Apply {
			t.Errorf("phase %s ran for an empty plan", p)
		}
	}
	if len(e.gens.gens) != 0 || e.applier.applies != 0 {
		t.Error("generation or apply for an empty plan")
	}
	if e.account(ids.DefaultFolder) != acctB {
		t.Error("not swapped")
	}
	e.checkInvariants("empty")
}

func TestRepeatedSwitchConverges(t *testing.T) {
	e := newEnv(t)
	o := e.orch()
	ctx := context.Background()
	for i, to := range []ids.Alias{"b", "a"} {
		if _, err := o.Switch(ctx, yes(to)); err != nil {
			t.Fatalf("switch %d: %v", i, err)
		}
	}
	res, err := o.Switch(ctx, SwitchOptions{To: "b", Yes: true, LogOffset: -1})
	if err != nil {
		t.Fatalf("third switch: %v", err)
	}
	if !res.Plan.Empty() || res.Plan.NeedsConfirmation || len(res.Plan.Changes) != 0 {
		t.Fatalf("third plan = ops %d changes %v nc %v", len(res.Plan.Ops), res.Plan.Changes, res.Plan.NeedsConfirmation)
	}
	e.checkInvariants("converged")
}

func TestBusyAtP0(t *testing.T) {
	busy := []ids.SessionID{fakeSession(7)}
	t.Run("non-interactive exits 12", func(t *testing.T) {
		e := newEnv(t)
		e.app.busySeq = [][]ids.SessionID{busy}
		_, err := e.orch().Switch(context.Background(), yes("b"))
		if codeOf(t, err) != pderr.SessionsBusy || e.app.quits != 0 {
			t.Fatalf("err = %v, quits %d", err, e.app.quits)
		}
	})
	t.Run("interactive asks with sanitized titles", func(t *testing.T) {
		e := newEnv(t)
		e.ui.interactive = true
		e.sess.infos = []sources.SessionInfo{{ID: fakeSession(8), Title: "fix\x1b[31m the\u202e bug\n now", Busy: true}}
		e.app.busySeq = [][]ids.SessionID{busy}
		if _, err := e.orch().Switch(context.Background(), SwitchOptions{To: "b", LogOffset: -1}); err != nil {
			t.Fatal(err)
		}
		i := slices.Index(e.ui.asked, ConfirmBusy)
		if i < 0 {
			t.Fatalf("asked = %v", e.ui.asked)
		}
		joined := strings.Join(e.ui.lines[i], "|")
		if strings.ContainsAny(joined, "\x1b\u202e\n") || !strings.Contains(joined, "fix[31m the bug now") || len(e.ui.lines[i]) != 2 {
			t.Errorf("lines = %q", e.ui.lines[i])
		}
	})
	t.Run("interactive decline exits 3", func(t *testing.T) {
		e := newEnv(t)
		e.ui.interactive = true
		e.ui.answers[ConfirmBusy] = false
		e.app.busySeq = [][]ids.SessionID{busy}
		_, err := e.orch().Switch(context.Background(), SwitchOptions{To: "b", LogOffset: -1})
		if codeOf(t, err) != pderr.Declined || e.app.quits != 0 {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("the invoking session is excluded", func(t *testing.T) {
		e := newEnv(t)
		e.sess.cli["cli-1"] = fakeSession(7)
		e.app.busySeq = [][]ids.SessionID{busy}
		opts := yes("b")
		opts.ReopenCLISession = "cli-1"
		if _, err := e.orch().Switch(context.Background(), opts); err != nil {
			t.Fatal(err)
		}
	})
}

func TestP3BusyWaitTimesOutDetached(t *testing.T) {
	e := newEnv(t)
	e.app.busySeq = [][]ids.SessionID{nil, {fakeSession(7)}}
	opts := yes("b")
	opts.FromRunner = true
	start := e.clk.Now()
	_, err := e.orch().Switch(context.Background(), opts)
	if codeOf(t, err) != pderr.SessionsBusy {
		t.Fatalf("err = %v", err)
	}
	if e.app.quits != 0 || !e.app.running || e.app.launches != 0 {
		t.Errorf("quits %d running %v launches %d", e.app.quits, e.app.running, e.app.launches)
	}
	if e.clk.Now().Sub(start) < 60*time.Second {
		t.Error("did not wait for quit_timeout")
	}
	if e.st.root(t).Run != nil {
		t.Error("run left open")
	}
}

func TestQuitOutcomes(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(*fakeApp)
		running     bool
		launches    int
		quitPending bool
	}{
		{"timeout without a veto", func(a *fakeApp) { a.ignoreQuit = true }, true, 0, false},
		{"veto then a late exit", func(a *fakeApp) { a.veto, a.lateExit = true, true }, true, 1, false},
		{"veto and no exit", func(a *fakeApp) { a.veto = true }, true, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			tc.setup(e.app)
			res, err := e.orch().Switch(context.Background(), yes("b"))
			if codeOf(t, err) != pderr.AppDidNotQuit {
				t.Fatalf("err = %v", err)
			}
			if e.app.running != tc.running || e.app.launches != tc.launches || res.QuitPending != tc.quitPending {
				t.Errorf("running %v launches %d pending %v", e.app.running, e.app.launches, res.QuitPending)
			}
			if len(e.gens.gens) != 0 || e.account(ids.DefaultFolder) != acctA ||
				slices.Contains(e.keys("Claude-b", model.RowSessions), string(fakeSession(1))) {
				t.Error("something was written")
			}
			e.checkInvariants(tc.name)
		})
	}
}

func TestTurnEndedBeforeP3(t *testing.T) {
	e := newEnv(t)
	e.sess.cli["cli-1"] = fakeSession(1)
	e.app.turnEnds = []int64{150}
	opts := yes("b")
	opts.FromRunner, opts.ReopenCLISession, opts.LogOffset = true, "cli-1", 100
	if _, err := e.orch().Switch(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if e.app.turnWaited || !slices.Equal(e.app.turnFrom, []int64{100}) {
		t.Errorf("waited %v from %v", e.app.turnWaited, e.app.turnFrom)
	}
	if !slices.Equal(e.app.opened, []ids.SessionID{fakeSession(1)}) {
		t.Errorf("reopened %v", e.app.opened)
	}
}

func TestStagedUpdate(t *testing.T) {
	t.Run("refused without --allow-update", func(t *testing.T) {
		e := newEnv(t)
		e.app.staged = true
		_, err := e.orch().Switch(context.Background(), yes("b"))
		if codeOf(t, err) != pderr.UpdatePending || e.app.quits != 0 {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("updater relaunches the app, P3 quits again", func(t *testing.T) {
		e := newEnv(t)
		e.app.staged, e.app.updRelaunch = true, true
		opts := yes("b")
		opts.AllowUpdate = true
		res, err := e.orch().Switch(context.Background(), opts)
		if err != nil {
			t.Fatal(err)
		}
		if e.app.quits != 2 || e.account(ids.DefaultFolder) != acctB || res.Code != pderr.OK {
			t.Errorf("quits %d", e.app.quits)
		}
		if e.st.log[0] != "begin:quit" {
			t.Errorf("first intent %v", e.st.log)
		}
		e.checkInvariants("update")
	})
	t.Run("a decline after the update quit relaunches S", func(t *testing.T) {
		e := newEnv(t)
		e.app.staged = true
		e.ui.interactive = true
		e.ui.answers[ConfirmPlan] = false
		_, err := e.orch().Switch(context.Background(), SwitchOptions{To: "b", AllowUpdate: true, LogOffset: -1})
		if codeOf(t, err) != pderr.Declined {
			t.Fatalf("err = %v", err)
		}
		if e.app.quits != 1 || e.app.launches != 1 || e.account(ids.DefaultFolder) != acctA {
			t.Errorf("quits %d launches %d", e.app.quits, e.app.launches)
		}
		e.checkInvariants("update decline")
	})
}

func TestPreflightRefusals(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*env)
		opts  func() SwitchOptions
		want  pderr.Code
	}{
		{"foreign tool", func(e *env) { e.makeFolder("Claude-Other1", model.Replica{}, nil) }, func() SwitchOptions { return yes("b") }, pderr.ForeignTool},
		{"identity mismatch", func(e *env) { e.makeFolder("Claude-b", model.Replica{Account: acctC, Org: orgB}, nil) }, func() SwitchOptions { return yes("b") }, pderr.IdentityMismatch},
		{"base invalid non-interactive", func(*env) {}, func() SwitchOptions { return SwitchOptions{To: "b", Yes: true, LogOffset: -1} }, pderr.BaseInvalid},
		{"refuse", func(e *env) { e.compat.m = nil }, func() SwitchOptions { return yes("b") }, pderr.CompatRefused},
		{"automation missing", func(e *env) { e.app.autoErr = app.ErrAutomationDenied }, func() SwitchOptions { return yes("b") }, pderr.AutomationMissing},
		{"open run", func(e *env) {
			r := e.st.root(e.t)
			r.Run = &state.Run{ID: "old-run-1", Kind: state.RunSwitch}
			_ = e.st.Commit(r)
		}, func() SwitchOptions { return yes("b") }, pderr.NeedsRecover},
		{"unknown profile", func(*env) {}, func() SwitchOptions { return yes("zz") }, pderr.Usage},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			tc.setup(e)
			_, err := e.orch().Switch(context.Background(), tc.opts())
			if got := codeOf(t, err); got != tc.want {
				t.Fatalf("code = %d (%v), want %d", got, err, tc.want)
			}
			if e.app.quits != 0 || len(e.gens.gens) != 0 {
				t.Error("quit or wrote")
			}
		})
	}
}

func TestValidationRefusals(t *testing.T) {
	t.Run("breaker 22", func(t *testing.T) {
		e := newEnv(t)
		e.valid.err = pderr.New(pderr.DeletionBreaker, "validate", "too many")
		_, err := e.orch().Switch(context.Background(), yes("b"))
		if codeOf(t, err) != pderr.DeletionBreaker {
			t.Fatalf("err = %v", err)
		}
		if e.app.launches != 1 || e.account(ids.DefaultFolder) != acctA || len(e.gens.gens) != 0 {
			t.Error("S not relaunched or something written")
		}
		e.checkInvariants("breaker")
	})
	t.Run("worktree risk 23", func(t *testing.T) {
		e := newEnv(t)
		wt := t.TempDir()
		e.wt.lost = []string{wt}
		e.app.dirty[wt] = true
		_, err := e.orch().Switch(context.Background(), yes("b"))
		if codeOf(t, err) != pderr.WorktreeRisk {
			t.Fatalf("err = %v", err)
		}
		if w := e.valid.last.Worktrees; len(w) != 1 || !w[0].Dirty {
			t.Errorf("risks = %+v", w)
		}
		e.checkInvariants("worktree")
	})
	t.Run("worktree path that does not exist is no risk", func(t *testing.T) {
		e := newEnv(t)
		gone := filepath.Join(t.TempDir(), "removed-worktree")
		kept := t.TempDir()
		e.wt.lost = []string{gone, kept}
		e.app.dirty[gone] = true
		if _, err := e.orch().Switch(context.Background(), yes("b")); err != nil {
			t.Fatalf("err = %v", err)
		}
		if w := e.valid.last.Worktrees; len(w) != 1 || w[0].Path != kept || w[0].Dirty {
			t.Errorf("risks = %+v", w)
		}
	})
	t.Run("worktree on a volume that is not mounted stays guarded", func(t *testing.T) {
		e := newEnv(t)
		// Neither the worktree nor its parent exists, as on an external
		// volume that is not mounted at switch time.
		unmounted := filepath.Join(t.TempDir(), "Volumes", "Ext", "repo", ".wt", "x")
		e.wt.lost = []string{unmounted}
		e.app.dirty[unmounted] = true
		_, err := e.orch().Switch(context.Background(), yes("b"))
		if codeOf(t, err) != pderr.WorktreeRisk {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("validate input", func(t *testing.T) {
		e := newEnv(t)
		opts := yes("b")
		opts.AllowDeletions = 4
		if _, err := e.orch().Switch(context.Background(), opts); err != nil {
			t.Fatal(err)
		}
		in := e.valid.last
		if in.Limits.AllowDeletions != 4 || in.Before[model.CatSessions] != 1 || !slices.Contains(in.DenyPaths, "identity.json") {
			t.Errorf("validate input = %+v", in)
		}
		if !slices.Equal(in.AllowPaths, []string{"rows/**"}) {
			t.Errorf("allow paths = %v", in.AllowPaths)
		}
	})
}

func TestModes(t *testing.T) {
	t.Run("swap-only 31", func(t *testing.T) {
		e := newEnv(t)
		e.compat.decide = func(compat.DecideInput) compat.Decision { return compat.Decision{Mode: compat.ModeSwapOnly} }
		res, err := e.orch().Switch(context.Background(), yes("b"))
		if codeOf(t, err) != pderr.SwapOnly || res.Code != pderr.SwapOnly {
			t.Fatalf("err = %v", err)
		}
		if e.account(ids.DefaultFolder) != acctB || len(e.keys(ids.DefaultFolder, model.RowSessions)) != 1 {
			t.Error("swap-only did not swap, or carried rows")
		}
		e.checkInvariants("swap-only")
	})
	t.Run("unknown compression 31", func(t *testing.T) {
		e := newEnv(t)
		for _, f := range []string{"Claude", "Claude-b"} {
			dir := sources.LevelDBPath(e.root + "/" + f)
			e.makeDir(dir)
			e.mem.Put(dir, kv.NewMem())
		}
		e.mem.FormatErr = fmt.Errorf("table 000005.ldb: %w", kv.ErrUnknownCompression)
		_, err := e.orch().Switch(context.Background(), yes("b"))
		if codeOf(t, err) != pderr.SwapOnly || e.compat.lastIn.CompressionOK {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("degraded 32", func(t *testing.T) {
		e := newEnv(t)
		e.compat.decide = func(compat.DecideInput) compat.Decision {
			return compat.Decision{Mode: compat.ModeDegraded, SkippedRows: []model.RowID{model.RowEnvironments}}
		}
		res, err := e.orch().Switch(context.Background(), yes("b"))
		if codeOf(t, err) != pderr.Degraded {
			t.Fatalf("err = %v", err)
		}
		if len(e.keys(ids.DefaultFolder, model.RowEnvironments)) != 1 || len(e.keys(ids.DefaultFolder, model.RowSessions)) != 2 {
			t.Error("degraded rows wrong")
		}
		if res.Plan.Skipped[model.RowEnvironments] == "" {
			t.Error("skipped row not reported")
		}
	})
}

func TestApplyFailureRollsBack(t *testing.T) {
	e := newEnv(t)
	e.applier.failAfter = 1
	before := e.keys("Claude-b", model.RowEnvironments)
	_, err := e.orch().Switch(context.Background(), yes("b"))
	if codeOf(t, err) != pderr.ApplyRolledBack {
		t.Fatalf("err = %v", err)
	}
	if e.gens.restores != 1 || e.app.launches != 1 || e.account(ids.DefaultFolder) != acctA {
		t.Errorf("restores %d launches %d", e.gens.restores, e.app.launches)
	}
	if got := e.keys("Claude-b", model.RowEnvironments); !slices.Equal(got, before) {
		t.Errorf("b not restored: %v", got)
	}
	e.checkInvariants("apply failure")
}

func TestSwapUnsupportedUsesFallback(t *testing.T) {
	e := newEnv(t)
	e.fs.NoSwap = true
	if _, err := e.orch().Switch(context.Background(), yes("b")); err != nil {
		t.Fatal(err)
	}
	log := strings.Join(e.st.log, ",")
	if !strings.Contains(log, "begin:swapping,begin:fallback_s_parked,done:fallback_s_parked,begin:fallback_t_placed,done:fallback_t_placed") {
		t.Errorf("intents = %s", log)
	}
	if slices.Contains(e.st.log, "begin:parked") {
		t.Error("P9 ran after the fallback")
	}
	if e.account(ids.DefaultFolder) != acctB || e.account("Claude-a") != acctA {
		t.Error("fallback did not place the folders")
	}
	e.checkInvariants("fallback")
}

func TestPostSwapIntruderSwapsBack(t *testing.T) {
	e := newEnv(t)
	e.app.intruder = true
	before := e.keys("Claude-b", model.RowSessions)
	_, err := e.orch().Switch(context.Background(), yes("b"))
	if codeOf(t, err) != pderr.ApplyRolledBack {
		t.Fatalf("err = %v", err)
	}
	if !slices.Contains(e.st.log, "begin:swap_back") {
		t.Errorf("intents = %v", e.st.log)
	}
	if e.account(ids.DefaultFolder) != acctA || e.account("Claude-b") != acctB {
		t.Error("not swapped back")
	}
	if got := e.keys("Claude-b", model.RowSessions); !slices.Equal(got, before) {
		t.Errorf("b not restored: %v", got)
	}
	e.checkInvariants("swap back")

	e2 := newEnv(t)
	e2.app.intruder, e2.app.holders = true, true
	_, err = e2.orch().Switch(context.Background(), yes("b"))
	if codeOf(t, err) != pderr.NeedsRecover {
		t.Fatalf("holder case: %v", err)
	}
}

func TestCancellationInEveryPhase(t *testing.T) {
	phases := []Phase{P0Preflight, P2Preview, P3Quit, P4Quiesce, P5Harvest, P6Backup, P7Apply, P8Swap, P9Park, P10Commit, P11Launch, P12Verify}
	for _, p := range phases {
		t.Run(string(p), func(t *testing.T) {
			e := newEnv(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			e.ui.onEvent = func(ev Event) {
				if ev.Phase == p && ev.Status == "start" {
					cancel()
				}
			}
			_, err := e.orch().Switch(ctx, yes("b"))
			beforeCommit := slices.Index(phases, p) <= slices.Index(phases, P5Harvest)
			if beforeCommit {
				if err == nil || e.account(ids.DefaultFolder) != acctA {
					t.Fatalf("not aborted: %v", err)
				}
				quit := slices.Index(phases, p) >= slices.Index(phases, P3Quit)
				if quit && (e.app.launches != 1 || !e.app.running) {
					t.Errorf("S not relaunched: launches %d", e.app.launches)
				}
				if !quit && e.app.quits != 0 {
					t.Error("quit after a cancel before P3")
				}
			} else if e.account(ids.DefaultFolder) != acctB {
				t.Fatalf("run after the P5 commit did not complete: %v", err)
			}
			e.checkInvariants(string(p))
		})
	}
}

func TestConfirmedPlans(t *testing.T) {
	t.Run("first sync with --confirm-first-sync proceeds", func(t *testing.T) {
		e := newEnv(t)
		pv, err := e.orch().Preview(context.Background(), PreviewOptions{To: "b", Save: true})
		if err != nil {
			t.Fatal(err)
		}
		opts := SwitchOptions{To: "b", FromRunner: true, ConfirmedPlan: pv.PlanHash, ConfirmFirstSync: true, LogOffset: 10}
		if _, err := e.orch().Switch(context.Background(), opts); err != nil {
			t.Fatal(err)
		}
		if e.account(ids.DefaultFolder) != acctB || e.st.root(t).Generations[0].N != 1 {
			t.Error("switch did not complete")
		}
	})
	t.Run("first sync without the flag exits 21", func(t *testing.T) {
		e := newEnv(t)
		pv, _ := e.orch().Preview(context.Background(), PreviewOptions{To: "b", Save: true})
		_, err := e.orch().Switch(context.Background(), SwitchOptions{To: "b", FromRunner: true, ConfirmedPlan: pv.PlanHash, LogOffset: 10})
		if codeOf(t, err) != pderr.BaseInvalid || e.app.quits != 0 {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("expired plan exits 3", func(t *testing.T) {
		e := newEnv(t)
		pv, _ := e.orch().Preview(context.Background(), PreviewOptions{To: "b", Save: true})
		e.clk.Advance(3 * time.Minute)
		_, err := e.orch().Switch(context.Background(), SwitchOptions{To: "b", FromRunner: true, ConfirmedPlan: pv.PlanHash, ConfirmFirstSync: true, LogOffset: 10})
		if codeOf(t, err) != pderr.Declined || e.app.quits != 0 {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("grown destructive set exits 3 before quitting", func(t *testing.T) {
		e := converged(t)
		pv, err := e.orch().Preview(context.Background(), PreviewOptions{To: "b", Save: true})
		if err != nil || len(pv.Plan.Ops) != 0 {
			t.Fatalf("preview: %v %+v", err, pv.Plan.Ops)
		}
		e.writeRow(ids.DefaultFolder, model.RowSessions, map[string]any{string(fakeSession(2)): map[string]any{"title": "two"}})
		quits := e.app.quits
		_, err = e.orch().Switch(context.Background(), SwitchOptions{To: "b", FromRunner: true, ConfirmedPlan: pv.PlanHash, LogOffset: 10})
		if codeOf(t, err) != pderr.Declined || e.app.quits != quits {
			t.Fatalf("err = %v quits %d", err, e.app.quits)
		}
	})
	t.Run("new destructive diff at P5 aborts a detached run", func(t *testing.T) {
		e := converged(t)
		e.ui.onEvent = func(ev Event) {
			if ev.Phase == P3Quit && ev.Status == "start" {
				e.writeRow(ids.DefaultFolder, model.RowSessions, map[string]any{string(fakeSession(2)): map[string]any{"title": "two"}})
			}
		}
		launches := e.app.launches
		opts := yes("b")
		opts.FromRunner = true
		_, err := e.orch().Switch(context.Background(), opts)
		if codeOf(t, err) != pderr.Declined {
			t.Fatalf("err = %v", err)
		}
		if e.app.launches != launches+1 || e.account(ids.DefaultFolder) != acctA {
			t.Error("S not relaunched")
		}
		e.checkInvariants("p5 destructive")
	})
}

// converged returns an env after a -> b -> a, where both profiles hold
// the union and every base is valid.
func converged(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	for _, to := range []ids.Alias{"b", "a"} {
		if _, err := e.orch().Switch(context.Background(), yes(to)); err != nil {
			t.Fatal(err)
		}
	}
	e.st.log = nil
	e.markBases()
	return e
}

func TestDetachedParent(t *testing.T) {
	t.Run("non-interactive parent never takes the lock", func(t *testing.T) {
		e := newEnv(t)
		e.det.inside = true
		var childErr error
		e.det.child = func(args []string) {
			if e.st.lockCalls != 0 {
				t.Errorf("parent took the lock (%d calls)", e.st.lockCalls)
			}
			_, childErr = e.orch().Switch(context.Background(), parseJobArgs(t, args))
		}
		res, err := e.orch().Switch(context.Background(), SwitchOptions{To: "b", Yes: true, ConfirmFirstSync: true, LogOffset: -1})
		if err != nil || !res.Detached || res.RunID != "child-run-0001" {
			t.Fatalf("parent: %+v %v", res, err)
		}
		if childErr != nil || e.account(ids.DefaultFolder) != acctB || e.st.lockCalls != 1 {
			t.Fatalf("child: %v, locks %d", childErr, e.st.lockCalls)
		}
		args := e.det.submitted[0]
		if !slices.Contains(args, "--from-runner") || !slices.Contains(args, "--log-offset=1000") || slices.Contains(args, "--detach") {
			t.Errorf("args = %v", args)
		}
	})
	t.Run("non-interactive parent needs a confirmation", func(t *testing.T) {
		e := newEnv(t)
		e.det.inside = true
		_, err := e.orch().Switch(context.Background(), SwitchOptions{To: "b", LogOffset: -1})
		if codeOf(t, err) != pderr.Declined || len(e.det.submitted) != 0 {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("interactive parent confirms and saves the plan", func(t *testing.T) {
		e := newEnv(t)
		e.ui.interactive = true
		res, err := e.orch().Switch(context.Background(), SwitchOptions{To: "b", Detach: true, LogOffset: -1})
		if err != nil || !res.Detached {
			t.Fatalf("%v", err)
		}
		args := strings.Join(e.det.submitted[0], " ")
		if !strings.Contains(args, "--confirmed-plan="+res.PlanHash) || !strings.Contains(args, "--confirm-first-sync") ||
			e.plans.saves != 1 || e.st.lockCalls != 0 || e.app.quits != 0 {
			t.Errorf("args %s saves %d locks %d", args, e.plans.saves, e.st.lockCalls)
		}
	})
}

// parseJobArgs turns JobArgs output back into options (the subset the
// tests use).
func parseJobArgs(t *testing.T, args []string) SwitchOptions {
	t.Helper()
	o := SwitchOptions{LogOffset: -1}
	for i, a := range args {
		switch {
		case a == "--":
			o.To = ids.Alias(args[i+1])
		case a == "--"+FlagFromRunner:
			o.FromRunner = true
		case a == "--"+FlagYes:
			o.Yes = true
		case a == "--"+FlagConfirmFirstSync:
			o.ConfirmFirstSync = true
		case strings.HasPrefix(a, "--"+FlagConfirmedPlan+"="):
			o.ConfirmedPlan = strings.TrimPrefix(a, "--"+FlagConfirmedPlan+"=")
		case strings.HasPrefix(a, "--"+FlagLogOffset+"="):
			if _, err := fmt.Sscan(strings.TrimPrefix(a, "--"+FlagLogOffset+"="), &o.LogOffset); err != nil {
				t.Fatal(err)
			}
		}
	}
	return o
}

func TestReadOnlyWritesNothing(t *testing.T) {
	e := newEnv(t)
	e.readOnly = true
	e.det.inside = true
	o := e.orch()
	res, err := o.Switch(context.Background(), yes("b"))
	if err != nil || res.Plan == nil {
		t.Fatalf("%v", err)
	}
	if _, err := o.Preview(context.Background(), PreviewOptions{To: "b", Save: true}); err != nil {
		t.Fatal(err)
	}
	if e.st.commits != 0 || e.st.lockCalls != 0 || len(e.det.submitted) != 0 || e.plans.saves != 0 || e.app.quits != 0 {
		t.Errorf("commits %d locks %d submits %d saves %d", e.st.commits, e.st.lockCalls, len(e.det.submitted), e.plans.saves)
	}
	if _, err := o.Recover(context.Background(), RecoverOptions{}); codeOf(t, err) != pderr.Usage {
		t.Errorf("recover in read-only: %v", err)
	}
}

func TestReopenSkipsSessionNotInTarget(t *testing.T) {
	e := newEnv(t)
	e.sess.cli["cli-1"] = fakeSession(1)
	opts := yes("b")
	opts.FromRunner, opts.ReopenCLISession = true, "cli-1"
	opts.Skip = []model.Category{model.CatSessions}
	res, err := e.orch().Switch(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.app.opened) != 0 {
		t.Errorf("opened %v", e.app.opened)
	}
	if !slices.ContainsFunc(res.Warnings, func(w string) bool { return strings.Contains(w, "not reopened") }) {
		t.Errorf("warnings = %v", res.Warnings)
	}
}

func TestOutsideSwitchAsks(t *testing.T) {
	e := newEnv(t)
	r := e.st.root(t)
	r.Active = "b" // Claude still holds a: a switch made outside paritydesk
	_ = e.st.Commit(r)
	e.ui.interactive = true
	e.ui.answers[ConfirmOutsideSwitch] = false
	_, err := e.orch().Switch(context.Background(), SwitchOptions{To: "b", LogOffset: -1})
	if codeOf(t, err) != pderr.Declined || !slices.Contains(e.ui.asked, ConfirmOutsideSwitch) || e.app.quits != 0 {
		t.Fatalf("err = %v asked %v", err, e.ui.asked)
	}
}
