package app

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/ids"
)

// HoldState is what the last keep-awake hold line of the app's run said.
type HoldState string

// Hold states.
const (
	HoldUnknown HoldState = "unknown" // no hold line in the run, or a release for another reason
	HoldHeld    HoldState = "held"    // the last hold line took the hold
	HoldIdle    HoldState = "idle"    // the last hold line released it for an idle reason
)

const (
	// DefaultBusyStaleAfter is used when BusyOptions.StaleAfter is 0.
	DefaultBusyStaleAfter = 6 * time.Hour
	// holdGrace is the app's release delay of the keep-awake hold.
	holdGrace = 30 * time.Second
	// holdSlack covers whole-second timestamps on both lines.
	holdSlack = time.Second
	// holdWait: a counted turn takes the hold within seconds of its start,
	// so a Code start older than this with the hold idle is not running.
	holdWait = 2 * time.Minute
)

// BusyOptions are the inputs of one busy report.
type BusyOptions struct {
	// Since is the start time of the app's earliest running process.
	// Lines older than Since-1s belong to an earlier run and are ignored.
	// Zero: unknown; the last app_started line marks the run.
	Since      time.Time
	Now        time.Time
	StaleAfter time.Duration
	// HoldTrusted is true when none of the manifest's work_hold_prefs is
	// false in the active folder's preferences.
	HoldTrusted bool
	// Counted reports whether the keep-awake hold counts a session: true
	// for a Code session. nil means no session is counted, so the hold
	// evidence (R5′, R7) is not used at all.
	Counted func(ids.SessionID) bool
}

// BusySession is one session with an open turn.
type BusySession struct {
	ID        ids.SessionID
	StartedAt time.Time // time of the start line; zero when unknown
}

// BusyReport is the outcome of a busy scan.
type BusyReport struct {
	Working   []BusySession // sorted by ID
	Stale     []BusySession // started > StaleAfter ago, no end seen; sorted by ID
	Hold      HoldState
	Truncated bool // the window did not reach Since: older turns are not seen
}

// BusySupport says which parts of busy detection the manifest enables.
type BusySupport struct {
	Starts, Ends bool // turn_started and turn_ended set, valid, naming a session
	Clears       int  // usable turn_cleared patterns
	Hold         bool // work_held and work_released set
}

// busySignals are the compiled busy patterns.
type busySignals struct {
	started, ended *signal
	cleared        []*signal
	held, released *signal
	reason         int // group index of released's reason
	idle           map[string]bool
	uncounted      *signal
	appStarted     *signal
}

// compileBusy compiles the busy signals.
func compileBusy(sig compat.Signals) (*busySignals, BusySupport, error) {
	var sup BusySupport
	if sig.TurnStarted == "" || sig.TurnEnded == "" {
		return nil, sup, ErrSignalUnknown
	}
	b := &busySignals{reason: -1, idle: map[string]bool{}}
	var err error
	if b.started, err = compileSignal(sig.TurnStarted, ""); err != nil {
		return nil, sup, fmt.Errorf("app: turn start: %w", err)
	}
	if b.ended, err = compileSignal(sig.TurnEnded, ""); err != nil {
		return nil, sup, fmt.Errorf("app: turn end: %w", err)
	}
	if !b.started.namesSession() || !b.ended.namesSession() {
		return nil, sup, fmt.Errorf("%w: turn pattern names no session", ErrSignalUnknown)
	}
	sup.Starts, sup.Ends = true, true
	for _, p := range sig.TurnCleared {
		c, err := compileSignal(p, "")
		if err != nil || !c.namesSession() {
			continue
		}
		b.cleared = append(b.cleared, c)
	}
	sup.Clears = len(b.cleared)
	if sig.WorkHeld != "" && sig.WorkReleased != "" {
		held, herr := compileSignal(sig.WorkHeld, "")
		rel, rerr := compileSignal(sig.WorkReleased, "")
		if herr == nil && rerr == nil && rel.re.SubexpIndex("reason") >= 0 {
			b.held, b.released, b.reason = held, rel, rel.re.SubexpIndex("reason")
			for _, r := range sig.WorkIdleReasons {
				b.idle[r] = true
			}
			sup.Hold = true
			if u, err := compileOptional(sig.WorkUncounted); err == nil && u != nil && u.namesSession() {
				b.uncounted = u
			}
		}
	}
	if a, err := compileOptional(sig.AppStarted); err == nil {
		b.appStarted = a
	}
	return b, sup, nil
}

// CheckBusySignals compiles the busy signals. It returns ErrSignalUnknown
// (wrapped) when turn_started or turn_ended is empty, does not compile,
// or names no session. Used by doctor and NewBusyTracker.
func CheckBusySignals(sig compat.Signals) (BusySupport, error) {
	_, sup, err := compileBusy(sig)
	return sup, err
}

// openTurn is a session whose turn started and has not ended.
type openTurn struct {
	at        time.Time // start line time; zero when unknown
	uncounted bool      // work_uncounted named the session after this start
}

// busyMachine follows the record lines of one app run.
type busyMachine struct {
	sig    *busySignals
	since  time.Time
	open   map[ids.SessionID]openTurn
	hold   HoldState
	idleAt time.Time // time of the last idle release; zero: none
	last   time.Time // time of the last record line with a known time
}

func newBusyMachine(sig *busySignals, since time.Time) *busyMachine {
	return &busyMachine{sig: sig, since: since, open: map[ids.SessionID]openTurn{}, hold: HoldUnknown}
}

// line applies one record line (rules R0 to R8; the first that matches
// wins).
func (m *busyMachine) line(t time.Time, msg []byte) {
	if !t.IsZero() {
		m.last = t
	}
	// R0: a line of an earlier run of the app.
	if !m.since.IsZero() && !m.last.IsZero() && m.last.Before(m.since.Add(-time.Second)) {
		return
	}
	s := m.sig
	// R1: an app start marks the run when its start time is unknown.
	if m.since.IsZero() && s.appStarted.matches(msg) {
		clear(m.open)
		m.hold, m.idleAt = HoldUnknown, time.Time{}
		return
	}
	// R2: an end.
	if id, ok := s.ended.match(msg); ok {
		delete(m.open, id)
		return
	}
	for _, c := range s.cleared {
		if id, ok := c.match(msg); ok {
			delete(m.open, id)
			return
		}
	}
	// R3: a start (set semantics: the last event wins).
	if id, ok := s.started.match(msg); ok {
		m.open[id] = openTurn{at: m.last}
		return
	}
	if s.held == nil {
		return
	}
	// R4: hold taken.
	if s.held.matches(msg) {
		m.hold = HoldHeld
		return
	}
	// R5: hold released.
	if sub := s.released.find(msg); sub != nil {
		if s.idle[string(sub[s.reason])] {
			m.hold, m.idleAt = HoldIdle, m.last
		} else {
			m.hold = HoldUnknown
		}
		return
	}
	// R8: the hold no longer counts an open session.
	if id, ok := s.uncounted.match(msg); ok {
		if o, open := m.open[id]; open {
			o.uncounted = true
			m.open[id] = o
		}
	}
}

// report evaluates the open turns for opts (R5′, R6, R7).
func (m *busyMachine) report(opts BusyOptions) BusyReport {
	stale := opts.StaleAfter
	if stale <= 0 {
		stale = DefaultBusyStaleAfter
	}
	rep := BusyReport{Hold: m.hold}
	for id, o := range m.open {
		counted := opts.Counted != nil && opts.Counted(id) && !o.uncounted
		age := max(0, opts.Now.Sub(o.at))
		bs := BusySession{ID: id, StartedAt: o.at}
		switch {
		case counted && !m.idleAt.IsZero() && !o.at.IsZero() && !o.at.After(m.idleAt.Add(-holdGrace-holdSlack)):
			// R5′: an idle release counted no turn although this one had
			// started more than the grace period before it.
		case !o.at.IsZero() && age > stale:
			rep.Stale = append(rep.Stale, bs) // R6
		case counted && opts.HoldTrusted && m.hold == HoldIdle && !o.at.IsZero() && age > holdWait:
			// R7: the hold never counted this turn.
		default:
			rep.Working = append(rep.Working, bs)
		}
	}
	byID := func(a, b BusySession) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	}
	slices.SortFunc(rep.Working, byID)
	slices.SortFunc(rep.Stale, byID)
	return rep
}

// BusyTracker follows main.log for busy detection: a window read on the
// first Update, then only new record lines. Not safe for concurrent use.
type BusyTracker struct {
	log       fileLog
	sig       *busySignals
	since     time.Time
	m         *busyMachine
	cur       LogCursor
	primed    bool
	truncated bool
}

// NewBusyTracker compiles sig (CheckBusySignals); ErrSignalUnknown when
// busy detection is not available for this manifest.
func (l fileLog) NewBusyTracker(sig compat.Signals) (*BusyTracker, error) {
	b, _, err := compileBusy(sig)
	if err != nil {
		return nil, err
	}
	return &BusyTracker{log: l, sig: b}, nil
}

// Update reads what is new and returns the report for opts. A different
// opts.Since than the tracker's (the app restarted), or a gap in the
// cursor read, resets the machine and re-reads the window.
func (t *BusyTracker) Update(ctx context.Context, opts BusyOptions) (BusyReport, error) {
	if !t.primed || !opts.Since.Equal(t.since) {
		if err := t.prime(ctx, opts.Since); err != nil {
			return BusyReport{}, err
		}
	} else {
		next, gap, err := t.log.readFrom(ctx, t.cur, t.m.line)
		if err != nil {
			return BusyReport{}, err
		}
		if gap {
			if err := t.prime(ctx, opts.Since); err != nil {
				return BusyReport{}, err
			}
		} else {
			t.cur = next
		}
	}
	rep := t.m.report(opts)
	rep.Truncated = t.truncated
	return rep, nil
}

// prime resets the machine and reads the window of the run since since.
func (t *BusyTracker) prime(ctx context.Context, since time.Time) error {
	t.m = newBusyMachine(t.sig, since)
	t.since = since
	end, truncated, err := t.log.readWindow(ctx, windowOpts{Since: since}, t.m.line)
	if err != nil {
		t.primed = false
		return err
	}
	t.cur, t.truncated, t.primed = end, truncated, true
	return nil
}
