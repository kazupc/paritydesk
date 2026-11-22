package sidebar

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/sources"
)

// Members and item key prefixes of a sidebar scope object.
const (
	seededMember   = "prefsSeeded"  // the app's own seeding flag; never carried
	migratedMember = "migratedFrom" // the app's own migration marker; never carried
	sectionsMember = "sections"
	fieldPrefix    = "field:"
	sectionPrefix  = "section:"
)

// SidebarView is the adapter for row model.RowSidebarView.
type SidebarView struct{}

var (
	_ sources.Adapter      = SidebarView{}
	_ sources.MirrorReader = SidebarView{}
)

// NewSidebarView returns the adapter.
func NewSidebarView() sources.Adapter { return SidebarView{} }

// Row implements sources.Adapter.
func (SidebarView) Row() model.RowID { return model.RowSidebarView }

// viewLayout: the mirror path ends in the scope key "<acct>/<org>"; the
// LSS value and the store's codeSidebarByScope are keyed the same way.
func viewLayout(spec compat.RowSpec) (layout, error) {
	return newLayout(model.RowSidebarView, spec, func(mp jsonx.Path) (jsonx.Path, jsonx.Path) {
		scope := mp[len(mp)-1]
		return jsonx.Path{"value", scope}, jsonx.Path{"state", "codeSidebarByScope", scope}
	})
}

// scopeItems splits a scope object into items: "field:<name>" per member
// except prefsSeeded, migratedFrom and sections, "section:<id>" per
// section. order lists the section ids. prefsSeeded and migratedFrom are
// the app's own markers of one account's seeding and migration, so each
// scope keeps its own and they are never carried.
func scopeItems(v json.RawMessage, name string, stamp model.Stamp) (map[string]model.Item, []string, error) {
	obj, err := sources.DecodeObject(v, name)
	if err != nil {
		return nil, nil, err
	}
	items := map[string]model.Item{}
	var order []string
	for k, raw := range obj {
		switch k {
		case seededMember, migratedMember:
			continue
		case sectionsMember:
			order, err = sectionItems(raw, name, stamp, items)
			if err != nil {
				return nil, nil, err
			}
			continue
		}
		it, err := model.NewItem(fieldPrefix+k, raw, raw, stamp, false)
		if err != nil {
			return nil, nil, fmt.Errorf("sidebar: %s: %w", name, err)
		}
		items[it.Key] = it
	}
	return items, order, nil
}

func sectionItems(raw json.RawMessage, name string, stamp model.Stamp, items map[string]model.Item) ([]string, error) {
	arr, err := sources.DecodeArray(raw, name)
	if err != nil {
		return nil, err
	}
	var order []string
	for _, e := range arr {
		obj, err := sources.DecodeObject(e, name)
		if err != nil {
			return nil, err
		}
		id, ok := sources.IDString(obj)
		if !ok || items[sectionPrefix+id].Key != "" {
			return nil, fmt.Errorf("sidebar: %s: section without a unique id", name)
		}
		it, err := model.NewItem(sectionPrefix+id, e, e, stamp, false)
		if err != nil {
			return nil, fmt.Errorf("sidebar: %s: %w", name, err)
		}
		items[it.Key] = it
		order = append(order, id)
	}
	return order, nil
}

// Read implements sources.Adapter. Items come from the config mirror;
// every item whose Local Storage copies differ is listed in Stale and gets
// AppTime 0. Without a mirror value the LSS copy (then the store) is used.
func (SidebarView) Read(ctx context.Context, rc sources.ReadContext) (*model.RowSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l, err := viewLayout(rc.Spec)
	if err != nil {
		return nil, err
	}
	mirror, ls, ts, err := l.readCopies(rc)
	if err != nil {
		return nil, err
	}
	snap := sources.NewSnapshot(model.RowSidebarView)
	switch {
	case mirror.found:
		items, order, err := scopeItems(mirror.value, l.mirrorFile, model.Stamp{})
		if err != nil {
			return nil, err
		}
		stale, err := staleKeys(items, ls, l)
		if err != nil {
			return nil, err
		}
		for k, it := range items {
			if !stale[k] && ls[0].found {
				it.Stamp.AppTime = ts
			}
			snap.Items[k] = it
			if stale[k] {
				snap.Stale = append(snap.Stale, k)
			}
		}
		snap.Order = order
	case ls[0].found:
		return fillFrom(snap, ls[0], model.Stamp{AppTime: ts})
	case len(ls) > 1 && ls[1].found:
		return fillFrom(snap, ls[1], model.Stamp{})
	}
	sort.Strings(snap.Stale)
	return snap, nil
}

func fillFrom(snap *model.RowSnapshot, c copyValue, stamp model.Stamp) (*model.RowSnapshot, error) {
	items, order, err := scopeItems(c.value, c.doc.Name(), stamp)
	if err != nil {
		return nil, err
	}
	snap.Items, snap.Order = items, order
	return snap, nil
}

// staleKeys returns the item keys on which a present LS copy disagrees
// with the mirror's items.
func staleKeys(mirror map[string]model.Item, ls []copyValue, l layout) (map[string]bool, error) {
	stale := map[string]bool{}
	for _, c := range ls {
		if !c.found {
			continue
		}
		items, _, err := scopeItems(c.value, c.doc.Name(), model.Stamp{})
		if err != nil {
			return nil, err
		}
		for k, it := range mirror {
			if o, ok := items[k]; !ok || o.Hash != it.Hash {
				stale[k] = true
			}
		}
		for k := range items {
			if _, ok := mirror[k]; !ok {
				stale[k] = true
			}
		}
	}
	return stale, nil
}

// MirrorItems implements sources.MirrorReader.
func (SidebarView) MirrorItems(spec compat.RowSpec, mirror []byte) (*model.RowSnapshot, error) {
	l, err := viewLayout(spec)
	if err != nil {
		return nil, err
	}
	snap := sources.NewSnapshot(model.RowSidebarView)
	cv, err := valueAt(sources.Doc{Loc: locFile(l.mirrorFile), Present: true, Stored: mirror, Text: mirror}, l.mirrorPath)
	if err != nil || !cv.found {
		return snap, err
	}
	return fillFrom(snap, cv, model.Stamp{})
}

// sectionKeys returns the section item keys of s in section order.
func sectionKeys(s *model.RowSnapshot) []string {
	var fallback []string
	for _, id := range s.Order {
		fallback = append(fallback, sectionPrefix+id)
	}
	return sources.ItemsWithPrefix(&model.RowSnapshot{Items: s.Items, Order: fallback}, sectionPrefix, nil)
}

func sectionsChanged(current, desired *model.RowSnapshot) bool {
	a, b := sectionKeys(current), sectionKeys(desired)
	if len(a) != len(b) {
		return true
	}
	for i := range a {
		if a[i] != b[i] || current.Items[a[i]].Hash != desired.Items[b[i]].Hash {
			return true
		}
	}
	return false
}

// scopeObject builds a whole scope object from desired (for a missing LSS
// item).
func scopeObject(desired *model.RowSnapshot) (json.RawMessage, error) {
	obj := map[string]json.RawMessage{}
	for k, it := range desired.Items {
		if name, ok := strings.CutPrefix(k, fieldPrefix); ok {
			obj[name] = it.Value
		}
	}
	if secs := sectionKeys(desired); len(secs) > 0 {
		obj[sectionsMember] = sectionsArray(desired, secs)
	}
	return jsonx.Marshal(obj)
}

func sectionsArray(s *model.RowSnapshot, keys []string) json.RawMessage {
	elems := make([]json.RawMessage, len(keys))
	for i, k := range keys {
		elems[i] = s.Items[k].Value
	}
	return sources.JoinArray(elems)
}

// Ops implements sources.Adapter. Only the scope key's declared sub-paths
// are edited: ["<field>"] per changed field and ["sections"] when any
// section or their order changed, on the mirror, the LSS envelope and
// dframe-store (whose persisted version must match the manifest). The
// scope key is the target replica's; values are never rewritten.
func (SidebarView) Ops(ctx context.Context, oc sources.OpsContext, current, desired *model.RowSnapshot) (sources.OpsResult, error) {
	var res sources.OpsResult
	if err := ctx.Err(); err != nil {
		return res, err
	}
	row := model.RowSidebarView
	current, desired = sources.OrEmpty(current, row), sources.OrEmpty(desired, row)
	fields := changedFields(current, desired)
	secs := sectionsChanged(current, desired)
	if len(fields) == 0 && !secs {
		return res, nil
	}
	l, err := viewLayout(oc.Spec)
	if err != nil {
		return res, err
	}
	w, ok, err := newWriter(oc, l)
	if err != nil || !ok {
		for _, k := range fields {
			res.Skipped = append(res.Skipped, sources.Skip(row, k, sources.NoteMirrorMissing))
		}
		if secs {
			res.Skipped = append(res.Skipped, sources.Skip(row, sectionsMember, sources.NoteMirrorMissing))
		}
		return res, err
	}
	if w.lssFull, err = scopeObject(desired); err != nil {
		return res, err
	}
	for _, k := range fields {
		d, has := desired.Items[k]
		if err := w.setOrDelete(jsonx.Path{strings.TrimPrefix(k, fieldPrefix)}, d.Value, has, k); err != nil {
			return res, err
		}
	}
	if secs {
		keys := sectionKeys(desired)
		if err := w.setOrDelete(jsonx.Path{sectionsMember}, sectionsArray(desired, keys), true, sectionsMember); err != nil {
			return res, err
		}
	}
	return w.result(), nil
}

// changedFields returns the sorted field item keys that differ.
func changedFields(current, desired *model.RowSnapshot) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range []*model.RowSnapshot{current, desired} {
		for k := range s.Items {
			if seen[k] || !strings.HasPrefix(k, fieldPrefix) {
				continue
			}
			seen[k] = true
			c, okc := current.Items[k]
			d, okd := desired.Items[k]
			if okc != okd || c.Hash != d.Hash {
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}
