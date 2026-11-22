package model

import (
	"encoding/json"
	"testing"
)

func TestItemHashIgnoresKeyOrderAndSpacing(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		same bool
	}{
		{"key order", `{"a":1,"b":[1,2]}`, `{ "b": [1, 2], "a": 1 }`, true},
		{"number text kept", `{"a":1.0}`, `{"a":1}`, false},
		{"html not escaped", `"<a&b>"`, `"<a&b>"`, true},
		{"array order matters", `[1,2]`, `[2,1]`, false},
		{"nested", `{"x":{"b":1,"a":2}}`, `{"x":{"a":2,"b":1}}`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ha, err := ItemHash(json.RawMessage(tc.a))
			if err != nil {
				t.Fatal(err)
			}
			hb, err := ItemHash(json.RawMessage(tc.b))
			if err != nil {
				t.Fatal(err)
			}
			if (ha == hb) != tc.same {
				t.Fatalf("hash equal = %v, want %v", ha == hb, tc.same)
			}
		})
	}
}

func TestCanonicalRejectsTrailingData(t *testing.T) {
	for _, in := range []string{`{} {}`, `{`, ``} {
		if _, err := Canonical(json.RawMessage(in)); err == nil {
			t.Fatalf("%q accepted", in)
		}
	}
}

func TestNewItemHashesValueNotRaw(t *testing.T) {
	a, err := NewItem("order", json.RawMessage(`["x","y"]`), []byte(`{"value":["x","y"],"timestamp":1}`), Stamp{AppTime: 1}, false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewItem("order", json.RawMessage(`["x", "y"]`), []byte(`["x","y"]`), Stamp{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash != b.Hash {
		t.Fatal("envelope metadata changed the item hash")
	}
}
