package rows

import (
	"sort"
	"strings"

	"github.com/kazupc/paritydesk/internal/model"
)

// orderKey is the item key of an ordered list: the real item of
// group-order and pinned-order, and the stand-in for RowSnapshot.Order in
// sidebar-view (whose own keys are "field:" and "section:" keys).
const orderKey = "order"

// sectionPrefix marks sidebar-view section items.
const sectionPrefix = "section:"

// orderModule merges group-order and pinned-order: one "order" item whose
// value is a JSON array of strings, merged as an OrderedList when both
// sides changed it. RowSnapshot.Order mirrors the merged array.
type orderModule struct{ row model.RowID }

func (o orderModule) Row() model.RowID { return o.row }

// Merge implements Module.
func (o orderModule) Merge(in Input) (Output, error) {
	if err := checkRow(in, o.row); err != nil {
		return Output{}, err
	}
	spec := keyedSpec{row: o.row, fieldMerge: func(p pair) (model.Item, bool, bool, error) {
		if p.key != orderKey {
			return model.Item{}, false, false, nil
		}
		return listMerge(p)
	}}
	out, err := runKeyed(in, spec)
	if err != nil {
		return Output{}, err
	}
	for _, s := range outputSnapshots(&out) {
		s.Order = orderOf(s)
	}
	markMoved(out.Changes, true)
	return out, nil
}

// sidebarModule merges sidebar-view: Scalar per "field:" item, sections
// keyed by "section:<id>", and the section order (RowSnapshot.Order) as an
// OrderedList.
type sidebarModule struct{}

func (sidebarModule) Row() model.RowID { return model.RowSidebarView }

// Merge implements Module.
func (sidebarModule) Merge(in Input) (Output, error) {
	if err := checkRow(in, model.RowSidebarView); err != nil {
		return Output{}, err
	}
	in, err := withOrderItems(in)
	if err != nil {
		return Output{}, err
	}
	sections := recordMerger(in.Policy.Fields[RolePerField], in.Policy.Fields[RoleMaxTime])
	spec := keyedSpec{row: model.RowSidebarView, fieldMerge: func(p pair) (model.Item, bool, bool, error) {
		switch {
		case p.key == orderKey:
			return listMerge(p)
		case strings.HasPrefix(p.key, sectionPrefix) && sections != nil:
			return sections(p)
		}
		return model.Item{}, false, false, nil
	}}
	out, err := runKeyed(in, spec)
	if err != nil {
		return Output{}, err
	}
	for _, s := range outputSnapshots(&out) {
		s.Order = orderOf(s)
		delete(s.Items, orderKey)
		delete(s.Tombstones, orderKey)
	}
	for _, s := range []*model.RowSnapshot{out.Canonical, out.Desired} {
		if s != nil {
			s.Order = completeSectionOrder(s)
		}
	}
	markMoved(out.Changes, false)
	return out, nil
}

func outputSnapshots(out *Output) []*model.RowSnapshot {
	var ss []*model.RowSnapshot
	for _, s := range []*model.RowSnapshot{out.Canonical, out.LeavingOverlay, out.EnteringOverlay, out.Desired} {
		if s != nil {
			ss = append(ss, s)
		}
	}
	return ss
}

// orderOf returns the string elements of the snapshot's order item.
func orderOf(s *model.RowSnapshot) []string {
	it, ok := s.Items[orderKey]
	if !ok {
		return nil
	}
	l, ok := stringList(it.Value)
	if !ok || len(l) == 0 {
		return nil
	}
	return l
}

// withOrderItems adds an "order" item built from RowSnapshot.Order to
// every input snapshot that has an order. Inputs are never mutated.
func withOrderItems(in Input) (Input, error) {
	var err error
	conv := func(s *model.RowSnapshot) *model.RowSnapshot {
		if s == nil || len(s.Order) == 0 || err != nil {
			return s
		}
		c := s.Clone()
		if c.Items == nil {
			c.Items = map[string]model.Item{}
		}
		v, merr := marshalList(c.Order)
		if merr != nil {
			err = merr
			return s
		}
		it, nerr := model.NewItem(orderKey, v, nil, model.Stamp{}, false)
		if nerr != nil {
			err = nerr
			return s
		}
		c.Items[orderKey] = it
		return c
	}
	in.Leaving.Snapshot = conv(in.Leaving.Snapshot)
	in.Leaving.Base = conv(in.Leaving.Base)
	in.Entering.Snapshot = conv(in.Entering.Snapshot)
	in.Entering.Base = conv(in.Entering.Base)
	in.Canonical = conv(in.Canonical)
	in.LeavingOverlay = conv(in.LeavingOverlay)
	in.EnteringOverlay = conv(in.EnteringOverlay)
	return in, err
}

// OrderItemKey is the item key that stands for an ordered list
// (RowSnapshot.Order) in merges and in contested entries.
const OrderItemKey = orderKey

// OrderHash returns the hash the merge gives the stand-in item of order
// (RowSnapshot.Order), or "" for an empty order, which has no stand-in.
// verify records it in contested entries so a server-side reorder is not
// harvested as an edit.
func OrderHash(order []string) (string, error) {
	if len(order) == 0 {
		return "", nil
	}
	v, err := marshalList(order)
	if err != nil {
		return "", err
	}
	return model.ItemHash(v)
}

// completeSectionOrder drops ids of sections the snapshot no longer has
// and appends sections missing from the order, sorted.
func completeSectionOrder(s *model.RowSnapshot) []string {
	present := map[string]bool{}
	for k := range s.Items {
		if id, ok := strings.CutPrefix(k, sectionPrefix); ok {
			present[id] = true
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, id := range s.Order {
		if present[id] && !seen[id] {
			out = append(out, id)
			seen[id] = true
		}
	}
	var missing []string
	for id := range present {
		if !seen[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	return append(out, missing...)
}

// markMoved reports changes of the order item as moves. With
// keepDeleted, a removal of the order item stays a deletion: the adapter
// then deletes the stored list (a destructive op that plan.Validate maps
// to that deletion, and the deletion breaker counts). sidebar-view always
// keeps a completed section order, so its order item is never deleted.
func markMoved(changes []model.Change, keepDeleted bool) {
	for i := range changes {
		c := &changes[i]
		if c.Key != orderKey || c.Kind == model.ChangeSkipped || (keepDeleted && c.Kind == model.ChangeDeleted) {
			continue
		}
		c.Kind, c.Note = model.ChangeMoved, noteMoved
	}
}
