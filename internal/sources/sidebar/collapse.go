package sidebar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/sources"
)

// Collapse is the adapter for row model.RowSidebarCollapse: the sets of
// collapsed sidebar groups and fork families, members of dframe-store's
// state named by the manifest (fields.sets). The store is kept in Local
// Storage only (no config mirror), so the row is written there alone and
// verified after launch from a Local Storage snapshot (sources.LSVerifier).
type Collapse struct{}

var (
	_ sources.Adapter      = Collapse{}
	_ sources.LSVerifier   = Collapse{}
	_ sources.TargetStater = Collapse{}
)

// NewCollapse returns the adapter.
func NewCollapse() sources.Adapter { return Collapse{} }

// Row implements sources.Adapter.
func (Collapse) Row() model.RowID { return model.RowSidebarCollapse }

// collapseSpec is the validated manifest spec: the store key, the members
// and the limits.
type collapseSpec struct {
	key            string   // spec.LSKeys[0], "dframe-store"
	members        []string // spec.Fields[sources.RoleSets]
	persistVersion int
	maxMembers     int // spec.Limits["max_members"], 0 = no limit
	maxMemberBytes int // spec.Limits["max_member_bytes"], 0 = no limit
}

// Limit names of the collapse row (compat.RowSpec.Limits).
const (
	LimitMaxMembers     = "max_members"
	LimitMaxMemberBytes = "max_member_bytes"
)

var memberName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`)

func newCollapseSpec(spec compat.RowSpec) (collapseSpec, error) {
	members := spec.Fields[sources.RoleSets]
	if len(spec.LSKeys) != 1 || len(members) == 0 {
		return collapseSpec{}, errors.New("sidebar: sidebar-collapse: manifest spec lacks keys or sets")
	}
	for _, m := range members {
		if !memberName.MatchString(m) {
			return collapseSpec{}, errors.New("sidebar: sidebar-collapse: manifest spec lacks keys or sets")
		}
	}
	return collapseSpec{key: spec.LSKeys[0], members: members, persistVersion: spec.PersistVersion,
		maxMembers: spec.Limits[LimitMaxMembers], maxMemberBytes: spec.Limits[LimitMaxMemberBytes]}, nil
}

// normalizeSet returns the sorted, deduplicated string elements of a JSON
// array (non-strings dropped, as the app does on hydration). The result is
// never nil.
func normalizeSet(raw json.RawMessage, name string) ([]string, error) {
	elems, err := stringElems(raw, name)
	if err != nil {
		return nil, err
	}
	sort.Strings(elems)
	return elems, nil
}

// stringElems returns the string elements of a JSON array in their order,
// without duplicates (never nil).
func stringElems(raw json.RawMessage, name string) ([]string, error) {
	arr, err := sources.DecodeArray(raw, name)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []string{}
	for _, e := range arr {
		var s string
		if t := bytesTrimLeft(e); len(t) == 0 || t[0] != '"' || json.Unmarshal(e, &s) != nil {
			continue
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, nil
}

func bytesTrimLeft(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\n' || b[0] == '\r') {
		b = b[1:]
	}
	return b
}

// targetOrder returns the array to write: the target's current string
// elements that desired keeps, in their current order without duplicates,
// then the remaining desired members in desired's (sorted) order.
func targetOrder(current []string, desired []string) []string {
	want := map[string]bool{}
	for _, d := range desired {
		want[d] = true
	}
	out := []string{}
	done := map[string]bool{}
	for _, c := range current {
		if want[c] && !done[c] {
			done[c] = true
			out = append(out, c)
		}
	}
	for _, d := range desired {
		if !done[d] {
			done[d] = true
			out = append(out, d)
		}
	}
	return out
}

// errShape is the read error of a member that is not an array. It names
// the store only, never a value.
func errShape(key string) error { return fmt.Errorf("sidebar: %s: unexpected shape", key) }

// Read implements sources.Adapter. An absent store gives an empty
// snapshot. Each listed member present in the store's state becomes one
// item whose value is its sorted, deduplicated string elements; Raw keeps
// the stored array bytes. openedGroups and every other member are never
// read.
func (Collapse) Read(ctx context.Context, rc sources.ReadContext) (*model.RowSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cs, err := newCollapseSpec(rc.Spec)
	if err != nil {
		return nil, err
	}
	snap := sources.NewSnapshot(model.RowSidebarCollapse)
	doc, err := sources.LoadLS(rc.LS, cs.key)
	if err != nil || !doc.Present {
		return snap, err
	}
	if err := checkStore(doc, cs.persistVersion); err != nil {
		return nil, err
	}
	for _, m := range cs.members {
		raw, found, err := doc.Get(jsonx.Path{"state", m})
		if err != nil {
			return nil, errShape(cs.key)
		}
		if !found {
			continue
		}
		set, err := normalizeSet(raw, cs.key)
		if err != nil {
			return nil, errShape(cs.key)
		}
		v, err := jsonx.Marshal(set)
		if err != nil {
			return nil, err
		}
		it, err := model.NewItem(m, v, append([]byte(nil), raw...), model.Stamp{}, false)
		if err != nil {
			return nil, fmt.Errorf("sidebar: %s: %w", cs.key, err)
		}
		snap.Items[m] = it
	}
	return snap, nil
}

// VerifyItems implements sources.LSVerifier: the same items as Read, read
// from the snapshot copy rc.LS.
func (c Collapse) VerifyItems(ctx context.Context, rc sources.ReadContext) (*model.RowSnapshot, error) {
	return c.Read(ctx, rc)
}

// TargetState implements sources.TargetStater: StoreMissing when the
// folder has no store (nothing may be written there). Seeded is always
// false for this row.
func (Collapse) TargetState(ctx context.Context, rc sources.ReadContext) (sources.TargetState, error) {
	if err := ctx.Err(); err != nil {
		return sources.TargetState{}, err
	}
	cs, err := newCollapseSpec(rc.Spec)
	if err != nil {
		return sources.TargetState{}, err
	}
	doc, err := sources.LoadLS(rc.LS, cs.key)
	if err != nil {
		return sources.TargetState{}, err
	}
	return sources.TargetState{StoreMissing: !doc.Present}, nil
}

// Ops implements sources.Adapter. Only state.<member> of each changed
// member is edited, on dframe-store alone; every other byte of the store
// (shared, version, other members, key order) is kept. The store is never
// created, a member desired lacks is never deleted, and a set over the
// manifest limits is skipped. The merge already keeps the target's value
// in those cases; the checks here guard a folder that changed since.
func (Collapse) Ops(ctx context.Context, oc sources.OpsContext, current, desired *model.RowSnapshot) (sources.OpsResult, error) {
	var res sources.OpsResult
	if err := ctx.Err(); err != nil {
		return res, err
	}
	row := model.RowSidebarCollapse
	current, desired = sources.OrEmpty(current, row), sources.OrEmpty(desired, row)
	if sources.SameItems(current, desired, false) {
		return res, nil
	}
	cs, err := newCollapseSpec(oc.Spec)
	if err != nil {
		return res, err
	}
	var changed []string
	for _, m := range cs.members {
		d, has := desired.Items[m]
		if c, ok := current.Items[m]; has && (!ok || c.Hash != d.Hash) {
			changed = append(changed, m)
		}
	}
	if len(changed) == 0 {
		return res, nil
	}
	doc, err := sources.LoadLS(oc.LS, cs.key)
	if err != nil {
		return res, err
	}
	if !doc.Present {
		for _, m := range changed {
			res.Skipped = append(res.Skipped, sources.Skip(row, m, sources.NoteStoreMissing))
		}
		return res, nil
	}
	if err := checkStore(doc, cs.persistVersion); err != nil {
		return res, err
	}
	b := sources.NewBuilder(doc, row)
	for _, m := range changed {
		want, err := normalizeSet(desired.Items[m].Value, cs.key)
		if err != nil {
			return res, errShape(cs.key)
		}
		if cs.overLimits(want) {
			res.Skipped = append(res.Skipped, sources.Skip(row, m, sources.NoteTooLarge))
			continue
		}
		var cur []string
		raw, found, err := doc.Get(jsonx.Path{"state", m})
		if err != nil {
			return res, errShape(cs.key)
		}
		if found {
			if cur, err = stringElems(raw, cs.key); err != nil {
				return res, errShape(cs.key)
			}
		}
		v, err := jsonx.Marshal(targetOrder(cur, want))
		if err != nil {
			return res, err
		}
		if err := b.Ensure(jsonx.Path{"state"}, m); err != nil {
			return res, errShape(cs.key)
		}
		b.Set(jsonx.Path{"state", m}, v, m, false)
	}
	res.Edits = b.Edits()
	return res, nil
}

// overLimits reports whether a set has more members, or a longer member
// (UTF-8 bytes), than the manifest allows. A zero limit is no limit.
func (cs collapseSpec) overLimits(set []string) bool {
	if cs.maxMembers > 0 && len(set) > cs.maxMembers {
		return true
	}
	if cs.maxMemberBytes > 0 {
		for _, s := range set {
			if len(s) > cs.maxMemberBytes {
				return true
			}
		}
	}
	return false
}
