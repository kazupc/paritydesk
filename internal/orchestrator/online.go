package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/online"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/rows"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/state"
)

// Online is the online layer as the orchestrator sees it
// (online.Service). nil turns it off (v0.1 behaviour).
type Online interface {
	// Capture returns the leaving side's usable P1 capture, or nil.
	Capture(ctx context.Context, alias ids.Alias, rep model.Replica, epoch uint64) (*online.CaptureSnapshot, error)
	Build(in online.BuildInput) online.Draft
	// Publish writes queue.json (and steps/ files) and returns the steps
	// hash for Root.Online.StepsSHA.
	Publish(ctx context.Context, d online.Draft, h online.QueueHeader) (stepsSHA string, err error)
	Invalidate(ctx context.Context, reason string) error
	Cleanup(ctx context.Context, root *state.Root) error
	ReadHead() (*online.QueueHead, error)
	// Resolved returns the queue's steps with statuses resolved from
	// recorded results, without offering anything; nil, nil when there is
	// no such queue.
	Resolved(ctx context.Context, queueID string) ([]online.Step, error)
	// BreakLease stops every call of the old queue while a run works.
	BreakLease(ctx context.Context) error
	// SetLaunched records the launch time of a published queue (P11).
	SetLaunched(ctx context.Context, queueID string, t time.Time) error
	// Finish marks a queue complete for its caller and removes the lease.
	Finish(ctx context.Context, queueID string, c online.Caller) (*online.Queue, error)
	// Provides reports whether a provider builds steps of kind k.
	Provides(k online.Kind) bool
	// QueueCounts are a queue's counts, with the providers' own counts.
	QueueCounts(q *online.Queue) online.Counts
}

var _ Online = (*online.Service)(nil)

// onlineDraftKind is the state object kind of the P5 draft.
const onlineDraftKind = "online-draft"

// onlineRootWait is how long `online done` waits for the run lock.
const onlineRootWait = 3 * time.Second

// onlineActive reports whether the online layer runs for manifest m.
func (o *Orchestrator) onlineActive(m *compat.Manifest) bool {
	return o.d.Online != nil && m != nil && m.Online.Enabled && o.d.Config != nil &&
		o.d.Config.Online.Reconcile != config.OnlineReconcileOff
}

// DefaultSupportDir reports whether the support directory is the default
// one, the only one the plugin's hooks and shim know (4.1).
func (o *Orchestrator) defaultSupportDir() bool {
	return o.d.Home != "" && filepath.Clean(o.d.Paths.SupportDir) == filepath.Join(o.d.Home, "Library", "Application Support", "paritydesk")
}

// onlineBuild builds the online draft from a plan's merge outputs (P2
// approximate, P5 final), and returns the input it was built from.
func (r *run) onlineBuild(pl *planned, approximate bool) (online.Draft, online.BuildInput) {
	if !r.o.onlineActive(r.manifest) || pl == nil {
		return online.Draft{}, online.BuildInput{}
	}
	outs := map[model.RowID]*rows.Output{}
	cur := map[model.RowID]*model.RowSnapshot{}
	for row, w := range pl.Work {
		outs[row] = w.Out
		cur[row] = w.T
	}
	cfg := r.d().Config
	in := online.BuildInput{
		Spec: r.manifest.Online, Config: cfg.Online, From: r.S.Profile.Alias, To: r.T.Profile.Alias,
		Replica: r.T.Replica, Outputs: outs, Current: cur, PinsSource: online.PinsNone,
		ReopenSession: r.invoking, Approximate: approximate, Phase: online.PhaseReconcile,
		Limits: plan.Limits{MaxDeleteFraction: cfg.Limits.MaxDeleteFraction, MaxDeleteMin: cfg.Limits.MaxDeleteMin},
	}
	r.pinsBuild(context.Background(), pl, &in)
	return r.d().Online.Build(in), in
}

// onlineSummary is Result.Online for a draft (nil when nothing is
// queued and nothing was skipped).
func onlineSummary(d online.Draft, queued bool) *online.Summary {
	c := d.Counts
	s := &online.Summary{PinsAdd: c.PinsAdd, PinsRemove: c.PinsRemove, PinsKeep: c.PinsKeep, PinsBlocked: c.PinsBlocked,
		Sidebar: c.Sidebar, Other: c.Other, PinsSource: string(online.PinsNone), Queued: queued}
	if !s.Any() && !queued {
		return nil
	}
	return s
}

// shownOnlineWarnings are the draft warnings that go into
// Result.Warnings (the rest goes to the run log): a provider that failed,
// and the pin provider's reasons for changes it did not queue (spec-pins
// 8.2, 8.5: the warning says why, and what unpinning by hand does).
// Settings Claude's tools cannot set are expected (for example a group-by
// by environment) and stay in the run log.
var shownOnlineWarnings = map[string]bool{
	string(i18n.OnlineWarnBuildFailed):       true,
	string(i18n.OnlineWarnPinsFamilyBlocked): true,
	string(i18n.OnlineWarnPinsHidden):        true,
	string(i18n.OnlineWarnPinsCapped):        true,
}

// onlinePreview is P2's online part: the approximate counts. The pin
// provider's warnings are shown with the plan (plan --json carries them);
// P5 replaces them with the final draft's.
func (r *run) onlinePreview() {
	d, in := r.onlineBuild(r.p2, true)
	r.res.Online = r.pinsSummary(r.p2, onlineSummary(d, false), in, true)
	for _, w := range d.Warnings {
		if shownOnlineWarnings[w.Msg] && w.Msg != string(i18n.OnlineWarnBuildFailed) {
			r.warn(P2Preview, w.Text)
			r.previewWarns = append(r.previewWarns, w.Text)
		} else {
			r.o.progress(P2Preview, StatusInfo, w.Text)
		}
	}
}

// dropPreviewWarns removes the warnings P2's approximate draft added.
func (r *run) dropPreviewWarns() {
	for _, t := range r.previewWarns {
		if i := slices.IndexFunc(r.res.Warnings, func(w i18n.Text) bool { return w.English() == t.English() }); i >= 0 {
			r.res.Warnings = slices.Delete(r.res.Warnings, i, i+1)
		}
	}
	r.previewWarns = nil
}

// onlineHarvest is P5's online part: the final draft.
func (r *run) onlineHarvest() {
	var in online.BuildInput
	r.draft, in = r.onlineBuild(r.p5, false)
	r.res.Online = r.pinsSummary(r.p5, onlineSummary(r.draft, false), in, false)
	if r.p5 != nil && r.p5.Pins != nil && r.p5.Pins.SProbePassed {
		// S's read passed the freshness probe and becomes its base in the
		// P5 commit (spec-pins 6.4 item 7).
		clearPinsProbe(r.root, r.S.Profile.Alias)
	}
	r.dropPreviewWarns()
	for _, w := range r.draft.Warnings {
		if shownOnlineWarnings[w.Msg] {
			r.warn(P5Harvest, w.Text)
		} else {
			r.o.progress(P5Harvest, StatusInfo, w.Text)
		}
	}
}

// storeOnlineDraft stores the P5 draft as a state object in the planned
// commit, so recover can still publish it.
func (r *run) storeOnlineDraft() error {
	if r.draft.Empty() {
		r.root.Run.Online = ""
		return nil
	}
	ref, err := r.d().State.PutObject(onlineDraftKind, r.draft)
	if err != nil {
		return pderr.Wrap(pderr.Internal, "state", err)
	}
	r.root.Run.Online = ref
	return nil
}

// onRow reports a row whose write path is "on": nothing was applied
// offline, so its base at P10 is the entering side's re-read.
func onRow(m *compat.Manifest, row model.RowID) bool {
	if m == nil {
		return false
	}
	spec, ok := m.Row(row)
	return ok && spec.WritePath == compat.PathOn
}

// publishOnline is P10's publish point, between recordProfiles and the
// root commit: the queue gets root.Epoch after recordProfiles (switch:
// incremented; sync: unchanged). An empty draft clears Root.Online.
func publishOnline(ctx context.Context, o *Orchestrator, root *state.Root, m *compat.Manifest, d online.Draft, runID string, to, from ids.Alias, rep model.Replica) bool {
	if !o.onlineActive(m) || d.Empty() {
		root.Online = nil
		return false
	}
	h := online.QueueHeader{ID: runID, Profile: to, From: from, Replica: rep, Epoch: root.Epoch, Spec: m.Online}
	sha, err := o.d.Online.Publish(ctx, d, h)
	if err != nil {
		root.Online = nil
		o.progress(P10Commit, "warn", i18n.Tx(i18n.ProgressP10OnlineNotQueued))
		return false
	}
	root.Online = &state.OnlineRecord{Queue: runID, Profile: to, From: from, Replica: rep, Epoch: root.Epoch,
		Created: o.now(), State: string(online.QueuePending), Counts: d.Counts, StepsSHA: sha}
	return true
}

// afterPublish removes an older queue when nothing was published.
func (o *Orchestrator) afterPublish(ctx context.Context, published bool) {
	if o.d.Online == nil || o.d.ReadOnly || published {
		return
	}
	_ = o.d.Online.Invalidate(ctx, "superseded")
}

// onlineP0 runs under the run lock before anything is read: confirmations
// of an earlier queue's done steps, then the lease is broken so no call
// of the old queue passes while this run works.
func (r *run) onlineP0(ctx context.Context) error {
	d := r.d()
	if d.Online == nil || d.ReadOnly {
		return nil
	}
	if r.o.closeArtifactsRecord(r.root) {
		if err := r.o.commit(r.root); err != nil {
			return err
		}
	}
	if res, err := r.o.confirmPending(ctx, r.root, r.manifest); err == nil && res != nil && res.changed {
		if err := r.o.commit(r.root); err != nil {
			return err
		}
	}
	if err := d.Online.BreakLease(ctx); err != nil {
		r.warn(P0Preflight, i18n.Tx(i18n.WarnOnlineLeaseNotRemoved))
	}
	return nil
}

// captureReopen is the P1 capture's reopen mapping, used when the
// invoking session was not found by its CLI session id.
func (r *run) captureReopen(ctx context.Context) ids.SessionID {
	d := r.d()
	if !r.o.onlineActive(r.manifest) || r.root == nil {
		return ""
	}
	snap, err := d.Online.Capture(ctx, r.S.Profile.Alias, r.S.Replica, r.root.Epoch)
	if err != nil || snap == nil {
		return ""
	}
	return snap.ReopenSession
}

// onlineLaunched records the launch time in the published queue (P11).
func (r *run) onlineLaunched(ctx context.Context, t0 time.Time) {
	d := r.d()
	if d.Online == nil || r.root.Online == nil || r.root.Online.Queue != r.res.RunID {
		return
	}
	_ = d.Online.SetLaunched(ctx, r.res.RunID, t0)
}

// OnlineConfirmInput is `online done`'s call.
type OnlineConfirmInput struct {
	Queue  string
	Caller online.Caller
}

// OnlineConfirmResult is the outcome of `online done`.
type OnlineConfirmResult struct {
	State            string             `json:"state"`
	Queue            string             `json:"queue,omitempty"`
	Profile          ids.Alias          `json:"profile,omitempty"`
	Counts           model.OnlineCounts `json:"counts"`
	Steps            []online.Step      `json:"-"`
	BasesAdvanced    int                `json:"bases_advanced"`
	ContestedCleared int                `json:"contested_cleared"`
	ContestedLeft    int                `json:"contested_left"`
	RootBusy         bool               `json:"root_busy"`
	changed          bool
}

// OnlineConfirm is `online done`: under the run lock (then the online
// lock) it marks the queue complete, moves the entering profile's bases
// to the confirmed values, clears contested entries and records the
// queue complete in root. With the run lock busy it still completes the
// queue and reports RootBusy; the next done, verify or switch applies the
// confirmation.
func (o *Orchestrator) OnlineConfirm(ctx context.Context, in OnlineConfirmInput) (*OnlineConfirmResult, error) {
	d := o.d
	if d.ReadOnly {
		return nil, pderr.NewT(pderr.Usage, "online done", i18n.ErrOnlineDoneReadOnly)
	}
	if d.Online == nil {
		return &OnlineConfirmResult{State: online.StateNone}, nil
	}
	var root *state.Root
	l, lerr := d.State.Lock(ctx, onlineRootWait)
	rootBusy := pderr.CodeOf(lerr) == pderr.RunLockBusy
	switch {
	case lerr == nil:
		defer releaseLock(l)
	case !rootBusy:
		return nil, lerr
	}
	root, err := o.loadRoot()
	if err != nil {
		return nil, err
	}
	if root.Run != nil {
		return nil, pderr.NewT(pderr.NeedsRecover, "online done", i18n.ErrInterrupted)
	}
	rec := root.Online
	if rec == nil || rec.Queue != in.Queue || rec.Epoch != root.Epoch || rec.Profile != root.Active {
		return &OnlineConfirmResult{State: online.StateStale, Queue: in.Queue}, nil
	}
	if rec.Kind != "" {
		return &OnlineConfirmResult{State: online.StateNone, Queue: in.Queue}, nil
	}
	q, err := d.Online.Finish(ctx, in.Queue, in.Caller)
	if errors.Is(err, online.ErrBusyElsewhere) {
		return &OnlineConfirmResult{State: online.StateBusyElsewhere, Queue: in.Queue}, nil
	}
	if err != nil {
		return nil, err
	}
	if q == nil {
		return &OnlineConfirmResult{State: online.StateStale, Queue: in.Queue}, nil
	}
	res := &OnlineConfirmResult{State: string(online.QueueComplete), Queue: q.ID, Profile: q.Profile, Steps: q.Steps,
		Counts: d.Online.QueueCounts(q), RootBusy: rootBusy}
	if rootBusy {
		return res, nil
	}
	m := o.currentManifest(ctx)
	conf, err := o.confirmPending(ctx, root, m)
	if err != nil {
		return nil, err
	}
	if conf != nil {
		res.BasesAdvanced, res.ContestedCleared, res.ContestedLeft = conf.BasesAdvanced, conf.ContestedCleared, conf.ContestedLeft
	}
	root.Online.State = string(online.QueueComplete)
	root.Online.Counts = res.Counts
	if err := o.commit(root); err != nil {
		return nil, err
	}
	return res, nil
}

// currentManifest is the manifest of the installed Claude (nil when it
// cannot be found).
func (o *Orchestrator) currentManifest(ctx context.Context) *compat.Manifest {
	b, err := o.d.App.Locate(ctx, o.d.Config.App.BundleID)
	if err != nil {
		return nil
	}
	m, _, err := o.d.Compat.Manifest(ctx, b.Version)
	if err != nil {
		return nil
	}
	return m
}

// confirmPending applies the confirmations of root.Online's done steps
// that are not in Confirmed yet (16.4): T's sidebar-view base items move
// to the observed values the steps set, and contested entries go or
// follow the live state. The caller holds the run lock and commits.
// Without a usable record nothing happens (nil, nil).
func (o *Orchestrator) confirmPending(ctx context.Context, root *state.Root, m *compat.Manifest) (*OnlineConfirmResult, error) {
	d := o.d
	rec := root.Online
	if d.Online == nil || rec == nil || rec.Kind != "" || rec.Epoch != root.Epoch {
		return nil, nil
	}
	steps, err := d.Online.Resolved(ctx, rec.Queue)
	if err != nil || len(steps) == 0 {
		return nil, nil
	}
	kT := state.BaseKey{Alias: rec.Profile, Replica: rec.Replica, Row: model.RowSidebarView}
	bases := online.ConfirmBases{Contested: map[string]model.Contested{}}
	if ref, ok := root.Bases[kT.String()]; ok {
		bases.SidebarBase = o.object(ref)
	}
	if ref, ok := root.Bases[state.BaseKey{Alias: rec.Profile, Replica: rec.Replica, Row: model.RowPins}.String()]; ok {
		bases.PinsBase = o.object(ref)
	}
	for _, c := range contestedFor(root, kT) {
		bases.Contested[c.Key] = c
	}
	if m != nil {
		bases.Spec = m.Online.Sidebar
		if rec.Profile == root.Active {
			bases.Observed = o.observedSidebar(m, rec.Replica)
		}
	}
	e := online.ConfirmPlan(rec, steps, bases)
	res := &OnlineConfirmResult{BasesAdvanced: e.BasesAdvanced, ContestedCleared: e.ContestedCleared}
	if moved, err := o.pinsConfirmed(root, rec, e.Pins); err != nil {
		return nil, err
	} else if moved {
		res.changed = true
		if e.Pins.Moved {
			res.BasesAdvanced++
		}
	}
	if e.SidebarBase != nil {
		ref, err := d.State.PutSnapshot(e.SidebarBase)
		if err != nil {
			return nil, pderr.Wrap(pderr.Internal, "state", err)
		}
		root.Bases[kT.String()] = ref
		res.changed = true
	}
	for _, k := range e.ContestedDelete {
		delete(root.Contested, state.ContestedKey(kT, k))
		res.changed = true
	}
	for k, c := range e.ContestedUpdate {
		root.Contested[state.ContestedKey(kT, k)] = c
		res.changed = true
	}
	for _, id := range e.Confirmed {
		if !slices.Contains(rec.Confirmed, id) {
			rec.Confirmed = append(rec.Confirmed, id)
			res.changed = true
		}
	}
	res.ContestedLeft = len(contestedFor(root, kT))
	if res.ContestedLeft == 0 && root.LastVerify != nil && slices.Contains(root.LastVerify.Drift, model.RowSidebarView) {
		root.LastVerify.Drift = slices.DeleteFunc(slices.Clone(root.LastVerify.Drift), func(r model.RowID) bool { return r == model.RowSidebarView })
		res.changed = true
	}
	return res, nil
}

// observedSidebar reads the active folder's sidebar-view items from the
// config mirror (as P12 does); nil when it cannot be read.
func (o *Orchestrator) observedSidebar(m *compat.Manifest, rep model.Replica) *model.RowSnapshot {
	spec, ok := m.Row(model.RowSidebarView)
	mr, isMirror := o.d.Adapters[model.RowSidebarView].(sources.MirrorReader)
	if !ok || !isMirror {
		return nil
	}
	name := spec.MirrorFile
	if name == "" {
		name = sources.DesktopConfigFile
	}
	mirror, found, err := readContained(o.claudePath(), name)
	if err != nil || !found {
		return nil
	}
	snap, err := mr.MirrorItems(spec.Expand(rep), mirror)
	if err != nil {
		return nil
	}
	return snap
}

// dropOnline invalidates the queue (rollback, rebase, profile rename or
// remove of its profile) and clears root.Online; the caller commits.
func (o *Orchestrator) dropOnline(ctx context.Context, root *state.Root, reason string) {
	if root.Online == nil && o.d.Online == nil {
		return
	}
	root.Online = nil
	if o.d.Online != nil && !o.d.ReadOnly {
		_ = o.d.Online.Invalidate(ctx, reason)
	}
}

// onlineStatus is Status.Online from root.Online only (no file read, G5).
func onlineStatus(root *state.Root) *OnlineStatus {
	rec := root.Online
	if rec == nil {
		return nil
	}
	return &OnlineStatus{State: rec.State, Profile: rec.Profile, Counts: rec.Counts, Kind: rec.Kind}
}

// OnlineStatus is the online part of `status`.
type OnlineStatus struct {
	State   string             `json:"state"`
	Profile ids.Alias          `json:"profile"`
	Kind    string             `json:"kind,omitempty"`
	Counts  model.OnlineCounts `json:"counts"`
}

// Pending is the number of changes still waiting (0 unless the queue is
// pending or active).
func (s *OnlineStatus) Pending() int {
	if s == nil || (s.State != string(online.QueuePending) && s.State != string(online.QueueActive)) {
		return 0
	}
	c := s.Counts
	return c.PinsAdd + c.PinsRemove + c.Sidebar + c.Other
}

// confirmOnline is recover's first online step: the confirmations of the
// queue's done steps (as at P0), before the run is settled.
func (rc *recovery) confirmOnline(ctx context.Context) {
	o := rc.o
	if o.d.Online == nil {
		return
	}
	if o.closeArtifactsRecord(rc.root) {
		_ = o.commit(rc.root)
	}
	if res, err := o.confirmPending(ctx, rc.root, rc.manifest); err == nil && res != nil && res.changed {
		_ = o.commit(rc.root)
	}
}

// cleanupOnline is recover's last online step: stale leases, invalid
// queues (an orphan published before a crash) and old files go.
func (rc *recovery) cleanupOnline(ctx context.Context) {
	if rc.o.d.Online == nil || rc.root == nil {
		return
	}
	_ = rc.o.d.Online.Cleanup(ctx, rc.root)
}

// republishOnline publishes a continued run's draft from Run.Online, as
// P10 does (idempotent: same id). The caller commits.
func (rc *recovery) republishOnline(ctx context.Context) bool {
	o := rc.o
	run := rc.root.Run
	if run == nil || run.Online == "" || !o.onlineActive(rc.manifest) {
		rc.root.Online = nil
		return false
	}
	var d online.Draft
	if err := o.d.State.GetObject(run.Online, onlineDraftKind, &d); err != nil {
		rc.root.Online = nil
		return false
	}
	t := run.To
	rep := model.Replica{}
	if ps := rc.root.Profiles[t]; ps != nil {
		rep = ps.Replica
	}
	return publishOnline(ctx, o, rc.root, rc.manifest, d, run.ID, t, run.From, rep)
}

// verifyConfirm applies, under verify's run lock, the confirmations a
// busy `online done` could not commit: a queue whose head is complete
// while root.Online is not.
func (o *Orchestrator) verifyConfirm(ctx context.Context, root *state.Root, m *compat.Manifest) error {
	if o.closeArtifactsRecord(root) {
		return o.commit(root)
	}
	rec := root.Online
	if o.d.Online == nil || rec == nil || rec.Kind != "" || rec.State == string(online.QueueComplete) {
		return nil
	}
	h, err := o.d.Online.ReadHead()
	if err != nil || h == nil || h.ID != rec.Queue || h.State != online.QueueComplete {
		return nil
	}
	if _, err := o.confirmPending(ctx, root, m); err != nil {
		return err
	}
	rec.State = string(online.QueueComplete)
	if steps, err := o.d.Online.Resolved(ctx, rec.Queue); err == nil && steps != nil {
		rec.Counts = o.d.Online.QueueCounts(&online.Queue{Steps: steps})
	}
	return o.commit(root)
}

// OnlineCancel records root.Online as cancelled when the run lock is free
// right now (it never waits: `online cancel` must work during a run).
func (o *Orchestrator) OnlineCancel(ctx context.Context, queue string) (bool, error) {
	if o.d.ReadOnly {
		return false, nil
	}
	l, err := o.d.State.Lock(ctx, 0)
	if err != nil {
		return false, nil
	}
	defer releaseLock(l)
	root, err := o.loadRoot()
	if err != nil || root.Online == nil || (queue != "" && root.Online.Queue != queue) {
		return false, err
	}
	root.Online.State = string(online.QueueCancelled)
	if err := o.commit(root); err != nil {
		return false, err
	}
	return true, nil
}

// onlineNotice adds the fixed reconcile hint to a notification text when
// this run published a queue (P12: the notification mentions it).
func (r *run) onlineNotice(text string) string {
	if r.root != nil && r.root.Online != nil && r.root.Online.Queue == r.res.RunID {
		return text + " " + i18n.En(i18n.SwitchOnlineNext)
	}
	return text
}
