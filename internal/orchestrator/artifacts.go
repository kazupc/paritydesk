package orchestrator

import (
	"context"
	"time"

	"github.com/kazupc/paritydesk/internal/artifacts"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/online"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/state"
)

// artifactsRootWait is how long an artifacts round waits for the run
// lock (spec-artifacts 6.2 step 6).
const artifactsRootWait = 3 * time.Second

// ArtifactsPreview is what a plan shows about artifacts (P2): the
// leaving profile was not synced while it was active, and how many
// artifacts the entering profile would get. It is not part of the plan
// hash.
type ArtifactsPreview struct {
	// Unsynced is the leaving profile's alias when it was synced before
	// but not since it became active ("" otherwise).
	Unsynced ids.Alias
	// Due is the number of pairs a sync on the entering profile would
	// publish; To is that profile.
	Due int
	To  ids.Alias
}

// artifactsActive reports whether artifact sync is on for manifest m:
// config sync.artifacts, the manifest's [artifacts] and [online] tables,
// and the online layer (artifact rounds run on it; online.reconcile =
// "off" turns it off). Profile rename and remove do not ask this: they
// keep the archive's aliases right whenever there is an archive.
func (o *Orchestrator) artifactsActive(m *compat.Manifest) bool {
	return o.d.Artifacts != nil && m != nil && m.Artifacts.Enabled && m.Online.Enabled && o.d.Config != nil &&
		o.d.Config.Sync.Artifacts && o.d.Config.Online.Reconcile != config.OnlineReconcileOff
}

// artifactsPreview is P2's artifacts part: the two plan lines' facts.
// It reads the archive only (no lock, never the app).
func (r *run) artifactsPreview() {
	if !r.o.artifactsActive(r.manifest) || r.root == nil || r.S == nil || r.T == nil {
		return
	}
	a := r.d().Artifacts
	p := &ArtifactsPreview{To: r.T.Profile.Alias}
	if epoch, ok, err := a.LastSyncEpoch(r.S.Profile.Alias); err == nil && ok && epoch != r.root.Epoch {
		p.Unsynced = r.S.Profile.Alias
	}
	if n, err := a.Due(r.T.Profile.Alias, r.T.Replica); err == nil {
		p.Due = n
	}
	if p.Unsynced == "" && p.Due == 0 {
		return
	}
	r.res.Artifacts = p
}

// artifactsDue is P10's artifacts part, after the commit: due.json for
// the entering profile and the new epoch. A busy archive lock skips it;
// the run still succeeds.
func (r *run) artifactsDue(ctx context.Context) {
	if !r.o.artifactsActive(r.manifest) || r.d().ReadOnly || r.root == nil {
		return
	}
	_ = r.d().Artifacts.WriteDue(ctx, r.T.Profile.Alias, r.T.Replica, r.root.Epoch)
}

// artifactsRenamed moves a renamed profile's copies in the archive.
func (o *Orchestrator) artifactsRenamed(ctx context.Context, from, to ids.Alias) {
	if o.d.Artifacts == nil || o.d.ReadOnly {
		return
	}
	if err := o.d.Artifacts.RenameProfile(ctx, from, to); err != nil {
		o.progress(P10Commit, "warn", i18n.Tx(i18n.ArtifactsRenameFailed))
	}
}

// artifactsRemoved forgets a removed profile's record in the archive
// (its copies stay, reported as orphaned).
func (o *Orchestrator) artifactsRemoved(ctx context.Context, alias ids.Alias) {
	if o.d.Artifacts == nil || o.d.ReadOnly {
		return
	}
	_ = o.d.Artifacts.RemoveProfile(ctx, alias)
}

// ArtifactsRound runs f under the run lock (3 s; busy: exit 11) with no
// open run, then publishes the draft f returns (if any) and records it
// in Root.Online as an artifacts round queue, in one commit (A3).
func (o *Orchestrator) ArtifactsRound(ctx context.Context, f func(root *state.Root) (*online.Draft, *online.QueueHeader, error)) (string, error) {
	d := o.d
	if d.ReadOnly {
		return "", pderr.NewT(pderr.Usage, "artifacts", i18n.ArtifactsErrReadOnly)
	}
	if d.Online == nil {
		return "", pderr.NewT(pderr.Usage, "artifacts", i18n.ArtifactsOff)
	}
	l, err := d.State.Lock(ctx, artifactsRootWait)
	if err != nil {
		if pderr.CodeOf(err) == pderr.RunLockBusy {
			return "", pderr.NewT(pderr.RunLockBusy, "artifacts", i18n.OnlineErrBusy)
		}
		return "", err
	}
	defer releaseLock(l)
	root, err := o.loadRoot()
	if err != nil {
		return "", err
	}
	if root.Run != nil {
		return "", pderr.NewT(pderr.NeedsRecover, "artifacts", i18n.ErrInterrupted)
	}
	draft, h, err := f(root)
	if err != nil || draft == nil || h == nil {
		return "", err
	}
	if h.Profile != root.Active || h.Epoch != root.Epoch {
		return "", pderr.NewT(pderr.Internal, "artifacts", i18n.ArtifactsErrRoundInvalid)
	}
	h.Kind = artifacts.KindArtifacts
	sha, err := d.Online.Publish(ctx, *draft, *h)
	if err != nil {
		return "", err
	}
	root.Online = &state.OnlineRecord{Queue: h.ID, Kind: artifacts.KindArtifacts, Profile: h.Profile, From: h.From,
		Replica: h.Replica, Epoch: root.Epoch, Created: o.now(), State: string(online.QueuePending), Counts: draft.Counts,
		StepsSHA: sha}
	if err := o.commit(root); err != nil {
		return "", err
	}
	return sha, nil
}

// PublishArtifactsRound publishes an artifacts round's draft under the
// run lock and records it in Root.Online (A3).
func (o *Orchestrator) PublishArtifactsRound(ctx context.Context, d online.Draft, h online.QueueHeader) (string, error) {
	return o.ArtifactsRound(ctx, func(*state.Root) (*online.Draft, *online.QueueHeader, error) { return &d, &h, nil })
}

// CloseArtifactsQueue records an artifacts round queue complete in
// Root.Online when the run lock is free within 3 s; false when it was
// busy (the next holder closes it: Cleanup keeps it until then).
func (o *Orchestrator) CloseArtifactsQueue(ctx context.Context, queueID string) (bool, error) {
	if o.d.ReadOnly {
		return false, nil
	}
	l, err := o.d.State.Lock(ctx, artifactsRootWait)
	if err != nil {
		if pderr.CodeOf(err) == pderr.RunLockBusy {
			return false, nil
		}
		return false, err
	}
	defer releaseLock(l)
	root, err := o.loadRoot()
	if err != nil {
		return false, err
	}
	rec := root.Online
	if rec == nil || rec.Kind != artifacts.KindArtifacts || rec.Queue != queueID || rec.State == string(online.QueueComplete) {
		return true, nil
	}
	rec.State = string(online.QueueComplete)
	return true, o.commit(root)
}

// closeArtifactsRecord closes root.Online when it records an artifacts
// round queue whose head is complete or cancelled while the record is
// not: the next run-lock holder does what a busy `artifacts done` could
// not (spec-artifacts 6.4, spec-online 4.5). The caller holds the run
// lock and commits when it returns true.
func (o *Orchestrator) closeArtifactsRecord(root *state.Root) bool {
	rec := root.Online
	if o.d.Online == nil || o.d.ReadOnly || root == nil || rec == nil || rec.Kind != artifacts.KindArtifacts ||
		rec.State == string(online.QueueComplete) || rec.State == string(online.QueueCancelled) {
		return false
	}
	h, err := o.d.Online.ReadHead()
	if err != nil || h == nil || h.ID != rec.Queue || (h.State != online.QueueComplete && h.State != online.QueueCancelled) {
		return false
	}
	rec.State = string(h.State)
	return true
}
