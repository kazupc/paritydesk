package orchestrator

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/kazupc/paritydesk/internal/app"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/profile"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/state"
)

// maxDoctorChanges is how many logged account changes doctor lists.
const maxDoctorChanges = 5

// maxDoctorBusyIDs is how many masked session ids busy-now lists.
const maxDoctorBusyIDs = 5

// doctorBusy is what the log-signal checks share within one doctor run.
type doctorBusy struct {
	sinceDone   bool
	since       time.Time
	bundlePath  string
	running     bool
	changesDone bool
	changes     []profile.AttributedChange // nil: no account_changed pattern, or unreadable
}

// appSince is the start of the app's earliest running process (zero when
// unknown or not running), memoized.
func (dr *doctor) appSince(ctx context.Context) time.Time {
	if dr.busy.sinceDone {
		return dr.busy.since
	}
	dr.busy.sinceDone = true
	d := dr.o.d
	b, err := d.App.Locate(ctx, d.Config.App.BundleID)
	if err != nil {
		return time.Time{}
	}
	dr.busy.bundlePath = b.Path
	if procs, err := d.App.Running(ctx, b.Path); err == nil {
		dr.busy.running = len(procs) > 0
	}
	dr.busy.since = appStart(ctx, d.App, b.Path)
	return dr.busy.since
}

// accountChanges are the logged account changes since the last committed
// run (memoized; nil when unknown).
func (dr *doctor) accountChanges(ctx context.Context, root *state.Root, m *compat.Manifest) []profile.AttributedChange {
	if dr.busy.changesDone || m == nil {
		return dr.busy.changes
	}
	dr.busy.changesDone = true
	dr.busy.changes, _ = loadChanges(ctx, dr.o.d.App, m.Signals, root)
	return dr.busy.changes
}

// busyDetection reports how paritydesk sees working sessions: from the
// session records (a sessions busy field), from Claude's log (turn
// start and end lines, plus clears and the keep-awake hold), or not at
// all, when only the app's own quit dialog protects a working session.
func (dr *doctor) busyDetection(m *compat.Manifest) {
	spec, _ := m.Row(model.RowSessions)
	if len(spec.Fields[sources.RoleBusy]) > 0 {
		dr.add("busy-detection", "ok", i18n.Tx(i18n.DoctorBusyDetectionRecord))
		return
	}
	sup, err := app.CheckBusySignals(m.Signals)
	switch {
	case err != nil:
		dr.add("busy-detection", "warn", i18n.Tx(i18n.DoctorBusyDetectionUnavailable))
	case sup.Clears > 0 && sup.Hold:
		dr.add("busy-detection", "ok", i18n.Tx(i18n.DoctorBusyDetectionOK))
	default:
		dr.add("busy-detection", "warn", i18n.Tx(i18n.DoctorBusyDetectionPartial, humanAge(dr.staleAfter())))
	}
}

// staleAfter is app.busy_stale_after (the default when unset).
func (dr *doctor) staleAfter() time.Duration {
	if dr.o != nil && dr.o.d.Config != nil && dr.o.d.Config.App.BusyStaleAfter.Duration > 0 {
		return dr.o.d.Config.App.BusyStaleAfter.Duration
	}
	return app.DefaultBusyStaleAfter
}

// busyChecks are the log-signal checks after busy-detection: busy-now,
// account-change and chrome-pairing-org. They never write.
func (dr *doctor) busyChecks(ctx context.Context, root *state.Root, folders []profile.Folder, m *compat.Manifest) {
	if m == nil {
		return
	}
	active := activeFolder(folders)
	dr.busyNow(ctx, root, active, m)
	dr.accountChange(ctx, root, folders, m)
	dr.pairingOrg(ctx, active, m)
}

// activeFolder returns Claude/ from a scan, or nil.
func activeFolder(folders []profile.Folder) *profile.Folder {
	for i := range folders {
		if folders[i].Name == ids.DefaultFolder {
			return &folders[i]
		}
	}
	return nil
}

// busyNow lists the sessions Claude's log shows working now (masked).
// Doctor has no invoking session, so a doctor run inside Claude lists its
// own session; the text says so.
func (dr *doctor) busyNow(ctx context.Context, root *state.Root, f *profile.Folder, m *compat.Manifest) {
	since := dr.appSince(ctx)
	if !dr.busy.running {
		dr.add("busy-now", "ok", i18n.Tx(i18n.DoctorBusyNowAppStopped))
		return
	}
	in := busyInput{sig: m.Signals, manifest: m, since: since}
	if f != nil {
		in.folder = f.Path
		if f.Identity != nil {
			if ps := root.Profiles[aliasOf(root, f.Identity.Account)]; ps != nil {
				in.replica = ps.Replica
			}
		}
		if in.replica.Account == "" && len(f.Replicas) > 0 {
			in.replica = f.Replicas[0]
		}
	}
	rep, _, lg := dr.o.busyScan(ctx, in)
	status := "ok"
	var parts []i18n.Text
	masked := func() string {
		var out []string
		for i, s := range rep.Working {
			if i == maxDoctorBusyIDs {
				out = append(out, "…")
				break
			}
			out = append(out, maskSession(s.ID))
		}
		return strings.Join(out, ", ")
	}
	n := len(rep.Working)
	switch {
	case lg == busyLogUnread:
		// Never claim "none" for a log that was not read.
		status = "warn"
		parts = append(parts, i18n.Tx(i18n.DoctorBusyNowUnreadable))
	case lg == busyLogNoSignals:
		parts = append(parts, i18n.Tx(i18n.DoctorBusyNowNoSignals))
	case n > 0:
		parts = append(parts, i18n.Tx(i18n.DoctorBusyNowSome, n, masked()))
	default:
		parts = append(parts, i18n.Tx(i18n.DoctorBusyNowNone))
	}
	if lg != busyLogRead && n > 0 {
		// Only the records' busy field can list sessions here.
		parts = append(parts, i18n.Tx(i18n.DoctorBusyNowRecords, n, masked()))
	}
	if k := len(rep.Stale); k > 0 {
		parts = append(parts, i18n.Tx(i18n.DoctorBusyNowStale, k))
	}
	if rep.Truncated {
		parts = append(parts, i18n.Tx(i18n.DoctorBusyNowTruncated))
	}
	dr.add("busy-now", status, i18n.Join(parts...))
}

// accountChange reports account changes inside a data folder: fail for
// a folder signed in to another profile's account and for Claude's own
// multi-account mode (log chain or recorded history), one warn line per
// logged change (newest 5), else ok.
func (dr *doctor) accountChange(ctx context.Context, root *state.Root, folders []profile.Folder, m *compat.Manifest) {
	d := dr.o.d
	changes := dr.accountChanges(ctx, root, m)
	added := false
	add := func(status string, detail i18n.Text) {
		dr.add("account-change", status, detail)
		added = true
	}
	var res *profile.Resolution
	if d.Resolve != nil {
		res, _ = d.Resolve(d.Config, root, folders, profile.Hints{})
	}
	if c, ok := profile.InFolderSignIn(d.Config, root, res, changes); ok {
		add("fail", i18n.Tx(i18n.DoctorAccountChangeInFolder, c.Folder, c.Holder, c.Folder))
	}
	var holder ids.Alias
	if res != nil && res.Active != nil {
		holder = res.Active.Profile.Alias
	}
	if name, n := nativeLogName(changes, holder); n >= 2 {
		add("fail", i18n.Tx(i18n.DoctorAccountChangeNativeLog, name, n))
	}
	if res != nil {
		aliases := make([]string, 0, len(res.Bindings))
		for a := range res.Bindings {
			aliases = append(aliases, string(a))
		}
		sort.Strings(aliases)
		for _, a := range aliases {
			b := res.Bindings[ids.Alias(a)]
			if b == nil || b.Folder == nil || b.Folder.Identity == nil {
				continue
			}
			if profile.NativeMultiAccount(root.Profiles[b.Profile.Alias], b.Folder.Identity.Account) {
				add("fail", i18n.Tx(i18n.DoctorAccountChangeNativeHistory, b.Profile.Alias))
			}
		}
	}
	for i := max(0, len(changes)-maxDoctorChanges); i < len(changes); i++ {
		c := changes[i]
		to := ids.Mask(string(c.To))
		if c.ToAlias != "" {
			to = i18n.Tx(i18n.DoctorAccountChangeToProfile, c.ToAlias, to).String()
		}
		at := i18n.Tx(i18n.DoctorAccountChangeUnknownTime)
		if !c.At.IsZero() {
			at = i18n.Raw(c.At.Local().Format("2006-01-02 15:04"))
		}
		if c.FromAlias != "" {
			add("warn", i18n.Tx(i18n.DoctorAccountChangeOne, c.FromAlias, ids.Mask(string(c.From)), to, at))
		} else {
			add("warn", i18n.Tx(i18n.DoctorAccountChangeUnknown, ids.Mask(string(c.From)), to, at))
		}
	}
	switch {
	case added:
	case m.Signals.AccountChanged == "":
		dr.add("account-change", "ok", i18n.Tx(i18n.DoctorAccountChangeUnavailable))
	default:
		dr.add("account-change", "ok", i18n.Tx(i18n.DoctorAccountChangeNone))
	}
}

// pairingOrg reports which organization Claude in Chrome pairs with: the
// active one from Claude's log (masked; never read from cookies), and the
// last local pairing state logged since the app started.
func (dr *doctor) pairingOrg(ctx context.Context, f *profile.Folder, m *compat.Manifest) {
	d := dr.o.d
	detail := i18n.Tx(i18n.DoctorChromePairingOrgUnknown)
	if f != nil && f.Identity != nil {
		if org, found, err := d.App.ActiveOrg(ctx, m.Signals, f.Identity.Account, dr.appSince(ctx)); err == nil && found {
			detail = i18n.Tx(i18n.DoctorChromePairingOrgKnown, ids.Mask(string(org)))
		}
	}
	var from int64
	if n, err := d.App.LogSize(); err == nil && n > verifyLogTail {
		from = n - verifyLogTail
	}
	if evs, _, err := d.App.LogEvents(ctx, from, m.Signals, ""); err == nil {
		switch lastSinceStart(evs, app.EvPairingEstablished, app.EvPairingClosed) {
		case app.EvPairingEstablished:
			detail = i18n.Join(detail, i18n.Tx(i18n.DoctorChromePairingOrgState, "established"))
		case app.EvPairingClosed:
			detail = i18n.Join(detail, i18n.Tx(i18n.DoctorChromePairingOrgState, "closed"))
		}
	}
	dr.add("chrome-pairing-org", "ok", detail)
}

// lastSinceStart returns the last of the wanted events after the last
// app start in evs (anywhere when evs has no app start), or "".
func lastSinceStart(evs []app.LogEvent, want ...app.LogEvent) app.LogEvent {
	start := 0
	for i, e := range evs {
		if e == app.EvAppStarted {
			start = i + 1
		}
	}
	var last app.LogEvent
	for _, e := range evs[start:] {
		for _, w := range want {
			if e == w {
				last = e
			}
		}
	}
	return last
}

// sinceLastStart reports whether ev appears after the last app start in
// evs (anywhere when evs has no app start).
func sinceLastStart(evs []app.LogEvent, ev app.LogEvent) bool {
	return lastSinceStart(evs, ev) != ""
}

// statusAccounts adds the account-change report of the full status: the
// logged changes (masked), whether Claude's own multi-account mode looks
// active for the active profile, and the warnings.
func (o *Orchestrator) statusAccounts(ctx context.Context, root *state.Root, sig compat.Signals, res *profile.Resolution, st *Status) {
	changes, _ := loadChanges(ctx, o.d.App, sig, root)
	st.AccountChanges = accountChangeViews(changes)
	if c, ok := profile.InFolderSignIn(o.d.Config, root, res, changes); ok {
		st.Warnings = append(st.Warnings, i18n.Tx(i18n.WarnInFolderSignIn, c.Folder, c.Holder))
	} else if res.OutsideSwitch {
		st.Warnings = append(st.Warnings, i18n.Tx(i18n.WarnAccountChangedOutside))
	}
	st.Warnings = append(st.Warnings, accountChangeWarnings(changes)...)
	var holder ids.Alias
	if res.Active != nil {
		holder = res.Active.Profile.Alias
	}
	name, n := nativeLogName(changes, holder)
	native := n >= 2
	if res.Active != nil {
		if f := res.Active.Folder; f != nil && f.Identity != nil &&
			profile.NativeMultiAccount(root.Profiles[res.Active.Profile.Alias], f.Identity.Account) {
			if !native {
				name = string(holder)
			}
			native = true
		}
	}
	st.NativeMultiAccount = &native
	if native {
		st.Warnings = append(st.Warnings, i18n.Tx(i18n.WarnNativeMulti, name))
	}
}
