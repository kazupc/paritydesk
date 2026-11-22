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
	// rows limits the comparison to these rows (nil: every enabled row).
	rows []model.RowID
}

// Verify runs P12 for the last run: waits for LOCK and a stable config
// mirror, compares verify rows (reading JSON only, never LevelDB), marks
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
		return nil, pderr.New(pderr.Usage, "verify", "no active profile; run paritydesk init")
	}
	if root.Run != nil {
		return nil, pderr.New(pderr.NeedsRecover, "verify", "a run was interrupted; run paritydesk recover")
	}
	b, err := d.App.Locate(ctx, d.Config.App.BundleID)
	if err != nil {
		return nil, pderr.Wrap(pderr.CompatRefused, "verify", err)
	}
	m, _, err := d.Compat.Manifest(ctx, b.Version)
	if err != nil || m == nil {
		return nil, pderr.New(pderr.CompatRefused, "verify", "no compatibility manifest covers this Claude version")
	}
	var from int64
	if n, err := d.App.LogSize(); err == nil && n > verifyLogTail {
		from = n - verifyLogTail
	}
	return o.verifyCore(ctx, verifyIn{
		root: root, alias: root.Active, replica: root.Profiles[root.Active].Replica,
		manifest: m, sig: m.Signals, from: from,
	})
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
	if !o.waitSettled(ctx, claude, mirrorName) {
		o.progress(P12Verify, "warn", "Claude did not settle in time; verifying what is there")
	}
	mirror, _, err := readContained(claude, mirrorName)
	if err != nil {
		return nil, pderr.Wrap(pderr.Internal, "verify", err)
	}
	res := &VerifyResult{PairingOK: true}
	if err := o.compareMirror(in, mirror, res); err != nil {
		return nil, err
	}
	o.readSignals(ctx, in, res)
	identityOK := o.activeIdentityIs(ctx, in.replica.Account)
	in.root.LastVerify = &state.VerifyRecord{RunID: in.runID, At: o.now(), Drift: res.Drift,
		SignIn: res.SignInNeeded, Focused: res.SessionFocused}
	if !d.ReadOnly && ctx.Err() == nil {
		if err := o.commit(in.root); err != nil {
			return res, err
		}
	}
	switch {
	case !identityOK:
		return res, pderr.New(pderr.IdentityMismatch, "verify", "the active folder does not hold "+string(in.alias)+"'s account")
	case res.SignInNeeded:
		return res, pderr.New(pderr.NeedsSignIn, "verify", "sign in with "+string(in.alias)+"'s account only; every other account stays signed in")
	case len(res.Drift) > 0:
		return res, pderr.New(pderr.VerifyDrift, "verify", "some values differ from what was written; the drift is recorded")
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

// compareMirror compares every mirrored row with its base; drift stores
// per-item contested entries and sets the base to what was observed.
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
		res.Drift = append(res.Drift, r)
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
