package sidebar

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/sources"
)

// Field role of the seed row naming the keys written only to a folder
// whose sidebar scope is not seeded (compat.RowSpec.Fields).
const roleSeedOnly = "seed_only"

// Seed is the adapter for row model.RowSidebarSeed: the app's legacy
// per-account sidebar keys (one boolean each), mirrored in the config
// mirror and kept as LSS envelopes. Item keys are the manifest's
// fields.seed_keys, in the order of mirror_paths and ls_keys.
type Seed struct{}

var (
	_ sources.Adapter      = Seed{}
	_ sources.MirrorReader = Seed{}
	_ sources.TargetStater = Seed{}
)

// NewSeed returns the adapter.
func NewSeed() sources.Adapter { return Seed{} }

// Row implements sources.Adapter.
func (Seed) Row() model.RowID { return model.RowSidebarSeed }

// seedLayouts returns one layout per seed key (item key, mirror path, LSS
// key; LSS value path {"value"}; no store copy), built with newLayout from
// a one-path copy of spec. A spec that declares no keys at all (the row is
// not carried by this manifest) gives no layouts.
func seedLayouts(spec compat.RowSpec) ([]string, []layout, error) {
	keys := spec.Fields[sources.RoleSeedKeys]
	if len(keys) == 0 && len(spec.LSKeys) == 0 && len(spec.MirrorPaths) == 0 {
		return nil, nil, nil
	}
	if len(keys) == 0 || len(spec.MirrorPaths) != len(keys) || len(spec.LSKeys) != len(keys) {
		return nil, nil, fmt.Errorf("sidebar: %s: manifest spec lacks seed keys", model.RowSidebarSeed)
	}
	out := make([]layout, len(keys))
	for i := range keys {
		one := spec
		one.MirrorPaths = [][]string{spec.MirrorPaths[i]}
		one.LSKeys = []string{spec.LSKeys[i]}
		l, err := newLayout(model.RowSidebarSeed, one, func(jsonx.Path) (jsonx.Path, jsonx.Path) {
			return jsonx.Path{"value"}, nil
		})
		if err != nil {
			return nil, nil, err
		}
		out[i] = l
	}
	return keys, out, nil
}

// seedOnly returns the set of spec.Fields["seed_only"].
func seedOnly(spec compat.RowSpec) map[string]bool {
	out := map[string]bool{}
	for _, k := range spec.Fields[roleSeedOnly] {
		out[k] = true
	}
	return out
}

// seedItem checks that v is a JSON boolean and builds the item. Errors
// name the file or key only, never the value.
func seedItem(key string, v json.RawMessage, where string, stamp model.Stamp) (model.Item, error) {
	var b bool
	if t := bytesTrimLeft(v); len(t) == 0 || (t[0] != 't' && t[0] != 'f') || json.Unmarshal(v, &b) != nil {
		return model.Item{}, fmt.Errorf("sidebar: %s: unexpected shape in %s", model.RowSidebarSeed, where)
	}
	return model.NewItem(key, v, v, stamp, false)
}

// Read implements sources.Adapter. Each key's mirror value is
// authoritative; a differing LSS copy lists the key in Stale and gives
// AppTime 0; agreeing copies take the envelope timestamp; an LSS copy
// alone is used with its timestamp. The value must be a JSON boolean.
func (Seed) Read(ctx context.Context, rc sources.ReadContext) (*model.RowSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	keys, ls, err := seedLayouts(rc.Spec)
	if err != nil {
		return nil, err
	}
	snap := sources.NewSnapshot(model.RowSidebarSeed)
	for i, key := range keys {
		l := ls[i]
		mirror, copies, ts, err := l.readCopies(rc)
		if err != nil {
			return nil, err
		}
		value, where, stamp := mirror.value, l.mirrorFile, int64(0)
		switch {
		case mirror.found:
			if copies[0].found && !sameValue(copies[0].value, mirror.value) {
				snap.Stale = append(snap.Stale, key)
			} else if copies[0].found {
				stamp = ts
			}
		case copies[0].found:
			value, where, stamp = copies[0].value, l.lss.key, ts
		default:
			continue
		}
		it, err := seedItem(key, value, where, model.Stamp{AppTime: stamp})
		if err != nil {
			return nil, err
		}
		snap.Items[key] = it
	}
	sort.Strings(snap.Stale)
	return snap, nil
}

// MirrorItems implements sources.MirrorReader: the items of Read's mirror
// path.
func (Seed) MirrorItems(spec compat.RowSpec, mirror []byte) (*model.RowSnapshot, error) {
	keys, ls, err := seedLayouts(spec)
	if err != nil {
		return nil, err
	}
	snap := sources.NewSnapshot(model.RowSidebarSeed)
	for i, key := range keys {
		l := ls[i]
		cv, err := valueAt(sources.Doc{Loc: locFile(l.mirrorFile), Present: true, Stored: mirror, Text: mirror}, l.mirrorPath)
		if err != nil {
			return nil, err
		}
		if !cv.found {
			continue
		}
		it, err := seedItem(key, cv.value, l.mirrorFile, model.Stamp{})
		if err != nil {
			return nil, err
		}
		snap.Items[key] = it
	}
	return snap, nil
}

// TargetState implements sources.TargetStater: Seeded = seededState(rc).
// StoreMissing is always false (the LSS items are created when absent).
func (Seed) TargetState(ctx context.Context, rc sources.ReadContext) (sources.TargetState, error) {
	if err := ctx.Err(); err != nil {
		return sources.TargetState{}, err
	}
	return sources.TargetState{Seeded: seededState(rc)}, nil
}

// seededState reports whether rc's replica scope is seeded: the
// sidebar-view row's copies (rc.Manifest.Row(model.RowSidebarView),
// expanded for rc.Replica, read through viewLayout and readCopies) are
// checked for prefsSeeded == true. Any copy true → true. A copy present
// but not an object, a prefsSeeded that is not a boolean, a missing
// sidebar-view spec or any read error → true (unknown counts as seeded).
// All copies absent or false → false. migratedFrom is never read.
func seededState(rc sources.ReadContext) bool {
	if rc.Manifest == nil {
		return true
	}
	spec, ok := rc.Manifest.Row(model.RowSidebarView)
	if !ok {
		return true
	}
	l, err := viewLayout(spec.Expand(rc.Replica))
	if err != nil {
		return true
	}
	mirror, ls, _, err := l.readCopies(rc)
	if err != nil {
		return true
	}
	for _, c := range append([]copyValue{mirror}, ls...) {
		if !c.found {
			continue
		}
		obj, err := sources.DecodeObject(c.value, c.doc.Name())
		if err != nil {
			return true
		}
		raw, ok := obj[seededMember]
		if !ok {
			continue
		}
		var b bool
		if t := bytesTrimLeft(raw); len(t) == 0 || (t[0] != 't' && t[0] != 'f') || json.Unmarshal(raw, &b) != nil || b {
			return true
		}
	}
	return false
}

// Ops implements sources.Adapter. Each changed key is written on its
// mirror path and its LSS envelope (created when absent; its timestamp is
// reported in WrittenStamps). A key desired lacks is never deleted. A
// seed-only key is skipped when the folder's scope is seeded (re-checked
// on disk). dframe-code-sections, prefsSeeded and dframe-store are never
// written.
func (Seed) Ops(ctx context.Context, oc sources.OpsContext, current, desired *model.RowSnapshot) (sources.OpsResult, error) {
	var res sources.OpsResult
	if err := ctx.Err(); err != nil {
		return res, err
	}
	row := model.RowSidebarSeed
	current, desired = sources.OrEmpty(current, row), sources.OrEmpty(desired, row)
	if sources.SameItems(current, desired, false) {
		return res, nil
	}
	keys, ls, err := seedLayouts(oc.Spec)
	if err != nil {
		return res, err
	}
	only := seedOnly(oc.Spec)
	seeded, checked := false, false
	for i, key := range keys {
		d, has := desired.Items[key]
		if c, ok := current.Items[key]; !has || (ok && c.Hash == d.Hash) {
			continue
		}
		if only[key] {
			if !checked {
				seeded, checked = seededState(oc.ReadContext), true
			}
			if seeded {
				res.Skipped = append(res.Skipped, sources.Skip(row, key, sources.NoteSeeded))
				continue
			}
		}
		w, ok, err := newWriter(oc, ls[i])
		if err != nil {
			return res, err
		}
		if !ok {
			res.Skipped = append(res.Skipped, sources.Skip(row, key, sources.NoteMirrorMissing))
			continue
		}
		w.lssFull = d.Value
		if err := w.setOrDelete(nil, d.Value, true, key); err != nil {
			return res, err
		}
		r := w.result()
		res.Edits = append(res.Edits, r.Edits...)
		for k, v := range r.WrittenStamps {
			if res.WrittenStamps == nil {
				res.WrittenStamps = map[string]int64{}
			}
			res.WrittenStamps[k] = v
		}
	}
	return res, nil
}
