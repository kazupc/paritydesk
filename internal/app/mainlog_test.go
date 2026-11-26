package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/ids"
)

// testZone is the time zone of the log fixtures' local timestamps.
var testZone = time.FixedZone("T", 9*3600)

// testLog is a logs directory in a temp dir that tests append to and
// rotate like the app does.
type testLog struct {
	t   *testing.T
	dir string
	// clock is the time of the next stamped line (stampAt).
	clock time.Time
}

func newTestLog(t *testing.T) *testLog {
	return &testLog{t: t, dir: t.TempDir(), clock: time.Date(2026, 11, 26, 10, 0, 0, 0, testZone)}
}

func (l *testLog) path(name string) string { return filepath.Join(l.dir, name) }

func (l *testLog) append(lines ...string) {
	l.t.Helper()
	l.appendTo("main.log", lines...)
}

func (l *testLog) appendTo(name string, lines ...string) {
	l.t.Helper()
	f, err := os.OpenFile(l.path(name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
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

// rotate renames main.log to main1.log, shifting the others (main4.log is
// dropped), as the app's tailable file transport does.
func (l *testLog) rotate() {
	l.t.Helper()
	_ = os.Remove(l.path("main4.log"))
	for k := maxRotated - 1; k >= 1; k-- {
		if _, err := os.Stat(l.path(fmt.Sprintf("main%d.log", k))); err == nil {
			if err := os.Rename(l.path(fmt.Sprintf("main%d.log", k)), l.path(fmt.Sprintf("main%d.log", k+1))); err != nil {
				l.t.Fatal(err)
			}
		}
	}
	if _, err := os.Stat(l.path("main.log")); err == nil {
		if err := os.Rename(l.path("main.log"), l.path("main1.log")); err != nil {
			l.t.Fatal(err)
		}
	}
}

func (l *testLog) log() fileLog { return fileLog{dir: l.dir, loc: testZone} }

// stamp is a record line in the bracketed form with milliseconds.
func stamp(s string) string { return "[2026-01-01 00:00:00.000] [info] " + s + "\n" }

// stampAt is a record line in the app's local form at the log's clock,
// which then advances by step.
func (l *testLog) stampAt(step time.Duration, s string) string {
	line := l.clock.Format("2006-01-02 15:04:05") + " [info] " + s + "\n"
	l.clock = l.clock.Add(step)
	return line
}

// at is a record line at a fixed time.
func at(t time.Time, s string) string {
	return t.In(testZone).Format("2006-01-02 15:04:05") + " [info] " + s + "\n"
}

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
	if !slices.Equal(evs, []LogEvent{EvAppStarted, EvBridgeConnected, EvTurnEnded}) {
		t.Fatalf("events %v", evs)
	}
	// A partial line is not consumed until it is complete.
	l.append("[2026-01-01 00:00:01.000] [info] [quit] refused by ")
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

func TestEventsNewKinds(t *testing.T) {
	l := newTestLog(t)
	sig := testSig
	sig.TurnCleared = []string{`\[turn\] stop id={session}`}
	sig.PairingState = `\[pair\] state (?P<state>established|closed)\b`
	sig.PairingDisabled = `\[pair\] disabled for this launch`
	l.append(stamp("[turn] stop id="+sessA), stamp("[pair] state established"), stamp("[pair] state closed"),
		stamp("[pair] state broken"), stamp("[pair] disabled for this launch: x"))
	evs, _, err := l.log().Events(t.Context(), 0, sig, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []LogEvent{EvTurnCleared, EvPairingEstablished, EvPairingClosed, EvPairingDisabled}
	if !slices.Equal(evs, want) {
		t.Fatalf("events %v, want %v", evs, want)
	}
	// A turn clear of another session does not count for a session.
	evs, _, err = l.log().Events(t.Context(), 0, sig, ids.SessionID(sessB))
	if err != nil || slices.Contains(evs, EvTurnCleared) {
		t.Fatalf("events %v %v", evs, err)
	}
	bad := sig
	bad.PairingState = `\[pair\] state (established|closed)`
	if _, _, err := l.log().Events(t.Context(), 0, bad, ""); !errors.Is(err, ErrSignalUnknown) {
		t.Fatalf("pairing state without its group: %v", err)
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

func TestCursor(t *testing.T) {
	l := newTestLog(t)
	c, err := l.log().Cursor()
	if err != nil || c != (LogCursor{}) {
		t.Fatalf("missing log: %+v %v", c, err)
	}
	l.append(stamp("[bridge] peer up"))
	c, err = l.log().Cursor()
	fi, _ := os.Stat(l.path("main.log"))
	if err != nil || c.Ino == 0 || c.Ino != fileIno(fi) || c.Off != fi.Size() {
		t.Fatalf("cursor %+v %v", c, err)
	}
}

func TestActiveOrg(t *testing.T) {
	ctx := t.Context()
	l := newTestLog(t)
	lg := l.log()
	if _, _, err := lg.ActiveOrg(ctx, compat.Signals{}, acctP, time.Time{}); !errors.Is(err, ErrSignalUnknown) {
		t.Fatalf("got %v", err)
	}
	if _, _, err := lg.ActiveOrg(ctx, compat.Signals{ActiveOrg: `\[acct\] org=`}, acctP, time.Time{}); !errors.Is(err, ErrSignalUnknown) {
		t.Fatalf("no group: %v", err)
	}
	if _, found, err := lg.ActiveOrg(ctx, testSig, acctP, time.Time{}); err != nil || found {
		t.Fatalf("empty log: %v %v", found, err)
	}
	l.append(stamp("[acct] account="+acctP+" org="+orgX+" active"), stamp("[bridge] peer up"))
	org, found, err := lg.ActiveOrg(ctx, testSig, acctP, time.Time{})
	if err != nil || !found || org != orgX {
		t.Fatalf("step 1: %q %v %v", org, found, err)
	}
	l.append(stamp("[acct] account=" + acctP + " org=" + orgY + " active"))
	org, found, err = lg.ActiveOrg(ctx, testSig, acctP, time.Time{})
	if err != nil || !found || org != orgY {
		t.Fatalf("step 2: %q %v %v", org, found, err)
	}
	// main.log is shared: the last line is another account's, so the
	// active folder's org is unknown (not that account's org).
	l.append(stamp("[acct] account=" + acctQ + " org=" + orgX + " active"))
	if org, found, err = lg.ActiveOrg(ctx, testSig, acctP, time.Time{}); err != nil || found {
		t.Fatalf("another account's line: %q %v %v", org, found, err)
	}
	if org, found, err = lg.ActiveOrg(ctx, testSig, acctQ, time.Time{}); err != nil || !found || org != orgX {
		t.Fatalf("its own account: %q %v %v", org, found, err)
	}
	// A pattern without an account group cannot be tied to an account.
	noAcct := compat.Signals{ActiveOrg: `\[acct\] account=[^ ]+ org=(?P<org>[^ ]+) active`}
	if _, found, err = lg.ActiveOrg(ctx, noAcct, acctQ, time.Time{}); err != nil || found {
		t.Fatalf("no account group: %v %v", found, err)
	}
	l.append(stamp("[acct] account=" + acctP + " org=NOT-A-UUID active"))
	if org, found, err = lg.ActiveOrg(ctx, testSig, acctP, time.Time{}); err != nil || found {
		t.Fatalf("invalid last org: %q %v %v", org, found, err)
	}
}

// TestActiveOrgAcrossRotation: the Initialized line of the current run is
// in main1.log after a rotation; with since it is found, and a line of an
// earlier run (before since) is not used.
func TestActiveOrgAcrossRotation(t *testing.T) {
	ctx := t.Context()
	l := newTestLog(t)
	start := time.Date(2026, 11, 26, 11, 0, 0, 0, testZone)
	l.append(at(start.Add(-time.Hour), "[acct] account="+acctP+" org="+orgY+" active"))
	l.rotate()
	l.append(at(start.Add(-time.Minute), "[boot] ready v=1"), at(start.Add(time.Second), "[acct] account="+acctP+" org="+orgX+" active"))
	l.rotate()
	l.append(at(start.Add(time.Hour), "[bridge] peer up"))
	org, found, err := l.log().ActiveOrg(ctx, testSig, acctP, start)
	if err != nil || !found || org != orgX {
		t.Fatalf("current run: %q %v %v", org, found, err)
	}
	// Only the earlier run's line exists: not used with since.
	l2 := newTestLog(t)
	l2.append(at(start.Add(-time.Hour), "[acct] account="+acctP+" org="+orgY+" active"))
	l2.rotate()
	l2.append(at(start.Add(time.Minute), "[bridge] peer up"))
	if org, found, err := l2.log().ActiveOrg(ctx, testSig, acctP, start); err != nil || found {
		t.Fatalf("earlier run: %q %v %v", org, found, err)
	}
	// Without since, the last line of the window counts.
	if org, found, err := l2.log().ActiveOrg(ctx, testSig, acctP, time.Time{}); err != nil || !found || org != orgY {
		t.Fatalf("no since: %q %v %v", org, found, err)
	}
}
