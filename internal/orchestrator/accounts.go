package orchestrator

import (
	"context"
	"fmt"
	"time"

	"github.com/kazupc/paritydesk/internal/app"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/profile"
	"github.com/kazupc/paritydesk/internal/state"
)

// appSince returns the start time of the app's earliest running process
// (memoized per run): zero when nothing runs, the listing fails, or any
// start time is unknown.
func (r *run) appSince(ctx context.Context) time.Time {
	if r.sinceDone {
		return r.since
	}
	r.sinceDone = true
	r.since = appStart(ctx, r.d().App, r.bundle.Path)
	return r.since
}

// appStart is the earliest start time of the app's processes, or zero.
func appStart(ctx context.Context, a AppControl, bundlePath string) time.Time {
	procs, err := a.Running(ctx, bundlePath)
	if err != nil {
		return time.Time{}
	}
	return earliestStart(procs)
}

// earliestStart is the earliest Started of procs; zero when procs is
// empty or any start time is unknown.
func earliestStart(procs []app.Process) time.Time {
	var since time.Time
	for _, p := range procs {
		if p.Started.IsZero() {
			return time.Time{}
		}
		if since.IsZero() || p.Started.Before(since) {
			since = p.Started
		}
	}
	return since
}

// loadAccountChanges reads the logged changes back to root.LastCommitAt
// and attributes them. It sets r.changes; on app.ErrSignalUnknown or any
// read error r.changes stays nil and nothing else happens. A truncated
// window adds the warning txtWarnAccountWindow once.
func (r *run) loadAccountChanges(ctx context.Context) {
	r.changes = nil
	changes, truncated := loadChanges(ctx, r.d().App, r.sig, r.root)
	if changes == nil {
		return
	}
	r.changes = changes
	if truncated && !r.warnedWindow {
		r.warnedWindow = true
		r.warn(P0Preflight, txtWarnAccountWindow)
	}
}

// loadChanges reads and attributes the account changes since the last
// committed run. truncated is reported only when there is such a mark
// (without one the window is the newest part of the log by design).
func loadChanges(ctx context.Context, a AppControl, sig compat.Signals, root *state.Root) ([]profile.AttributedChange, bool) {
	var mark time.Time
	if root != nil {
		mark = root.LastCommitAt
	}
	changes, truncated, err := a.AccountChanges(ctx, sig, mark)
	if err != nil {
		return nil, false
	}
	logged := make([]profile.LoggedChange, len(changes))
	for i, c := range changes {
		logged[i] = profile.LoggedChange{From: c.From, To: c.To, At: c.At}
	}
	out := profile.AttributeChanges(root, logged)
	if out == nil {
		out = []profile.AttributedChange{}
	}
	return out, truncated && !mark.IsZero()
}

// checkInFolderSignIn refuses (exit 20) a state where Claude/ is one
// profile's folder signed in, inside Claude, to another profile's
// account. Nothing has been written when it runs.
func (r *run) checkInFolderSignIn(res *profile.Resolution) error {
	c, ok := profile.InFolderSignIn(r.d().Config, r.root, res, r.changes)
	if !ok {
		return nil
	}
	return pderr.New(pderr.IdentityMismatch, "resolve", fmt.Sprintf(txtErrInFolderSignIn, c.Folder, c.Holder, c.Folder))
}

// aliasOrMasked names a profile, or the masked account when no profile
// has it.
func aliasOrMasked(a ids.Alias, id ids.AccountID) string {
	if a != "" {
		return string(a)
	}
	return "account " + ids.Mask(string(id))
}

// nativeLogName names the folder of the native multi-account chain in
// changes: the profile that holds the chain's first account, else holder
// (the profile bound to Claude/; "" when none), else the masked account.
// n is the chain length (0 when there is no chain of two).
func nativeLogName(changes []profile.AttributedChange, holder ids.Alias) (string, int) {
	first, n := profile.NativeMultiChain(changes)
	if n < 2 {
		return "", 0
	}
	if first.FromAlias == "" && holder != "" {
		return string(holder), n
	}
	return aliasOrMasked(first.FromAlias, first.From), n
}

// accountChangeWarnings returns one warning per change since the last
// switch, unless the changes form a chain (native multi-account mode),
// which only the swap-only cause reports.
func accountChangeWarnings(changes []profile.AttributedChange) []string {
	if _, n := profile.NativeMultiFromLog(changes); n >= 2 {
		return nil
	}
	var out []string
	for _, c := range changes {
		if c.SinceCommit {
			out = append(out, fmt.Sprintf(txtWarnAccountChange, aliasOrMasked(c.FromAlias, c.From), aliasOrMasked(c.ToAlias, c.To)))
		}
	}
	return out
}

// nativeCause explains a swap-only mode caused by Claude's own
// multi-account mode: the warning for a switch and the error detail for
// sync. ok is false when neither the log chain nor the history rule
// holds.
func (r *run) nativeCause() (warning, syncErr string, ok bool) {
	var holder ids.Alias
	if r.S != nil {
		holder = r.S.Profile.Alias
	}
	if name, n := nativeLogName(r.changes, holder); n >= 2 {
		return fmt.Sprintf(txtWarnSwapOnlyNativeLog, name, n), fmt.Sprintf(txtErrSyncSwapOnlyNativeLog, name, n), true
	}
	if h := r.nativeMultiAlias(); h != "" {
		return fmt.Sprintf(txtWarnSwapOnlyNativeHistory, h), fmt.Sprintf(txtErrSyncSwapOnlyNativeHistory, h), true
	}
	return "", "", false
}

// accountChangeViews lists the changes for status (masked).
func accountChangeViews(changes []profile.AttributedChange) []AccountChangeView {
	var out []AccountChangeView
	for _, c := range changes {
		out = append(out, AccountChangeView{
			Profile: c.FromAlias, FromMasked: ids.Mask(string(c.From)), ToMasked: ids.Mask(string(c.To)),
			ToProfile: c.ToAlias, At: c.At, SinceSwitch: c.SinceCommit,
		})
	}
	return out
}
