package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"slices"
	"time"

	"github.com/kazupc/paritydesk/internal/clock"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
)

const (
	// pollInterval paces every wait loop (QuitAndWait, WaitExited,
	// WaitTurnEnded, WaitUpdateInstalled).
	pollInterval = 250 * time.Millisecond
	// turnSettle is waited after a turn end, so the app has left its
	// working state before the quit event arrives.
	turnSettle = 2 * time.Second
)

// clk returns the platform clock, the real one when unset.
func (p *Platform) clk() clock.Clock {
	if p.Clock == nil {
		return clock.System{}
	}
	return p.Clock
}

// Running returns the app's process set (IsAppProcess over Procs).
func (p *Platform) Running(ctx context.Context, bundlePath string) ([]Process, error) {
	procs, err := p.Procs.Processes(ctx)
	if err != nil {
		return nil, err
	}
	var out []Process
	for _, pr := range procs {
		if IsAppProcess(pr, bundlePath) {
			out = append(out, pr)
		}
	}
	return out, nil
}

// CheckQuiescence evaluates every D15 signal once: app processes (by
// bundle path, or by --user-data-dir into one of the folders), the
// updater, held LOCK files, write holders versus read-only holders under
// the folders, and app, updater or write-holder processes started after
// in.After. This process's own descriptors are ignored. A write holder
// whose start time (native scan only) differs from its process list entry
// is a reused pid: it is reported in StartedAfter when it started after
// in.After.
func (p *Platform) CheckQuiescence(ctx context.Context, in QuiescenceInput) (QuiescenceReport, error) {
	var rep QuiescenceReport
	procs, err := p.Procs.Processes(ctx)
	if err != nil {
		return rep, err
	}
	byPID := make(map[int]Process, len(procs))
	late := map[int]bool{}
	var extra []Process // write holders newer than their process list entry
	for _, pr := range procs {
		byPID[pr.PID] = pr
		isApp := IsAppProcess(pr, in.BundlePath) || usesDataFolder(pr, in.Folders)
		isUpd := isUpdater(pr, in.BundleID, in.BundlePath)
		if isApp {
			rep.AppPIDs = append(rep.AppPIDs, pr.PID)
		}
		rep.UpdaterRunning = rep.UpdaterRunning || isUpd
		if (isApp || isUpd) && startedAfter(pr, in.After) {
			late[pr.PID] = true
		}
	}
	for _, lf := range in.LockFiles {
		held, err := p.Locks.LockHeld(lf)
		if err != nil {
			return rep, err
		}
		if held {
			rep.LocksHeld = append(rep.LocksHeld, lf)
		}
	}
	if len(in.Folders) > 0 {
		holders, err := p.Holders.Holders(ctx, in.Folders)
		if err != nil {
			return rep, err
		}
		self := os.Getpid()
		for _, h := range holders {
			if h.PID == self {
				continue
			}
			if !h.Writes() {
				rep.ReadOnlyHolders = append(rep.ReadOnlyHolders, h)
				continue
			}
			rep.WriteHolders = append(rep.WriteHolders, h)
			pr, ok := byPID[h.PID]
			switch {
			case ok && (h.Started.IsZero() || h.Started.Equal(pr.Started)):
				if startedAfter(pr, in.After) {
					late[pr.PID] = true
				}
			case !h.Started.IsZero() && !in.After.IsZero() && h.Started.After(in.After):
				// The holder is a newer process than the list entry (pid
				// reused) or started after the list was taken: report it
				// too.
				extra = append(extra, Process{PID: h.PID, Started: h.Started})
			}
		}
	}
	for _, pr := range procs {
		if late[pr.PID] {
			rep.StartedAfter = append(rep.StartedAfter, pr)
		}
	}
	for _, pr := range extra {
		dup := slices.ContainsFunc(rep.StartedAfter, func(q Process) bool { return q.PID == pr.PID && q.Started.Equal(pr.Started) })
		if !dup {
			rep.StartedAfter = append(rep.StartedAfter, pr)
		}
	}
	rep.Quiet = len(rep.AppPIDs) == 0 && !rep.UpdaterRunning && len(rep.LocksHeld) == 0 &&
		len(rep.WriteHolders) == 0 && len(rep.StartedAfter) == 0
	return rep, nil
}

// HolderScan describes the open-file scan in use (doctor only): the
// holders' Info when they have one, else a plain lsof backend (fakes).
func (p *Platform) HolderScan(ctx context.Context) HolderScanInfo {
	if i, ok := p.Holders.(interface {
		Info(context.Context) HolderScanInfo
	}); ok {
		return i.Info(ctx)
	}
	return HolderScanInfo{Backend: "lsof"}
}

// startedAfter reports whether pr started after t (never for a zero t).
func startedAfter(pr Process, t time.Time) bool { return !t.IsZero() && pr.Started.After(t) }

// QuitAndWait sends the quit event and waits until the app's process set
// is empty, up to timeout. A veto (main.log shows the quit-vetoed line) is
// reported through onVeto (so the UI can tell the user to choose "Wait
// for Claude" or "Cancel") and waited on. A quit the app refused
// (ErrQuitRefused, AppleScript error -128) or left without a reply
// (ErrEventTimedOut, -1712) is waited on the same way, since the app may
// still quit by itself: it sets Refused or NoReply, and Vetoed only once
// main.log shows the quit-vetoed line. A timeout returns the result
// (Pending set when the app vetoed, refused or did not reply) together
// with *pderr.Error AppDidNotQuit, wrapping the quit event's error after a
// refusal or a missing reply; no signal is ever sent, and no command after
// the one quit event. Any other failure of the quit event (Automation,
// -10004, an unknown error) is returned at once.
func (p *Platform) QuitAndWait(ctx context.Context, bundleID, bundlePath string, sig compat.Signals, timeout time.Duration, onVeto func()) (QuitResult, error) {
	clk := p.clk()
	start := clk.Now()
	cursor, err := p.Log.Cursor()
	logVeto := sig.QuitVetoed != "" && err == nil // else veto detection is unavailable; the wait still works
	var res QuitResult
	var cause error // the refused or unanswered quit's error, kept for the timeout error
	if err := p.Control.Quit(ctx, bundleID); err != nil {
		switch {
		case errors.Is(err, ErrNotRunning):
		case errors.Is(err, ErrQuitRefused):
			res.Refused, cause = true, err
		case errors.Is(err, ErrEventTimedOut):
			res.NoReply, cause = true, err
		default:
			return QuitResult{}, err
		}
	}
	vetoSig := compat.Signals{QuitVetoed: sig.QuitVetoed}
	for {
		running, err := p.Running(ctx, bundlePath)
		if err != nil {
			return res, err
		}
		res.Elapsed = clk.Now().Sub(start)
		if len(running) == 0 {
			res.Exited = true
			return res, nil
		}
		if !res.Vetoed && logVeto {
			evs, next, err := p.Log.EventsAt(ctx, cursor, vetoSig, "")
			if err == nil {
				cursor = next
				if slices.Contains(evs, EvQuitVetoed) {
					res.Vetoed = true
					if onVeto != nil {
						onVeto()
					}
				}
			}
		}
		if res.Elapsed >= timeout {
			res.Pending = res.Vetoed || res.Refused || res.NoReply
			e := &pderr.Error{Code: pderr.AppDidNotQuit, Op: "app/quit", Msg: "the app did not quit before the timeout", Err: cause}
			switch {
			case res.Refused:
				e.Msg = "the app refused to quit and did not quit before the timeout"
			case res.NoReply:
				e.Msg = "the quit event got no reply in time and the app did not quit before the timeout"
			}
			return res, e
		}
		if err := clk.Sleep(ctx, pollInterval); err != nil {
			return res, err
		}
	}
}

// WaitTurnEnded waits until main.log reports, after the cursor from,
// that the given session's turn ended (a turn_ended or turn_cleared line
// for it), then a settle delay, so an in-app switch does not trip the
// app's own quit veto (ADR 0003 finding 1). from is recorded by the
// invoking process before it hands off to the detached job
// (SwitchOptions.LogOffset and LogInode), so a turn that ended before the
// job reached P3 is found immediately, also after a rotation. A timeout
// gives *pderr.Error SessionsBusy; an empty TurnEnded pattern gives
// ErrSignalUnknown.
func (p *Platform) WaitTurnEnded(ctx context.Context, from LogCursor, sig compat.Signals, session ids.SessionID, timeout time.Duration) error {
	if _, err := ids.ParseSession(string(session)); err != nil {
		return ErrInvalidSession
	}
	if sig.TurnEnded == "" {
		return ErrSignalUnknown
	}
	clk := p.clk()
	start := clk.Now()
	only := compat.Signals{TurnEnded: sig.TurnEnded, TurnCleared: sig.TurnCleared}
	cursor := from
	for {
		evs, next, err := p.Log.EventsAt(ctx, cursor, only, session)
		if err != nil {
			return err
		}
		cursor = next
		if slices.Contains(evs, EvTurnEnded) || slices.Contains(evs, EvTurnCleared) {
			return clk.Sleep(ctx, turnSettle)
		}
		if clk.Now().Sub(start) >= timeout {
			return pderr.New(pderr.SessionsBusy, "app/turn-wait", "the session's turn did not end in time")
		}
		if err := clk.Sleep(ctx, pollInterval); err != nil {
			return err
		}
	}
}

// WaitExited polls the app's process set until it is empty or timeout
// passes, and reports whether the app exited. Used after a vetoed quit
// timed out, so a late "Wait for Claude" quit is noticed and the abort can
// relaunch the leaving profile (I4).
func (p *Platform) WaitExited(ctx context.Context, bundlePath string, timeout time.Duration) (bool, error) {
	clk := p.clk()
	start := clk.Now()
	for {
		running, err := p.Running(ctx, bundlePath)
		if err != nil {
			return false, err
		}
		if len(running) == 0 {
			return true, nil
		}
		if clk.Now().Sub(start) >= timeout {
			return false, nil
		}
		if err := clk.Sleep(ctx, pollInterval); err != nil {
			return false, err
		}
	}
}

// BusySessions returns the busy report of the app's current run. It keeps
// one BusyTracker, keyed by a fingerprint of the busy signals, so a poll
// reads only the lines written since the last one. ErrSignalUnknown when
// the manifest has no usable busy signals.
func (p *Platform) BusySessions(ctx context.Context, sig compat.Signals, opts BusyOptions) (BusyReport, error) {
	p.busyMu.Lock()
	defer p.busyMu.Unlock()
	key := busyKey(sig)
	if p.busy == nil || p.busyFor != key {
		t, err := p.Log.NewBusyTracker(sig)
		if err != nil {
			return BusyReport{}, err
		}
		p.busy, p.busyFor = t, key
	}
	return p.busy.Update(ctx, opts)
}

// busyKey fingerprints the busy signals.
func busyKey(sig compat.Signals) string {
	parts := append([]string{sig.TurnStarted, sig.TurnEnded, sig.WorkHeld, sig.WorkReleased, sig.WorkUncounted, sig.AppStarted},
		sig.TurnCleared...)
	parts = append(parts, sig.WorkIdleReasons...)
	h := sha256.New()
	for _, s := range parts {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// AccountChanges delegates to Log.AccountChanges.
func (p *Platform) AccountChanges(ctx context.Context, sig compat.Signals, since time.Time) ([]AccountChange, bool, error) {
	return p.Log.AccountChanges(ctx, sig, since)
}

// LastReplica delegates to Log.LastReplica.
func (p *Platform) LastReplica(ctx context.Context, sig compat.Signals, since time.Time) (model.Replica, bool, error) {
	return p.Log.LastReplica(ctx, sig, since)
}

// LogCursor delegates to Log.Cursor.
func (p *Platform) LogCursor() (LogCursor, error) { return p.Log.Cursor() }

// WaitUpdateInstalled delegates to Bundle.
func (p *Platform) WaitUpdateInstalled(ctx context.Context, bundleID string, timeout time.Duration) error {
	return p.Bundle.WaitUpdateInstalled(ctx, bundleID, timeout)
}

// ActiveOrg delegates to Log.ActiveOrg.
func (p *Platform) ActiveOrg(ctx context.Context, sig compat.Signals, account ids.AccountID, since time.Time) (ids.OrgID, bool, error) {
	return p.Log.ActiveOrg(ctx, sig, account, since)
}

// The methods below expose the Platform's parts as one interface for the
// orchestrator.

// Locate delegates to Bundle.
func (p *Platform) Locate(ctx context.Context, bundleID string) (BundleIdentity, error) {
	return p.Bundle.Locate(ctx, bundleID)
}

// UpdateStaged delegates to Bundle.
func (p *Platform) UpdateStaged(ctx context.Context, bundleID string) (bool, error) {
	return p.Bundle.UpdateStaged(ctx, bundleID)
}

// Launch delegates to Control.
func (p *Platform) Launch(ctx context.Context, bundleID string) error {
	return p.Control.Launch(ctx, bundleID)
}

// OpenSession delegates to Control.
func (p *Platform) OpenSession(ctx context.Context, id ids.SessionID) error {
	return p.Control.OpenSession(ctx, id)
}

// ProbeAutomation delegates to Control.
func (p *Platform) ProbeAutomation(ctx context.Context, bundleID string) error {
	return p.Control.ProbeAutomation(ctx, bundleID)
}

// Notify delegates to Control.
func (p *Platform) Notify(ctx context.Context, title, text string) error {
	return p.Control.Notify(ctx, title, text)
}

// LogSize delegates to Log.
func (p *Platform) LogSize() (int64, error) { return p.Log.Size() }

// LogEvents delegates to Log.
func (p *Platform) LogEvents(ctx context.Context, from int64, sig compat.Signals, session ids.SessionID) ([]LogEvent, int64, error) {
	return p.Log.Events(ctx, from, sig, session)
}

// GitDirty delegates to Git.
func (p *Platform) GitDirty(ctx context.Context, dir string) (bool, error) {
	return p.Git.Dirty(ctx, dir)
}

// LockHeld delegates to Locks.
func (p *Platform) LockHeld(path string) (bool, error) { return p.Locks.LockHeld(path) }
