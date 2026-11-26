package merge

import (
	"slices"
	"testing"
)

func TestSetBases(t *testing.T) {
	tests := []struct {
		name         string
		lb, eb, l, e []string
		lv, ev       bool
		want         []string
	}{
		{"leaving removed x", []string{"x", "y"}, []string{"x", "y"}, []string{"y"}, []string{"x", "y"}, true, true, []string{"y"}},
		{"entering removed x", []string{"x"}, []string{"x", "z"}, []string{"x"}, []string{"z"}, true, true, []string{"z"}},
		{"entering added y", []string{"x"}, []string{"x"}, []string{"x"}, []string{"x", "y"}, true, true, []string{"x", "y"}},
		{"leaving added y", []string{"x"}, []string{"x"}, []string{"y", "x"}, []string{"x"}, true, true, []string{"x", "y"}},
		{"invalid leaving base never removes", []string{"x"}, []string{"x"}, nil, []string{"x"}, false, true, []string{"x"}},
		{"invalid entering base never removes", []string{"x"}, []string{"x"}, []string{"x"}, nil, true, false, []string{"x"}},
		{"base-only members not revived", []string{"a", "b"}, []string{"c"}, []string{"x"}, []string{"y"}, false, false, []string{"x", "y"}},
		{"different bases", []string{"a"}, []string{"b"}, []string{"b"}, []string{"a"}, true, true, []string{}},
		{"sorted and unique", nil, nil, []string{"c", "a", "a"}, []string{"b", "c"}, false, false, []string{"a", "b", "c"}},
		{"both empty", nil, nil, nil, nil, true, true, []string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SetBases(tc.lb, tc.eb, tc.l, tc.e, tc.lv, tc.ev)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("SetBases = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSetBasesUnionEqualsSet: without bases both functions are the union.
func TestSetBasesUnionEqualsSet(t *testing.T) {
	l, e := []string{"b", "a"}, []string{"c", "a"}
	if got, want := SetBases(nil, nil, l, e, false, false), Set(nil, l, e, false, false); !slices.Equal(got, want) {
		t.Fatalf("SetBases %v, Set %v", got, want)
	}
}
