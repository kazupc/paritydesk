package rows

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/merge"
	"github.com/kazupc/paritydesk/internal/model"
)

// Field roles and limits of row sidebar-collapse (compat.RowSpec.Fields
// and Limits), and its constant notes.
const (
	RoleSets               = "sets"                 // members of the store merged as sets (one item each)
	RoleEnvRefPrefix       = "env_ref_prefix"       // member prefixes followed by an environment id
	RoleProjectPrefix      = "project_prefix"       // member prefixes naming a project (sync.exclude globs)
	RoleFamilyPrefix       = "family_prefix"        // member prefixes followed by a session id (sync.exclude ids)
	RoleProjectFixed       = "project_fixed"        // project members that never name a project
	RoleProjectFixedPrefix = "project_fixed_prefix" // prefixes of project members that never name a project

	limitMaxMembers     = "max_members"
	limitMaxMemberBytes = "max_member_bytes"

	noteStoreMissing = "Claude's sidebar store is missing on the target; left unchanged"
	noteTooLarge     = "value exceeds the manifest limit; target value kept"

	// folderKeyPrefix starts the folder part of a project key
	// ("folder::<x>::<projectKey>").
	folderKeyPrefix = "folder::"
)

// collapseModule merges row sidebar-collapse: each item is a set of
// strings (collapsed group ids, collapsed fork families) merged member by
// member. The row never deletes: a missing item is restored, and while
// either base is invalid every member of both sides is kept (C1 at member
// level). Members kept private by sync.exclude never leave their profile.
type collapseModule struct{}

func (collapseModule) Row() model.RowID { return model.RowSidebarCollapse }

// Merge implements Module.
func (collapseModule) Merge(in Input) (Output, error) {
	if err := checkRow(in, model.RowSidebarCollapse); err != nil {
		return Output{}, err
	}
	f := in.Policy.Fields
	in, err := remapMembers(in, f[RoleEnvRefPrefix])
	if err != nil {
		return Output{}, err
	}
	// The entering folder's own read, before its private members are split
	// off: the merge runs on shared members only, and Desired gets the
	// entering folder's private members back afterwards (restorePrivate).
	enteringOwn := in.Entering.Snapshot
	in, enteringPrivate, err := splitPrivate(in, memberExcluder(in.Policy.Exclude, f))
	if err != nil {
		return Output{}, err
	}
	maxN, maxB := in.Policy.Limits[limitMaxMembers], in.Policy.Limits[limitMaxMemberBytes]
	kept := map[string]string{} // key -> note, for a kept key whose candidate differed
	held := map[string]bool{}   // every key the target keeps as it is
	keep := func(k string, cand, cur *model.Item) bool {
		if cand == nil {
			held[k] = true
			return true
		}
		ms, err := setMembers(*cand)
		if err != nil {
			held[k] = true
			return true // not a set: the target keeps its own value
		}
		// keepTarget runs on shared members; the limits count the members
		// the entering folder would hold, its own private ones included.
		full := sortedUnion(ms, enteringPrivate[k])
		note := ""
		switch {
		case in.Policy.EnteringStoreMissing:
			note = noteStoreMissing
		case overLimits(full, maxN, maxB):
			note = noteTooLarge
		default:
			return false
		}
		held[k] = true
		if it, err := newSetItem(k, full, cand.Stamp); err == nil && !sameItemHash(snapshotItem(enteringOwn, k), &it) {
			kept[k] = note
		}
		return true
	}
	out, err := runKeyed(in, keyedSpec{
		row:              model.RowSidebarCollapse,
		strictTombstones: true,
		fieldMerge:       setMerge,
		keepTarget:       keep,
		fixup:            unionFixup,
	})
	if err != nil {
		return Output{}, err
	}
	if err := restorePrivate(out.Desired, enteringOwn, enteringPrivate, held, in.Policy.EnteringImports); err != nil {
		return Output{}, fmt.Errorf("rows: %s: %w", model.RowSidebarCollapse, err)
	}
	remap := in.Policy.EnvRemap
	if err := unmapOverlay(out.LeavingOverlay, remap, func(it model.Item) (model.Item, error) {
		nit, _, err := rewriteMembers(it, f[RoleEnvRefPrefix], invert(remap))
		return nit, err
	}); err != nil {
		return Output{}, err
	}
	keys := make([]string, 0, len(kept))
	for k := range kept {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out.Changes = append(out.Changes, model.Change{Row: model.RowSidebarCollapse, Kind: model.ChangeSkipped, Key: k, Note: kept[k]})
	}
	// Set rows: a base miss merges as a union, which cannot delete;
	// nothing needs confirming.
	out.NeedsConfirmation = false
	return out, nil
}

// restorePrivate gives Desired the entering folder's own private members
// back. The engine merged shared members only, so its changes already
// compare like with like; Desired must still never drop what the entering
// folder keeps to itself. A key the target keeps (keepTarget) and every
// key of a profile that does not import the row take the entering
// folder's own item exactly (absent stays absent), so Ops writes nothing
// there; every other Desired item gains the entering folder's private
// members of that key.
func restorePrivate(d, own *model.RowSnapshot, private map[string][]string, held map[string]bool, imports bool) error {
	if d == nil {
		return nil
	}
	var ownItems map[string]model.Item
	if own != nil {
		ownItems = own.Items
	}
	for _, k := range unionKeys(d.Items, ownItems) {
		if !imports || held[k] {
			if it, ok := ownItems[k]; ok {
				d.Items[k] = it
			} else {
				delete(d.Items, k)
			}
			continue
		}
		it, ok := d.Items[k]
		if !ok || len(private[k]) == 0 {
			continue
		}
		ms, err := setMembers(it)
		if err != nil {
			return err
		}
		nit, err := newSetItem(k, sortedUnion(ms, private[k]), it.Stamp)
		if err != nil {
			return err
		}
		d.Items[k] = nit
	}
	return nil
}

// snapshotItem returns item k of s, or nil.
func snapshotItem(s *model.RowSnapshot, k string) *model.Item {
	if s == nil {
		return nil
	}
	if it, ok := s.Items[k]; ok {
		return &it
	}
	return nil
}

// sameItemHash reports whether both items are present with equal hashes.
func sameItemHash(a, b *model.Item) bool {
	return a != nil && b != nil && a.Hash == b.Hash
}

// setMembers decodes a set item (a JSON array of strings).
func setMembers(it model.Item) ([]string, error) {
	l, ok := stringList(it.Value)
	if !ok {
		return nil, fmt.Errorf("item %q is not a list of strings", it.Key)
	}
	return l, nil
}

// newSetItem builds a set item: members sorted and unique, Raw nil (Ops
// always marshals).
func newSetItem(key string, members []string, stamp model.Stamp) (model.Item, error) {
	v, err := marshalList(sortedUnion(members))
	if err != nil {
		return model.Item{}, err
	}
	return model.NewItem(key, v, nil, stamp, false)
}

// sortedUnion returns the sorted distinct members of every list (never
// nil).
func sortedUnion(lists ...[]string) []string {
	set := map[string]bool{}
	for _, l := range lists {
		for _, x := range l {
			set[x] = true
		}
	}
	out := make([]string, 0, len(set))
	for x := range set {
		out = append(out, x)
	}
	sort.Strings(out)
	return out
}

// overLimits reports whether a set has more than maxN members or a member
// longer than maxB UTF-8 bytes. A zero limit is no limit.
func overLimits(set []string, maxN, maxB int) bool {
	if maxN > 0 && len(set) > maxN {
		return true
	}
	if maxB > 0 {
		for _, s := range set {
			if len(s) > maxB {
				return true
			}
		}
	}
	return false
}

// setMerge merges a set item both sides changed. Both bases valid:
// SetBases with each side's base. Either base invalid: the union of both
// sides (C1: an invalid base never deletes, also at member level). Always
// ok=true, conflict=false. The merged item carries the Prefer winner's
// stamp and Raw nil (Ops always marshals).
func setMerge(p pair) (model.Item, bool, bool, error) {
	l, err := setMembers(*p.l.Item)
	if err != nil {
		return model.Item{}, false, false, err
	}
	e, err := setMembers(*p.e.Item)
	if err != nil {
		return model.Item{}, false, false, err
	}
	var merged []string
	if p.l.BaseValid && p.e.BaseValid {
		lb, eb := baseMembers(p.l), baseMembers(p.e)
		merged = merge.SetBases(lb, eb, l, e, true, true)
	} else {
		merged = merge.SetBases(nil, nil, l, e, false, false)
	}
	it, err := newSetItem(p.key, merged, p.win().Item.Stamp)
	if err != nil {
		return model.Item{}, false, false, err
	}
	return it, true, false, nil
}

// baseMembers returns the members of a version's base (none when absent
// or not a list).
func baseMembers(v merge.Version) []string {
	if v.Base == nil {
		return nil
	}
	l, _ := stringList(v.Base.Value)
	return l
}

// unionFixup keeps C1 at member level when the keyed engine decided a key
// for one side only: while either base is invalid, canonical holds every
// member of both exporting sides, so no member is ever removed.
//
// A union key of a seeded base (merger.unionKey) gets the same treatment.
func unionFixup(m *merger) error {
	if !m.union && !m.anyUnionKey {
		return nil
	}
	keys := unionKeys(m.l.items, m.e.items)
	for _, k := range keys {
		ku := m.unionKey(k)
		if !m.union && !ku {
			continue
		}
		var lists [][]string
		c, hasC := m.canon.Items[k]
		if hasC {
			cm, err := setMembers(c)
			if err != nil {
				return err
			}
			lists = append(lists, cm)
		}
		stamp := c.Stamp
		for _, side := range []struct {
			v       sideView
			exports bool
		}{{m.l, m.pol.LeavingExports}, {m.e, m.pol.EnteringExports}} {
			it, ok := side.v.items[k]
			// A contested value on a side with an invalid base is read
			// as absent (C2), as the engine does.
			if !ok || !side.exports || ((!side.v.valid || ku) && side.v.isContested(k, &it)) {
				continue
			}
			sm, err := setMembers(it)
			if err != nil {
				return err
			}
			lists = append(lists, sm)
			if !hasC {
				stamp, hasC = it.Stamp, true
			}
		}
		if len(lists) == 0 {
			continue
		}
		it, err := newSetItem(k, sortedUnion(lists...), stamp)
		if err != nil {
			return err
		}
		if cur, ok := m.canon.Items[k]; !ok || cur.Hash != it.Hash {
			m.canon.Items[k] = it
			delete(m.canon.Tombstones, k)
		}
	}
	return nil
}

// remapMembers rewrites members "<prefix><id>" with id in in.Policy.EnvRemap
// to "<prefix><EnvRemap[id]>" in the leaving snapshot, its base and
// canonical (moving them into the entering id space, as remapSessions
// does). Inputs are never mutated.
func remapMembers(in Input, prefixes []string) (Input, error) {
	if len(in.Policy.EnvRemap) == 0 || len(prefixes) == 0 {
		return in, nil
	}
	rw := func(s *model.RowSnapshot) (*model.RowSnapshot, error) {
		if s == nil {
			return nil, nil
		}
		c := s.Clone()
		for k, it := range c.Items {
			nit, changed, err := rewriteMembers(it, prefixes, in.Policy.EnvRemap)
			if err != nil {
				return nil, err
			}
			if changed {
				c.Items[k] = nit
			}
		}
		return c, nil
	}
	var err error
	if in.Leaving.Snapshot, err = rw(in.Leaving.Snapshot); err != nil {
		return in, err
	}
	if in.Leaving.Base, err = rw(in.Leaving.Base); err != nil {
		return in, err
	}
	if in.Canonical, err = rw(in.Canonical); err != nil {
		return in, err
	}
	return in, nil
}

// rewriteMembers rewrites the members of a set item that start with one of
// prefixes and continue with a key of m. The result is re-normalized.
func rewriteMembers(it model.Item, prefixes []string, m map[string]string) (model.Item, bool, error) {
	ms, err := setMembers(it)
	if err != nil {
		return it, false, err
	}
	changed := false
	for i, x := range ms {
		for _, p := range prefixes {
			id, ok := strings.CutPrefix(x, p)
			if !ok {
				continue
			}
			if to, ok := m[id]; ok {
				ms[i], changed = p+to, true
			}
			break
		}
	}
	if !changed {
		return it, false, nil
	}
	nit, err := newSetItem(it.Key, ms, it.Stamp)
	return nit, err == nil, err
}

// memberExcluder returns the rule of sync.exclude for set members. Entries
// that parse as session ids are ids; the others are project-path globs.
// A family member (family_prefix + session id) is private when its id is
// excluded, and always while any glob is set (the member alone does not
// tell the session's project). A project member (project_prefix, not in
// project_fixed, not starting with a project_fixed_prefix entry) is
// private, while any glob is set, when its project key (after one
// "folder::<x>::" part) is a path a glob matches, or is not a path at all
// (cannot tell: unknown counts as excluded). Every other member is shared.
func memberExcluder(exclude []string, f map[string][]string) func(member string) bool {
	byID := map[string]bool{}
	var globs []string
	for _, pat := range exclude {
		if _, err := ids.ParseSession(pat); err == nil {
			byID[pat] = true
		} else {
			globs = append(globs, pat)
		}
	}
	return func(member string) bool {
		for _, p := range f[RoleFamilyPrefix] {
			if id, ok := strings.CutPrefix(member, p); ok {
				return byID[id] || len(globs) > 0
			}
		}
		for _, p := range f[RoleProjectPrefix] {
			rest, ok := strings.CutPrefix(member, p)
			if !ok {
				continue
			}
			if slices.Contains(f[RoleProjectFixed], member) || slices.ContainsFunc(f[RoleProjectFixedPrefix], func(fp string) bool {
				return strings.HasPrefix(member, fp)
			}) || len(globs) == 0 {
				return false
			}
			if r, ok := strings.CutPrefix(rest, folderKeyPrefix); ok {
				if i := strings.Index(r, "::"); i >= 0 {
					rest = r[i+2:]
				}
			}
			if !strings.HasPrefix(rest, "/") {
				return true
			}
			return slices.ContainsFunc(globs, func(g string) bool { return globMatch(g, rest) })
		}
		return false
	}
}

// splitPrivate returns copies of the input in which every set item holds
// only shared members, plus the entering side's private members by key.
// Private members are removed from both sides, both bases, canonical and
// both overlays; an item left empty stays as [].
func splitPrivate(in Input, private func(string) bool) (Input, map[string][]string, error) {
	enteringPrivate := map[string][]string{}
	filter := func(s *model.RowSnapshot, collect bool) (*model.RowSnapshot, error) {
		if s == nil {
			return nil, nil
		}
		c := s.Clone()
		for k, it := range c.Items {
			ms, err := setMembers(it)
			if err != nil {
				return nil, err
			}
			var shared, priv []string
			for _, x := range ms {
				if private(x) {
					priv = append(priv, x)
				} else {
					shared = append(shared, x)
				}
			}
			if collect && len(priv) > 0 {
				enteringPrivate[k] = sortedUnion(priv)
			}
			if len(priv) == 0 {
				continue
			}
			nit, err := newSetItem(k, shared, it.Stamp)
			if err != nil {
				return nil, err
			}
			c.Items[k] = nit
		}
		return c, nil
	}
	var err error
	for _, p := range []struct {
		s       **model.RowSnapshot
		collect bool
	}{
		{&in.Leaving.Snapshot, false}, {&in.Leaving.Base, false},
		{&in.Entering.Snapshot, true}, {&in.Entering.Base, false},
		{&in.Canonical, false}, {&in.LeavingOverlay, false}, {&in.EnteringOverlay, false},
	} {
		if *p.s, err = filter(*p.s, p.collect); err != nil {
			return in, nil, err
		}
	}
	return in, enteringPrivate, nil
}
