package app

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/ids"
)

// testLog is a main.log in a temp dir that tests append to.
type testLog struct {
	t    *testing.T
	path string
}

func newTestLog(t *testing.T) *testLog {
	return &testLog{t: t, path: filepath.Join(t.TempDir(), "main.log")}
}

func (l *testLog) append(lines ...string) {
	l.t.Helper()
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		l.t.Fatal(err)
	}
	defer f.Close()
	for _, line := range lines {
		if _, err := f.WriteString(line); err != nil {
			l.t.Fatal(err)
		}
	}
}

func (l *testLog) log() fileLog { return fileLog{path: l.path} }

func stamp(s string) string { return "[2026-01-01 00:00:00.000] [info] " + s + "\n" }

func TestEventsIncremental(t *testing.T) {
	ctx := t.Context()
	l := newTestLog(t)
	lg := l.log()
	if n, err := lg.Size(); err != nil || n != 0 {
		t.Fatalf("missing log: %d %v", n, err)
	}
	evs, off, err := lg.Events(ctx, 0, testSig, "")
	if err != nil || len(evs) != 0 || off != 0 {
		t.Fatalf("missing log: %v %d %v", evs, off, err)
	}
	l.append(stamp("[boot] ready v=1.2.3"), stamp("[bridge] peer up"), stamp("[turn] done id="+sessA))
	evs, off, err = lg.Events(ctx, 0, testSig, "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(evs, []LogEvent{EvBridgeConnected, EvTurnEnded}) {
		t.Fatalf("events %v", evs)
	}
	// A partial line is not consumed until it is complete.
	l.append("[x] [quit] refused by ")
	evs, off2, err := lg.Events(ctx, off, testSig, "")
	if err != nil || len(evs) != 0 || off2 != off {
		t.Fatalf("partial: %v %d %v", evs, off2, err)
	}
	l.append("interceptor\n", stamp("[focus] id="+sessB), stamp("[turn] done id="+sessB))
	evs, off3, err := lg.Events(ctx, off2, testSig, ids.SessionID(sessA))
	if err != nil {
		t.Fatal(err)
	}
	// Session-bound events of another session do not count.
	if !slices.Equal(evs, []LogEvent{EvQuitVetoed}) {
		t.Fatalf("events %v", evs)
	}
	evs, _, err = lg.Events(ctx, off2, testSig, ids.SessionID(sessB))
	if err != nil || !slices.Equal(evs, []LogEvent{EvQuitVetoed, EvFocusedSession, EvTurnEnded}) {
		t.Fatalf("events %v %v", evs, err)
	}
	size, _ := lg.Size()
	if off3 != size {
		t.Fatalf("offset %d, size %d", off3, size)
	}
	if _, _, err := lg.Events(ctx, 0, testSig, "local_bad"); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("got %v", err)
	}
}

func TestEventsSkipsLongLinesAndRotation(t *testing.T) {
	ctx := t.Context()
	l := newTestLog(t)
	l.append(stamp("[bridge] peer up"+strings.Repeat("x", maxLineLen+10)), stamp("[bridge] peer up"))
	evs, off, err := l.log().Events(ctx, 0, testSig, "")
	if err != nil || len(evs) != 1 {
		t.Fatalf("events %v %v", evs, err)
	}
	// An offset past the end means the log was rotated: read from 0.
	evs, _, err = l.log().Events(ctx, off*10, testSig, "")
	if err != nil || len(evs) != 1 {
		t.Fatalf("rotated: %v %v", evs, err)
	}
}

func TestEventsDoNotLeakLines(t *testing.T) {
	l := newTestLog(t)
	secret := "token-should-never-appear"
	l.append(stamp(secret))
	_, _, err := l.log().Events(t.Context(), 0, compat.Signals{BridgeConnected: `(`}, "")
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("got %v", err)
	}
}

func TestBusy(t *testing.T) {
	ctx := t.Context()
	l := newTestLog(t)
	lg := l.log()
	l.append(
		stamp("[turn] begin id="+sessC), // before the last app start: ignored
		stamp("[boot] ready v=1.0"),
		stamp("[turn] begin id="+sessA),
		stamp("[turn] begin id=local_not-a-valid-id"),
	)
	got, err := lg.Busy(ctx, 0, testSig)
	if err != nil || !slices.Equal(got, []ids.SessionID{sessA}) {
		t.Fatalf("step 1: %v %v", got, err)
	}
	l.append(stamp("[turn] begin id="+sessB), stamp("[turn] done id="+sessA))
	got, err = lg.Busy(ctx, 0, testSig)
	if err != nil || !slices.Equal(got, []ids.SessionID{sessB}) {
		t.Fatalf("step 2: %v %v", got, err)
	}
	mark, _ := lg.Size()
	l.append(stamp("[turn] begin id="+sessA), stamp("[turn] done id="+sessB), stamp("[turn] begin id="+sessC))
	got, err = lg.Busy(ctx, 0, testSig)
	if err != nil || !slices.Equal(got, []ids.SessionID{sessA, sessC}) {
		t.Fatalf("step 3: %v %v", got, err)
	}
	// From a later offset, earlier turn starts are not seen.
	l.append(stamp("[turn] done id=" + sessC))
	got, err = lg.Busy(ctx, mark, testSig)
	if err != nil || !slices.Equal(got, []ids.SessionID{sessA}) {
		t.Fatalf("from mark: %v %v", got, err)
	}
	// A new app start clears everything before it.
	l.append(stamp("[boot] ready v=1.1"))
	got, err = lg.Busy(ctx, 0, testSig)
	if err != nil || len(got) != 0 {
		t.Fatalf("after restart: %v %v", got, err)
	}
}

func TestBusyNeedsPatterns(t *testing.T) {
	l := newTestLog(t)
	for _, sig := range []compat.Signals{
		{TurnEnded: testSig.TurnEnded},
		{TurnStarted: testSig.TurnStarted},
		{TurnStarted: `begin`, TurnEnded: testSig.TurnEnded},
	} {
		if _, err := l.log().Busy(t.Context(), 0, sig); !errors.Is(err, ErrSignalUnknown) {
			t.Fatalf("%+v: got %v", sig, err)
		}
	}
}

func TestActiveOrg(t *testing.T) {
	ctx := t.Context()
	l := newTestLog(t)
	lg := l.log()
	if _, _, err := lg.ActiveOrg(ctx, compat.Signals{}, acctP); !errors.Is(err, ErrSignalUnknown) {
		t.Fatalf("got %v", err)
	}
	if _, _, err := lg.ActiveOrg(ctx, compat.Signals{ActiveOrg: `org=`}, acctP); !errors.Is(err, ErrSignalUnknown) {
		t.Fatalf("no group: %v", err)
	}
	if _, found, err := lg.ActiveOrg(ctx, testSig, acctP); err != nil || found {
		t.Fatalf("empty log: %v %v", found, err)
	}
	l.append(stamp("[acct] account="+acctP+" org="+orgX+" active"), stamp("[bridge] peer up"))
	org, found, err := lg.ActiveOrg(ctx, testSig, acctP)
	if err != nil || !found || org != orgX {
		t.Fatalf("step 1: %q %v %v", org, found, err)
	}
	l.append(stamp("[acct] account=" + acctP + " org=" + orgY + " active"))
	org, found, err = lg.ActiveOrg(ctx, testSig, acctP)
	if err != nil || !found || org != orgY {
		t.Fatalf("step 2: %q %v %v", org, found, err)
	}
	// main.log is shared: the last line is another account's, so the
	// active folder's org is unknown (not that account's org).
	l.append(stamp("[acct] account=" + acctQ + " org=" + orgX + " active"))
	if org, found, err = lg.ActiveOrg(ctx, testSig, acctP); err != nil || found {
		t.Fatalf("another account's line: %q %v %v", org, found, err)
	}
	if org, found, err = lg.ActiveOrg(ctx, testSig, acctQ); err != nil || !found || org != orgX {
		t.Fatalf("its own account: %q %v %v", org, found, err)
	}
	// A pattern without an account group cannot be tied to an account.
	noAcct := compat.Signals{ActiveOrg: `\[acct\] account=[^ ]+ org=(?P<org>[^ ]+) active`}
	if _, found, err = lg.ActiveOrg(ctx, noAcct, acctQ); err != nil || found {
		t.Fatalf("no account group: %v %v", found, err)
	}
	l.append(stamp("[acct] account=" + acctP + " org=NOT-A-UUID active"))
	if org, found, err = lg.ActiveOrg(ctx, testSig, acctP); err != nil || found {
		t.Fatalf("invalid last org: %q %v %v", org, found, err)
	}
}
