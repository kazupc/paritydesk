package rows

import (
	"fmt"
	"sort"

	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/merge"
	"github.com/kazupc/paritydesk/internal/model"
)

// Field roles read from Policy.Fields. The first five are the manifest
// roles named by the plan; the others are additional roles this package
// reads when the manifest provides them (absent roles disable the rule
// that needs them).
const (
	RolePerField     = "per_field"     // fields merged one by one (D8)
	RoleAccountBound = "account_bound" // fields kept from the target (D10)
	RoleGrant        = "grant"         // sessions: security grants, never carried between accounts
	RoleRunState     = "run_state"     // fields never carried (D8, H11)
	RoleResumeMark   = "resume_mark"   // sessions: the app's quit-resume marks (S15)
	RoleMaxTime      = "max_time"      // fields that take the maximum
	RoleParent       = "parent"        // dotted paths naming a family parent key
	RoleWorktree     = "worktree"      // sessions: fields that move together as one worktree
	RolePermission   = "permission"    // sessions: the permission-mode field (D8)
	RoleEnvRef       = "env_ref"       // sessions: fields holding environment ids
	RoleProjectPath  = "project_path"  // sessions: field matched by sync.exclude globs
	RoleName         = "name"          // environments: preset name for the fallback match
	RoleSecret       = "secret"        // dotted paths of env-var maps whose values are secret
	RoleKnownKinds   = "known_kinds"   // waiting-input: kinds merged with deletions
	RoleSessionRef   = "session_ref"   // tasks: field naming the originating session (sync.exclude ids)
	RoleWorktreePath = "worktree_path" // worktrees: the entry's directory
)

// Constant notes for changes and conflicts (never untrusted text).
const (
	noteAdded       = "new on the target"
	noteUpdated     = "newer version"
	noteDeleted     = "deleted on another profile"
	noteRevived     = "kept: newer than its tombstone"
	noteRestored    = "restored: missing without a tombstone"
	noteMoved       = "order changed"
	noteCapped      = "permission mode capped to what the target allows"
	noteRemapped    = "environment id remapped"
	noteSecret      = "secret kept: not carried across a managed edge"
	noteCollision   = "preset name matches several presets; kept both"
	noteStartedGone = "started environment of a removed session"
	noteParentKept  = "kept: parent of a kept session"
	reasonCanonical = "canonical"
)

func noteFor(k model.ChangeKind) string {
	switch k {
	case model.ChangeAdded:
		return noteAdded
	case model.ChangeUpdated:
		return noteUpdated
	case model.ChangeDeleted:
		return noteDeleted
	case model.ChangeRevived:
		return noteRevived
	case model.ChangeRestored:
		return noteRestored
	case model.ChangeMoved:
		return noteMoved
	}
	return ""
}

// pair is a key both sides changed and still hold.
type pair struct {
	key    string
	l, e   merge.Version
	winner merge.Side
}

func (p pair) win() merge.Version {
	if p.winner == merge.Leaving {
		return p.l
	}
	return p.e
}

func (p pair) lose() merge.Version {
	if p.winner == merge.Leaving {
		return p.e
	}
	return p.l
}

// fieldMergeFunc merges a record both sides changed. ok=false falls back
// to the Prefer winner; conflict=true reports the loser.
type fieldMergeFunc func(p pair) (merged model.Item, ok, conflict bool, err error)

// keyedSpec customizes the shared keyed engine for one row.
type keyedSpec struct {
	row model.RowID
	// strictTombstones: a removal counts only with a tombstone; a record
	// missing without one is restored (D9).
	strictTombstones bool
	// nativeTombstones: Desired carries tombstones for absent keys.
	nativeTombstones bool
	fieldMerge       fieldMergeFunc
	// excluded reports keys that never leave their profile.
	excluded func(key string, it *model.Item) bool
	// secretAllowed reports whether a secret item may replace cur across
	// a managed edge (sync.secrets_allow).
	secretAllowed func(key string, cand, cur *model.Item) bool
	// keepTarget reports keys whose Desired value stays the target's own.
	keepTarget func(key string, cand, cur *model.Item) bool
	// fixup runs after every key is decided and before Desired is built.
	fixup func(m *merger) error
	// transform adjusts a Desired item for the entering replica.
	transform func(key string, d model.Item, cur *model.Item) (model.Item, []model.Change, error)
	// ignored lists top-level fields that transform keeps from (or clears
	// for) each replica. A difference only in them is never a conflict:
	// no side's value is lost, because each replica keeps its own.
	ignored []string
}

// sideView is one side's harvested state, prepared for merging.
type sideView struct {
	alias     ids.Alias
	info      bool // the snapshot is present
	valid     bool
	items     map[string]model.Item
	base      map[string]model.Item
	tombs     map[string]model.Tombstone
	contested map[string]model.Contested
	epoch     uint64 // the side's live epoch (merge.Version.Epoch)
	// seeded: the base was seeded by profile adopt --from-legacy (Side.Seeded).
	seeded bool
}

func newSideView(s Side) sideView {
	v := sideView{alias: s.Alias, valid: s.BaseValid, contested: s.Contested, epoch: s.LiveEpoch, seeded: s.Seeded}
	if s.Snapshot == nil {
		return v
	}
	v.info = true
	v.items = make(map[string]model.Item, len(s.Snapshot.Items))
	for k, it := range s.Snapshot.Items {
		it.Stamp.Epoch = s.LiveEpoch
		if ts, ok := s.ToolStamps[k]; ok && ts != 0 && ts == it.Stamp.AppTime {
			it.Stamp.ToolWritten = true
		}
		v.items[k] = it
	}
	v.tombs = s.Snapshot.Tombstones
	if s.BaseValid && s.Base != nil {
		v.base = s.Base.Items
	}
	return v
}

// version returns the side's merge.Version of key k. A missing snapshot is
// "no information" (never a change). In strict mode a removal without a
// tombstone is not a change. On a side with a valid base, an item whose
// hash matches a contested hash is not a change.
func (v sideView) version(k string, strict bool) merge.Version {
	if !v.info {
		return merge.Version{}
	}
	out := merge.Version{BaseValid: v.valid, Epoch: v.epoch}
	if it, ok := v.items[k]; ok {
		out.Item = &it
	}
	if v.valid {
		if b, ok := v.base[k]; ok {
			out.Base = &b
		}
	}
	if t, ok := v.tombs[k]; ok {
		out.Tomb = &t
	}
	if strict && out.Item == nil && out.Tomb == nil {
		out.Base = nil
	}
	// Only a side with a valid base may treat a contested value as
	// unchanged here: forcing BaseValid on an invalid side would take this
	// one key out of the union merge, so a deletion from the other side
	// could win (an invalid base never deletes, C1). hideContested handles
	// the invalid side.
	if v.valid && v.isContested(k, out.Item) {
		b := *out.Item
		out.Base, out.BaseValid = &b, true
	}
	return out
}

// isContested reports whether it matches a contested hash of key k.
func (v sideView) isContested(k string, it *model.Item) bool {
	c, ok := v.contested[k]
	return ok && it != nil && (it.Hash == c.ServerHash || it.Hash == c.IntendedHash)
}

// hideContested reads a contested value on a side with an invalid base as
// absent, so a stale server value is never harvested as a user edit (C2).
// An absent item on an invalid side is never a removal. When the other
// side removed the key against its valid base, the value is kept instead:
// the union keeps it against the removal (C1).
func (v sideView) hideContested(k string, own, other merge.Version) merge.Version {
	if v.valid || !v.isContested(k, own.Item) {
		return own
	}
	if other.Item == nil && other.Changed() {
		return own
	}
	return merge.Version{Epoch: own.Epoch}
}

// inBase reports whether the side's base holds key k.
func (v sideView) inBase(k string) bool {
	_, ok := v.base[k]
	return ok
}

func (v sideView) current(k string) *model.Item {
	if it, ok := v.items[k]; ok {
		return &it
	}
	return nil
}

// mask turns a changed version into an unchanged one (its base copy).
func mask(v merge.Version) merge.Version {
	if !v.BaseValid || v.Base == nil {
		return merge.Version{BaseValid: v.BaseValid, Epoch: v.Epoch}
	}
	return merge.Version{Item: v.Base, Base: v.Base, BaseValid: true, Epoch: v.Epoch}
}

// merger holds one keyed merge in progress.
type merger struct {
	spec       keyedSpec
	pol        Policy
	l, e       sideView
	canonIn    *model.RowSnapshot // never nil
	canonKnown bool
	loIn, eoIn *model.RowSnapshot // never nil
	union      bool
	// anyUnionKey is set when some key merged as a union key (unionKey).
	anyUnionKey bool
	canon       *model.RowSnapshot
	lo, eo      *model.RowSnapshot
	hints       map[string]model.ChangeKind
	// notes override the note of a hinted change.
	notes     map[string]string
	conflicts []model.Conflict
}

func emptySnapshot(row model.RowID) *model.RowSnapshot {
	return &model.RowSnapshot{Row: row, Schema: 1, Items: map[string]model.Item{}, Tombstones: map[string]model.Tombstone{}}
}

// cloneOrEmpty deep-copies s with non-nil maps, or returns an empty
// snapshot.
func cloneOrEmpty(s *model.RowSnapshot, row model.RowID) *model.RowSnapshot {
	if s == nil {
		return emptySnapshot(row)
	}
	c := s.Clone()
	c.Row = row
	if c.Schema == 0 {
		c.Schema = 1
	}
	if c.Items == nil {
		c.Items = map[string]model.Item{}
	}
	if c.Tombstones == nil {
		c.Tombstones = map[string]model.Tombstone{}
	}
	if len(c.Order) == 0 {
		c.Order = nil
	}
	c.Stale = nil
	return c
}

func newMerger(in Input, spec keyedSpec) *merger {
	m := &merger{
		spec:       spec,
		pol:        in.Policy,
		l:          newSideView(in.Leaving),
		e:          newSideView(in.Entering),
		canonIn:    cloneOrEmpty(in.Canonical, spec.row),
		canonKnown: in.Canonical != nil,
		loIn:       cloneOrEmpty(in.LeavingOverlay, spec.row),
		eoIn:       cloneOrEmpty(in.EnteringOverlay, spec.row),
		union:      !in.Leaving.BaseValid || !in.Entering.BaseValid,
		hints:      map[string]model.ChangeKind{},
		notes:      map[string]string{},
	}
	m.canon = cloneOrEmpty(in.Canonical, spec.row)
	m.lo = cloneOrEmpty(in.LeavingOverlay, spec.row)
	m.eo = cloneOrEmpty(in.EnteringOverlay, spec.row)
	return m
}

// runKeyed merges a keyed row with the shared rules: three-way per key
// over canonical, data boundary and overlays, tombstones, contested items,
// secrets and union mode for invalid bases.
func runKeyed(in Input, spec keyedSpec) (Output, error) {
	m := newMerger(in, spec)
	for _, k := range m.keys() {
		if err := m.mergeKey(k); err != nil {
			return Output{}, fmt.Errorf("rows: %s: %w", spec.row, err)
		}
	}
	if spec.fixup != nil {
		if err := spec.fixup(m); err != nil {
			return Output{}, fmt.Errorf("rows: %s: %w", spec.row, err)
		}
	}
	desired, extra, err := m.desired()
	if err != nil {
		return Output{}, fmt.Errorf("rows: %s: %w", spec.row, err)
	}
	out := Output{
		Canonical:         m.canon,
		LeavingOverlay:    m.lo,
		EnteringOverlay:   m.eo,
		Desired:           desired,
		Conflicts:         m.conflicts,
		NeedsConfirmation: m.union || m.anyUnionKey,
	}
	out.Changes = append(m.changes(desired), extra...)
	return out, nil
}

// keys returns every key any input mentions, sorted.
func (m *merger) keys() []string {
	set := map[string]bool{}
	addItems := func(items map[string]model.Item) {
		for k := range items {
			set[k] = true
		}
	}
	addTombs := func(ts map[string]model.Tombstone) {
		for k := range ts {
			set[k] = true
		}
	}
	for _, v := range []sideView{m.l, m.e} {
		addItems(v.items)
		addItems(v.base)
		addTombs(v.tombs)
	}
	for _, s := range []*model.RowSnapshot{m.canonIn, m.loIn, m.eoIn} {
		addItems(s.Items)
		addTombs(s.Tombstones)
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (m *merger) isExcluded(k string, v merge.Version) bool {
	if m.spec.excluded == nil {
		return false
	}
	it := v.Item
	if it == nil {
		it = v.Base
	}
	return m.spec.excluded(k, it)
}

// unionKey reports whether key k merges as under an invalid base although
// the row is not in union mode: a side whose valid base was seeded by
// profile adopt --from-legacy lacks k in that base, so the folders never
// agreed on k. Such a key never deletes and ignores epochs (first-sync
// union rules), on both sides.
func (m *merger) unionKey(k string) bool {
	return !m.union && ((m.l.seeded && m.l.valid && !m.l.inBase(k)) || (m.e.seeded && m.e.valid && !m.e.inBase(k)))
}

func (m *merger) mergeKey(k string) error {
	ku := m.unionKey(k)
	// l and e are value copies; their maps are shared and never written.
	l, e := m.l, m.e
	if ku {
		l.valid, e.valid = false, false
		m.anyUnionKey = true
	}
	lv0 := l.version(k, m.spec.strictTombstones)
	ev0 := e.version(k, m.spec.strictTombstones)
	lv, ev := l.hideContested(k, lv0, ev0), e.hideContested(k, ev0, lv0)
	lc, ec := lv.Changed(), ev.Changed()
	lExcl, eExcl := m.isExcluded(k, lv), m.isExcluded(k, ev)
	lx := lc && m.pol.LeavingExports && !lExcl
	ex := ec && m.pol.EnteringExports && !eExcl
	lm, em := lv, ev
	if lc && !lx {
		lm = mask(lv)
	}
	if ec && !ex {
		em = mask(ev)
	}
	res, err := m.decide(k, lm, em, lx, ex, lExcl, eExcl)
	if err != nil {
		return err
	}
	tomb := m.setCanon(k, res, lv, ev)
	updateOverlay(m.lo, k, lv, lc, lx, lExcl, tomb)
	updateOverlay(m.eo, k, ev, ec, ex, eExcl, tomb)
	m.unionGuard(k, ku, m.pol.EnteringExports && !eExcl)
	return nil
}

// decide returns the canonical item for k after the merge (nil: absent).
func (m *merger) decide(k string, lm, em merge.Version, lx, ex, lExcl, eExcl bool) (*model.Item, error) {
	c, hasC := m.canonIn.Items[k]
	_, hasCT := m.canonIn.Tombstones[k]
	switch {
	case lx || ex:
		o := merge.Scalar(k, lm, em, m.pol.TombstoneMode)
		res := o.Result
		if lx && ex && o.Loser != nil && lm.Item != nil && em.Item != nil && m.spec.fieldMerge != nil {
			merged, ok, conflict, err := m.spec.fieldMerge(pair{key: k, l: lm, e: em, winner: o.Winner})
			if err != nil {
				return nil, err
			}
			if ok {
				res = &merged
				if !conflict {
					o.Loser = nil
				}
			}
		}
		if o.Loser != nil {
			m.addConflict(k, o.Winner, o.Loser.Hash, o.Reason)
		}
		m.hint(k, o.Kind)
		if lx != ex && hasC {
			side, sv := merge.Leaving, lm
			if ex {
				side, sv = merge.Entering, em
			}
			m.canonicalConflict(k, c, res, sv, side)
		}
		return res, nil
	case hasC:
		return &c, nil
	case hasCT && m.canonKnown:
		return nil, nil
	}
	return m.restore(k, lm, em, lExcl, eExcl), nil
}

// restore handles a key missing from canonical without a tombstone and
// changed by no exporting side: it is restored from a shared copy. Copies
// a side holds through its own overlay, or excluded ones, are private and
// never restored. A side that does not export keeps its own copy in its
// overlay instead of publishing it.
func (m *merger) restore(k string, lm, em merge.Version, lExcl, eExcl bool) *model.Item {
	shared := func(v merge.Version, excl bool, ov *model.RowSnapshot, exports bool, out *model.RowSnapshot) merge.Version {
		if excl || private(ov, k) {
			return merge.Version{}
		}
		if !exports {
			if v.Item != nil {
				out.Items[k] = *v.Item
			}
			return merge.Version{}
		}
		return v
	}
	lr := shared(lm, lExcl, m.loIn, m.pol.LeavingExports, m.lo)
	er := shared(em, eExcl, m.eoIn, m.pol.EnteringExports, m.eo)
	o := merge.Scalar(k, lr, er, m.pol.TombstoneMode)
	m.hint(k, o.Kind)
	return o.Result
}

func private(ov *model.RowSnapshot, k string) bool {
	_, item := ov.Items[k]
	_, hidden := ov.Tombstones[k]
	return item || hidden
}

func (m *merger) hint(k string, kind model.ChangeKind) {
	if kind == model.ChangeRevived || kind == model.ChangeRestored {
		m.hints[k] = kind
	}
}

func (m *merger) alias(s merge.Side) ids.Alias {
	if s == merge.Leaving {
		return m.l.alias
	}
	return m.e.alias
}

func (m *merger) addConflict(k string, winner merge.Side, loserHash, reason string) {
	loser := merge.Entering
	if winner == merge.Entering {
		loser = merge.Leaving
	}
	m.conflicts = append(m.conflicts, model.Conflict{
		Row: m.spec.row, Key: k, Winner: m.alias(winner), Loser: m.alias(loser), LoserHash: loserHash, Reason: reason,
	})
}

// canonicalConflict records the canonical value an edit of one side
// replaced when that side had not seen it (it differs from the side's
// base), so the value is kept in the conflict list.
func (m *merger) canonicalConflict(k string, c model.Item, res *model.Item, sv merge.Version, side merge.Side) {
	if res != nil && m.same(*res, c) {
		return
	}
	if sv.Base != nil && m.same(*sv.Base, c) {
		return
	}
	m.conflicts = append(m.conflicts, model.Conflict{
		Row: m.spec.row, Key: k, Winner: m.alias(side), LoserHash: c.Hash, Reason: reasonCanonical,
	})
}

// same reports whether a and b are equal once the spec's ignored fields
// are removed.
func (m *merger) same(a, b model.Item) bool {
	if a.Hash == b.Hash {
		return true
	}
	if len(m.spec.ignored) == 0 {
		return false
	}
	as, bs := strip(a.Value, m.spec.ignored), strip(b.Value, m.spec.ignored)
	return as != nil && bs != nil && sameJSON(as, true, bs, true)
}

// setCanon stores the decision for k and returns the tombstone recorded
// for a removal (zero when none).
func (m *merger) setCanon(k string, res *model.Item, lv, ev merge.Version) model.Tombstone {
	if res != nil {
		m.canon.Items[k] = *res
		delete(m.canon.Tombstones, k)
		return model.Tombstone{}
	}
	delete(m.canon.Items, k)
	t, ok := latestTomb(k, lv.Tomb, ev.Tomb, m.canonIn.Tombstones)
	if !ok && !m.spec.strictTombstones && m.seen(k, lv, ev) {
		t, ok = model.Tombstone{Key: k}, true
	}
	if ok {
		m.canon.Tombstones[k] = t
	}
	return t
}

func (m *merger) seen(k string, lv, ev merge.Version) bool {
	_, hasC := m.canonIn.Items[k]
	return hasC || lv.Item != nil || lv.Base != nil || ev.Item != nil || ev.Base != nil
}

func latestTomb(k string, a, b *model.Tombstone, canon map[string]model.Tombstone) (model.Tombstone, bool) {
	var out model.Tombstone
	found := false
	for _, t := range []*model.Tombstone{a, b} {
		if t != nil && (!found || t.At > out.At) {
			out, found = *t, true
		}
	}
	if t, ok := canon[k]; ok && (!found || t.At > out.At) {
		out, found = t, true
	}
	return out, found
}

// updateOverlay records a side's private state for k: excluded items
// always, edits the side does not export, and edits of keys already in the
// overlay. A private removal hides the shared copy with a tombstone. An
// overlay item is only dropped when its own side removed it against a
// valid base (a change with no item implies a valid base).
func updateOverlay(ov *model.RowSnapshot, k string, v merge.Version, changed, exported, excluded bool, tomb model.Tombstone) {
	if excluded && v.Item != nil {
		ov.Items[k] = *v.Item
		delete(ov.Tombstones, k)
		return
	}
	if !changed {
		return
	}
	_, inOv := ov.Items[k]
	if exported && !inOv {
		if v.Item != nil {
			delete(ov.Tombstones, k)
		}
		return
	}
	if v.Item != nil {
		ov.Items[k] = *v.Item
		delete(ov.Tombstones, k)
		return
	}
	delete(ov.Items, k)
	if !exported {
		if v.Tomb != nil {
			tomb = *v.Tomb
		}
		tomb.Key = k
		ov.Tombstones[k] = tomb
	}
}

// visible returns what profile overlay ov sees for k (canonical ⊕ ov).
func (m *merger) visible(ov *model.RowSnapshot, k string) *model.Item {
	if it, ok := ov.Items[k]; ok {
		return &it
	}
	if _, hidden := ov.Tombstones[k]; hidden {
		return nil
	}
	if it, ok := m.canon.Items[k]; ok {
		return &it
	}
	return nil
}

// unionGuard keeps the entering copy of k while a base is invalid (the
// row merges as a union) or k is a union key of a seeded base: such a key
// never deletes (C1).
func (m *merger) unionGuard(k string, unionKey, enteringExports bool) {
	if (!m.union && !unionKey) || !m.pol.EnteringImports {
		return
	}
	cur := m.e.current(k)
	if cur == nil || m.visible(m.eo, k) != nil {
		return
	}
	if enteringExports {
		m.canon.Items[k] = *cur
		delete(m.canon.Tombstones, k)
	} else {
		m.eo.Items[k] = *cur
	}
	delete(m.eo.Tombstones, k)
}

// desired builds the entering replica's snapshot: canonical ⊕ entering
// overlay when importing, else the entering side's own state.
func (m *merger) desired() (*model.RowSnapshot, []model.Change, error) {
	if !m.pol.EnteringImports {
		if !m.e.info {
			return nil, nil, nil
		}
		return cloneOrEmpty(m.e.snapshot(), m.spec.row), nil, nil
	}
	d := emptySnapshot(m.spec.row)
	for k := range m.canon.Items {
		if it := m.visible(m.eo, k); it != nil {
			d.Items[k] = *it
		}
	}
	for k, it := range m.eo.Items {
		d.Items[k] = it
	}
	var extra []model.Change
	for _, k := range unionKeys(d.Items, m.e.items) {
		ch, err := m.adjust(d, k)
		if err != nil {
			return nil, nil, fmt.Errorf("item %q: %w", k, err)
		}
		extra = append(extra, ch...)
	}
	if m.spec.nativeTombstones {
		m.desiredTombstones(d)
	}
	return d, extra, nil
}

// snapshot rebuilds a RowSnapshot view of the side (items with stamps).
func (v sideView) snapshot() *model.RowSnapshot {
	return &model.RowSnapshot{Schema: 1, Items: v.items, Tombstones: v.tombs}
}

// adjust applies the per-key Desired rules: target-kept keys, secrets
// across a managed edge and the row's transform.
func (m *merger) adjust(d *model.RowSnapshot, k string) ([]model.Change, error) {
	cur := m.e.current(k)
	var cand *model.Item
	if it, ok := d.Items[k]; ok {
		cand = &it
	}
	keep := func() {
		if cur != nil {
			d.Items[k] = *cur
		} else {
			delete(d.Items, k)
		}
	}
	if m.spec.keepTarget != nil && m.spec.keepTarget(k, cand, cur) {
		keep()
		return nil, nil
	}
	if m.secretCrosses(k, cand, cur) {
		keep()
		return []model.Change{{Row: m.spec.row, Kind: model.ChangeSkipped, Key: k, Note: noteSecret}}, nil
	}
	if cand == nil || m.spec.transform == nil {
		return nil, nil
	}
	t, ch, err := m.spec.transform(k, *cand, cur)
	if err != nil {
		return nil, err
	}
	d.Items[k] = t
	return ch, nil
}

// secretCrosses reports whether cand would carry a secret value across a
// managed_by_org edge that sync.secrets does not allow (D27).
func (m *merger) secretCrosses(k string, cand, cur *model.Item) bool {
	if !m.pol.ManagedEdge() || m.pol.SecretsMayCross || cand == nil {
		return false
	}
	if !cand.Secret && (cur == nil || !cur.Secret) {
		return false
	}
	if cur != nil && cur.Hash == cand.Hash {
		return false
	}
	return m.spec.secretAllowed == nil || !m.spec.secretAllowed(k, cand, cur)
}

func (m *merger) desiredTombstones(d *model.RowSnapshot) {
	add := func(ts map[string]model.Tombstone) {
		for k, t := range ts {
			if _, present := d.Items[k]; present {
				continue
			}
			if old, ok := d.Tombstones[k]; !ok || t.At > old.At {
				d.Tombstones[k] = t
			}
		}
	}
	add(m.canon.Tombstones)
	add(m.eo.Tombstones)
	add(m.e.tombs)
}

// changes lists the item changes from the entering side's current state
// to d.
func (m *merger) changes(d *model.RowSnapshot) []model.Change {
	if d == nil || !m.pol.EnteringImports {
		return nil
	}
	var out []model.Change
	for _, k := range unionKeys(d.Items, m.e.items) {
		var after *model.Item
		if it, ok := d.Items[k]; ok {
			after = &it
		}
		kind := merge.KindOf(m.e.current(k), after, m.hints[k])
		if kind == "" {
			continue
		}
		note := noteFor(kind)
		if n, ok := m.notes[k]; ok && kind == m.hints[k] {
			note = n
		}
		out = append(out, model.Change{Row: m.spec.row, Kind: kind, Key: k, Note: note})
	}
	return out
}

func unionKeys(a, b map[string]model.Item) []string {
	set := make(map[string]bool, len(a)+len(b))
	for k := range a {
		set[k] = true
	}
	for k := range b {
		set[k] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
