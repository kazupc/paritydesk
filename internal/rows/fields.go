package rows

import (
	"encoding/json"
	"sort"
	"strconv"

	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/merge"
	"github.com/kazupc/paritydesk/internal/model"
)

// recordMerger returns a field merge over the given field lists, or nil
// when both are empty (no field merge, whole-record Prefer).
func recordMerger(perField, maxTime []string) fieldMergeFunc {
	if len(perField) == 0 && len(maxTime) == 0 {
		return nil
	}
	return func(p pair) (model.Item, bool, bool, error) {
		return mergeRecord(p, perField, maxTime)
	}
}

// mergeRecord merges a record both sides changed (D8). The result starts
// from the winner's record; each per-field field comes from the side that
// changed it (the winner when both did; object values are merged member by
// member first); max-time fields take the maximum. Edits go through jsonx
// on the winner's value and, when it mirrors the value, its raw bytes. Any
// failure falls back to the whole winner (ok=false); so does a secret item
// whose raw bytes cannot follow the merge.
func mergeRecord(p pair, perField, maxTime []string) (model.Item, bool, bool, error) {
	w, lo := p.win(), p.lose()
	if !isObject(w.Item.Value) || !isObject(lo.Item.Value) {
		return model.Item{}, false, false, nil
	}
	d := newEditDoc(*w.Item)
	secret := w.Item.Secret || lo.Item.Secret
	conflict := false
	for _, f := range perField {
		c, err := mergeField(d, jsonx.Path{f}, w, lo, 1)
		if err != nil {
			return model.Item{}, false, false, nil
		}
		conflict = conflict || c
	}
	for _, f := range maxTime {
		if err := maxField(d, jsonx.Path{f}, *lo.Item); err != nil {
			return model.Item{}, false, false, nil
		}
	}
	if secret && d.raw == nil {
		return model.Item{}, false, false, nil
	}
	if !conflict {
		conflict = otherFieldsLost(w, lo, append(append([]string(nil), perField...), maxTime...))
	}
	st := w.Item.Stamp
	if lo.Item.Stamp.AppTime > st.AppTime {
		st.AppTime, st.ToolWritten = lo.Item.Stamp.AppTime, lo.Item.Stamp.ToolWritten
	}
	it, err := d.item(p.key, st, secret)
	if err != nil {
		return model.Item{}, false, false, nil
	}
	return it, true, conflict, nil
}

// fieldChanged reports whether v changed the value at path since its base.
// Without a usable base, presence counts as a change.
func fieldChanged(v merge.Version, path jsonx.Path) bool {
	cur, has := getAt(v.Item.Value, path)
	if !v.BaseValid || v.Base == nil {
		return has
	}
	b, bhas := getAt(v.Base.Value, path)
	return !sameJSON(cur, has, b, bhas)
}

// mergeField merges one field into d (which starts as the winner). depth
// > 0 allows a member-wise merge of object values. It reports a conflict
// when both sides changed the field to different values.
func mergeField(d *editDoc, path jsonx.Path, w, lo merge.Version, depth int) (bool, error) {
	wc, lc := fieldChanged(w, path), fieldChanged(lo, path)
	if !lc {
		return false, nil
	}
	if !wc {
		return false, d.copyFrom(path, *lo.Item)
	}
	wv, wok := getAt(w.Item.Value, path)
	lv, lok := getAt(lo.Item.Value, path)
	if sameJSON(wv, wok, lv, lok) {
		return false, nil
	}
	if depth > 0 && wok && lok && isObject(wv) && isObject(lv) {
		return mergeMembers(d, path, w, lo, wv, lv)
	}
	return true, nil
}

func mergeMembers(d *editDoc, path jsonx.Path, w, lo merge.Version, wv, lv []byte) (bool, error) {
	names := map[string]bool{}
	for _, obj := range [][]byte{wv, lv, baseAt(w, path), baseAt(lo, path)} {
		if obj == nil {
			continue
		}
		ms, err := objectMembers(obj)
		if err != nil {
			continue
		}
		for n := range ms {
			names[n] = true
		}
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	conflict := false
	for _, n := range sorted {
		sub := append(append(jsonx.Path(nil), path...), n)
		c, err := mergeField(d, sub, w, lo, 0)
		if err != nil {
			return false, err
		}
		conflict = conflict || c
	}
	return conflict, nil
}

func baseAt(v merge.Version, path jsonx.Path) []byte {
	if !v.BaseValid || v.Base == nil {
		return nil
	}
	b, ok := getAt(v.Base.Value, path)
	if !ok {
		return nil
	}
	return b
}

// maxField makes path hold the larger of d's value and other's value.
// Numbers compare numerically, strings lexically; an absent value loses.
func maxField(d *editDoc, path jsonx.Path, other model.Item) error {
	cur, cok := getAt(d.val, path)
	ov, ook := getAt(other.Value, path)
	if !ook || (cok && !greater(ov, cur)) {
		return nil
	}
	return d.copyFrom(path, other)
}

// greater reports whether a > b for two numbers or two strings.
func greater(a, b []byte) bool {
	var na, nb json.Number
	if json.Unmarshal(a, &na) == nil && json.Unmarshal(b, &nb) == nil {
		fa, err1 := strconv.ParseFloat(string(na), 64)
		fb, err2 := strconv.ParseFloat(string(nb), 64)
		return err1 == nil && err2 == nil && fa > fb
	}
	var sa, sb string
	if json.Unmarshal(a, &sa) == nil && json.Unmarshal(b, &sb) == nil {
		return sa > sb
	}
	return false
}

// otherFieldsLost reports whether the loser changed fields outside the
// merged lists that the winner's record does not carry, so the losing
// record must be kept as a conflict.
func otherFieldsLost(w, lo merge.Version, fields []string) bool {
	lr := strip(lo.Item.Value, fields)
	if lr == nil {
		return true
	}
	if lo.BaseValid && lo.Base != nil {
		if b := strip(lo.Base.Value, fields); b != nil && sameJSON(lr, true, b, true) {
			return false
		}
	}
	wr := strip(w.Item.Value, fields)
	return wr == nil || !sameJSON(lr, true, wr, true)
}

// strip removes the top-level fields from an object, or returns nil.
func strip(v []byte, fields []string) []byte {
	out := append([]byte(nil), v...)
	for _, f := range fields {
		var err error
		if out, err = deletePath(out, jsonx.Path{f}); err != nil {
			return nil
		}
	}
	return out
}

// listMerge merges an ordered list item (a JSON array of strings) both
// sides changed: the winner's order with the loser's additions placed next
// to their neighbours, and removals against valid bases applied.
func listMerge(p pair) (model.Item, bool, bool, error) {
	w, lo := p.win(), p.lose()
	wl, ok1 := stringList(w.Item.Value)
	ll, ok2 := stringList(lo.Item.Value)
	if !ok1 || !ok2 {
		return model.Item{}, false, false, nil
	}
	merged := merge.OrderedListBases(baseList(w), baseList(lo), wl, ll, w.BaseValid, lo.BaseValid)
	val, err := marshalList(merged)
	if err != nil {
		return model.Item{}, false, false, nil
	}
	it, err := model.NewItem(p.key, val, nil, w.Item.Stamp, false)
	if err != nil {
		return model.Item{}, false, false, nil
	}
	if it.Hash == w.Item.Hash {
		it.Raw = w.Item.Raw
	}
	return it, true, false, nil
}

func baseList(v merge.Version) []string {
	if !v.BaseValid || v.Base == nil {
		return nil
	}
	l, _ := stringList(v.Base.Value)
	return l
}
