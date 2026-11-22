package rows

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/kazupc/paritydesk/internal/model"
)

// Item key prefixes of the environments row (plan section 5).
const (
	presetPrefix  = "preset/"
	startedPrefix = "started/"
)

// The app's limits for one account's presets (row 6).
const (
	MaxPresets     = 200
	MaxPresetBytes = 512 << 10
)

// ErrPresetLimit reports a merge whose presets exceed the app's limits.
var ErrPresetLimit = errors.New("rows: environments exceed the app's preset limits")

// environmentsModule merges presets, the session to environment map and
// place defaults (rows 6, 7).
type environmentsModule struct{}

func (environmentsModule) Row() model.RowID { return model.RowEnvironments }

// Merge implements Module. Presets are matched by id first; a leaving
// preset without an id match takes the id of the entering preset with the
// same name only when that name has exactly one unmatched candidate on
// each side; otherwise both are kept and reported. The resulting remap
// moves the leaving side (and canonical) into the entering id space,
// rewrites started/<session id> values and is returned in Output.EnvRemap.
// Settings merge per field; secret values stay hashed; the app's limits
// are enforced on Desired.
func (environmentsModule) Merge(in Input) (Output, error) {
	if err := checkRow(in, model.RowEnvironments); err != nil {
		return Output{}, err
	}
	f := in.Policy.Fields
	nameField := first(f[RoleName])
	remap, collisions := matchPresets(in, nameField)
	in, err := remapEnvironments(in, remap)
	if err != nil {
		return Output{}, err
	}
	spec := keyedSpec{
		row:           model.RowEnvironments,
		fieldMerge:    recordMerger(f[RolePerField], f[RoleMaxTime]),
		secretAllowed: secretAllow(in.Policy.SecretsAllow, f[RoleSecret], nameField, presetPrefix),
	}
	out, err := runKeyed(in, spec)
	if err != nil {
		return Output{}, err
	}
	if err := checkPresetLimits(out.Desired); err != nil {
		return Output{}, err
	}
	if err := unmapEnvOverlay(out.LeavingOverlay, remap); err != nil {
		return Output{}, err
	}
	out.EnvRemap = remap
	annotateCollisions(out.Changes, collisions)
	return out, nil
}

// presetNames maps preset ids to names over the given snapshots (earlier
// snapshots win).
func presetNames(nameField string, snaps ...*model.RowSnapshot) map[string]string {
	names := map[string]string{}
	for _, s := range snaps {
		if s == nil {
			continue
		}
		for k, it := range s.Items {
			id, ok := strings.CutPrefix(k, presetPrefix)
			if !ok {
				continue
			}
			if _, seen := names[id]; seen {
				continue
			}
			n, _ := getString(it.Value, nameField)
			names[id] = n
		}
	}
	return names
}

// matchPresets returns the leaving→entering id remap from the unique-name
// fallback and the leaving preset keys whose name was ambiguous.
func matchPresets(in Input, nameField string) (map[string]string, []string) {
	if nameField == "" || in.Leaving.Snapshot == nil || in.Entering.Snapshot == nil {
		return nil, nil
	}
	lNames := presetNames(nameField, in.Leaving.Snapshot, in.Leaving.Base, in.Canonical)
	eNames := presetNames(nameField, in.Entering.Snapshot, in.Entering.Base)
	byName := func(own, other map[string]string) map[string][]string {
		out := map[string][]string{}
		for id, n := range own {
			if _, matched := other[id]; matched || n == "" {
				continue
			}
			out[n] = append(out[n], id)
		}
		return out
	}
	lUn, eUn := byName(lNames, eNames), byName(eNames, lNames)
	remap := map[string]string{}
	var collisions []string
	for n, lids := range lUn {
		eids := eUn[n]
		switch {
		case len(eids) == 0:
		case len(lids) == 1 && len(eids) == 1:
			remap[lids[0]] = eids[0]
		default:
			for _, id := range lids {
				collisions = append(collisions, presetPrefix+id)
			}
		}
	}
	sort.Strings(collisions)
	if len(remap) == 0 {
		remap = nil
	}
	return remap, collisions
}

// remapEnvironments renames matched leaving presets to the entering ids
// and rewrites every id string in the leaving snapshot, its base and
// canonical. Inputs are never mutated.
func remapEnvironments(in Input, remap map[string]string) (Input, error) {
	if len(remap) == 0 {
		return in, nil
	}
	var err error
	if in.Leaving.Snapshot, err = remapSnapshot(in.Leaving.Snapshot, remap); err != nil {
		return in, err
	}
	if in.Leaving.Base, err = remapSnapshot(in.Leaving.Base, remap); err != nil {
		return in, err
	}
	if in.Canonical, err = remapSnapshot(in.Canonical, remap); err != nil {
		return in, err
	}
	return in, nil
}

func remapKey(k string, remap map[string]string) string {
	if id, ok := strings.CutPrefix(k, presetPrefix); ok {
		if to, ok := remap[id]; ok {
			return presetPrefix + to
		}
	}
	return k
}

func remapSnapshot(s *model.RowSnapshot, remap map[string]string) (*model.RowSnapshot, error) {
	if s == nil {
		return nil, nil
	}
	c := s.Clone()
	c.Items = make(map[string]model.Item, len(s.Items))
	for k, it := range s.Items {
		nit, err := remapItem(remapKey(k, remap), it, remap)
		if err != nil {
			return nil, err
		}
		c.Items[nit.Key] = nit
	}
	if s.Tombstones != nil {
		c.Tombstones = make(map[string]model.Tombstone, len(s.Tombstones))
		for k, t := range s.Tombstones {
			t.Key = remapKey(k, remap)
			c.Tombstones[t.Key] = t
		}
	}
	return c, nil
}

// remapItem rewrites id strings in an item's value and raw bytes and sets
// its key.
func remapItem(key string, it model.Item, remap map[string]string) (model.Item, error) {
	val, changed, err := rewriteStrings(it.Value, remap)
	if err != nil {
		return model.Item{}, fmt.Errorf("item %q: %w", it.Key, err)
	}
	raw := it.Raw
	if changed && len(raw) > 0 {
		if r, _, rerr := rewriteStrings(raw, remap); rerr == nil {
			raw = r
		}
	}
	if !changed && key == it.Key {
		return it, nil
	}
	return model.NewItem(key, val, raw, it.Stamp, it.Secret)
}

func unmapEnvOverlay(ov *model.RowSnapshot, remap map[string]string) error {
	if ov == nil || len(remap) == 0 {
		return nil
	}
	back, err := remapSnapshot(ov, invert(remap))
	if err != nil {
		return err
	}
	ov.Items, ov.Tombstones = back.Items, back.Tombstones
	return nil
}

func annotateCollisions(changes []model.Change, collisions []string) {
	set := map[string]bool{}
	for _, k := range collisions {
		set[k] = true
	}
	for i := range changes {
		if set[changes[i].Key] {
			changes[i].Note = noteCollision
		}
	}
}

// checkPresetLimits enforces the app's 200-preset and 512 KiB limits on the
// serialized preset list (raw bytes when known, else the value).
func checkPresetLimits(d *model.RowSnapshot) error {
	if d == nil {
		return nil
	}
	n, size := 0, 2
	for k, it := range d.Items {
		if !strings.HasPrefix(k, presetPrefix) {
			continue
		}
		n++
		b := it.Raw
		if len(b) == 0 {
			b = it.Value
		}
		size += len(b) + 1
	}
	if n > MaxPresets {
		return fmt.Errorf("%w: %d presets, limit %d", ErrPresetLimit, n, MaxPresets)
	}
	if size > MaxPresetBytes {
		return fmt.Errorf("%w: %d bytes, limit %d", ErrPresetLimit, size, MaxPresetBytes)
	}
	return nil
}

// secretAllow returns the sync.secrets_allow check for a row whose secret
// env-var maps sit at the dotted paths in secretPaths. Every variable whose
// value would change must be listed as "<name>/<VAR>", where name is the
// item's id (the key without prefix) or its name field. Without secret
// paths nothing is allowed.
func secretAllow(allow map[string]bool, secretPaths []string, nameField, prefix string) func(string, *model.Item, *model.Item) bool {
	return func(k string, cand, cur *model.Item) bool {
		if len(secretPaths) == 0 {
			return false
		}
		names := []string{strings.TrimPrefix(k, prefix)}
		for _, it := range []*model.Item{cand, cur} {
			if it == nil {
				continue
			}
			if n, ok := getString(it.Value, nameField); ok && n != "" {
				names = append(names, n)
			}
		}
		for _, v := range changedSecretVars(cand, cur, secretPaths) {
			ok := false
			for _, n := range names {
				ok = ok || allow[n+"/"+v]
			}
			if !ok {
				return false
			}
		}
		return true
	}
}

// changedSecretVars lists the variables under the secret paths whose
// hashed value differs between a and b.
func changedSecretVars(a, b *model.Item, secretPaths []string) []string {
	set := map[string]bool{}
	for _, sp := range secretPaths {
		p := strings.Split(sp, ".")
		am, bm := secretMap(a, p), secretMap(b, p)
		for n, av := range am {
			if bv, ok := bm[n]; !ok || !sameJSON(av, true, bv, true) {
				set[n] = true
			}
		}
		for n := range bm {
			if _, ok := am[n]; !ok {
				set[n] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func secretMap(it *model.Item, p []string) map[string][]byte {
	out := map[string][]byte{}
	if it == nil {
		return out
	}
	v, ok := getAt(it.Value, p)
	if !ok {
		return out
	}
	ms, err := objectMembers(v)
	if err != nil {
		return out
	}
	for n, x := range ms {
		out[n] = x
	}
	return out
}
