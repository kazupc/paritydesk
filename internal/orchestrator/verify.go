package orchestrator

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/kazupc/paritydesk/internal/app"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/rows"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/state"
)

// verifyIn is one P12 run.
type verifyIn struct {
	root     *state.Root
	alias    ids.Alias
	replica  model.Replica
	manifest *compat.Manifest
	sig      compat.Signals
	from     int64
	session  ids.SessionID
	runID    string
	// record: drift is stored as contested entries and the bases move to
	// what was observed. Only P12 of a run, or the first standalone verify
	// shortly after a run's launch, records. Any other verify only
	// reports, so the user's own later edits are never taken for drift.
	record bool
	// rows limits the comparison to these rows (nil: every enabled row).
	rows []model.RowID
	// settled: the caller already waited for Claude to settle.
	settled bool
}

// Verify runs P12 for the last run: waits for LOCK and a stable config
// mirror, compares verify rows (reading the config mirror, and Local
// Storage only through a snapshot copy for rows kept nowhere else), marks
// drifted rows contested, and checks main.log signals.
func (o *Orchestrator) Verify(ctx context.Context) (*VerifyResult, error) {
	d := o.d
	if !d.ReadOnly {
		l, err := d.State.Lock(ctx, 0)
		if err != nil {
			return nil, err
		}
		defer releaseLock(l)
	}
	root, err := o.loadRoot()
	if err != nil {
		return nil, err
	}
	if root.Active == "" || root.Profiles[root.Active] == nil {
		return nil, pderr.NewT(pderr.Usage, "verify", i18n.ErrVerifyNoActive)
	}
	if root.Run != nil {
		return nil, pderr.NewT(pderr.NeedsRecover, "verify", i18n.ErrInterrupted)
	}
	b, err := d.App.Locate(ctx, d.Config.App.BundleID)
	if err != nil {
		return nil, pderr.Wrap(pderr.CompatRefused, "verify", err)
	}
	m, _, err := d.Compat.Manifest(ctx, b.Version)
	if err != nil || m == nil {
		return nil, pderr.NewT(pderr.CompatRefused, "verify", i18n.ErrVerifyNoManifest)
	}
	if !d.ReadOnly {
		if err := o.verifyConfirm(ctx, root, m); err != nil {
			return nil, err
		}
	}
	var from int64
	if n, err := d.App.LogSize(); err == nil && n > verifyLogTail {
		from = n - verifyLogTail
	}
	in := verifyIn{
		root: root, alias: root.Active, replica: root.Profiles[root.Active].Replica,
		manifest: m, sig: m.Signals, from: from,
	}
	if last, ok := lastRunInto(root, root.Active); ok {
		in.runID = last.RunID
		in.record = o.now().Sub(last.At) <= verifyRecordWindow &&
			(root.LastVerify == nil || root.LastVerify.RunID != last.RunID)
	}
	return o.verifyCore(ctx, in)
}

// lastRunInto returns the newest generation whose run entered alias.
func lastRunInto(root *state.Root, alias ids.Alias) (state.GenerationRef, bool) {
	for i := len(root.Generations) - 1; i >= 0; i-- {
		if g := root.Generations[i]; g.Target == alias && g.Purpose == "" {
			return g, true
		}
	}
	return state.GenerationRef{}, false
}

// verifyCore is P12 under the caller's lock.
func (o *Orchestrator) verifyCore(ctx context.Context, in verifyIn) (*VerifyResult, error) {
	d := o.d
	claude := o.claudePath()
	mirrorName := sources.DesktopConfigFile
	for _, r := range model.AllRows {
		if spec, ok := in.manifest.Row(r); ok && spec.MirrorFile != "" {
			mirrorName = spec.MirrorFile
			break
		}
	}
	if !in.settled && !o.waitSettled(ctx, claude, mirrorName) {
		o.progress(P12Verify, "warn", i18n.Tx(i18n.ProgressP12NotSettled))
	}
	mirror, _, err := readContained(claude, mirrorName)
	if err != nil {
		return nil, pderr.Wrap(pderr.Internal, "verify", err)
	}
	res := &VerifyResult{PairingOK: true}
	if err := o.compareMirror(in, mirror, res); err != nil {
		return nil, err
	}
	if err := o.compareLS(ctx, in, res); err != nil {
		return nil, err
	}
	o.readSignals(ctx, in, res)
	identityOK := o.activeIdentityIs(ctx, in.replica.Account)
	if in.record {
		in.root.LastVerify = &state.VerifyRecord{RunID: in.runID, At: o.now(), Drift: res.Drift,
			SignIn: res.SignInNeeded, Focused: res.SessionFocused}
	}
	if in.record && !d.ReadOnly && ctx.Err() == nil {
		if err := o.commit(in.root); err != nil {
			return res, err
		}
	}
	switch {
	case !identityOK:
		return res, pderr.NewT(pderr.IdentityMismatch, "verify", i18n.ErrVerifyIdentity, in.alias)
	case res.SignInNeeded:
		return res, pderr.NewT(pderr.NeedsSignIn, "verify", i18n.ErrVerifySignIn, in.alias)
	case len(res.Drift) > 0 && in.record:
		return res, pderr.NewT(pderr.VerifyDrift, "verify", i18n.ErrVerifyDrift)
	case len(res.Drift) > 0:
		return res, pderr.NewT(pderr.VerifyDrift, "verify", i18n.ErrVerifyDriftNotRecorded)
	}
	return res, nil
}

// waitSettled waits until LOCK is held and the mirror's mtime has been
// stable for verifyStable, up to verifyTimeout.
func (o *Orchestrator) waitSettled(ctx context.Context, folder, mirror string) bool {
	d := o.d
	lock := filepath.Join(sources.LevelDBPath(folder), "LOCK")
	deadline := o.now().Add(verifyTimeout)
	var last time.Time
	var stableSince time.Time
	for {
		held, _ := d.App.LockHeld(lock)
		mt := mtime(filepath.Join(folder, mirror))
		now := o.now()
		if !mt.Equal(last) || stableSince.IsZero() {
			last, stableSince = mt, now
		}
		if held && now.Sub(stableSince) >= verifyStable {
			return true
		}
		if !now.Before(deadline) {
			return false
		}
		if err := d.Clock.Sleep(ctx, verifyPoll); err != nil {
			return false
		}
	}
}

func mtime(p string) time.Time {
	fi, err := os.Stat(p)
	if errors.Is(err, fs.ErrNotExist) || err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// compareMirror compares every mirrored row with its base. When in.record
// is set, drift stores per-item contested entries and sets the base to
// what was observed; otherwise it is only reported.
func (o *Orchestrator) compareMirror(in verifyIn, mirror []byte, res *VerifyResult) error {
	d := o.d
	rs := in.rows
	if rs == nil {
		rs = model.AllRows
	}
	for _, r := range rs {
		spec, ok := in.manifest.Row(r)
		mr, isMirror := d.Adapters[r].(sources.MirrorReader)
		if !ok || !isMirror || spec.MirrorFile == "" || !d.Config.RowEnabled(r) {
			continue
		}
		key := state.BaseKey{Alias: in.alias, Replica: in.replica, Row: r}
		ref, ok := in.root.Bases[key.String()]
		if !ok {
			continue
		}
		intended, err := d.State.GetSnapshot(ref)
		if err != nil {
			continue
		}
		observed, err := mr.MirrorItems(spec.Expand(in.replica), mirror)
		if err != nil {
			return pderr.Wrap(pderr.Internal, "verify", err)
		}
		contested, err := driftOf(r, in.replica, intended, observed)
		if err != nil {
			return pderr.Wrap(pderr.Internal, "verify", err)
		}
		if len(contested) == 0 {
			continue
		}
		if spec.Contested {
			res.Replaced = append(res.Replaced, r)
		} else {
			res.Drift = append(res.Drift, r)
		}
		if !in.record {
			continue
		}
		if in.root.Contested == nil {
			in.root.Contested = map[string]model.Contested{}
		}
		for _, c := range contested {
			in.root.Contested[state.ContestedKey(key, c.Key)] = c
		}
		newRef, err := d.State.PutSnapshot(observed)
		if err != nil {
			return pderr.Wrap(pderr.Internal, "state", err)
		}
		in.root.Bases[key.String()] = newRef
	}
	return nil
}

// driftOf returns the contested entries of every drifted item, plus the
// section order (RowSnapshot.Order) of a row that keeps it outside its
// items, under the merge's stand-in key and hashes.
func driftOf(r model.RowID, rep model.Replica, intended, observed *model.RowSnapshot) ([]model.Contested, error) {
	var out []model.Contested
	for _, item := range driftedItems(intended, observed) {
		out = append(out, model.Contested{Row: r, Replica: rep, Key: item,
			ServerHash: itemHash(observed, item), IntendedHash: itemHash(intended, item)})
	}
	_, inItems := itemsOf(intended)[rows.OrderItemKey]
	_, obItems := itemsOf(observed)[rows.OrderItemKey]
	if inItems || obItems {
		return out, nil // the order is an item of this row
	}
	ih, err := rows.OrderHash(orderOf(intended))
	if err != nil {
		return nil, err
	}
	oh, err := rows.OrderHash(orderOf(observed))
	if err != nil {
		return nil, err
	}
	if ih != oh {
		out = append(out, model.Contested{Row: r, Replica: rep, Key: rows.OrderItemKey, ServerHash: oh, IntendedHash: ih})
	}
	return out, nil
}

func orderOf(s *model.RowSnapshot) []string {
	if s == nil {
		return nil
	}
	return s.Order
}

// driftedItems lists the item keys whose hashes differ, sorted.
func driftedItems(a, b *model.RowSnapshot) []string {
	keys := map[string]bool{}
	for k := range itemsOf(a) {
		keys[k] = true
	}
	for k := range itemsOf(b) {
		keys[k] = true
	}
	var out []string
	for k := range keys {
		if itemHash(a, k) != itemHash(b, k) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func itemHash(s *model.RowSnapshot, k string) string {
	if s == nil {
		return ""
	}
	return s.Items[k].Hash
}

// readSignals reads main.log events since the launch.
func (o *Orchestrator) readSignals(ctx context.Context, in verifyIn, res *VerifyResult) {
	evs, _, err := o.d.App.LogEvents(ctx, in.from, in.sig, in.session)
	if err != nil {
		return
	}
	for _, e := range evs {
		switch e {
		case app.EvFocusedSession:
			res.SessionFocused = true
		case app.EvPairingDisabled:
			res.PairingOK = false
		case app.EvLoginRequired:
			res.SignInNeeded = true
		case app.EvBridgeConnected:
			res.BridgeSeen = true
		}
	}
}

// activeIdentityIs checks Claude/'s identity through a folder scan.
func (o *Orchestrator) activeIdentityIs(ctx context.Context, a ids.AccountID) bool {
	folders, err := o.d.Folders.Scan(ctx)
	if err != nil {
		return false
	}
	for _, f := range folders {
		if f.Name == ids.DefaultFolder {
			return f.Identity != nil && f.Identity.Account == a
		}
	}
	return false
}

// VerifyAfter waits for Claude to settle, then verifies the run runID
// under the run lock. It records nothing, and reports nothing, when a run
// is open or a newer run has entered the active profile since: that run
// checks for itself.
func (o *Orchestrator) VerifyAfter(ctx context.Context, runID string) (*VerifyResult, error) {
	d := o.d
	b, err := d.App.Locate(ctx, d.Config.App.BundleID)
	if err != nil {
		return nil, pderr.Wrap(pderr.CompatRefused, "verify", err)
	}
	m, _, err := d.Compat.Manifest(ctx, b.Version)
	if err != nil || m == nil {
		return nil, pderr.NewT(pderr.CompatRefused, "verify", i18n.ErrVerifyNoManifest)
	}
	mirrorName := sources.DesktopConfigFile
	for _, r := range model.AllRows {
		if spec, ok := m.Row(r); ok && spec.MirrorFile != "" {
			mirrorName = spec.MirrorFile
			break
		}
	}
	if !o.waitSettled(ctx, o.claudePath(), mirrorName) {
		o.progress(P12Verify, "warn", i18n.Tx(i18n.ProgressP12NotSettled))
	}
	if !d.ReadOnly {
		l, err := d.State.Lock(ctx, DetachedLockWait)
		if err != nil {
			return nil, err
		}
		defer releaseLock(l)
	}
	root, err := o.loadRoot()
	if err != nil {
		return nil, err
	}
	last, ok := lastRunInto(root, root.Active)
	if root.Run != nil || !ok || last.RunID != runID || root.Profiles[root.Active] == nil {
		return &VerifyResult{PairingOK: true}, nil
	}
	var from int64
	if n, err := d.App.LogSize(); err == nil && n > verifyLogTail {
		from = n - verifyLogTail
	}
	in := verifyIn{
		root: root, alias: root.Active, replica: root.Profiles[root.Active].Replica,
		manifest: m, sig: m.Signals, from: from, runID: runID, settled: true,
		record: o.now().Sub(last.At) <= verifyRecordWindow && (root.LastVerify == nil || root.LastVerify.RunID != runID),
	}
	return o.verifyCore(ctx, in)
}
