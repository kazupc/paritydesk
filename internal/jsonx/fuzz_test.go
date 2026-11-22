package jsonx

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

// seedDocs are shared seeds for the fuzz targets.
var seedDocs = []string{
	`{}`,
	` { } `,
	`{"a":1}`,
	`{"a":1,"b":{"c":[1,2,{"d":null}]},"e":"x"}`,
	"{\n  \"a.b\": 1.50,\n  \"c\": {\"x*y\": [1, 2]},\n  \"d\": \"<&>\"\n}\n",
	`{"value":{"k":[true,false]},"tabId":"t","timestamp":12}`,
	`{"\u0061":1,"b":2}`,
	`{"a":1,"a":2}`,
	`{"a":{"x":1,"x":2}}`,
	`{"\ud800":1}`,
	"{\"\xff\":1}",
	`[1,2]`,
	`"str"`,
	`{"a":"\u00e9\u03a9"}`,
	`{"a" : 1 , "b" : 2 }`,
	`{"a":1,}`,
	`{"a":`,
}

// path builds a one- or two-segment path from fuzz input.
func fuzzPath(k1, k2 string, deep bool) Path {
	if deep {
		return Path{k1, k2}
	}
	return Path{k1}
}

// commonAffixes returns the lengths of the common prefix and suffix of a
// and b, with the suffix limited so the two never overlap in either.
func commonAffixes(a, b []byte) (int, int) {
	p := 0
	for p < len(a) && p < len(b) && a[p] == b[p] {
		p++
	}
	s := 0
	for s < len(a)-p && s < len(b)-p && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	return p, s
}

func FuzzJSONXSet(f *testing.F) {
	for _, d := range seedDocs {
		f.Add([]byte(d), "a", "c", false, []byte(`[1,{"z":2}]`))
		f.Add([]byte(d), "b", "c", true, []byte(` "v" `))
		f.Add([]byte(d), "new", "", false, []byte(`null`))
		f.Add([]byte(d), "\xff", "", false, []byte(`1`))
		f.Add([]byte(d), "\ufffd", "", false, []byte(`1`))
	}
	f.Fuzz(func(t *testing.T, doc []byte, k1, k2 string, deep bool, v []byte) {
		path := fuzzPath(k1, k2, deep)
		out, err := Set(doc, path, v)
		if err != nil {
			return
		}
		if Validate(doc) != nil || Validate(v) != nil {
			t.Fatalf("Set accepted invalid input")
		}
		if err := Validate(out); err != nil {
			t.Fatalf("result does not validate: %v\n%q", err, out)
		}
		got, err := Get(out, path)
		if err != nil {
			t.Fatalf("Get after Set: %v", err)
		}
		if !bytes.Equal(got, bytes.TrimSpace(v)) {
			t.Fatalf("Get returned %q, want %q", got, bytes.TrimSpace(v))
		}
		// Only the old value's span (or, for an insert, nothing) changed.
		loc, err := locate(doc, path)
		if err != nil {
			t.Fatal(err)
		}
		if loc.index < 0 {
			if p, s := commonAffixes(doc, out); p+s != len(doc) {
				t.Fatalf("insert changed existing bytes: %q -> %q", doc, out)
			}
			return
		}
		m := loc.siblings[loc.index]
		want := append(append(append([]byte{}, doc[:m.valStart]...), bytes.TrimSpace(v)...), doc[m.valEnd:]...)
		if !bytes.Equal(out, want) {
			t.Fatalf("bytes outside the value changed: %q -> %q", doc, out)
		}
	})
}

func FuzzJSONXDelete(f *testing.F) {
	for _, d := range seedDocs {
		f.Add([]byte(d), "a", "", false)
		f.Add([]byte(d), "b", "c", true)
		f.Add([]byte(d), "e", "", false)
	}
	f.Fuzz(func(t *testing.T, doc []byte, k1, k2 string, deep bool) {
		path := fuzzPath(k1, k2, deep)
		before, err := locate(doc, path)
		out, derr := Delete(doc, path)
		if derr != nil {
			return
		}
		if err != nil || before.index < 0 {
			t.Fatalf("Delete succeeded on an absent member")
		}
		if err := Validate(out); err != nil {
			t.Fatalf("result does not validate: %v\n%q", err, out)
		}
		if _, err := Get(out, path); !errors.Is(err, ErrNotFound) {
			t.Fatalf("member still present: %v", err)
		}
		// The removal is one contiguous span.
		if p, s := commonAffixes(doc, out); p+s != len(out) {
			t.Fatalf("delete changed other bytes: %q -> %q", doc, out)
		}
		// Every sibling keeps its exact bytes.
		parent := path[:len(path)-1]
		for j, m := range before.siblings {
			if j == before.index {
				continue
			}
			got, err := Get(out, append(append(Path{}, parent...), m.key))
			if err != nil || !bytes.Equal(got, doc[m.valStart:m.valEnd]) {
				t.Fatalf("sibling %q changed: %q %v", m.key, got, err)
			}
		}
	})
}

func FuzzTopLevelString(f *testing.F) {
	for _, d := range seedDocs {
		f.Add([]byte(d), "a")
		f.Add([]byte(d), "e")
		f.Add([]byte(d), "\ufffd")
	}
	f.Fuzz(func(t *testing.T, doc []byte, key string) {
		s, found, err := TopLevelString(doc, key)
		has, herr := HasTopLevelKey(doc, key)
		if (err == nil || errors.Is(err, ErrNotString)) != (herr == nil) || (herr == nil && has != found) {
			t.Fatalf("TopLevelString and HasTopLevelKey disagree: %v/%v %v/%v", found, err, has, herr)
		}
		if Validate(doc) != nil {
			return
		}
		var m map[string]json.RawMessage
		if json.Unmarshal(doc, &m) != nil || m == nil {
			if err == nil {
				t.Fatalf("accepted a non-object: %q", doc)
			}
			return
		}
		raw, ok := m[key]
		if err != nil && !errors.Is(err, ErrNotString) {
			t.Fatalf("valid object refused: %v", err)
		}
		if found != ok {
			t.Fatalf("found=%v, encoding/json says %v", found, ok)
		}
		if !ok {
			return
		}
		var want string
		if json.Unmarshal(raw, &want) != nil {
			if !errors.Is(err, ErrNotString) {
				t.Fatalf("non-string accepted: %q", raw)
			}
			return
		}
		if err != nil || s != want {
			t.Fatalf("got %q %v, want %q", s, err, want)
		}
	})
}

func TestSetRefusesInvalidUTF8PathKey(t *testing.T) {
	for _, path := range []Path{{"\xff"}, {"a", "\xff"}, {"\xff", "a"}} {
		if _, err := Set([]byte(`{"a":{},"\ufffd":{}}`), path, []byte(`1`)); err == nil {
			t.Errorf("%q: Set accepted an invalid UTF-8 key", path)
		}
	}
}

func TestTopLevelRefusesAnyDuplicate(t *testing.T) {
	for _, doc := range []string{
		`{"x":1,"k":"v","x":2}`,
		`{"x":1,"k":"v","\u0078":2}`,
	} {
		if _, _, err := TopLevelString([]byte(doc), "k"); !errors.Is(err, ErrDuplicateKey) {
			t.Errorf("%s: TopLevelString: %v", doc, err)
		}
		if _, err := HasTopLevelKey([]byte(doc), "k"); !errors.Is(err, ErrDuplicateKey) {
			t.Errorf("%s: HasTopLevelKey: %v", doc, err)
		}
	}
	// Nested duplicates are not the top level's concern; values stay opaque.
	if s, found, err := TopLevelString([]byte(`{"k":"v","o":{"x":1,"x":2}}`), "k"); err != nil || !found || s != "v" {
		t.Fatalf("got %q %v %v", s, found, err)
	}
}
