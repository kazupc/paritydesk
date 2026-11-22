package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/ids"
)

const (
	// maxLineLen bounds one main.log line; longer lines are skipped.
	maxLineLen = 64 << 10
	// tailWindow bounds Busy and ActiveOrg scans to the end of the log.
	tailWindow = 8 << 20
	// sessionPlaceholder is replaced in session patterns (compat.Signals).
	sessionPlaceholder = "{session}"
	// anySession matches any local session id in a pattern.
	anySession = `local_` + ids.UUIDPattern
)

// fileLog reads main.log from byte offsets. Lines are only matched; they
// are never returned, logged or placed in errors.
type fileLog struct{ path string }

// Size returns the log's size; a missing log has size 0.
func (l fileLog) Size() (int64, error) {
	fi, err := os.Stat(l.path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("app: main.log: %w", err)
	}
	return fi.Size(), nil
}

// sessionPattern is a compiled signal that may name a session.
type sessionPattern struct {
	re    *regexp.Regexp
	fixed ids.SessionID // set when {session} was replaced by this id
	group int           // index of the session group, or -1
	want  ids.SessionID // when set, only this session matches
}

// compileSignal compiles a manifest pattern. {session} becomes the quoted
// want, or a session group when want is empty.
func compileSignal(pat string, want ids.SessionID) (*sessionPattern, error) {
	p := &sessionPattern{group: -1, want: want}
	if strings.Contains(pat, sessionPlaceholder) {
		repl := `(?P<session>` + anySession + `)`
		if want != "" {
			repl = regexp.QuoteMeta(string(want))
			p.fixed = want
		}
		pat = strings.ReplaceAll(pat, sessionPlaceholder, repl)
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid pattern", ErrSignalUnknown)
	}
	p.re = re
	if p.fixed == "" {
		p.group = re.SubexpIndex("session")
	}
	return p, nil
}

// namesSession reports whether matches carry a session id.
func (p *sessionPattern) namesSession() bool { return p.fixed != "" || p.group >= 0 }

// match reports whether line matches and the session it names (empty for
// patterns without a session). Lines naming an invalid id, or another
// session than want, do not match.
func (p *sessionPattern) match(line []byte) (ids.SessionID, bool) {
	if p.fixed != "" {
		return p.fixed, p.re.Match(line)
	}
	if p.group < 0 {
		return "", p.re.Match(line)
	}
	m := p.re.FindSubmatch(line)
	if m == nil {
		return "", false
	}
	id, err := ids.ParseSession(string(m[p.group]))
	if err != nil || (p.want != "" && id != p.want) {
		return "", false
	}
	return id, true
}

// eventPatterns compiles the non-empty event signals in LogEvent order.
func eventPatterns(sig compat.Signals, session ids.SessionID) ([]LogEvent, []*sessionPattern, error) {
	all := []struct {
		ev  LogEvent
		pat string
	}{
		{EvQuitVetoed, sig.QuitVetoed},
		{EvTurnEnded, sig.TurnEnded},
		{EvFocusedSession, sig.FocusedSession},
		{EvBridgeConnected, sig.BridgeConnected},
		{EvPairingDisabled, sig.PairingDisabled},
		{EvUncleanExit, sig.UncleanExit},
		{EvLoginRequired, sig.LoginRequired},
		{EvNativeMulti, sig.NativeMultiAccount},
	}
	var (
		evs  []LogEvent
		pats []*sessionPattern
	)
	for _, s := range all {
		if s.pat == "" {
			continue
		}
		p, err := compileSignal(s.pat, session)
		if err != nil {
			return nil, nil, fmt.Errorf("app: signal %s: %w", s.ev, err)
		}
		evs, pats = append(evs, s.ev), append(pats, p)
	}
	return evs, pats, nil
}

// Events returns, in line order, the events recognized at or after from,
// and the offset after the last complete line. A session-bound event
// (turn end, focused session) counts only for session when it is given.
// A log shorter than from was rotated and is read from the start.
func (l fileLog) Events(ctx context.Context, from int64, sig compat.Signals, session ids.SessionID) ([]LogEvent, int64, error) {
	if session != "" {
		if _, err := ids.ParseSession(string(session)); err != nil {
			return nil, from, ErrInvalidSession
		}
	}
	kinds, pats, err := eventPatterns(sig, session)
	if err != nil {
		return nil, from, err
	}
	var out []LogEvent
	next, err := l.scan(ctx, from, false, func(line []byte) {
		for i, p := range pats {
			if _, ok := p.match(line); ok {
				out = append(out, kinds[i])
			}
		}
	})
	return out, next, err
}

// Busy returns the sessions with a TurnStarted line and no later TurnEnded
// line, scanning from the later of from and the last AppStarted line, and
// at most the last 8 MiB.
func (l fileLog) Busy(ctx context.Context, from int64, sig compat.Signals) ([]ids.SessionID, error) {
	if sig.TurnStarted == "" || sig.TurnEnded == "" {
		return nil, ErrSignalUnknown
	}
	started, err := compileSignal(sig.TurnStarted, "")
	if err != nil {
		return nil, fmt.Errorf("app: turn start: %w", err)
	}
	ended, err := compileSignal(sig.TurnEnded, "")
	if err != nil {
		return nil, fmt.Errorf("app: turn end: %w", err)
	}
	if !started.namesSession() || !ended.namesSession() {
		return nil, fmt.Errorf("%w: turn pattern names no session", ErrSignalUnknown)
	}
	var appStarted *regexp.Regexp
	if sig.AppStarted != "" {
		if appStarted, err = regexp.Compile(sig.AppStarted); err != nil {
			return nil, fmt.Errorf("app: app start: %w: invalid pattern", ErrSignalUnknown)
		}
	}
	busy := map[ids.SessionID]bool{}
	_, err = l.scan(ctx, from, true, func(line []byte) {
		switch {
		case appStarted != nil && appStarted.Match(line):
			clear(busy)
		default:
			if id, ok := ended.match(line); ok {
				delete(busy, id)
			} else if id, ok := started.match(line); ok {
				busy[id] = true
			}
		}
	})
	if err != nil {
		return nil, err
	}
	out := make([]ids.SessionID, 0, len(busy))
	for id := range busy {
		out = append(out, id)
	}
	slices.Sort(out)
	return out, nil
}

// ActiveOrg returns the org named by the last ActiveOrg line in the last
// 8 MiB, when that line names account. main.log is shared by every
// account, so found is false when no line matched, the last one names
// another account or no valid UUID, or the pattern has no account group
// to check.
func (l fileLog) ActiveOrg(ctx context.Context, sig compat.Signals, account ids.AccountID) (ids.OrgID, bool, error) {
	if sig.ActiveOrg == "" {
		return "", false, ErrSignalUnknown
	}
	re, err := regexp.Compile(sig.ActiveOrg)
	if err != nil {
		return "", false, fmt.Errorf("app: active org: %w: invalid pattern", ErrSignalUnknown)
	}
	group := re.SubexpIndex("org")
	if group < 0 {
		return "", false, fmt.Errorf("app: active org: %w: pattern has no org group", ErrSignalUnknown)
	}
	acctGroup := re.SubexpIndex("account")
	last, lastAcct, seen := "", "", false
	_, err = l.scan(ctx, 0, true, func(line []byte) {
		if m := re.FindSubmatch(line); m != nil {
			last, seen = string(m[group]), true
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

// scan calls fn for every complete line at or after from and returns the
// offset after the last complete line. With tail, the scan starts no
// earlier than tailWindow bytes before the end (dropping the partial first
// line). Lines longer than maxLineLen are skipped. A missing log has no
// lines.
func (l fileLog) scan(ctx context.Context, from int64, tail bool, fn func(line []byte)) (int64, error) {
	f, err := os.Open(l.path)
	if errors.Is(err, fs.ErrNotExist) {
		return from, nil
	}
	if err != nil {
		return from, fmt.Errorf("app: main.log: %w", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return from, fmt.Errorf("app: main.log: %w", err)
	}
	size := fi.Size()
	if from < 0 || from > size {
		from = 0 // rotated or truncated
	}
	start, skipFirst := from, false
	if tail && size-tailWindow > start {
		start, skipFirst = size-tailWindow, true
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return from, fmt.Errorf("app: main.log: %w", err)
	}
	return readLines(ctx, bufio.NewReaderSize(f, maxLineLen), start, skipFirst, fn)
}

// readLines feeds complete lines to fn, skipping overlong lines (and the
// first line when skipFirst), and returns the offset after the last
// complete line.
func readLines(ctx context.Context, r *bufio.Reader, off int64, skipFirst bool, fn func([]byte)) (int64, error) {
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
			// An incomplete last line is read again on the next call.
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
