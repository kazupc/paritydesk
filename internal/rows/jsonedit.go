package rows

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/model"
)

// editDoc is a record being edited at literal paths. val is the item's
// normalized value; raw is the item's raw bytes while they have the same
// top-level shape as val (so every edit can be applied to both), else nil.
type editDoc struct {
	val, raw []byte
	// Top-level members of val and raw, and of the last item copyFrom
	// read, parsed once: a session record can be hundreds of kilobytes
	// and the transform looks at dozens of its top-level fields, most of
	// which are absent or equal on both sides. nil means not parsed (or
	// stale after an edit).
	valTop, rawTop map[string]json.RawMessage
	from           *byte // identity of the item the from* maps describe
	fromVal        map[string]json.RawMessage
	fromRaw        map[string]json.RawMessage
}

// topOf returns doc's top-level members, or nil if doc is not a valid
// object (the slow path then handles it).
func topOf(doc []byte) map[string]json.RawMessage {
	if len(doc) == 0 {
		return nil
	}
	m, err := jsonx.TopLevelMembers(doc)
	if err != nil {
		return nil
	}
	return m
}

// sameTop reports whether key is absent from both maps or holds the same
// bytes in both. A nil map is unknown: never the same.
func sameTop(a, b map[string]json.RawMessage, key string) bool {
	if a == nil || b == nil {
		return false
	}
	av, aok := a[key]
	bv, bok := b[key]
	return aok == bok && (!aok || bytes.Equal(av, bv))
}

func (d *editDoc) tops() {
	if d.valTop == nil {
		d.valTop = memoTop(d.val)
	}
	if d.raw != nil && d.rawTop == nil {
		d.rawTop = memoTop(d.raw)
	}
}

func (d *editDoc) edited() { d.valTop, d.rawTop = nil, nil }

// newEditDoc starts from the item's own bytes: every edit builds a new
// slice (jsonx splices into a fresh buffer), so the item is never changed,
// and the parsed top-level members of its value can be reused.
func newEditDoc(it model.Item) *editDoc {
	d := &editDoc{val: it.Value}
	if mirrors(it.Value, it.Raw) {
		d.raw = it.Raw
	}
	return d
}

// mirrors reports whether raw is a JSON object with the same top-level
// member names as the object val.
func mirrors(val, raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	if bytes.Equal(val, raw) {
		return true
	}
	a, err := objectMembers(val)
	if err != nil {
		return false
	}
	b, err := objectMembers(raw)
	if err != nil || len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

// objectMembers decodes a JSON object's top-level members.
func objectMembers(doc []byte) (map[string]json.RawMessage, error) {
	if err := jsonx.Validate(doc); err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(doc, &m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, jsonx.ErrNotObject
	}
	return m, nil
}

func isObject(v []byte) bool {
	v = bytes.TrimSpace(v)
	return len(v) > 0 && v[0] == '{'
}

// getAt returns the value at path, or false when it is absent or doc is
// not an object along the path.
func getAt(doc []byte, path jsonx.Path) (json.RawMessage, bool) {
	if len(doc) == 0 {
		return nil, false
	}
	// The first segment comes from the document's top-level members,
	// parsed once per document (memoTop); the rest of the path is looked
	// up in that member alone.
	if len(path) > 0 {
		if top := memoTop(doc); top != nil {
			v, ok := top[path[0]]
			if !ok {
				return nil, false
			}
			if len(path) == 1 {
				return v, true
			}
			doc, path = v, path[1:]
		}
	}
	v, err := jsonx.Get(doc, path)
	if err != nil {
		return nil, false
	}
	return v, true
}

// getString returns the string value of the top-level field name.
func getString(doc []byte, name string) (string, bool) {
	if name == "" {
		return "", false
	}
	v, ok := getAt(doc, jsonx.Path{name})
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return "", false
	}
	return s, true
}

// getStringPath returns the string value at a nested path.
func getStringPath(doc []byte, path jsonx.Path) (string, bool) {
	if len(path) == 0 || path[0] == "" {
		return "", false
	}
	v, ok := getAt(doc, path)
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return "", false
	}
	return s, true
}

// sameJSON compares two optional JSON values by canonical form.
func sameJSON(a []byte, aok bool, b []byte, bok bool) bool {
	if !aok || !bok {
		return aok == bok
	}
	ha, err1 := model.ItemHash(a)
	hb, err2 := model.ItemHash(b)
	if err1 != nil || err2 != nil {
		return bytes.Equal(bytes.TrimSpace(a), bytes.TrimSpace(b))
	}
	return ha == hb
}

// setPath sets v at path, creating missing parent objects.
func setPath(doc []byte, path jsonx.Path, v json.RawMessage) ([]byte, error) {
	for i := 1; i < len(path); i++ {
		if _, err := jsonx.Get(doc, path[:i]); errors.Is(err, jsonx.ErrNotFound) {
			if doc, err = jsonx.Set(doc, path[:i], json.RawMessage(`{}`)); err != nil {
				return nil, err
			}
		} else if err != nil {
			return nil, err
		}
	}
	return jsonx.Set(doc, path, v)
}

// deletePath removes path if present.
func deletePath(doc []byte, path jsonx.Path) ([]byte, error) {
	out, err := jsonx.Delete(doc, path)
	if errors.Is(err, jsonx.ErrNotFound) {
		return doc, nil
	}
	return out, err
}

// set writes the same value at path in val and raw (non-secret values).
func (d *editDoc) set(path jsonx.Path, v json.RawMessage) error {
	d.edited()
	var err error
	if d.val, err = setPath(d.val, path, v); err != nil {
		return fmt.Errorf("rows: set %s: %w", strings.Join(path, "."), err)
	}
	if d.raw != nil {
		if d.raw, err = setPath(d.raw, path, v); err != nil {
			d.raw = nil
		}
	}
	return nil
}

// del removes path from val and raw.
func (d *editDoc) del(path jsonx.Path) error {
	if len(path) == 1 {
		d.tops()
		absent := map[string]json.RawMessage{}
		if sameTop(d.valTop, absent, path[0]) && (d.raw == nil || sameTop(d.rawTop, absent, path[0])) {
			return nil
		}
	}
	d.edited()
	var err error
	if d.val, err = deletePath(d.val, path); err != nil {
		return fmt.Errorf("rows: delete %s: %w", strings.Join(path, "."), err)
	}
	if d.raw != nil {
		if d.raw, err = deletePath(d.raw, path); err != nil {
			d.raw = nil
		}
	}
	return nil
}

// copyFrom makes path hold what it holds in from (or removes it), taking
// the raw counterpart from from's raw bytes so secrets stay consistent.
func (d *editDoc) copyFrom(path jsonx.Path, from model.Item) error {
	if len(path) == 1 && len(from.Value) > 0 {
		if d.from != &from.Value[0] {
			d.from, d.fromVal, d.fromRaw = &from.Value[0], memoTop(from.Value), nil
			if mirrors(from.Value, from.Raw) {
				d.fromRaw = memoTop(from.Raw)
			}
		}
		d.tops()
		rawSame := d.raw == nil || sameTop(d.rawTop, d.fromRaw, path[0])
		if sameTop(d.valTop, d.fromVal, path[0]) && rawSame {
			return nil
		}
	}
	d.edited()
	v, ok := getAt(from.Value, path)
	var err error
	if ok {
		d.val, err = setPath(d.val, path, v)
	} else {
		d.val, err = deletePath(d.val, path)
	}
	if err != nil {
		return fmt.Errorf("rows: copy %s: %w", strings.Join(path, "."), err)
	}
	if d.raw == nil {
		return nil
	}
	if !mirrors(from.Value, from.Raw) {
		d.raw = nil
		return nil
	}
	if rv, rok := getAt(from.Raw, path); rok {
		d.raw, err = setPath(d.raw, path, rv)
	} else {
		d.raw, err = deletePath(d.raw, path)
	}
	if err != nil {
		d.raw = nil
	}
	return nil
}

// item builds the edited item. Raw is the edited raw when it was kept in
// step, else nil.
func (d *editDoc) item(key string, st model.Stamp, secret bool) (model.Item, error) {
	return model.NewItem(key, d.val, d.raw, st, secret)
}

// rewriteStrings replaces every JSON string in v that is a key of m by its
// mapped value. It reports whether anything changed; unchanged input is
// returned as is.
func rewriteStrings(v json.RawMessage, m map[string]string) (json.RawMessage, bool, error) {
	if len(m) == 0 || len(v) == 0 {
		return v, false, nil
	}
	dec := json.NewDecoder(bytes.NewReader(v))
	dec.UseNumber()
	var x any
	if err := dec.Decode(&x); err != nil {
		return nil, false, fmt.Errorf("rows: rewrite ids: %w", err)
	}
	y, changed := walkStrings(x, m)
	if !changed {
		return v, false, nil
	}
	out, err := jsonx.Marshal(y)
	if err != nil {
		return nil, false, fmt.Errorf("rows: rewrite ids: %w", err)
	}
	return out, true, nil
}

func walkStrings(x any, m map[string]string) (any, bool) {
	switch t := x.(type) {
	case string:
		if r, ok := m[t]; ok {
			return r, true
		}
	case []any:
		changed := false
		for i := range t {
			var c bool
			t[i], c = walkStrings(t[i], m)
			changed = changed || c
		}
		return t, changed
	case map[string]any:
		changed := false
		for k := range t {
			var c bool
			t[k], c = walkStrings(t[k], m)
			changed = changed || c
		}
		return t, changed
	}
	return x, false
}

// stringList decodes a JSON array of strings.
func stringList(v []byte) ([]string, bool) {
	var out []string
	if err := json.Unmarshal(v, &out); err != nil {
		return nil, false
	}
	return out, true
}

// marshalList encodes a string list, never as null.
func marshalList(xs []string) (json.RawMessage, error) {
	if xs == nil {
		xs = []string{}
	}
	return jsonx.Marshal(xs)
}

// memoTop returns doc's top-level members, parsed once per document for
// the life of the process (documents are never mutated in place: edits
// build new slices). Large session records are looked at for dozens of
// fields by several merge steps; re-parsing each time dominated planning.
// nil means doc is not a valid object. The memo is cleared when it grows
// past memoLimit entries, so a long-lived process stays bounded.
func memoTop(doc []byte) map[string]json.RawMessage {
	if len(doc) < memoMinSize {
		return topOf(doc)
	}
	k := memoKey{p: &doc[0], n: len(doc)}
	memoMu.Lock()
	m, ok := memo[k]
	memoMu.Unlock()
	if ok {
		return m
	}
	m = topOf(doc)
	memoMu.Lock()
	if len(memo) >= memoLimit {
		memo = map[memoKey]map[string]json.RawMessage{}
	}
	memo[k] = m
	memoMu.Unlock()
	return m
}

type memoKey struct {
	p *byte
	n int
}

const (
	memoLimit   = 50000
	memoMinSize = 4096 // smaller documents parse faster than a lookup is worth
)

var (
	memoMu sync.Mutex
	memo   = map[memoKey]map[string]json.RawMessage{}
)
