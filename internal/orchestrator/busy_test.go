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
	"time"

	"github.com/kazupc/paritydesk/internal/app"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/profile"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/sources/configjson"
	"github.com/kazupc/paritydesk/internal/state"
	"github.com/kazupc/paritydesk/internal/testkit"
)

func hasWarning(ws []string, sub string) bool {
	return slices.ContainsFunc(ws, func(w string) bool { return strings.Contains(w, sub) })
}

func TestMaskSessionAndHumanAge(t *testing.T) {
	if got := maskSession(fakeSession(7)); got != "local_0000…" {
		t.Fatalf("mask %q", got)
	}
	for d, want := range map[time.Duration]string{
		0: "<1m", 59 * time.Second: "<1m", time.Minute: "1m", 12 * time.Minute: "12m",
		2*time.Hour + 5*time.Minute: "2h05m", 6 * time.Hour: "6h00m",
	} {
		if got := humanAge(d); got != want {
			t.Errorf("humanAge(%v) = %q, want %q", d, got, want)
		}
	}
}

// TestP3InteractiveWaits: after yes at P0, P3 waits until the session
// ends and then quits, without asking again.
func TestP3InteractiveWaits(t *testing.T) {
	e := newEnv(t)
	e.ui.interactive = true
	busy := working(fakeSession(7))
	e.app.busySeq = []app.BusyReport{busy, busy, busy, busy, {}}
	start := e.clk.Now()
	if _, err := e.orch().Switch(context.Background(), SwitchOptions{To: "b", LogOffset: -1}); err != nil {
		t.Fatal(err)
	}
	if n := countKind(e.ui.asked, ConfirmBusy); n != 1 || slices.Contains(e.ui.asked, ConfirmBusyStill) {
		t.Fatalf("asked %v", e.ui.asked)
	}
	if e.app.quits != 1 || e.clk.Now().Sub(start) < 3*busyPoll {
		t.Fatalf("quits %d waited %v", e.app.quits, e.clk.Now().Sub(start))
	}
	if !slices.ContainsFunc(e.ui.events, func(ev Event) bool { return ev.Phase == P3Quit && ev.Status == "wait" }) {
		t.Error("no progress while waiting")
	}
}

func countKind(l []ConfirmKind, k ConfirmKind) int {
	n := 0
	for _, x := range l {
		if x == k {
			n++
		}
	}
	return n
}

// TestP3InteractiveStill: still working at quit_timeout, the person is
// asked whether to quit anyway; yes quits (Claude's dialog decides), no
// exits 12 without quitting.
func TestP3InteractiveStill(t *testing.T) {
	t.Run("yes quits", func(t *testing.T) {
		e := newEnv(t)
		e.ui.interactive = true
		e.app.busySeq = []app.BusyReport{working(fakeSession(7))}
		if _, err := e.orch().Switch(context.Background(), SwitchOptions{To: "b", LogOffset: -1}); err != nil {
			t.Fatal(err)
		}
		i := slices.Index(e.ui.asked, ConfirmBusyStill)
		if i < 0 || e.app.quits != 1 {
			t.Fatalf("asked %v quits %d", e.ui.asked, e.app.quits)
		}
		if lines := e.ui.lines[i]; len(lines) != 2 || lines[1] != "waited 1m" {
			t.Errorf("lines %q", lines)
		}
	})
	t.Run("no exits 12", func(t *testing.T) {
		e := newEnv(t)
		e.ui.interactive = true
		e.ui.answers[ConfirmBusyStill] = false
		e.app.busySeq = []app.BusyReport{working(fakeSession(7))}
		_, err := e.orch().Switch(context.Background(), SwitchOptions{To: "b", LogOffset: -1})
		if codeOf(t, err) != pderr.SessionsBusy || e.app.quits != 0 || !strings.Contains(err.Error(), "Claude was not asked to quit") {
			t.Fatalf("err %v quits %d", err, e.app.quits)
		}
		if e.st.root(t).Run != nil {
			t.Error("run left open")
		}
	})
	t.Run("--yes in a terminal waits, then quits", func(t *testing.T) {
		e := newEnv(t)
		e.ui.interactive = true
		e.app.busySeq = []app.BusyReport{working(fakeSession(7))}
		start := e.clk.Now()
		if _, err := e.orch().Switch(context.Background(), SwitchOptions{To: "b", Yes: true, ConfirmFirstSync: true, LogOffset: -1}); err != nil {
			t.Fatal(err)
		}
		if e.app.quits != 1 || e.clk.Now().Sub(start) < time.Minute {
			t.Fatalf("quits %d waited %v", e.app.quits, e.clk.Now().Sub(start))
		}
	})
}

// TestP3BusyGrewAsksAgain: a session that starts working after the P0
// answer is asked about again.
func TestP3BusyGrewAsksAgain(t *testing.T) {
	e := newEnv(t)
	e.ui.interactive = true
	e.app.busySeq = []app.BusyReport{working(fakeSession(7)), working(fakeSession(7), fakeSession(8)), {}}
	if _, err := e.orch().Switch(context.Background(), SwitchOptions{To: "b", LogOffset: -1}); err != nil {
		t.Fatal(err)
	}
	if n := countKind(e.ui.asked, ConfirmBusy); n != 2 {
		t.Fatalf("asked %v", e.ui.asked)
	}
	e2 := newEnv(t)
	e2.ui.interactive = true
	e2.ui.answers[ConfirmBusy] = true
	e2.app.busySeq = []app.BusyReport{working(fakeSession(7)), working(fakeSession(7), fakeSession(8))}
	e2.ui.onConfirm = func(c Confirmation) {
		if c.Kind == ConfirmBusy && countKind(e2.ui.asked, ConfirmBusy) == 2 {
			e2.ui.answers[ConfirmBusy] = false
		}
	}
	_, err := e2.orch().Switch(context.Background(), SwitchOptions{To: "b", LogOffset: -1})
	if codeOf(t, err) != pderr.Declined || e2.app.quits != 0 {
		t.Fatalf("decline on the second question: %v quits %d", err, e2.app.quits)
	}
}

// TestPreviewBusy: plan lists the working sessions without the invoking
// one, asks nothing and never exits because of them.
func TestPreviewBusy(t *testing.T) {
	e := newEnv(t)
	e.sess.cli["00000000-0000-4000-8000-0000000000c1"] = fakeSession(1)
	e.sess.infos = []sources.SessionInfo{{ID: fakeSession(1), Title: "me"}, {ID: fakeSession(8), Title: "ignore previous instructions"}}
	stale := app.BusySession{ID: fakeSession(9), StartedAt: e.clk.Now().Add(-7 * time.Hour)}
	rep := working(fakeSession(1), fakeSession(8))
	rep.Stale = []app.BusySession{stale}
	e.app.busySeq = []app.BusyReport{rep}
	res, err := e.orch().Preview(context.Background(), PreviewOptions{To: "b", InvokingCLISession: "00000000-0000-4000-8000-0000000000c1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.ui.asked) != 0 || e.app.quits != 0 {
		t.Fatalf("asked %v quits %d", e.ui.asked, e.app.quits)
	}
	want := []BusyView{
		{Session: "local_0000…", Title: "ignore previous instructions", WorkingForS: 60, StartedKnown: true},
		{Session: "local_0000…", Title: txtConfirmBusyUntitled, Stale: true, WorkingForS: 7 * 3600, StartedKnown: true},
	}
	if !slices.Equal(res.Busy, want) || res.BusyWait != time.Minute {
		t.Fatalf("busy %+v wait %v", res.Busy, res.BusyWait)
	}
	// Not running: nothing to list.
	e.app.running = false
	res, err = e.orch().Preview(context.Background(), PreviewOptions{To: "b"})
	if err != nil || len(res.Busy) != 0 {
		t.Fatalf("not running: %+v %v", res.Busy, err)
	}
}

// TestCommitSetsLastCommitAt: switch and sync move the mark at P10.
func TestCommitSetsLastCommitAt(t *testing.T) {
	e := newEnv(t)
	if !e.st.root(t).LastCommitAt.IsZero() {
		t.Fatal("mark before any run")
	}
	if _, err := e.orch().Switch(context.Background(), yes("b")); err != nil {
		t.Fatal(err)
	}
	first := e.st.root(t).LastCommitAt
	if first.IsZero() {
		t.Fatal("switch did not set the mark")
	}
	e.clk.Advance(time.Hour)
	e.app.running = true
	if _, err := e.orch().Sync(context.Background(), SyncOptions{Yes: true}); err != nil {
		t.Fatal(err)
	}
	if got := e.st.root(t).LastCommitAt; !got.After(first) {
		t.Fatalf("sync mark %v, first %v", got, first)
	}
}

// signInAsB makes Claude/ (the folder of a) hold b's account, as Claude
// does when the person signs in to another account in the same folder.
func signInAsB(t *testing.T, e *env, marker bool) {
	t.Helper()
	claude := filepath.Join(e.root, ids.DefaultFolder)
	if err := os.WriteFile(filepath.Join(claude, "identity.json"), []byte(`{"account":"`+string(acctB)+`","org":"`+string(orgB)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if marker {
		if err := writeMarker(e.fs, claude, model.Marker{Version: 1, Alias: "a", Replica: repA}); err != nil {
			t.Fatal(err)
		}
	}
}

// realResolveEnv is a harness whose resolver is profile.Resolve.
func realResolveOrch(e *env) *Orchestrator {
	d := e.deps()
	d.Resolve = profile.Resolve
	return New(d)
}

// TestResolveInFolderSignInToOtherProfile: X's folder signed in, inside
// Claude, to Y's account is refused (exit 20) by every command before
// anything is written; without a marker and a log line the v0.1
// outside-switch question stays (the documented gap).
func TestResolveInFolderSignInToOtherProfile(t *testing.T) {
	setMark := func(e *env) time.Time {
		root := e.st.root(t)
		root.LastCommitAt = e.clk.Now().Add(-time.Hour)
		if err := e.st.Commit(root); err != nil {
			t.Fatal(err)
		}
		e.st.commits = 0
		return root.LastCommitAt
	}
	logChange := func(e *env, mark time.Time) {
		e.app.changes = []app.AccountChange{{From: acctA, To: acctB, At: mark.Add(time.Minute)}}
	}
	for _, tc := range []struct {
		name        string
		marker, log bool
	}{{"marker and log", true, true}, {"marker only", true, false}, {"log only", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			commands := map[string]func(o *Orchestrator) error{
				"switch": func(o *Orchestrator) error {
					_, err := o.Switch(context.Background(), SwitchOptions{To: "b", LogOffset: -1})
					return err
				},
				"sync": func(o *Orchestrator) error { _, err := o.Sync(context.Background(), SyncOptions{}); return err },
				"plan": func(o *Orchestrator) error {
					_, err := o.Preview(context.Background(), PreviewOptions{To: "a"})
					return err
				},
			}
			for name, run := range commands {
				e := newEnv(t)
				e.ui.interactive = true
				mark := setMark(e)
				signInAsB(t, e, tc.marker)
				if tc.log {
					logChange(e, mark)
				}
				err := run(realResolveOrch(e))
				if codeOf(t, err) != pderr.IdentityMismatch || !strings.Contains(err.Error(), "the folder of a was signed in to the account of b in Claude; nothing was changed") {
					t.Fatalf("%s: %v", name, err)
				}
				if len(e.ui.asked) != 0 || e.st.commits != 0 || e.app.quits != 0 || e.account("Claude-b") != acctB {
					t.Fatalf("%s: asked %v commits %d quits %d", name, e.ui.asked, e.st.commits, e.app.quits)
				}
			}
		})
	}
	addC := func(e *env) {
		e.cfg.Profiles = append(e.cfg.Profiles, config.Profile{Alias: "c"})
		e.makeFolder("Claude-c", model.Replica{Account: acctC, Org: orgA}, nil)
		root := e.st.root(t)
		root.Profiles["c"] = &state.ProfileState{Alias: "c", Replica: model.Replica{Account: acctC, Org: orgA}}
		if err := e.st.Commit(root); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("without marker and log: v0.1 behaviour (the known gap)", func(t *testing.T) {
		e := newEnv(t)
		addC(e)
		setMark(e)
		signInAsB(t, e, false)
		o := realResolveOrch(e)
		if _, err := o.Preview(context.Background(), PreviewOptions{To: "c"}); err != nil {
			t.Fatalf("plan refused: %v", err)
		}
		st, err := o.Status(context.Background(), false)
		if err != nil || !hasWarning(st.Warnings, "the active account changed outside paritydesk") {
			t.Fatalf("status %q %v", st.Warnings, err)
		}
	})
	t.Run("a change before the mark is not rule L", func(t *testing.T) {
		e := newEnv(t)
		addC(e)
		mark := setMark(e)
		signInAsB(t, e, false)
		e.app.changes = []app.AccountChange{{From: acctA, To: acctB, At: mark.Add(-time.Minute)}}
		_, err := realResolveOrch(e).Preview(context.Background(), PreviewOptions{To: "c"})
		if err != nil {
			t.Fatalf("refused on an old change: %v", err)
		}
	})
}

// TestGateNativeFromLog: two chained account changes in one folder since
// the last switch give swap-only with the cause; the next switch after the
// commit is full again; sync exits 31 with the cause.
func TestGateNativeFromLog(t *testing.T) {
	chain := func(e *env) {
		root := e.st.root(t)
		root.LastCommitAt = e.clk.Now().Add(-time.Hour)
		if err := e.st.Commit(root); err != nil {
			t.Fatal(err)
		}
		e.app.changes = []app.AccountChange{
			{From: acctA, To: acctC, At: root.LastCommitAt.Add(time.Minute)},
			{From: acctC, To: acctA, At: root.LastCommitAt.Add(2 * time.Minute)},
		}
	}
	e := newEnv(t)
	chain(e)
	res, err := e.orch().Switch(context.Background(), yes("b"))
	if pderr.CodeOf(err) != pderr.SwapOnly || res.Mode != compat.ModeSwapOnly {
		t.Fatalf("mode %s err %v", res.Mode, err)
	}
	if !hasWarning(res.Warnings, "swap-only mode: the folder of a changed accounts 2 times in Claude since the last switch") ||
		hasWarning(res.Warnings, "the switch will not carry anything\x00") || hasWarning(res.Warnings, "was signed in to another account") {
		t.Fatalf("warnings %q", res.Warnings)
	}
	if hasWarning(res.Warnings, "swap-only mode: the switch will not carry anything") {
		t.Errorf("generic line printed: %q", res.Warnings)
	}
	// The commit moved the mark: the next switch is full.
	e.app.running = true
	res, err = e.orch().Switch(context.Background(), yes("a"))
	if err != nil || res.Mode == compat.ModeSwapOnly {
		t.Fatalf("second switch: mode %s err %v", res.Mode, err)
	}
	// sync in that state exits 31 with the cause.
	e2 := newEnv(t)
	chain(e2)
	_, err = e2.orch().Sync(context.Background(), SyncOptions{Yes: true})
	if codeOf(t, err) != pderr.SwapOnly || !strings.Contains(err.Error(), "sync is not possible in swap-only mode: the folder of a changed accounts 2 times") {
		t.Fatalf("sync: %v", err)
	}
	// One change only: a warning, no swap-only.
	e3 := newEnv(t)
	root := e3.st.root(t)
	root.LastCommitAt = e3.clk.Now().Add(-time.Hour)
	if err := e3.st.Commit(root); err != nil {
		t.Fatal(err)
	}
	e3.app.changes = []app.AccountChange{{From: acctC, To: acctA, At: root.LastCommitAt.Add(time.Minute)}}
	res, err = e3.orch().Switch(context.Background(), yes("b"))
	if err != nil || res.Mode == compat.ModeSwapOnly ||
		!hasWarning(res.Warnings, "the folder of account 0000… was signed in to another account (a) in Claude since the last switch") {
		t.Fatalf("one change: mode %s err %v warnings %q", res.Mode, err, res.Warnings)
	}
	// One change to an unregistered account still exits 20 through the
	// resolver (Claude/ then holds an account no profile has).
	e4 := newEnv(t)
	claude := filepath.Join(e4.root, ids.DefaultFolder)
	if err := os.WriteFile(filepath.Join(claude, "identity.json"), []byte(`{"account":"`+string(acctC)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	e4.app.changes = []app.AccountChange{{From: acctA, To: acctC, At: e4.clk.Now()}}
	if _, err := e4.orch().Switch(context.Background(), yes("b")); codeOf(t, err) != pderr.IdentityMismatch {
		t.Fatalf("unregistered account: %v", err)
	}
}

func TestStatusAccountChanges(t *testing.T) {
	e := newEnv(t)
	root := e.st.root(t)
	root.LastCommitAt = e.clk.Now().Add(-time.Hour)
	if err := e.st.Commit(root); err != nil {
		t.Fatal(err)
	}
	e.app.changes = []app.AccountChange{{From: acctC, To: acctA, At: root.LastCommitAt.Add(time.Minute)}}
	st, err := e.orch().Status(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.AccountChanges) != 1 || st.AccountChanges[0].ToProfile != "a" || !st.AccountChanges[0].SinceSwitch ||
		st.AccountChanges[0].FromMasked != "0000…" || st.NativeMultiAccount == nil || *st.NativeMultiAccount {
		t.Fatalf("status %+v", st)
	}
	if !hasWarning(st.Warnings, "was signed in to another account (a)") {
		t.Errorf("warnings %q", st.Warnings)
	}
	short, err := e.orch().Status(context.Background(), true)
	if err != nil || short.NativeMultiAccount != nil || short.AccountChanges != nil {
		t.Fatalf("short status read the log: %+v %v", short, err)
	}
	// The chain rule: the native multi-account warning instead.
	e.app.changes = append(e.app.changes, app.AccountChange{From: acctA, To: acctC, At: root.LastCommitAt.Add(2 * time.Minute)})
	st, err = e.orch().Status(context.Background(), false)
	if err != nil || st.NativeMultiAccount == nil || !*st.NativeMultiAccount ||
		!hasWarning(st.Warnings, "multi-account mode looks active") || hasWarning(st.Warnings, "was signed in to another account") {
		t.Fatalf("chain: %+v %v", st, err)
	}
	// In-folder sign-in replaces the outside-switch warning.
	e2 := newEnv(t)
	signInAsB(t, e2, true)
	d := e2.deps()
	d.Resolve = profile.Resolve
	st, err = New(d).Status(context.Background(), false)
	if err != nil || !hasWarning(st.Warnings, "the folder of a holds the account of b") || hasWarning(st.Warnings, "changed outside paritydesk") {
		t.Fatalf("in folder: %q %v", st.Warnings, err)
	}
}

// doctorChecks runs doctor and returns the checks with id.
func doctorChecks(t *testing.T, o *Orchestrator, id string) []Check {
	t.Helper()
	checks, err := o.Doctor(context.Background(), DoctorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var out []Check
	for _, c := range checks {
		if c.ID == id {
			out = append(out, c)
		}
	}
	return out
}

func TestDoctorBusyDetection(t *testing.T) {
	m6 := testkit.Manifest(t)
	ms, err := compat.Load("")
	if err != nil {
		t.Fatal(err)
	}
	m7, _, _ := compat.Select(ms, "7.37095.0")
	e := newEnv(t)
	check := func(m *compat.Manifest) Check {
		dr := &doctor{o: e.orch()}
		dr.busyDetection(m)
		if len(dr.checks) != 1 {
			t.Fatalf("checks %+v", dr.checks)
		}
		return dr.checks[0]
	}
	if c := check(m6); c.Status != "warn" || c.Detail != txtDoctorBusyDetectionUnavailable {
		t.Errorf("6.x: %+v", c)
	}
	if c := check(m7); c.Status != "ok" || c.Detail != txtDoctorBusyDetectionOK {
		t.Errorf("7.x: %+v", c)
	}
	partial := *m7
	partial.Signals.TurnCleared = nil
	if c := check(&partial); c.Status != "warn" || !strings.Contains(c.Detail, "for up to 6h00m") {
		t.Errorf("7.x without clears: %+v", c)
	}
	record := *m6
	record.Rows = slices.Clone(m6.Rows)
	for i := range record.Rows {
		if record.Rows[i].Row == model.RowSessions {
			record.Rows[i].Fields = map[string][]string{sources.RoleBusy: {"isRunning"}}
		}
	}
	if c := check(&record); c.Status != "ok" || c.Detail != txtDoctorBusyDetectionRecord {
		t.Errorf("record field: %+v", c)
	}
}

func TestDoctorBusyNow(t *testing.T) {
	e := newEnv(t)
	e.app.running = false
	if c := doctorChecks(t, e.orch(), "busy-now"); len(c) != 1 || c[0].Detail != txtDoctorBusyNowAppStopped {
		t.Fatalf("stopped: %+v", c)
	}
	e.app.running = true
	if c := doctorChecks(t, e.orch(), "busy-now"); len(c) != 1 || c[0].Detail != txtDoctorBusyNowNone {
		t.Fatalf("none: %+v", c)
	}
	rep := working(fakeSession(1), fakeSession(2), fakeSession(3), fakeSession(4), fakeSession(5), fakeSession(6))
	rep.Stale = []app.BusySession{{ID: fakeSession(9)}}
	rep.Truncated = true
	e.app.busySeq = []app.BusyReport{rep}
	c := doctorChecks(t, e.orch(), "busy-now")
	if len(c) != 1 || c[0].Status != "ok" || !strings.Contains(c[0].Detail, "6 session(s) working now: local_0000…, local_0000…, local_0000…, local_0000…, local_0000…, …") ||
		!strings.Contains(c[0].Detail, "1 more session(s) started a turn long ago") || !strings.Contains(c[0].Detail, "log is large") {
		t.Fatalf("some: %+v", c)
	}
}

// TestBusyLogNotRead: a log that could not be read is never reported as
// "no session working": doctor warns, and a switch says so once and goes
// on with Claude's quit dialog as the guard. A manifest without busy
// signals says the log cannot show working sessions.
func TestBusyLogNotRead(t *testing.T) {
	ioErr := errors.New("app: main.log: permission denied")
	t.Run("doctor", func(t *testing.T) {
		e := newEnv(t)
		e.app.running = true
		e.app.busyErr = ioErr
		if c := doctorChecks(t, e.orch(), "busy-now"); len(c) != 1 || c[0].Status != "warn" || c[0].Detail != txtDoctorBusyNowUnreadable {
			t.Fatalf("unreadable: %+v", c)
		}
		e.app.busyErr = fmt.Errorf("compile: %w", app.ErrSignalUnknown)
		if c := doctorChecks(t, e.orch(), "busy-now"); len(c) != 1 || c[0].Status != "ok" || c[0].Detail != txtDoctorBusyNowNoSignals {
			t.Fatalf("no signals: %+v", c)
		}
	})
	t.Run("switch", func(t *testing.T) {
		e := newEnv(t)
		e.app.busyErr = ioErr
		res, err := e.orch().Switch(context.Background(), yes("b"))
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, w := range res.Warnings {
			if w == txtWarnBusyLogUnreadable {
				n++
			}
		}
		if n != 1 || e.app.quits != 1 {
			t.Fatalf("warnings %q quits %d", res.Warnings, e.app.quits)
		}
	})
	t.Run("switch without busy signals stays quiet", func(t *testing.T) {
		e := newEnv(t)
		e.app.busyErr = app.ErrSignalUnknown
		res, err := e.orch().Switch(context.Background(), yes("b"))
		if err != nil || hasWarning(res.Warnings, "could not be read") {
			t.Fatalf("err %v warnings %q", err, res.Warnings)
		}
	})
}

func TestDoctorAccountChange(t *testing.T) {
	at := time.Date(2026, 1, 2, 2, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		setup   func(e *env)
		resolve bool
		status  []string
		detail  string
	}{
		{"unavailable", func(e *env) {}, false, []string{"ok"}, txtDoctorAccountChangeUnavailable},
		{"none", func(e *env) {
			e.compat.m.Signals.AccountChanged = "x"
			e.app.changes = []app.AccountChange{}
		}, false, []string{"ok"}, txtDoctorAccountChangeNone},
		{"one", func(e *env) {
			e.app.changes = []app.AccountChange{{From: acctA, To: acctB, At: at}}
		}, false, []string{"warn"}, "the folder of a (account 0000…) was signed in to b (account 0000…) at " + at.Local().Format("2006-01-02 15:04")},
		{"unknown", func(e *env) {
			e.app.changes = []app.AccountChange{{From: acctC, To: acctA}}
		}, false, []string{"warn"}, "a folder that held account 0000…, which no profile registers, was signed in to a (account 0000…) at an unknown time"},
		{"in folder", func(e *env) { signInAsB(e.t, e, true) }, true, []string{"fail"}, "Claude holds the folder of a, signed in to the account of b"},
		{"native log", func(e *env) {
			root := e.st.root(e.t)
			root.LastCommitAt = at.Add(-time.Hour)
			_ = e.st.Commit(root)
			e.app.changes = []app.AccountChange{{From: acctA, To: acctC, At: at}, {From: acctC, To: acctA, At: at.Add(time.Minute)}}
		}, false, []string{"fail", "warn", "warn"}, "the folder of a changed accounts 2 times since the last switch"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			tc.setup(e)
			o := e.orch()
			if tc.resolve {
				o = realResolveOrch(e)
			}
			c := doctorChecks(t, o, "account-change")
			statuses := []string{}
			for _, x := range c {
				statuses = append(statuses, x.Status)
			}
			if !slices.Equal(statuses, tc.status) || !strings.Contains(c[0].Detail, tc.detail) {
				t.Fatalf("checks %+v", c)
			}
		})
	}
}

// TestNativeLogNamePendingAdd: while a profile add is pending, Claude/ is
// bound to no profile and the chain starts at an account no profile
// registers (release checklist step 4). The folder is then named by its
// masked account, never by an empty name.
func TestNativeLogNamePendingAdd(t *testing.T) {
	at := time.Date(2026, 1, 2, 2, 0, 0, 0, time.UTC)
	e := newEnv(t)
	root := e.st.root(t)
	root.LastCommitAt = at.Add(-time.Hour)
	_ = e.st.Commit(root)
	e.app.changes = []app.AccountChange{{From: acctC, To: acctA, At: at}, {From: acctA, To: acctC, At: at.Add(time.Minute)}}
	d := e.deps()
	d.Resolve = func(cfg *config.Config, root *state.Root, folders []profile.Folder, h profile.Hints) (*profile.Resolution, error) {
		res, err := fakeResolve(nil)(cfg, root, folders, h)
		if err != nil {
			return nil, err
		}
		res.Active = nil // what bindActive gives while a profile add is pending
		return res, nil
	}
	c := doctorChecks(t, New(d), "account-change")
	if len(c) == 0 || c[0].Status != "fail" || !strings.Contains(c[0].Detail, "the folder of account 0000… changed accounts 2 times") {
		t.Fatalf("doctor: %+v", c)
	}
	st, err := New(d).Status(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(st.Warnings, "multi-account mode looks active in the folder of account 0000…;") {
		t.Fatalf("status: %q", st.Warnings)
	}
	// With a holder bound to Claude/, an unregistered first account
	// keeps naming the holder; a registered one names its profile.
	if name, n := nativeLogName(attributed(root, e.app.changes), "b"); n != 2 || name != "b" {
		t.Fatalf("holder: %q %d", name, n)
	}
	reg := []app.AccountChange{{From: acctA, To: acctC, At: at}, {From: acctC, To: acctA, At: at.Add(time.Minute)}}
	if name, n := nativeLogName(attributed(root, reg), "b"); n != 2 || name != "a" {
		t.Fatalf("registered: %q %d", name, n)
	}
}

func attributed(root *state.Root, cs []app.AccountChange) []profile.AttributedChange {
	logged := make([]profile.LoggedChange, len(cs))
	for i, c := range cs {
		logged[i] = profile.LoggedChange{From: c.From, To: c.To, At: c.At}
	}
	return profile.AttributeChanges(root, logged)
}

// TestDoctorAccountChangeHistory: the recorded-history rule (a folder
// bound to a profile now holds a third account) is a fail.
func TestDoctorAccountChangeHistory(t *testing.T) {
	e := newEnv(t)
	root := e.st.root(t)
	root.Profiles["a"].AccountHistory = []ids.AccountID{acctB, acctA}
	if err := e.st.Commit(root); err != nil {
		t.Fatal(err)
	}
	d := e.deps()
	d.Resolve = func(cfg *config.Config, root *state.Root, folders []profile.Folder, h profile.Hints) (*profile.Resolution, error) {
		res, err := fakeResolve(nil)(cfg, root, folders, h)
		if err != nil {
			return nil, err
		}
		f := *res.Active.Folder
		f.Identity = &configjson.Identity{Account: acctC}
		res.Active.Folder = &f
		return res, nil
	}
	c := doctorChecks(t, New(d), "account-change")
	if len(c) != 1 || c[0].Status != "fail" || !strings.Contains(c[0].Detail, "the folder of a has held several accounts") {
		t.Fatalf("checks %+v", c)
	}
}

func TestDoctorPairingOrg(t *testing.T) {
	e := newEnv(t)
	if c := doctorChecks(t, e.orch(), "chrome-pairing-org"); len(c) != 1 || c[0].Detail != txtDoctorChromePairingOrgUnknown {
		t.Fatalf("unknown: %+v", c)
	}
	e.app.org = orgA
	e.app.events = []app.LogEvent{app.EvPairingClosed, app.EvAppStarted, app.EvPairingEstablished}
	c := doctorChecks(t, e.orch(), "chrome-pairing-org")
	if len(c) != 1 || c[0].Status != "ok" || !strings.Contains(c[0].Detail, "(0000…, from the Claude log") ||
		!strings.HasSuffix(c[0].Detail, "since it started: established") {
		t.Fatalf("known: %+v", c)
	}
	// A pairing state before the last app start is not shown.
	e.app.events = []app.LogEvent{app.EvPairingEstablished, app.EvAppStarted}
	if c := doctorChecks(t, e.orch(), "chrome-pairing-org"); strings.Contains(c[0].Detail, "since it started") {
		t.Fatalf("old state: %+v", c)
	}
	// The org is read for the current run of the app.
	e.app.since = e.clk.Now().Add(-time.Hour)
	e.app.orgSince = nil
	doctorChecks(t, e.orch(), "chrome-pairing-org")
	if len(e.app.orgSince) == 0 || !e.app.orgSince[len(e.app.orgSince)-1].Equal(e.app.since) {
		t.Fatalf("since %v", e.app.orgSince)
	}
}

func TestDoctorPairingDisabled(t *testing.T) {
	e := newEnv(t)
	e.app.events = []app.LogEvent{app.EvPairingDisabled, app.EvAppStarted}
	for _, c := range doctorChecks(t, e.orch(), "chrome-bridge") {
		if c.Detail == txtDoctorChromeBridgePairingDisabled {
			t.Fatal("a disabled pairing of an earlier launch was reported")
		}
	}
	e.app.events = []app.LogEvent{app.EvAppStarted, app.EvPairingDisabled}
	c := doctorChecks(t, e.orch(), "chrome-bridge")
	if c[0].Status != "warn" || c[0].Detail != txtDoctorChromeBridgePairingDisabled {
		t.Fatalf("checks %+v", c)
	}
	e.app.events = []app.LogEvent{app.EvPairingDisabled}
	if c := doctorChecks(t, e.orch(), "chrome-bridge"); c[0].Detail != txtDoctorChromeBridgePairingDisabled {
		t.Fatalf("no app start: %+v", c)
	}
}

func TestDoctorDeprecatedKey(t *testing.T) {
	e := newEnv(t)
	e.compat.m.Signals.NativeMultiAccount = "x"
	c := doctorChecks(t, e.orch(), "version")
	if !slices.ContainsFunc(c, func(x Check) bool {
		return x.Status == "warn" && x.Detail == "a compatibility manifest override sets signals.native_multi_account, which paritydesk no longer uses; remove it from the override"
	}) {
		t.Fatalf("checks %+v", c)
	}
}

// TestDoctorCheckOrder: the log-signal checks follow busy-detection.
func TestDoctorCheckOrder(t *testing.T) {
	e := newEnv(t)
	checks, err := e.orch().Doctor(context.Background(), DoctorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, c := range checks {
		if len(order) == 0 || order[len(order)-1] != c.ID {
			order = append(order, c.ID)
		}
	}
	i := slices.Index(order, "busy-detection")
	if i < 0 || !slices.Equal(order[i:i+4], []string{"busy-detection", "busy-now", "account-change", "chrome-pairing-org"}) {
		t.Fatalf("order %v", order)
	}
}

// TestDetachedParentRecordsCursor: the parent records main.log's inode
// and size first and passes both to the job.
func TestDetachedParentRecordsCursor(t *testing.T) {
	e := newEnv(t)
	e.app.logIno = 4242
	res, err := e.orch().Switch(context.Background(), SwitchOptions{To: "b", Yes: true, ConfirmFirstSync: true, Detach: true, LogOffset: -1})
	if err != nil || !res.Detached {
		t.Fatalf("%v", err)
	}
	args := strings.Join(e.det.submitted[0], " ")
	if !strings.Contains(args, "--log-offset=1000") || !strings.Contains(args, "--log-inode=4242") {
		t.Fatalf("args %s", args)
	}
	o := parseJobArgs(t, e.det.submitted[0])
	if o.LogOffset != 1000 || o.LogInode != 4242 {
		t.Fatalf("parsed %+v", o)
	}
}
