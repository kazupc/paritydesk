package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/kazupc/paritydesk/internal/app"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/render"
	"github.com/kazupc/paritydesk/internal/sources"
)

// busyProgressEvery paces the progress line of the interactive P3 wait.
const busyProgressEvery = 10 * time.Second

// BusyView is one working session in --json output.
type BusyView struct {
	Session     string `json:"session_masked"` // local_0000…
	Title       string `json:"title"`          // sanitized, ≤ 80 runes; data written by other sessions
	WorkingForS int64  `json:"working_for_s,omitempty"`
	Stale       bool   `json:"stale,omitempty"`
	// StartedKnown: the start time is known, so WorkingForS is an age
	// even when it is 0 (a start under a second ago, or a start the
	// clock puts in the future). Not in JSON.
	StartedKnown bool `json:"-"`
}

// busyInput says whose app run a busy scan looks at.
type busyInput struct {
	sig      compat.Signals
	manifest *compat.Manifest
	folder   string
	replica  model.Replica
	since    time.Time
	invoking ids.SessionID
}

// busyLog says what the log part of a busy scan saw.
type busyLog int

const (
	busyLogRead      busyLog = iota // the log was read
	busyLogNoSignals                // the manifest has no busy signals (app.ErrSignalUnknown)
	busyLogUnread                   // the log could not be read: sessions it would show are missing
)

// busyScan returns the busy report of one folder's app run: the log's
// working and stale sessions (the keep-awake evidence used for Code
// sessions only), without the invoking session, with the sessions whose
// records say they are busy added to Working, and the Code titles. A
// manifest without busy signals (app.ErrSignalUnknown) or a log that
// cannot be read gives the record-based list only; the busyLog result
// tells the two apart from a log that was read, so callers never report
// "no session working" for a log they did not read.
func (o *Orchestrator) busyScan(ctx context.Context, in busyInput) (app.BusyReport, map[ids.SessionID]string, busyLog) {
	titles, recordBusy := o.codeSessions(ctx, in.manifest, in.folder, in.replica)
	opts := app.BusyOptions{
		Since: in.since, Now: o.now(), StaleAfter: o.d.Config.App.BusyStaleAfter.Duration,
		HoldTrusted: holdTrusted(in.folder, in.sig.WorkHoldPrefs),
		// A Code session whose record is not written yet counts as not
		// counted, which only keeps it listed longer.
		Counted: func(id ids.SessionID) bool { _, ok := titles[id]; return ok },
	}
	lg := busyLogRead
	rep, err := o.d.App.BusySessions(ctx, in.sig, opts)
	switch {
	case errors.Is(err, app.ErrSignalUnknown):
		rep, lg = app.BusyReport{}, busyLogNoSignals
	case err != nil:
		rep, lg = app.BusyReport{}, busyLogUnread
	}
	drop := func(l []app.BusySession) []app.BusySession {
		var out []app.BusySession
		for _, s := range l {
			if in.invoking == "" || s.ID != in.invoking {
				out = append(out, s)
			}
		}
		return out
	}
	rep.Working, rep.Stale = drop(rep.Working), drop(rep.Stale)
	for _, id := range recordBusy {
		if id == in.invoking || hasSession(rep.Working, id) || hasSession(rep.Stale, id) {
			continue
		}
		rep.Working = append(rep.Working, app.BusySession{ID: id})
	}
	slices.SortFunc(rep.Working, func(a, b app.BusySession) int { return strings.Compare(string(a.ID), string(b.ID)) })
	return rep, titles, lg
}

func hasSession(l []app.BusySession, id ids.SessionID) bool {
	return slices.ContainsFunc(l, func(s app.BusySession) bool { return s.ID == id })
}

// codeSessions lists a folder's Code sessions: id → title, and the ids
// whose records say they are busy (the manifest's busy role).
func (o *Orchestrator) codeSessions(ctx context.Context, m *compat.Manifest, folder string, rep model.Replica) (map[ids.SessionID]string, []ids.SessionID) {
	titles := map[ids.SessionID]string{}
	if m == nil || folder == "" {
		return titles, nil
	}
	spec, _ := m.Row(model.RowSessions)
	infos, err := o.d.Sessions.ListSessions(ctx, folder, rep, spec.Expand(rep))
	if err != nil {
		return titles, nil
	}
	var busy []ids.SessionID
	for _, s := range infos {
		titles[s.ID] = s.Title
		if s.Busy {
			busy = append(busy, s.ID)
		}
	}
	return titles, busy
}

// holdTrusted reports whether no preference named in prefs is false in
// the folder's claude_desktop_config.json (a missing file or key counts
// as not false: the app's defaults are true). Only those booleans are
// looked at. A file that cannot be read or parsed is not trusted.
func holdTrusted(folder string, prefs []string) bool {
	if len(prefs) == 0 || folder == "" {
		return false
	}
	doc, err := sources.LoadFile(folder, sources.DesktopConfigFile)
	if err != nil {
		return false
	}
	if !doc.Present {
		return true
	}
	for _, p := range prefs {
		v, err := jsonx.Get(doc.Text, jsonx.Path{"preferences", p})
		switch {
		case errors.Is(err, jsonx.ErrNotFound):
			continue
		case err != nil:
			return false
		case bytes.Equal(bytes.TrimSpace(v), []byte("false")):
			return false
		}
	}
	return true
}

// busyNow returns the busy report of the leaving folder's app run and
// the confirmation lines (titles, masked ids, ages), without the
// invoking session. It returns an empty report when the app is not
// running; a manifest without busy signals leaves the record-based list
// only (the quit veto then protects working sessions, and doctor says
// so). Warnings about stale sessions, a short log and a log that could
// not be read are added once.
func (r *run) busyNow(ctx context.Context) (app.BusyReport, []i18n.Text) {
	procs, err := r.d().App.Running(ctx, r.bundle.Path)
	if err != nil || len(procs) == 0 {
		return app.BusyReport{}, nil
	}
	// The start time comes from this listing, not from the run's memo:
	// the app may have been relaunched since (an update installed during
	// this run), and a new start resets the tracker to the new run.
	since := earliestStart(procs)
	r.since, r.sinceDone = since, true
	rep, titles, lg := r.o.busyScan(ctx, busyInput{sig: r.sig, manifest: r.manifest, folder: r.sPath,
		replica: r.S.Replica, since: since, invoking: r.invoking})
	r.busyTitles = titles
	if lg == busyLogUnread && ctx.Err() == nil && !r.warnedBusyLog {
		r.warnedBusyLog = true
		r.warn(P0Preflight, i18n.Tx(i18n.WarnBusyLogUnreadable))
	}
	now := r.o.now()
	var lines []i18n.Text
	for _, s := range rep.Working {
		title := busyTitle(titles, s.ID)
		if s.StartedAt.IsZero() {
			lines = append(lines, i18n.Tx(i18n.ConfirmBusyLineNoTime, title, maskSession(s.ID)))
			continue
		}
		lines = append(lines, i18n.Tx(i18n.ConfirmBusyLine, title, maskSession(s.ID), humanAge(now.Sub(s.StartedAt))))
	}
	if len(rep.Stale) > 0 && !r.warnedStale {
		r.warnedStale = true
		r.warn(P0Preflight, i18n.Tx(i18n.WarnBusyStale, len(rep.Stale), humanAge(r.staleAfter())))
	}
	if rep.Truncated && !r.warnedTruncated {
		r.warnedTruncated = true
		r.warn(P0Preflight, i18n.Tx(i18n.WarnBusyTruncated))
	}
	return rep, lines
}

// staleAfter is app.busy_stale_after (the default when unset).
func (r *run) staleAfter() time.Duration {
	if d := r.d().Config.App.BusyStaleAfter.Duration; d > 0 {
		return d
	}
	return app.DefaultBusyStaleAfter
}

// busyTitle returns the sanitized title of a session, or the untitled
// text for a session without a Code record (a Cowork session).
func busyTitle(titles map[ids.SessionID]string, id ids.SessionID) i18n.Text {
	t, ok := titles[id]
	if !ok {
		return i18n.Tx(i18n.ConfirmBusyUntitled)
	}
	return i18n.Raw(sanitizeTitle(t))
}

// busyViews builds the --json busy list: working sessions, then stale
// ones.
func busyViews(rep app.BusyReport, titles map[ids.SessionID]string, now time.Time) []BusyView {
	var out []BusyView
	add := func(s app.BusySession, stale bool) {
		v := BusyView{Session: maskSession(s.ID), Title: render.Sanitize(busyTitle(titles, s.ID).String(), 80), Stale: stale}
		if !s.StartedAt.IsZero() {
			v.WorkingForS = int64(max(0, now.Sub(s.StartedAt)) / time.Second)
			v.StartedKnown = true
		}
		out = append(out, v)
	}
	for _, s := range rep.Working {
		add(s, false)
	}
	for _, s := range rep.Stale {
		add(s, true)
	}
	return out
}

// previewBusy fills the result's busy list for plan, a read-only switch
// and the interactive parent of a detached switch. It never asks and
// never exits because of busy sessions.
func (r *run) previewBusy(ctx context.Context, cliSession string) {
	if r.S == nil {
		return
	}
	if cliSession != "" && r.invoking == "" {
		if id, err := r.d().Sessions.ResolveByCLISession(ctx, r.sPath, r.S.Replica, cliSession); err == nil {
			r.invoking = id
		}
	}
	rep, _ := r.busyNow(ctx)
	r.res.Busy = busyViews(rep, r.busyTitles, r.o.now())
	r.res.BusyWait = r.o.quitTimeout()
}

// maskSession masks a session id: "local_" and the first characters of
// its UUID.
func maskSession(id ids.SessionID) string {
	return "local_" + ids.Mask(strings.TrimPrefix(string(id), "local_"))
}

// humanAge formats a duration as <1m, 12m or 2h05m.
func humanAge(d time.Duration) string { return render.HumanAge(d) }

func sessionIDs(l []app.BusySession) []ids.SessionID {
	out := make([]ids.SessionID, len(l))
	for i, s := range l {
		out[i] = s.ID
	}
	return out
}

// checkBusyP0 asks about busy sessions, or exits 12 when nobody can. A
// detached job (from the runner, with a confirmed plan or --yes) does not
// stop here: it warns and waits for them at P3.
func (r *run) checkBusyP0(ctx context.Context) error {
	rep, lines := r.busyNow(ctx)
	if len(rep.Working) == 0 {
		return nil
	}
	switch {
	case r.interactive:
		if err := r.o.ask(ctx, true, Confirmation{Kind: ConfirmBusy, Lines: lines}); err != nil {
			return err
		}
		for _, id := range sessionIDs(rep.Working) {
			r.busyAck[id] = true
		}
		return nil
	case r.opts.FromRunner:
		r.warn(P0Preflight, i18n.Tx(i18n.WarnBusyPreview, len(rep.Working), humanAge(r.o.quitTimeout())))
		return nil
	}
	return pderr.NewT(pderr.SessionsBusy, "preflight", i18n.ErrPreflightSessionsBusy, len(rep.Working))
}

// waitBusy is the P3 busy wait. It waits up to quit_timeout for the
// working sessions. Interactive runs ask again when a session nobody
// acknowledged starts working, and at the deadline ask whether to quit
// anyway (Claude's own dialog is then the last guard); other runs exit 12
// at the deadline without quitting.
func (r *run) waitBusy(ctx context.Context) error {
	d := r.d()
	deadline := r.o.now().Add(r.o.quitTimeout())
	var lastProgress time.Time
	for {
		rep, lines := r.busyNow(ctx)
		n := len(rep.Working)
		if n == 0 {
			return nil
		}
		busyErr := pderr.NewT(pderr.SessionsBusy, "quit", i18n.ErrQuitSessionsBusy, n)
		if r.interactive {
			grown := false
			for _, id := range sessionIDs(rep.Working) {
				grown = grown || !r.busyAck[id]
			}
			if grown {
				if err := r.o.ask(ctx, true, Confirmation{Kind: ConfirmBusy, Lines: lines}); err != nil {
					return err
				}
				for _, id := range sessionIDs(rep.Working) {
					r.busyAck[id] = true
				}
			}
			if !r.o.now().Before(deadline) {
				c := Confirmation{Kind: ConfirmBusyStill,
					Lines: append(lines, i18n.Tx(i18n.ConfirmBusyWaited, humanAge(r.o.quitTimeout())))}
				ok, err := d.UI.Confirm(ctx, c)
				if err != nil {
					return err
				}
				if !ok {
					return busyErr
				}
				return nil
			}
			if now := r.o.now(); lastProgress.IsZero() || now.Sub(lastProgress) >= busyProgressEvery {
				lastProgress = now
				r.o.progress(P3Quit, "wait", i18n.Tx(i18n.ProgressP3BusyWait, n))
			}
		} else if !r.o.now().Before(deadline) {
			return busyErr
		}
		if err := d.Clock.Sleep(ctx, busyPoll); err != nil {
			return pderr.Wrap(pderr.Declined, "interrupted", err)
		}
	}
}
