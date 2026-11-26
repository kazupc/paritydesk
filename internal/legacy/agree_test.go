package legacy

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"

	"pgregory.net/rapid"

	"github.com/kazupc/paritydesk/internal/model"
)

func item(t interface {
	Helper()
	Fatalf(string, ...any)
}, key, value string, appTime int64) model.Item {
	t.Helper()
	it, err := model.NewItem(key, json.RawMessage(value), []byte(value), model.Stamp{AppTime: appTime}, false)
	if err != nil {
		t.Fatalf("item: %v", err)
	}
	return it
}

func rowSnap(items ...model.Item) *model.RowSnapshot {
	s := &model.RowSnapshot{Row: model.RowSessions, Schema: 1, Items: map[string]model.Item{}}
	for _, it := range items {
		s.Items[it.Key] = it
	}
	return s
}

func keysOf(s *model.RowSnapshot) []string {
	var out []string
	for k := range s.Items {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func TestAgree(t *testing.T) {
	ignore := []string{"acct", "busy"}
	a := rowSnap(item(t, "k1", `{"t":"x","acct":"A"}`, 1), item(t, "k2", `{"t":"y"}`, 2), item(t, "k3", `"plain"`, 3),
		item(t, "k4", `{"t":"z"}`, 4), item(t, "k5", `{"t":"s"}`, 5))
	b := rowSnap(item(t, "k1", `{"t":"x","acct":"B","busy":true}`, 9), item(t, "k2", `{"t":"other"}`, 2), item(t, "k3", `"plain"`, 8),
		item(t, "k5", `{"t":"s"}`, 5))
	b.Stale = []string{"k5"}
	a.Order, b.Order = []string{"k1", "k2"}, []string{"k1", "k2"}
	a.Tombstones = map[string]model.Tombstone{"gone": {Key: "gone", At: 10}, "other": {Key: "other", At: 3}}
	b.Tombstones = map[string]model.Tombstone{"gone": {Key: "gone", At: 10}, "other": {Key: "other", At: 4}}
	a.Items["k1"] = withRaw(a.Items["k1"])
	bases, err := Agree([]*model.RowSnapshot{a, b}, ignore)
	if err != nil {
		t.Fatal(err)
	}
	// k1 differs only in ignored fields; k3 is not an object, equal by
	// hash; k2 differs; k4 is missing on b; k5 is stale on b.
	for i, base := range bases {
		if got := keysOf(base); !slices.Equal(got, []string{"k1", "k3"}) {
			t.Fatalf("base %d keys %v", i, got)
		}
		if base.Stale != nil || base.Row != model.RowSessions || base.Schema != 1 {
			t.Errorf("base %d header %+v", i, base)
		}
		for k, it := range base.Items {
			if it.Raw != nil {
				t.Errorf("base %d item %s carries Raw", i, k)
			}
		}
		if !slices.Equal(base.Order, []string{"k1", "k2"}) {
			t.Errorf("base %d order %v", i, base.Order)
		}
		if len(base.Tombstones) != 1 || base.Tombstones["gone"].At != 10 {
			t.Errorf("base %d tombstones %v", i, base.Tombstones)
		}
	}
	// Each base holds its own side's item.
	if bases[0].Items["k1"].Hash != a.Items["k1"].Hash || bases[1].Items["k1"].Hash != b.Items["k1"].Hash {
		t.Error("a base holds another side's item")
	}
	if bases[1].Items["k3"].Stamp.AppTime != 8 {
		t.Error("the base lost its own stamp")
	}
	st, err := Stats([]*model.RowSnapshot{a, b}, ignore)
	if err != nil || st.Agreed != 2 || st.Differing != 3 {
		t.Errorf("stats %+v %v", st, err)
	}
	// Unequal order: nil.
	b.Order = []string{"k2", "k1"}
	bases, _ = Agree([]*model.RowSnapshot{a, b}, ignore)
	if bases[0].Order != nil {
		t.Errorf("unequal orders kept: %v", bases[0].Order)
	}
	// Without an ignore list k1 differs.
	bases, _ = Agree([]*model.RowSnapshot{a, b}, nil)
	if got := keysOf(bases[0]); !slices.Equal(got, []string{"k3"}) {
		t.Errorf("no ignore list: %v", got)
	}
	// Three snapshots: a key two of them agree on is not agreed.
	c := rowSnap(item(t, "k3", `"plain"`, 1))
	bases, _ = Agree([]*model.RowSnapshot{a, b, c}, ignore)
	if len(bases) != 3 || !slices.Equal(keysOf(bases[2]), []string{"k3"}) {
		t.Errorf("three snapshots: %v", keysOf(bases[2]))
	}
	c.Items["k3"] = item(t, "k3", `"other"`, 1)
	bases, _ = Agree([]*model.RowSnapshot{a, b, c}, ignore)
	if len(bases[0].Items) != 0 {
		t.Errorf("three snapshots, one differs: %v", keysOf(bases[0]))
	}
	if _, err := Agree([]*model.RowSnapshot{a, nil}, nil); !errors.Is(err, ErrNoSnapshot) {
		t.Errorf("nil snapshot: %v", err)
	}
	if _, err := Agree(nil, nil); !errors.Is(err, ErrNoSnapshot) {
		t.Errorf("no snapshot: %v", err)
	}
}

func withRaw(it model.Item) model.Item {
	it.Raw = []byte("secret bytes")
	return it
}

func genSnaps(t *rapid.T) []*model.RowSnapshot {
	n := rapid.IntRange(1, 4).Draw(t, "n")
	var out []*model.RowSnapshot
	for i := 0; i < n; i++ {
		s := rowSnap()
		for _, k := range []string{"a", "b", "c", "d", "e"} {
			if rapid.IntRange(0, 3).Draw(t, fmt.Sprintf("%d-%s", i, k)) > 0 {
				v := rapid.IntRange(0, 1).Draw(t, fmt.Sprintf("%d-%s-v", i, k))
				x := rapid.IntRange(0, 2).Draw(t, fmt.Sprintf("%d-%s-x", i, k))
				s.Items[k] = item(t, k, fmt.Sprintf(`{"v":%d,"acct":%d}`, v, x), int64(i))
			}
		}
		if rapid.Bool().Draw(t, fmt.Sprintf("%d-stale", i)) {
			s.Stale = []string{"e"}
		}
		out = append(out, s)
	}
	return out
}

func TestPropAgreeSound(t *testing.T) {
	ignore := []string{"acct"}
	rapid.Check(t, func(t *rapid.T) {
		snaps := genSnaps(t)
		bases, err := Agree(snaps, ignore)
		if err != nil {
			t.Fatal(err)
		}
		want := keysOf(bases[0])
		for i, b := range bases {
			if !slices.Equal(keysOf(b), want) {
				t.Fatalf("base %d has another key set", i)
			}
			for k, it := range b.Items {
				if snaps[i].Items[k].Hash != it.Hash {
					t.Fatalf("base %d item %s is not its own snapshot's", i, k)
				}
			}
		}
		// Independent of input order.
		rev := slices.Clone(snaps)
		slices.Reverse(rev)
		rb, _ := Agree(rev, ignore)
		if !slices.Equal(keysOf(rb[0]), want) {
			t.Fatal("key set depends on the input order")
		}
		// Agreeing the bases again keeps the key set.
		again, _ := Agree(bases, ignore)
		if !slices.Equal(keysOf(again[0]), want) {
			t.Fatal("Agree(Agree(x)) changed the key set")
		}
	})
}
