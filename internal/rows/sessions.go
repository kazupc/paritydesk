package rows

import (
	"encoding/json"
	"path"
	"sort"
	"strings"

	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/model"
)

// defaultPermissionMode is the mode a new record gets when the target
// does not allow the source's mode (D8).
const defaultPermissionMode = "default"

// sessionsModule merges session records and tombstones (rows 1–3, 8).
type sessionsModule struct{}

func (sessionsModule) Row() model.RowID { return model.RowSessions }

// Merge implements Module. Records merge per key with D9 tombstones (a
// record missing without a tombstone is restored), per-field merges of
// Fields[per_field] and max-time fields, families kept whole, sync.exclude
// kept private, environment ids remapped, and the D8/D10 rules for the
// entering replica (permission cap, run state, account-bound fields).
func (sessionsModule) Merge(in Input) (Output, error) {
	if err := checkRow(in, model.RowSessions); err != nil {
		return Output{}, err
	}
	f := in.Policy.Fields
	refs := f[RoleEnvRef]
	if len(refs) == 0 {
		refs = f[RolePerField]
	}
	in, remapped, err := remapSessions(in, refs)
	if err != nil {
		return Output{}, err
	}
	excl := sessionExcluder(in.Policy.Exclude, first(f[RoleProjectPath]))
	spec := keyedSpec{
		row:              model.RowSessions,
		strictTombstones: true,
		nativeTombstones: true,
		fieldMerge:       recordMerger(f[RolePerField], f[RoleMaxTime]),
		excluded:         excl,
		keepTarget: func(k string, cand, cur *model.Item) bool {
			return excl(k, cand) || excl(k, cur)
		},
		fixup:     familyFixup(f[RoleParent]),
		transform: sessionTransform(in.Policy, f),
	}
	out, err := runKeyed(in, spec)
	if err != nil {
		return Output{}, err
	}
	if err := unmapOverlay(out.LeavingOverlay, in.Policy.EnvRemap, func(it model.Item) (model.Item, error) {
		return rewriteFields(it, refs, invert(in.Policy.EnvRemap))
	}); err != nil {
		return Output{}, err
	}
	out.Changes = append(out.Changes, remapChanges(out.Changes, remapped)...)
	return out, nil
}

func first(xs []string) string {
	if len(xs) == 0 {
		return ""
	}
	return xs[0]
}

// remapSessions rewrites environment ids in the given record fields of the
// leaving snapshot, its base and canonical, so canonical moves into the
// entering replica's id space (rows 7, 8). It returns the keys of leaving
// records it rewrote. Inputs are never mutated.
func remapSessions(in Input, fields []string) (Input, map[string]bool, error) {
	if len(in.Policy.EnvRemap) == 0 || len(fields) == 0 {
		return in, nil, nil
	}
	remapped := map[string]bool{}
	var err error
	rw := func(s *model.RowSnapshot, track bool) (*model.RowSnapshot, error) {
		if s == nil {
			return nil, nil
		}
		c := s.Clone()
		for k, it := range c.Items {
			nit, changed, err := rewriteFieldsReport(it, fields, in.Policy.EnvRemap)
			if err != nil {
				return nil, err
			}
			if changed {
				c.Items[k] = nit
				if track {
					remapped[k] = true
				}
			}
		}
		return c, nil
	}
	if in.Leaving.Snapshot, err = rw(in.Leaving.Snapshot, true); err != nil {
		return in, nil, err
	}
	if in.Leaving.Base, err = rw(in.Leaving.Base, false); err != nil {
		return in, nil, err
	}
	if in.Canonical, err = rw(in.Canonical, false); err != nil {
		return in, nil, err
	}
	return in, remapped, nil
}

func rewriteFields(it model.Item, fields []string, m map[string]string) (model.Item, error) {
	out, _, err := rewriteFieldsReport(it, fields, m)
	return out, err
}

// rewriteFieldsReport replaces top-level string fields whose value is a
// key of m.
func rewriteFieldsReport(it model.Item, fields []string, m map[string]string) (model.Item, bool, error) {
	if !isObject(it.Value) {
		return it, false, nil
	}
	d := newEditDoc(it)
	changed := false
	for _, f := range fields {
		s, ok := getString(it.Value, f)
		if !ok {
			continue
		}
		to, ok := m[s]
		if !ok {
			continue
		}
		v, err := jsonx.Marshal(to)
		if err != nil {
			return it, false, err
		}
		if err := d.set(jsonx.Path{f}, v); err != nil {
			return it, false, err
		}
		changed = true
	}
	if !changed {
		return it, false, nil
	}
	out, err := d.item(it.Key, it.Stamp, it.Secret)
	if err != nil {
		return it, false, err
	}
	return out, true, nil
}

func invert(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[v] = k
	}
	return out
}

// unmapOverlay moves overlay items back into their own profile's id space.
func unmapOverlay(ov *model.RowSnapshot, remap map[string]string, f func(model.Item) (model.Item, error)) error {
	if ov == nil || len(remap) == 0 {
		return nil
	}
	for k, it := range ov.Items {
		nit, err := f(it)
		if err != nil {
			return err
		}
		ov.Items[k] = nit
	}
	return nil
}

// remapChanges reports ChangeRemapped for written records that carry
// remapped environment ids.
func remapChanges(changes []model.Change, remapped map[string]bool) []model.Change {
	var out []model.Change
	for _, c := range changes {
		if remapped[c.Key] && (c.Kind == model.ChangeAdded || c.Kind == model.ChangeUpdated) {
			out = append(out, model.Change{Row: c.Row, Kind: model.ChangeRemapped, Key: c.Key, Note: noteRemapped})
		}
	}
	return out
}

// sessionExcluder matches sync.exclude entries: an entry that is a
// session id matches that key; any other entry is a glob over the
// record's project path field (a glob ending in "/**" matches everything
// below its prefix). Exclusion is a privacy control, so it fails closed:
// while any glob is set, a record without a readable project path (or a
// manifest without a project path field) counts as excluded. A missing
// record (nil) is matched by id only, so deletions still apply.
func sessionExcluder(exclude []string, projectField string) func(string, *model.Item) bool {
	byID := map[string]bool{}
	var globs []string
	for _, pat := range exclude {
		if _, err := ids.ParseSession(pat); err == nil {
			byID[pat] = true
		} else {
			globs = append(globs, pat)
		}
	}
	return func(k string, it *model.Item) bool {
		if byID[k] {
			return true
		}
		if len(globs) == 0 || it == nil {
			return false
		}
		proj, ok := getString(it.Value, projectField)
		if !ok || proj == "" {
			return true
		}
		for _, g := range globs {
			if globMatch(g, proj) {
				return true
			}
		}
		return false
	}
}

func globMatch(pat, s string) bool {
	if prefix, ok := strings.CutSuffix(pat, "/**"); ok {
		return s == prefix || strings.HasPrefix(s, prefix+"/")
	}
	ok, err := path.Match(pat, s)
	return err == nil && ok
}

// familyFixup revives the parent of every present record (fork and
// routine families are kept whole): a child that is visible to canonical
// or to a profile brings its parent back from any copy the inputs hold.
func familyFixup(parentFields []string) func(*merger) error {
	if len(parentFields) == 0 {
		return nil
	}
	return func(m *merger) error {
		for changed := true; changed; {
			changed = false
			for _, ov := range []*model.RowSnapshot{m.canon, m.eo, m.lo} {
				for _, k := range sortedKeys(ov.Items) {
					for _, p := range parentsOf(ov.Items[k], parentFields) {
						if m.reviveParent(ov, p) {
							changed = true
						}
					}
				}
			}
		}
		return nil
	}
}

func parentsOf(it model.Item, fields []string) []string {
	var out []string
	for _, f := range fields {
		if p, ok := getString(it.Value, f); ok && p != "" && p != it.Key {
			out = append(out, p)
		}
	}
	return out
}

// reviveParent makes parent p visible wherever ov's child is: in
// canonical, and through the overlay when ov is one. It reports whether
// anything changed.
func (m *merger) reviveParent(ov *model.RowSnapshot, p string) bool {
	changed := false
	if ov != m.canon {
		if _, hidden := ov.Tombstones[p]; hidden {
			delete(ov.Tombstones, p)
			changed = true
		}
		if _, ok := ov.Items[p]; ok {
			return changed
		}
	}
	if _, ok := m.canon.Items[p]; ok {
		return changed
	}
	cp, ok := m.copyOf(p)
	if !ok {
		return changed
	}
	m.canon.Items[p] = cp
	delete(m.canon.Tombstones, p)
	m.hints[p] = model.ChangeRevived
	return true
}

// copyOf finds any copy of key k in the inputs, newest source first.
func (m *merger) copyOf(k string) (model.Item, bool) {
	for _, src := range []map[string]model.Item{m.l.items, m.e.items, m.canonIn.Items, m.eoIn.Items, m.loIn.Items, m.l.base, m.e.base} {
		if it, ok := src[k]; ok {
			return it, true
		}
	}
	return model.Item{}, false
}

func sortedKeys(items map[string]model.Item) []string {
	out := make([]string, 0, len(items))
	for k := range items {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sessionTransform applies the D8/D10 rules to a record written to the
// entering replica.
func sessionTransform(pol Policy, f map[string][]string) func(string, model.Item, *model.Item) (model.Item, []model.Change, error) {
	perm := first(f[RolePermission])
	runState, bound := f[RoleRunState], f[RoleAccountBound]
	if perm == "" && len(runState) == 0 && len(bound) == 0 {
		return nil
	}
	return func(k string, d model.Item, cur *model.Item) (model.Item, []model.Change, error) {
		if !isObject(d.Value) {
			return d, nil, nil
		}
		doc := newEditDoc(d)
		var changes []model.Change
		capped, err := capPermission(doc, perm, pol.PermissionAcks, cur)
		if err != nil {
			return d, nil, err
		}
		if capped {
			changes = append(changes, model.Change{Row: model.RowSessions, Kind: model.ChangeCapped, Key: k, Note: noteCapped})
		}
		for _, fld := range runState {
			if err := keepOrClear(doc, jsonx.Path{fld}, cur, true); err != nil {
				return d, nil, err
			}
		}
		for _, fld := range bound {
			if err := keepOrClear(doc, jsonx.Path{fld}, cur, false); err != nil {
				return d, nil, err
			}
		}
		if string(doc.val) == string(d.Value) {
			return d, changes, nil
		}
		out, err := doc.item(k, d.Stamp, d.Secret)
		return out, changes, err
	}
}

// keepOrClear takes path from the target's own record when it has one;
// otherwise it removes the field when clearNew is set.
func keepOrClear(doc *editDoc, p jsonx.Path, cur *model.Item, clearNew bool) error {
	switch {
	case cur != nil && isObject(cur.Value):
		return doc.copyFrom(p, *cur)
	case cur == nil && clearNew:
		return doc.del(p)
	}
	return nil
}

// capPermission carries the record's permission mode only if the target
// permits it: the mode is "default", the target already uses it, or the
// target's acknowledgement for that mode is true. Otherwise the target's
// own value is kept, or "default" is set on a record new to the target.
func capPermission(doc *editDoc, field string, acks map[string]bool, cur *model.Item) (bool, error) {
	if field == "" {
		return false, nil
	}
	mode, ok := getString(doc.val, field)
	if !ok || mode == defaultPermissionMode || acks[mode] {
		return false, nil
	}
	p := jsonx.Path{field}
	if cur != nil {
		if cm, ok := getString(cur.Value, field); ok && cm == mode {
			return false, nil
		}
		return true, doc.copyFrom(p, *cur)
	}
	v, _ := json.Marshal(defaultPermissionMode)
	return true, doc.set(p, v)
}
