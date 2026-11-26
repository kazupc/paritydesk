package legacy

import (
	"errors"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/kazupc/paritydesk/internal/ids"
)

func TestDeriveAlias(t *testing.T) {
	long := "Claude-" + strings.Repeat("ab", 24)
	cases := []struct {
		in   string
		want ids.Alias
		err  error
	}{
		{"Claude-Work", "work", nil},
		{"Claude-my.home", "my-home", nil},
		{"Claude-A_B", "a-b", nil},
		{"Claude--x-", "x", nil},
		{"Claude-a..b__c", "a-b-c", nil},
		{"Claude-...", "", ErrAliasEmpty},
		{"Claude-", "", ErrAliasEmpty},
		{long, ids.Alias(strings.Repeat("ab", 16)), nil},
		{"Claude-" + strings.Repeat("a", 31) + ".b", ids.Alias(strings.Repeat("a", 31)), nil},
		{"Claude", "", ErrNoDerivedAlias},
		{"Other-x", "", ErrNoDerivedAlias},
		{"Claude-caf" + string(rune(0xe9)), "", ErrAliasChars},
		{"Claude-a b", "", ErrAliasChars},
	}
	for _, c := range cases {
		got, err := DeriveAlias(c.in)
		if got != c.want || !errors.Is(err, c.err) {
			t.Errorf("DeriveAlias(%q) = %q, %v; want %q, %v", c.in, got, err, c.want, c.err)
		}
		if err == nil {
			if _, perr := ids.ParseAlias(string(got)); perr != nil {
				t.Errorf("DeriveAlias(%q) = %q is not a valid alias", c.in, got)
			}
		}
	}
	if len(mustAlias(t, long)) != 32 {
		t.Error("long names are cut to 32 bytes")
	}
}

func mustAlias(t *testing.T, s string) ids.Alias {
	t.Helper()
	a, err := DeriveAlias(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// DeriveAlias is total on valid parked names: a valid alias or a
// documented error, the same every time.
func TestPropDeriveAlias(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		name := "Claude-" + rapid.StringMatching(`[A-Za-z0-9._-]{1,48}`).Draw(t, "name")
		a, err := DeriveAlias(name)
		b, err2 := DeriveAlias(name)
		if a != b || !errors.Is(err2, err) && err != err2 {
			t.Fatalf("not deterministic: %q/%v vs %q/%v", a, err, b, err2)
		}
		if err != nil {
			if !errors.Is(err, ErrAliasEmpty) {
				t.Fatalf("%q: undocumented error %v", name, err)
			}
			return
		}
		if _, perr := ids.ParseAlias(string(a)); perr != nil {
			t.Fatalf("%q gives invalid alias %q", name, a)
		}
	})
}
