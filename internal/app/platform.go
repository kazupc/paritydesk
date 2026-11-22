package app

import (
	"context"
	"errors"
	"os"
	"slices"
	"time"

	"github.com/kazupc/paritydesk/internal/clock"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/ids"
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
// in.After. This process's own descriptors are ignored.
func (p *Platform) CheckQuiescence(ctx context.Context, in QuiescenceInput) (QuiescenceReport, error) {
	var rep QuiescenceReport
	procs, err := p.Procs.Processes(ctx)
	if err != nil {
		return rep, err
	}
	byPID := make(map[int]Process, len(procs))
	late := map[int]bool{}
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
			if pr, ok := byPID[h.PID]; ok && startedAfter(pr, in.After) {
				late[pr.PID] = true
			}
		}
	}
	for _, pr := range procs {
		if late[pr.PID] {
			rep.StartedAfter = append(rep.StartedAfter, pr)
		}
	}
	rep.Quiet = len(rep.AppPIDs) == 0 && !rep.UpdaterRunning && len(rep.LocksHeld) == 0 &&
		len(rep.WriteHolders) == 0 && len(rep.StartedAfter) == 0
	return rep, nil
}

// startedAfter reports whether pr started after t (never for a zero t).
func startedAfter(pr Process, t time.Time) bool { return !t.IsZero() && pr.Started.After(t) }

// QuitAndWait sends the quit event and waits until the app's process set
// is empty, up to timeout. A veto is reported through onVeto (so the UI
// can tell the user to choose "Wait for Claude" or "Cancel") and waited
// on. A timeout returns the result (Pending set after a veto) together with
// *pderr.Error AppDidNotQuit; no signal is ever sent, and no command after
// the one quit event.
func (p *Platform) QuitAndWait(ctx context.Context, bundleID, bundlePath string, sig compat.Signals, timeout time.Duration, onVeto func()) (QuitResult, error) {
	clk := p.clk()
	start := clk.Now()
	offset, err := p.Log.Size()
	if err != nil {
		offset = -1 // veto detection unavailable; the wait still works
	}
	if err := p.Control.Quit(ctx, bundleID); err != nil {
		if !errors.Is(err, ErrNotRunning) {
			return QuitResult{}, err
		}
	}
	vetoSig := compat.Signals{QuitVetoed: sig.QuitVetoed}
	var res QuitResult
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
		if !res.Vetoed && sig.QuitVetoed != "" && offset >= 0 {
			evs, next, err := p.Log.Events(ctx, offset, vetoSig, "")
			if err == nil {
				offset = next
				if slices.Contains(evs, EvQuitVetoed) {
					res.Vetoed = true
					if onVeto != nil {
						onVeto()
					}
				}
			}
		}
		if res.Elapsed >= timeout {
			res.Pending = res.Vetoed
			return res, pderr.New(pderr.AppDidNotQuit, "app/quit", "the app did not quit before the timeout")
		}
		if err := clk.Sleep(ctx, pollInterval); err != nil {
			return res, err
		}
	}
}

// WaitTurnEnded waits until main.log reports, at or after offset from,
// that the given session's turn ended, then a settle delay, so an in-app
// switch does not trip the app's own quit veto (ADR 0003 finding 1). from
// is recorded by the invoking process before it hands off to the detached
// job (SwitchOptions.LogOffset), so a turn that ended before the job
// reached P3 is found immediately. A timeout gives *pderr.Error
// SessionsBusy; an empty TurnEnded pattern gives ErrSignalUnknown.
func (p *Platform) WaitTurnEnded(ctx context.Context, from int64, sig compat.Signals, session ids.SessionID, timeout time.Duration) error {
	if _, err := ids.ParseSession(string(session)); err != nil {
		return ErrInvalidSession
	}
	if sig.TurnEnded == "" {
		return ErrSignalUnknown
	}
	clk := p.clk()
	start := clk.Now()
	only := compat.Signals{TurnEnded: sig.TurnEnded}
	offset := from
	for {
		evs, next, err := p.Log.Events(ctx, offset, only, session)
		if err != nil {
			return err
		}
		offset = next
		if slices.Contains(evs, EvTurnEnded) {
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

// BusySessions delegates to Log.Busy.
func (p *Platform) BusySessions(ctx context.Context, from int64, sig compat.Signals) ([]ids.SessionID, error) {
	return p.Log.Busy(ctx, from, sig)
}

// WaitUpdateInstalled delegates to Bundle.
func (p *Platform) WaitUpdateInstalled(ctx context.Context, bundleID string, timeout time.Duration) error {
	return p.Bundle.WaitUpdateInstalled(ctx, bundleID, timeout)
}

// ActiveOrg delegates to Log.ActiveOrg.
func (p *Platform) ActiveOrg(ctx context.Context, sig compat.Signals) (ids.OrgID, bool, error) {
	return p.Log.ActiveOrg(ctx, sig)
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
