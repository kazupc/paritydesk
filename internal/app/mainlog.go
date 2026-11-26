package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
)

const (
	// maxLineLen bounds one main.log line; longer lines are skipped.
	maxLineLen = 64 << 10
	// outOfOrder is how far back in time a record line may go, within one
	// file, and still count for account changes and the active org. A
	// real log goes back only by a clock change; 65 min lets a DST
	// fall-back hour through.
	outOfOrder = 65 * time.Minute
)

// fileLog reads the app's log files (main.log and the rotated
// main1.log … main4.log). Lines are only matched; they are never
// returned, logged or placed in errors.
type fileLog struct {
	dir string         // the logs directory (main.log and main1..4.log)
	loc *time.Location // nil: time.Local
	// test hooks, nil in production
	afterList func()      // called between listing and opening (race tests)
	onRead    func(n int) // bytes read, for budget assertions
	budget    int64       // when > 0, replaces windowBytes and catchUpMax
}

// mainPath is the path of main.log.
func (l fileLog) mainPath() string { return filepath.Join(l.dir, logNames[0]) }

// Size returns main.log's size; a missing log has size 0.
func (l fileLog) Size() (int64, error) {
	fi, err := os.Stat(l.mainPath())
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("app: main.log: %w", err)
	}
	return fi.Size(), nil
}

// Cursor returns the inode and size of main.log, read from one open
// descriptor; a missing log gives the zero cursor.
func (l fileLog) Cursor() (LogCursor, error) {
	f, err := os.Open(l.mainPath())
	if errors.Is(err, fs.ErrNotExist) {
		return LogCursor{}, nil
	}
	if err != nil {
		return LogCursor{}, fmt.Errorf("app: main.log: %w", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return LogCursor{}, fmt.Errorf("app: main.log: %w", err)
	}
	return LogCursor{Ino: fileIno(fi), Off: fi.Size()}, nil
}

// signal is a compiled manifest pattern, anchored at the message start,
// with its prefilter literal.
type signal struct {
	re    *regexp.Regexp
	lit   []byte
	fixed ids.SessionID // set when {session} was replaced by this id
	group int           // index of the session group, or -1
	want  ids.SessionID // when set, only this session matches
}

// compileSignal compiles a manifest pattern anchored at the start of the
// message. {session} becomes the quoted want, or a session group when
// want is empty. The prefilter literal is computed from the manifest
// text with the group (compat.RequiredLiteral).
func compileSignal(pat string, want ids.SessionID) (*signal, error) {
	p := &signal{group: -1, want: want, lit: []byte(compat.RequiredLiteral(pat))}
	if strings.Contains(pat, compat.SessionPlaceholder) {
		repl := compat.SessionGroup
		if want != "" {
			repl = regexp.QuoteMeta(string(want))
			p.fixed = want
		}
		pat = strings.ReplaceAll(pat, compat.SessionPlaceholder, repl)
	}
	re, err := regexp.Compile("^(?:" + pat + ")")
	if err != nil {
		return nil, fmt.Errorf("%w: invalid pattern", ErrSignalUnknown)
	}
	p.re = re
	if p.fixed == "" {
		p.group = re.SubexpIndex("session")
	}
	if len(p.lit) == 0 {
		p.lit = nil
	}
	return p, nil
}

// compileOptional compiles pat, or returns nil for an empty pattern.
func compileOptional(pat string) (*signal, error) {
	if pat == "" {
		return nil, nil
	}
	return compileSignal(pat, "")
}

// namesSession reports whether matches carry a session id.
func (p *signal) namesSession() bool { return p.fixed != "" || p.group >= 0 }

// hit is the prefilter: false when msg cannot match.
func (p *signal) hit(msg []byte) bool { return p.lit == nil || bytes.Contains(msg, p.lit) }

// find returns the submatches of msg, or nil.
func (p *signal) find(msg []byte) [][]byte {
	if p == nil || !p.hit(msg) {
		return nil
	}
	return p.re.FindSubmatch(msg)
}

// matches reports whether msg matches (for patterns without a session).
func (p *signal) matches(msg []byte) bool {
	return p != nil && p.hit(msg) && p.re.Match(msg)
}

// match reports whether msg matches and the session it names (empty for
// patterns without a session). Messages naming an invalid id, or another
// session than want, do not match.
func (p *signal) match(msg []byte) (ids.SessionID, bool) {
	if p == nil || !p.hit(msg) {
		return "", false
	}
	if p.fixed != "" {
		return p.fixed, p.re.Match(msg)
	}
	if p.group < 0 {
		return "", p.re.Match(msg)
	}
	m := p.re.FindSubmatch(msg)
	if m == nil {
		return "", false
	}
	id, err := ids.ParseSession(string(m[p.group]))
	if err != nil || (p.want != "" && id != p.want) {
		return "", false
	}
	return id, true
}

// eventPattern maps a compiled signal to the event it reports. With a
// state group, the group's value picks the event from states.
type eventPattern struct {
	ev     LogEvent
	sig    *signal
	group  int
	states map[string]LogEvent
}

// eventPatterns compiles the non-empty event signals in LogEvent order.
func eventPatterns(sig compat.Signals, session ids.SessionID) ([]eventPattern, error) {
	type entry struct {
		ev  LogEvent
		pat string
	}
	all := []entry{
		{EvQuitVetoed, sig.QuitVetoed},
		{EvTurnEnded, sig.TurnEnded},
	}
	for _, p := range sig.TurnCleared {
		all = append(all, entry{EvTurnCleared, p})
	}
	all = append(all,
		entry{EvAppStarted, sig.AppStarted},
		entry{EvFocusedSession, sig.FocusedSession},
		entry{EvBridgeConnected, sig.BridgeConnected},
		entry{EvPairingDisabled, sig.PairingDisabled},
		entry{EvPairingEstablished, sig.PairingState},
		entry{EvUncleanExit, sig.UncleanExit},
		entry{EvLoginRequired, sig.LoginRequired},
	)
	var out []eventPattern
	for _, s := range all {
		if s.pat == "" {
			continue
		}
		p, err := compileSignal(s.pat, session)
		if err != nil {
			return nil, fmt.Errorf("app: signal %s: %w", s.ev, err)
		}
		ep := eventPattern{ev: s.ev, sig: p, group: -1}
		if s.ev == EvPairingEstablished {
			ep.group = p.re.SubexpIndex("state")
			if ep.group < 0 {
				return nil, fmt.Errorf("app: signal pairing state: %w: pattern has no state group", ErrSignalUnknown)
			}
			ep.states = map[string]LogEvent{"established": EvPairingEstablished, "closed": EvPairingClosed}
		}
		out = append(out, ep)
	}
	return out, nil
}

// eventOf returns the event msg reports for one pattern.
func (e eventPattern) eventOf(msg []byte) (LogEvent, bool) {
	if e.group < 0 {
		_, ok := e.sig.match(msg)
		return e.ev, ok
	}
	m := e.sig.find(msg)
	if m == nil {
		return "", false
	}
	ev, ok := e.states[string(m[e.group])]
	return ev, ok
}

// EventsAt returns, in line order, the events recognized on the record
// lines after from (across rotation), and the cursor after the last
// complete line of main.log. A session-bound event (turn end or clear,
// focused session) counts only for session when it is given.
func (l fileLog) EventsAt(ctx context.Context, from LogCursor, sig compat.Signals, session ids.SessionID) ([]LogEvent, LogCursor, error) {
	if session != "" {
		if _, err := ids.ParseSession(string(session)); err != nil {
			return nil, from, ErrInvalidSession
		}
	}
	pats, err := eventPatterns(sig, session)
	if err != nil {
		return nil, from, err
	}
	var out []LogEvent
	next, _, err := l.readFrom(ctx, from, func(_ time.Time, msg []byte) {
		for _, p := range pats {
			if ev, ok := p.eventOf(msg); ok {
				out = append(out, ev)
			}
		}
	})
	if err != nil {
		return nil, from, err
	}
	return out, next, nil
}

// Events is EventsAt with LogCursor{Off: from} (main.log by assumption);
// it returns the offset in main.log after the last complete line.
func (l fileLog) Events(ctx context.Context, from int64, sig compat.Signals, session ids.SessionID) ([]LogEvent, int64, error) {
	evs, next, err := l.EventsAt(ctx, LogCursor{Off: from}, sig, session)
	if err != nil {
		return nil, from, err
	}
	return evs, next.Off, nil
}

// orderGuard drops record lines that go back in time by more than
// outOfOrder within one file.
type orderGuard struct{ latest time.Time }

// reset starts a new file.
func (g *orderGuard) reset() { g.latest = time.Time{} }

// ok reports whether a line with time t counts, and remembers t.
func (g *orderGuard) ok(t time.Time) bool {
	if t.IsZero() {
		return true
	}
	if !g.latest.IsZero() && t.Before(g.latest.Add(-outOfOrder)) {
		return false
	}
	if t.After(g.latest) {
		g.latest = t
	}
	return true
}

// activeOrgSignal compiles active_org and returns its group indexes.
func activeOrgSignal(sig compat.Signals) (s *signal, org, acct int, err error) {
	if sig.ActiveOrg == "" {
		return nil, 0, 0, ErrSignalUnknown
	}
	s, err = compileSignal(sig.ActiveOrg, "")
	if err != nil {
		return nil, 0, 0, fmt.Errorf("app: active org: %w", err)
	}
	org = s.re.SubexpIndex("org")
	if org < 0 {
		return nil, 0, 0, fmt.Errorf("app: active org: %w: pattern has no org group", ErrSignalUnknown)
	}
	return s, org, s.re.SubexpIndex("account"), nil
}

// ActiveOrg returns the org named by the last active_org line of the
// window when that line names account. With since non-zero (the start of
// the running app) only lines of the current run count, across main1..4;
// with since zero, the last line of the newest windowBytes. main.log is
// shared by every account, so found is false when no line matched, the
// last one names another account or no valid UUID, or the pattern has no
// account group to check.
func (l fileLog) ActiveOrg(ctx context.Context, sig compat.Signals, account ids.AccountID, since time.Time) (ids.OrgID, bool, error) {
	re, group, acctGroup, err := activeOrgSignal(sig)
	if err != nil {
		return "", false, err
	}
	var g orderGuard
	last, lastAcct, seen := "", "", false
	_, _, err = l.readWindow(ctx, windowOpts{Since: since, OnFile: g.reset}, func(t time.Time, msg []byte) {
		if !g.ok(t) {
			return
		}
		if m := re.find(msg); m != nil {
			last, seen = string(m[group]), true
			lastAcct = ""
			if acctGroup >= 0 {
				lastAcct = string(m[acctGroup])
			}
		}
	})
	if err != nil || !seen {
		return "", false, err
	}
	if acctGroup < 0 || account == "" || lastAcct != string(account) {
		return "", false, nil
	}
	org, err := ids.ParseOrg(last)
	if err != nil {
		return "", false, nil
	}
	return org, true, nil
}

// LastReplica returns the account and org of the newest active_org line
// at or after since (since zero: the newest windowBytes of the log set),
// read with readWindow and the out-of-order guard. known is false when no
// such line exists, or when a record line matching account_changed or any
// account_changed_hint item lies after that line in the window. Ids are
// validated (ids.ParseAccount, ids.ParseOrg).
func (l fileLog) LastReplica(ctx context.Context, sig compat.Signals, since time.Time) (model.Replica, bool, error) {
	re, group, acctGroup, err := activeOrgSignal(sig)
	if err != nil {
		return model.Replica{}, false, err
	}
	changed, err := compileOptional(sig.AccountChanged)
	if err != nil {
		return model.Replica{}, false, fmt.Errorf("app: account change: %w", err)
	}
	var hints []*signal
	for _, p := range sig.AccountChangedHint {
		h, err := compileOptional(p)
		if err != nil {
			return model.Replica{}, false, fmt.Errorf("app: account change hint: %w", err)
		}
		if h != nil {
			hints = append(hints, h)
		}
	}
	var (
		g     orderGuard
		rep   model.Replica
		known bool
	)
	_, _, err = l.readWindow(ctx, windowOpts{Since: since, OnFile: g.reset}, func(t time.Time, msg []byte) {
		if !g.ok(t) {
			return
		}
		if m := re.find(msg); m != nil {
			known = false
			if acctGroup < 0 {
				return
			}
			a, aerr := ids.ParseAccount(string(m[acctGroup]))
			o, oerr := ids.ParseOrg(string(m[group]))
			if aerr == nil && oerr == nil {
				rep, known = model.Replica{Account: a, Org: o}, true
			}
			return
		}
		if !known {
			return
		}
		if changed.matches(msg) {
			known = false
			return
		}
		for _, h := range hints {
			if h.matches(msg) {
				known = false
				return
			}
		}
	})
	if err != nil || !known {
		return model.Replica{}, false, err
	}
	return rep, true, nil
}

// readLines feeds complete lines to fn, skipping overlong lines (and the
// first line when skipFirst), and returns the offset after the last
// complete line. With final, an incomplete last line is passed too (a
// rotated file no longer grows) and counts as complete.
func readLines(ctx context.Context, r *bufio.Reader, off int64, skipFirst, final bool, fn func([]byte)) (int64, error) {
	var (
		lineStart = off
		long      bool // inside an overlong line
		lines     int
	)
	for {
		chunk, err := r.ReadSlice('\n')
		off += int64(len(chunk))
		switch {
		case err == nil:
			if !long && !skipFirst {
				fn(chunk[:len(chunk)-1])
			}
			lineStart, long, skipFirst = off, false, false
		case errors.Is(err, bufio.ErrBufferFull):
			long = true
			continue
		case errors.Is(err, io.EOF):
			if final && len(chunk) > 0 {
				if !long && !skipFirst {
					fn(chunk)
				}
				lineStart = off
			}
			// An incomplete last line of main.log is read again on the
			// next call.
			return lineStart, nil
		default:
			return lineStart, fmt.Errorf("app: main.log: %w", err)
		}
		if lines++; lines%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return lineStart, err
			}
		}
	}
}
