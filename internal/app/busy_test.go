package app

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/ids"
)

// sig7x returns the signals of the embedded 7.x manifest.
func sig7x(t testing.TB) compat.Signals {
	t.Helper()
	ms, err := compat.Load("")
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := compat.Select(ms, "7.37095.0")
	if err != nil || m == nil {
		t.Fatalf("no 7.x manifest: %v", err)
	}
	return m.Signals
}

// Messages of the 7.x app, as the manifest's patterns expect them.
func msgStart(s string) string { return "Sending message to session " + s }
func msgEnd(s string) string   { return "[Stop hook] Query completed for session " + s }
func msgUncounted(s string) string {
	return "[keep-awake] session " + s + " no longer counted (stalled)"
}
func msgRelease(reason string) string {
	return "[keep-awake] code session hold released (" + reason + ", held 1234ms, battery=false)"
}

const (
	msgHeld     = "[keep-awake] code session hold taken (turn, battery=false)"
	msgAppStart = "[startup-perf] main window shown 900 ms after launch"
)

// msgClears are the turn_cleared messages of 7.x for session s.
func msgClears(s string) []string {
	return []string{
		"Interrupting session " + s,
		"[interruptTurn] Interrupting session " + s,
		"LocalSessions.interrupt: sessionId=" + s,
		"LocalSessions.stop: sessionId=" + s,
		"LocalSessions.stopFamily: sessionId=" + s,
		"Stopping session " + s,
		"[interruptTurn] Stopping session " + s + ": its process has not started",
		"Session " + s + " query error: something failed",
		"Session " + s + " query interrupted (intentional stop)",
		"Session " + s + " query interrupted by user (intentional stop)",
		"Session " + s + " query iterator completed",
		"[CCD] " + s + ": the turn failed; 1 queued message(s) now held unsent",
		"Session " + s + " stream ended without a result message, transitioning to idle",
		"[Result] Turn succeeded for session " + s,
		"[Result] Turn failed for session " + s + ": error",
		"[Result] Turn ended by user interrupt for session " + s,
	}
}

var busyT0 = time.Date(2026, 11, 26, 10, 0, 0, 0, testZone)

// bev is one record line at an offset from busyT0.
type bev struct {
	at  time.Duration
	msg string
}

func newMachine(t testing.TB, sig compat.Signals, since time.Time) *busyMachine {
	t.Helper()
	b, _, err := compileBusy(sig)
	if err != nil {
		t.Fatal(err)
	}
	return newBusyMachine(b, since)
}

func feed(m *busyMachine, evs []bev) {
	for _, e := range evs {
		m.line(busyT0.Add(e.at), []byte(e.msg))
	}
}

func idsOf(l []BusySession) []ids.SessionID {
	out := []ids.SessionID{}
	for _, s := range l {
		out = append(out, s.ID)
	}
	return out
}

// countedOnly counts the given sessions (Code sessions).
func countedOnly(code ...string) func(ids.SessionID) bool {
	return func(id ids.SessionID) bool { return slices.Contains(code, string(id)) }
}

func TestBusyRules(t *testing.T) {
	sig := sig7x(t)
	opts := func(now time.Duration, counted func(ids.SessionID) bool, trusted bool) BusyOptions {
		return BusyOptions{Now: busyT0.Add(now), StaleAfter: 6 * time.Hour, Counted: counted, HoldTrusted: trusted}
	}
	tests := []struct {
		name         string
		since        time.Time
		evs          []bev
		opts         BusyOptions
		working      []ids.SessionID
		stale        []ids.SessionID
		hold         HoldState
		skipHoldTest bool
	}{
		{name: "start then end", evs: []bev{{0, msgStart(sessA)}, {time.Second, msgEnd(sessA)}},
			opts: opts(time.Minute, nil, false)},
		{name: "start only", evs: []bev{{0, msgStart(sessA)}}, opts: opts(time.Minute, nil, false),
			working: []ids.SessionID{sessA}},
		{name: "R5' idle release 40 s after a Code start ends it",
			evs:  []bev{{0, msgStart(sessA)}, {40 * time.Second, msgRelease("idle")}},
			opts: opts(time.Minute, countedOnly(sessA), false), hold: HoldIdle},
		{name: "R5' start inside the grace window stays working",
			evs:  []bev{{0, msgStart(sessA)}, {10 * time.Second, msgRelease("idle")}},
			opts: opts(20*time.Second, countedOnly(sessA), true), working: []ids.SessionID{sessA}, hold: HoldIdle},
		{name: "R7 the hold never counted it",
			evs:  []bev{{0, msgStart(sessA)}, {10 * time.Second, msgRelease("idle")}},
			opts: opts(3*time.Minute, countedOnly(sessA), true), hold: HoldIdle},
		{name: "R7 needs a trusted hold",
			evs:  []bev{{0, msgStart(sessA)}, {10 * time.Second, msgRelease("idle")}},
			opts: opts(3*time.Minute, countedOnly(sessA), false), working: []ids.SessionID{sessA}, hold: HoldIdle},
		{name: "R4 hold taken after the release keeps it",
			evs:  []bev{{0, msgStart(sessA)}, {10 * time.Second, msgRelease("idle")}, {15 * time.Second, msgHeld}},
			opts: opts(3*time.Minute, countedOnly(sessA), true), working: []ids.SessionID{sessA}, hold: HoldHeld},
		{name: "Cowork is not cleared by an idle release",
			evs: []bev{{0, msgStart(sessB)}, {time.Second, msgStart(sessA)}, {2 * time.Second, msgEnd(sessA)},
				{40 * time.Second, msgRelease("idle")}},
			opts: opts(5*time.Minute, countedOnly(sessA), true), working: []ids.SessionID{sessB}, hold: HoldIdle},
		{name: "Counted nil ignores hold lines",
			evs:  []bev{{0, msgStart(sessA)}, {40 * time.Second, msgRelease("idle")}},
			opts: opts(5*time.Minute, nil, true), working: []ids.SessionID{sessA}, hold: HoldIdle},
		{name: "a battery release makes the hold unknown",
			evs:  []bev{{0, msgStart(sessA)}, {40 * time.Second, msgRelease("battery")}},
			opts: opts(5*time.Minute, countedOnly(sessA), true), working: []ids.SessionID{sessA}, hold: HoldUnknown},
		{name: "R8 an uncounted session survives an armed_grace release",
			evs: []bev{{0, msgStart(sessA)}, {time.Second, msgHeld}, {time.Hour, msgUncounted(sessA)},
				{time.Hour + 31*time.Second, msgRelease("armed_grace")}},
			opts: opts(time.Hour+5*time.Minute, countedOnly(sessA), true), working: []ids.SessionID{sessA}, hold: HoldIdle},
		{name: "R8 for a session that is not open does nothing",
			evs:  []bev{{0, msgUncounted(sessA)}, {time.Second, msgStart(sessA)}, {time.Minute, msgRelease("idle")}},
			opts: opts(2*time.Minute, countedOnly(sessA), true), hold: HoldIdle},
		{name: "R6 stale", evs: []bev{{0, msgStart(sessA)}},
			opts: opts(7*time.Hour, nil, false), stale: []ids.SessionID{sessA}},
		{name: "R0 a line older than Since is ignored", since: busyT0.Add(time.Hour),
			evs:  []bev{{0, msgStart(sessA)}, {time.Hour, msgStart(sessB)}},
			opts: opts(time.Hour+time.Minute, nil, false), working: []ids.SessionID{sessB}},
		{name: "R1 an app start clears without Since",
			evs:  []bev{{0, msgStart(sessA)}, {time.Second, msgAppStart}, {2 * time.Second, msgStart(sessB)}},
			opts: opts(time.Minute, nil, false), working: []ids.SessionID{sessB}},
		{name: "R1 is not used with Since", since: busyT0,
			evs:  []bev{{0, msgStart(sessA)}, {time.Second, msgAppStart}},
			opts: opts(time.Minute, nil, false), working: []ids.SessionID{sessA}},
		{name: "a second start after an end",
			evs:  []bev{{0, msgStart(sessA)}, {time.Second, msgEnd(sessA)}, {2 * time.Second, msgStart(sessA)}},
			opts: opts(time.Minute, nil, false), working: []ids.SessionID{sessA}},
		{name: "another session's end does not clear",
			evs:  []bev{{0, msgStart(sessA)}, {time.Second, msgEnd(sessB)}},
			opts: opts(time.Minute, nil, false), working: []ids.SessionID{sessA}},
		{name: "an invalid id never starts a turn",
			evs:  []bev{{0, msgStart("local_not-a-valid-id")}, {0, msgStart(strings.ToUpper(sessA))}},
			opts: opts(time.Minute, nil, false)},
		{name: "a start id with trailing text is not a session",
			evs:  []bev{{0, msgStart(sessA + "x")}},
			opts: opts(time.Minute, nil, false)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newMachine(t, sig, tc.since)
			feed(m, tc.evs)
			rep := m.report(tc.opts)
			if got := idsOf(rep.Working); !slices.Equal(got, append([]ids.SessionID{}, tc.working...)) {
				t.Errorf("working %v, want %v", got, tc.working)
			}
			if got := idsOf(rep.Stale); !slices.Equal(got, append([]ids.SessionID{}, tc.stale...)) {
				t.Errorf("stale %v, want %v", got, tc.stale)
			}
			want := tc.hold
			if want == "" {
				want = HoldUnknown
			}
			if rep.Hold != want {
				t.Errorf("hold %v, want %v", rep.Hold, want)
			}
		})
	}
	t.Run("every turn_cleared line ends a turn", func(t *testing.T) {
		for _, c := range msgClears(sessA) {
			m := newMachine(t, sig, time.Time{})
			feed(m, []bev{{0, msgStart(sessA)}, {time.Second, c}})
			if rep := m.report(opts(time.Minute, nil, false)); len(rep.Working) != 0 {
				t.Errorf("%q did not end the turn", c)
			}
			m = newMachine(t, sig, time.Time{})
			feed(m, []bev{{0, msgStart(sessA)}, {time.Second, strings.ReplaceAll(c, sessA, sessB)}})
			if rep := m.report(opts(time.Minute, nil, false)); len(rep.Working) != 1 {
				t.Errorf("%q of another session ended the turn", c)
			}
		}
	})
}

func TestCheckBusySignals(t *testing.T) {
	sup, err := CheckBusySignals(sig7x(t))
	if err != nil || !sup.Starts || !sup.Ends || sup.Clears != 9 || !sup.Hold {
		t.Fatalf("7.x: %+v %v", sup, err)
	}
	for _, s := range []compat.Signals{
		{TurnEnded: `\[turn\] done id={session}`},
		{TurnStarted: `\[turn\] begin id={session}`},
		{TurnStarted: `\[turn\] begin`, TurnEnded: `\[turn\] done id={session}`},
		{TurnStarted: `(`, TurnEnded: `\[turn\] done id={session}`},
	} {
		if _, err := CheckBusySignals(s); !errors.Is(err, ErrSignalUnknown) {
			t.Errorf("%+v: got %v", s, err)
		}
	}
	partial := sig7x(t)
	partial.TurnCleared, partial.WorkReleased = nil, ""
	if sup, err := CheckBusySignals(partial); err != nil || sup.Clears != 0 || sup.Hold {
		t.Fatalf("partial: %+v %v", sup, err)
	}
}

// writeEvents appends evs to l as record lines.
func writeEvents(l *testLog, evs []bev) {
	for _, e := range evs {
		l.append(at(busyT0.Add(e.at), e.msg))
	}
}

func TestPlatformBusyTrackerIncremental(t *testing.T) {
	ctx := t.Context()
	sig := sig7x(t)
	l := newTestLog(t)
	read := 0
	lg := l.log()
	lg.onRead = func(n int) { read += n }
	p := &Platform{Log: lg}
	writeEvents(l, []bev{{0, msgStart(sessA)}, {time.Second, msgStart(sessB)}})
	opts := BusyOptions{Since: busyT0, Now: busyT0.Add(time.Minute), Counted: countedOnly(sessA)}
	rep, err := p.BusySessions(ctx, sig, opts)
	if err != nil || !slices.Equal(idsOf(rep.Working), []ids.SessionID{sessA, sessB}) {
		t.Fatalf("first: %+v %v", rep, err)
	}
	before, _ := l.log().Size()
	writeEvents(l, []bev{{2 * time.Second, msgEnd(sessA)}})
	after, _ := l.log().Size()
	read = 0
	rep, err = p.BusySessions(ctx, sig, opts)
	if err != nil || !slices.Equal(idsOf(rep.Working), []ids.SessionID{sessB}) {
		t.Fatalf("second: %+v %v", rep, err)
	}
	if appended := int(after - before); read > appended+1 {
		t.Fatalf("read %d bytes for %d appended", read, appended)
	}
	// A changed Counted only changes the report, not the reading.
	read = 0
	opts.Counted = nil
	if _, err := p.BusySessions(ctx, sig, opts); err != nil || read > 1 {
		t.Fatalf("counted change read %d bytes: %v", read, err)
	}
	// A changed Since (the app restarted) re-reads the window.
	read = 0
	opts.Since = busyT0.Add(-time.Hour)
	if _, err := p.BusySessions(ctx, sig, opts); err != nil || read < int(after) {
		t.Fatalf("since change read %d of %d bytes: %v", read, after, err)
	}
	// Another manifest gets another tracker; unknown signals report so.
	if _, err := p.BusySessions(ctx, compat.Signals{TurnEnded: sig.TurnEnded}, opts); !errors.Is(err, ErrSignalUnknown) {
		t.Fatalf("got %v", err)
	}
}

func TestBusyTrackerAcrossRotationAndRestart(t *testing.T) {
	ctx := t.Context()
	sig := sig7x(t)
	l := newTestLog(t)
	tr, err := l.log().NewBusyTracker(sig)
	if err != nil {
		t.Fatal(err)
	}
	opts := BusyOptions{Now: busyT0.Add(time.Minute)}
	writeEvents(l, []bev{{0, msgAppStart}, {time.Second, msgStart(sessA)}})
	if rep, err := tr.Update(ctx, opts); err != nil || len(rep.Working) != 1 {
		t.Fatalf("first: %+v %v", rep, err)
	}
	l.rotate()
	writeEvents(l, []bev{{2 * time.Second, msgStart(sessB)}})
	if rep, err := tr.Update(ctx, opts); err != nil || !slices.Equal(idsOf(rep.Working), []ids.SessionID{sessA, sessB}) {
		t.Fatalf("after rotation: %+v %v", rep, err)
	}
	writeEvents(l, []bev{{3 * time.Second, msgAppStart}})
	if rep, err := tr.Update(ctx, opts); err != nil || len(rep.Working) != 0 {
		t.Fatalf("after restart: %+v %v", rep, err)
	}
}

// TestBusyTrackerMainLogMissing: a poll between the app's rename of
// main.log and the creation of the new one must not lose the lines the
// old file received after the cursor (a turn start would look idle).
func TestBusyTrackerMainLogMissing(t *testing.T) {
	ctx := t.Context()
	sig := sig7x(t)
	for _, primeDuring := range []bool{false, true} {
		l := newTestLog(t)
		tr, err := l.log().NewBusyTracker(sig)
		if err != nil {
			t.Fatal(err)
		}
		opts := BusyOptions{Now: busyT0.Add(time.Minute)}
		writeEvents(l, []bev{{0, msgAppStart}})
		if primeDuring {
			writeEvents(l, []bev{{time.Second, msgStart(sessA)}})
			l.rotate()
		}
		if rep, err := tr.Update(ctx, opts); err != nil || len(rep.Working) != len(idsIf(primeDuring, sessA)) {
			t.Fatalf("prime (%v): %+v %v", primeDuring, rep, err)
		}
		if !primeDuring {
			writeEvents(l, []bev{{time.Second, msgStart(sessA)}})
			l.rotate() // no new main.log yet
			if rep, err := tr.Update(ctx, opts); err != nil || !slices.Equal(idsOf(rep.Working), []ids.SessionID{sessA}) {
				t.Fatalf("during rotation: %+v %v", rep, err)
			}
		}
		writeEvents(l, []bev{{2 * time.Second, msgStart(sessB)}})
		if rep, err := tr.Update(ctx, opts); err != nil || !slices.Equal(idsOf(rep.Working), []ids.SessionID{sessA, sessB}) {
			t.Fatalf("after (%v): %+v %v (cur %+v)", primeDuring, rep, err, tr.cur)
		}
	}
}

func idsIf(ok bool, s ...ids.SessionID) []ids.SessionID {
	if ok {
		return s
	}
	return nil
}

// TestBusyWindowBounded: a window read reads at most the budget plus one
// line, and an incremental update reads only the appended bytes.
func TestBusyWindowBounded(t *testing.T) {
	ctx := t.Context()
	sig := sig7x(t)
	l := newTestLog(t)
	var lines []string
	for i := 0; len(strings.Join(lines, "")) < 300<<10; i++ {
		lines = append(lines, at(busyT0.Add(time.Duration(i)*time.Second), fmt.Sprintf("[other] filler line %d with some text", i)))
	}
	l.append(lines...)
	read := 0
	lg := l.log()
	lg.budget = 64 << 10
	lg.onRead = func(n int) { read += n }
	tr, err := lg.NewBusyTracker(sig)
	if err != nil {
		t.Fatal(err)
	}
	opts := BusyOptions{Now: busyT0.Add(time.Hour)}
	rep, err := tr.Update(ctx, opts)
	if err != nil || !rep.Truncated {
		t.Fatalf("window: %+v %v", rep, err)
	}
	if limit := int(lg.budget) + maxLineLen; read > limit {
		t.Fatalf("window read %d bytes, limit %d", read, limit)
	}
	before, _ := l.log().Size()
	writeEvents(l, []bev{{time.Hour, msgStart(sessA)}})
	after, _ := l.log().Size()
	read = 0
	if rep, err := tr.Update(ctx, opts); err != nil || len(rep.Working) != 1 {
		t.Fatalf("update: %+v %v", rep, err)
	}
	if read > int(after-before)+1 {
		t.Fatalf("update read %d bytes for %d appended", read, after-before)
	}
}

func TestPrefilterNoAlloc(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts differ under -race")
	}
	m := newMachine(t, sig7x(t), busyT0)
	msg := []byte("[other] a line no busy pattern matches, with some words in it")
	tm := busyT0.Add(time.Minute)
	if n := testing.AllocsPerRun(200, func() { m.line(tm, msg) }); n != 0 {
		t.Fatalf("%v allocations per non-matching line", n)
	}
}

// TestBusyNeverLeaksLines: text of a matched line never shows up in a
// report or an error.
func TestBusyNeverLeaksLines(t *testing.T) {
	const canary = "CANARY-token-7f3a"
	l := newTestLog(t)
	writeEvents(l, []bev{{0, msgStart(sessA) + " " + canary}, {time.Second, msgStart(canary)}})
	rep, err := (&Platform{Log: l.log()}).BusySessions(t.Context(), sig7x(t), BusyOptions{Now: busyT0.Add(time.Minute)})
	if err != nil || len(rep.Working) != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
	if s := fmt.Sprintf("%+v", rep); strings.Contains(s, canary) {
		t.Fatalf("report leaks the line: %s", s)
	}
	bad := sig7x(t)
	bad.TurnStarted = `(` + canary
	if _, err := (&Platform{Log: l.log()}).BusySessions(t.Context(), bad, BusyOptions{}); err == nil || strings.Contains(err.Error(), canary) {
		t.Fatalf("error %v", err)
	}
}

// TestContinuationLinesNeverMatch: a signal's message on a continuation
// line (after a record line that starts a multi-line message) or after
// leading text inside a record line gives no event, no busy entry, no
// account change and no org.
func TestContinuationLinesNeverMatch(t *testing.T) {
	ctx := t.Context()
	sig := sig7x(t)
	messages := append([]string{
		msgStart(sessA), msgEnd(sessA), msgHeld, msgRelease("idle"), msgUncounted(sessA), msgAppStart,
		"beforeQuit: vetoed by before-quit interceptor",
		"[CCD] LocalSessions.setFocusedSession: sessionId=" + sessA,
		"[claude-in-chrome] Paired with Chrome extension (duration: 10ms)",
		"[Chrome Extension MCP] local pairing disabled for this launch: userData relocated",
		"[Chrome Extension MCP] local pairing beside the relay: established",
		"[updater] Previous run did not exit cleanly; prompt install is switched off",
		activeOrgMsg(acctP, orgX),
		accountChangedMsg(acctP, acctQ),
		"[connector-owner] account changed — disconnecting 2 stored built-in connector(s)",
	}, msgClears(sessA)...)
	for _, msg := range messages {
		for _, form := range []string{"continuation", "inside"} {
			l := newTestLog(t)
			l.append(at(busyT0, "Session "+sessB+" CLI stderr tail:"))
			if form == "continuation" {
				l.append(msg + "\n")
			} else {
				l.append(at(busyT0.Add(time.Second), "prefix "+msg))
			}
			lg := l.log()
			evs, _, err := lg.Events(ctx, 0, sig, "")
			if err != nil || len(evs) != 0 {
				t.Errorf("%s %q: events %v %v", form, msg, evs, err)
			}
			rep, err := (&Platform{Log: lg}).BusySessions(ctx, sig, BusyOptions{Now: busyT0.Add(time.Minute)})
			if err != nil || len(rep.Working)+len(rep.Stale) != 0 || rep.Hold != HoldUnknown {
				t.Errorf("%s %q: busy %+v %v", form, msg, rep, err)
			}
			if ch, _, err := lg.AccountChanges(ctx, sig, time.Time{}); err != nil || len(ch) != 0 {
				t.Errorf("%s %q: changes %v %v", form, msg, ch, err)
			}
			if _, found, err := lg.ActiveOrg(ctx, sig, acctP, time.Time{}); err != nil || found {
				t.Errorf("%s %q: org %v %v", form, msg, found, err)
			}
			if _, known, err := lg.LastReplica(ctx, sig, time.Time{}); err != nil || known {
				t.Errorf("%s %q: replica %v %v", form, msg, known, err)
			}
		}
	}
}

func activeOrgMsg(acct, org string) string {
	return "[CCDScheduledTasks] Initialized { accountId: '" + acct + "', orgId: '" + org + "' }"
}

func accountChangedMsg(from, to string) string {
	return "[account] Login-state transition (loggedOut: false \u2192 false, uuid: " + from + " \u2192 " + to + "), clearing oauth cache"
}

// ---- property tests (seeded) ----

var propSessions = []string{sessA, sessB, sessC, "local_00000000-0000-4000-8000-00000000000d"}

// randomTurnEvents returns random start, end and clear events over the
// sessions, a second apart.
func randomTurnEvents(r *rand.Rand, n int) []bev {
	var evs []bev
	for i := 0; i < n; i++ {
		s := propSessions[r.IntN(len(propSessions))]
		var msg string
		switch r.IntN(3) {
		case 0:
			msg = msgStart(s)
		case 1:
			msg = msgEnd(s)
		default:
			c := msgClears(s)
			msg = c[r.IntN(len(c))]
		}
		evs = append(evs, bev{time.Duration(i) * time.Second, msg})
	}
	return evs
}

// lastIsStart reports whether s's last event in evs is a start.
func lastIsStart(evs []bev, s string) bool {
	last := false
	for _, e := range evs {
		if strings.Contains(e.msg, s) {
			last = strings.HasPrefix(e.msg, "Sending message to session ")
		}
	}
	return last
}

func TestBusyPropertyLastEventWins(t *testing.T) {
	sig := sig7x(t)
	r := rand.New(rand.NewPCG(1, 2))
	for iter := 0; iter < 300; iter++ {
		evs := randomTurnEvents(r, r.IntN(40))
		m := newMachine(t, sig, time.Time{})
		feed(m, evs)
		rep := m.report(BusyOptions{Now: busyT0.Add(time.Hour), StaleAfter: 6 * time.Hour})
		for _, s := range propSessions {
			if got := slices.Contains(idsOf(rep.Working), ids.SessionID(s)); got != lastIsStart(evs, s) {
				t.Fatalf("iter %d %s: working %v, last is start %v", iter, s, got, !got)
			}
		}
	}
}

func TestBusyPropertyIndependence(t *testing.T) {
	sig := sig7x(t)
	r := rand.New(rand.NewPCG(3, 4))
	for iter := 0; iter < 300; iter++ {
		var own []bev
		for i := 0; i < r.IntN(10); i++ {
			if r.IntN(2) == 0 {
				own = append(own, bev{0, msgStart(sessA)})
			} else {
				own = append(own, bev{0, msgEnd(sessA)})
			}
		}
		others := randomTurnEvents(r, r.IntN(30))
		others = slices.DeleteFunc(others, func(e bev) bool { return strings.Contains(e.msg, sessA) })
		mixed := slices.Clone(own)
		for _, o := range others {
			i := r.IntN(len(mixed) + 1)
			mixed = slices.Insert(mixed, i, o)
		}
		for i := range mixed {
			mixed[i].at = time.Duration(i) * time.Second
		}
		for i := range own {
			own[i].at = time.Duration(i) * time.Second
		}
		a, b := newMachine(t, sig, time.Time{}), newMachine(t, sig, time.Time{})
		feed(a, own)
		feed(b, mixed)
		o := BusyOptions{Now: busyT0.Add(time.Hour)}
		inA := slices.Contains(idsOf(a.report(o).Working), sessA)
		inB := slices.Contains(idsOf(b.report(o).Working), sessA)
		if inA != inB {
			t.Fatalf("iter %d: alone %v, mixed %v", iter, inA, inB)
		}
	}
}

func TestBusyPropertyMonotoneClearing(t *testing.T) {
	sig := sig7x(t)
	r := rand.New(rand.NewPCG(5, 6))
	for iter := 0; iter < 300; iter++ {
		evs := randomTurnEvents(r, r.IntN(30))
		if r.IntN(2) == 0 {
			evs = append(evs, bev{time.Duration(len(evs)) * time.Second, msgHeld})
		}
		counted := countedOnly(propSessions[:r.IntN(len(propSessions)+1)]...)
		o := BusyOptions{Now: busyT0.Add(time.Duration(r.IntN(600)) * time.Second), Counted: counted, HoldTrusted: r.IntN(2) == 0}
		m := newMachine(t, sig, time.Time{})
		feed(m, evs)
		before := idsOf(m.report(o).Working)
		var extra string
		s := propSessions[r.IntN(len(propSessions))]
		switch r.IntN(3) {
		case 0:
			extra = msgRelease("idle")
		case 1:
			extra = msgEnd(s)
		default:
			extra = msgClears(s)[r.IntN(len(msgClears(s)))]
		}
		m.line(busyT0.Add(time.Duration(len(evs))*time.Second), []byte(extra))
		for _, id := range idsOf(m.report(o).Working) {
			if !slices.Contains(before, id) {
				t.Fatalf("iter %d: %q added %s", iter, extra, id)
			}
		}
	}
}

func TestBusyPropertyCountingOff(t *testing.T) {
	sig := sig7x(t)
	r := rand.New(rand.NewPCG(7, 8))
	holdLines := []string{msgHeld, msgRelease("idle"), msgRelease("armed_grace"), msgRelease("battery"), msgUncounted(sessA)}
	for iter := 0; iter < 300; iter++ {
		evs := randomTurnEvents(r, r.IntN(30))
		with := slices.Clone(evs)
		for i := 0; i < r.IntN(8); i++ {
			with = slices.Insert(with, r.IntN(len(with)+1), bev{msg: holdLines[r.IntN(len(holdLines))]})
		}
		for i := range with {
			with[i].at = time.Duration(i) * time.Second
		}
		for i := range evs {
			evs[i].at = time.Duration(i) * time.Second
		}
		o := BusyOptions{Now: busyT0.Add(time.Hour), HoldTrusted: true}
		a, b := newMachine(t, sig, time.Time{}), newMachine(t, sig, time.Time{})
		feed(a, evs)
		feed(b, with)
		if x, y := idsOf(a.report(o).Working), idsOf(b.report(o).Working); !slices.Equal(x, y) {
			t.Fatalf("iter %d: %v vs %v with hold lines", iter, x, y)
		}
	}
}

// TestBusyPropertyIncrementalEqualsFull: writing the log in random chunks
// (mid-line included) with rotations between them, the tracker's report
// equals one window read over the final files, while nothing was rotated
// out.
func TestBusyPropertyIncrementalEqualsFull(t *testing.T) {
	ctx := t.Context()
	sig := sig7x(t)
	r := rand.New(rand.NewPCG(9, 10))
	for iter := 0; iter < 60; iter++ {
		evs := randomTurnEvents(r, 5+r.IntN(40))
		if r.IntN(3) == 0 {
			evs = slices.Insert(evs, r.IntN(len(evs)), bev{msg: msgAppStart})
		}
		var text strings.Builder
		for i, e := range evs {
			text.WriteString(at(busyT0.Add(time.Duration(i)*time.Second), e.msg))
		}
		data := text.String()
		l := newTestLog(t)
		tr, err := l.log().NewBusyTracker(sig)
		if err != nil {
			t.Fatal(err)
		}
		o := BusyOptions{Now: busyT0.Add(time.Hour)}
		rotations := 0
		for pos := 0; pos < len(data); {
			n := 1 + r.IntN(len(data)-pos)
			l.append(data[pos : pos+n])
			pos += n
			if _, err := tr.Update(ctx, o); err != nil {
				t.Fatal(err)
			}
			if rotations < maxRotated && r.IntN(3) == 0 {
				l.rotate()
				rotations++
			}
		}
		got, err := tr.Update(ctx, o)
		if err != nil {
			t.Fatal(err)
		}
		m := newMachine(t, sig, time.Time{})
		if _, _, err := l.log().readWindow(ctx, windowOpts{}, m.line); err != nil {
			t.Fatal(err)
		}
		want := m.report(o)
		if !slices.Equal(idsOf(got.Working), idsOf(want.Working)) || !slices.Equal(idsOf(got.Stale), idsOf(want.Stale)) {
			t.Fatalf("iter %d: incremental %v, full %v", iter, idsOf(got.Working), idsOf(want.Working))
		}
	}
}

// TestBusyPropertyNoPanic: random bytes never panic and never reach an
// error.
func TestBusyPropertyNoPanic(t *testing.T) {
	ctx := t.Context()
	sig := sig7x(t)
	r := rand.New(rand.NewPCG(11, 12))
	alphabet := []string{"\x00", "\xff\xfe", "\r\n", "\n", " [info] ", "2026-11-26 10:00:00", "Sending message to session ", sessA, "[keep-awake] ", "\u00e9", "\u2192"}
	for iter := 0; iter < 40; iter++ {
		var b strings.Builder
		for b.Len() < 4<<10 {
			if r.IntN(50) == 0 {
				b.WriteString(strings.Repeat("x", 70<<10))
				continue
			}
			b.WriteString(alphabet[r.IntN(len(alphabet))])
		}
		l := newTestLog(t)
		l.append(b.String())
		lg := l.log()
		if _, _, err := lg.Events(ctx, int64(r.IntN(100)), sig, ""); err != nil {
			t.Fatalf("events: %v", err)
		}
		if _, err := (&Platform{Log: lg}).BusySessions(ctx, sig, BusyOptions{Now: busyT0}); err != nil {
			t.Fatalf("busy: %v", err)
		}
		if _, _, err := lg.AccountChanges(ctx, sig, time.Time{}); err != nil {
			t.Fatalf("changes: %v", err)
		}
		if _, _, err := lg.LastReplica(ctx, sig, busyT0); err != nil {
			t.Fatalf("replica: %v", err)
		}
	}
}

// TestBusyPropertyContinuationInert: continuation lines inserted anywhere
// never change a report, the events, the account changes or the org.
func TestBusyPropertyContinuationInert(t *testing.T) {
	ctx := t.Context()
	sig := sig7x(t)
	r := rand.New(rand.NewPCG(13, 14))
	conts := []string{"    at Object.<anonymous> (x.js:1:2)", msgStart(sessD()), msgEnd(sessA), msgRelease("idle"),
		accountChangedMsg(acctQ, acctP), activeOrgMsg(acctQ, orgY), "", "Error: " + msgStart(sessB)}
	for iter := 0; iter < 60; iter++ {
		evs := randomTurnEvents(r, r.IntN(20))
		if r.IntN(2) == 0 {
			evs = append(evs, bev{msg: activeOrgMsg(acctP, orgX)})
		}
		if r.IntN(2) == 0 {
			evs = append(evs, bev{msg: accountChangedMsg(acctP, acctQ)})
		}
		var plain, mixed []string
		for i, e := range evs {
			line := at(busyT0.Add(time.Duration(i)*time.Second), e.msg)
			plain, mixed = append(plain, line), append(mixed, line)
			if r.IntN(2) == 0 {
				mixed = append(mixed, conts[r.IntN(len(conts))]+"\n")
			}
		}
		results := func(lines []string) string {
			l := newTestLog(t)
			l.append(lines...)
			lg := l.log()
			evs, _, err1 := lg.Events(ctx, 0, sig, "")
			rep, err2 := (&Platform{Log: lg}).BusySessions(ctx, sig, BusyOptions{Now: busyT0.Add(time.Hour)})
			ch, _, err3 := lg.AccountChanges(ctx, sig, time.Time{})
			org, found, err4 := lg.ActiveOrg(ctx, sig, acctP, time.Time{})
			rep2, known, err5 := lg.LastReplica(ctx, sig, time.Time{})
			if err := errors.Join(err1, err2, err3, err4, err5); err != nil {
				t.Fatal(err)
			}
			return fmt.Sprint(evs, idsOf(rep.Working), rep.Hold, ch, org, found, rep2, known)
		}
		if a, b := results(plain), results(mixed); a != b {
			t.Fatalf("iter %d:\n%s\n%s", iter, a, b)
		}
	}
}

func sessD() string { return "local_00000000-0000-4000-8000-00000000000d" }

func BenchmarkBusyWindow(b *testing.B) {
	sig := sig7x(b)
	l := &testLog{dir: b.TempDir(), clock: busyT0}
	var sb strings.Builder
	for i := 0; sb.Len() < windowBytes; i++ {
		msg := fmt.Sprintf("[other] filler line %d with some text that no pattern matches at all", i)
		if i%100 == 0 {
			msg = msgStart(propSessions[i%len(propSessions)])
		}
		sb.WriteString(at(busyT0.Add(time.Duration(i)*time.Millisecond), msg))
	}
	if err := os.WriteFile(l.path("main.log"), []byte(sb.String()), 0o600); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tr, err := l.log().NewBusyTracker(sig)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := tr.Update(b.Context(), BusyOptions{Now: busyT0.Add(time.Hour)}); err != nil {
			b.Fatal(err)
		}
	}
}
