package rows

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/merge"
	"github.com/kazupc/paritydesk/internal/model"
)

// Field roles of row sidebar-seed (compat.RowSpec.Fields) and its constant
// notes.
const (
	RoleSeedKeys    = "seed_keys"    // item keys, in mirror_paths order
	RoleSeedOnly    = "seed_only"    // keys written only to a folder whose scope is not seeded
	RoleSeedFrom    = "seed_from"    // dotted member of the seed section giving each key's value
	RoleSeedSection = "seed_section" // the built-in section the values come from
	noteSeed        = "seed for a folder Claude has not set up yet"
	noteFromView    = "set to match the carried sidebar view"
)

// seedModule merges row sidebar-seed: the app's legacy per-account sidebar
// keys. Each key merges three-way; a seed-only key is never written to a
// folder whose scope is seeded; an absent candidate never deletes the
// target's key (the app itself deletes these keys when an account's session
// ends). When the sidebar-view row carried a value for a key
// (Policy.SeedFrom), the target gets that value, so the seed and the live
// setting match the carried view.
type seedModule struct{}

func (seedModule) Row() model.RowID { return model.RowSidebarSeed }

// Merge implements Module.
func (seedModule) Merge(in Input) (Output, error) {
	if err := checkRow(in, model.RowSidebarSeed); err != nil {
		return Output{}, err
	}
	only := listSet(in.Policy.Fields[RoleSeedOnly])
	seeded := in.Policy.EnteringSeeded
	// The entering folder's state could not be read (the orchestrator
	// then sets both facts): the row is left unchanged.
	frozen := in.Policy.EnteringStoreMissing
	out, err := runKeyed(in, keyedSpec{
		row:              model.RowSidebarSeed,
		strictTombstones: true,
		keepTarget: func(k string, cand, _ *model.Item) bool {
			return frozen || cand == nil || (only[k] && seeded)
		},
	})
	if err != nil {
		return Output{}, err
	}
	if in.Policy.EnteringImports && out.Desired != nil && !frozen {
		keys := listSet(in.Policy.Fields[RoleSeedKeys])
		from := make([]string, 0, len(in.Policy.SeedFrom))
		for k := range in.Policy.SeedFrom {
			from = append(from, k)
		}
		sort.Strings(from)
		for _, k := range from {
			if !keys[k] || (only[k] && seeded) {
				continue
			}
			v := in.Policy.SeedFrom[k]
			it, err := model.NewItem(k, v, v, model.Stamp{}, false)
			if err != nil {
				return Output{}, fmt.Errorf("rows: %s: %w", model.RowSidebarSeed, err)
			}
			out.Desired.Items[k] = it
			out.Changes = withoutKey(out.Changes, k)
			var cur *model.Item
			if in.Entering.Snapshot != nil {
				if c, ok := in.Entering.Snapshot.Items[k]; ok {
					cur = &c
				}
			}
			kind := merge.KindOf(cur, &it, "")
			if kind == "" {
				continue
			}
			note := noteFromView
			if only[k] {
				note = noteSeed
			}
			out.Changes = append(out.Changes, model.Change{Row: model.RowSidebarSeed, Kind: kind, Key: k, Note: note})
		}
	}
	// Rows that never delete: nothing needs confirming.
	out.NeedsConfirmation = false
	return out, nil
}

func listSet(xs []string) map[string]bool {
	out := make(map[string]bool, len(xs))
	for _, x := range xs {
		out[x] = true
	}
	return out
}

func withoutKey(changes []model.Change, k string) []model.Change {
	out := changes[:0:0]
	for _, c := range changes {
		if c.Key != k {
			out = append(out, c)
		}
	}
	return out
}

// SeedValues returns, by seed key, the JSON boolean found at seed_from[i]
// (dotted path) in the item "section:<seed_section>" of the sidebar-view
// row's Desired. Keys whose value is absent or not a boolean are left out.
// A nil snapshot or missing section gives an empty map.
func SeedValues(sidebarDesired *model.RowSnapshot, fields map[string][]string) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	keys, from, secs := fields[RoleSeedKeys], fields[RoleSeedFrom], fields[RoleSeedSection]
	if len(keys) != len(from) || len(secs) > 1 || (len(keys) > 0 && len(secs) != 1) {
		return nil, fmt.Errorf("rows: %s: seed_keys, seed_from and seed_section do not match", model.RowSidebarSeed)
	}
	if sidebarDesired == nil || len(keys) == 0 {
		return out, nil
	}
	sec, ok := sidebarDesired.Items[sectionPrefix+secs[0]]
	if !ok || !isObject(sec.Value) {
		return out, nil
	}
	for i, k := range keys {
		v, ok := getAt(sec.Value, jsonx.Path(strings.Split(from[i], ".")))
		if !ok {
			continue
		}
		var b bool
		if t := strings.TrimSpace(string(v)); (t != "true" && t != "false") || json.Unmarshal(v, &b) != nil {
			continue
		}
		if b {
			out[k] = json.RawMessage("true")
		} else {
			out[k] = json.RawMessage("false")
		}
	}
	return out, nil
}
