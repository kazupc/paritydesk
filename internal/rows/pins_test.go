package rows

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/kazupc/paritydesk/internal/model"
)

// pin is a pins row item (the adapter's shape: value true, no stamp).
func pin(t fatalHelper, id string) model.Item {
	t.Helper()
	it, err := model.NewItem(id, json.RawMessage("true"), nil, model.Stamp{}, false)
	if err != nil {
		t.Fatalf("item: %v", err)
	}
	return it
}

func pinSet(t fatalHelper, ids ...string) *model.RowSnapshot {
	s := snap(model.RowPins)
	for _, id := range ids {
		s.Items[id] = pin(t, id)
	}
	return s
}

func pinKeys(s *model.RowSnapshot) string {
	if s == nil {
		return ""
	}
	ks := s.Keys()
	sort.Strings(ks)
	return strings.Join(ks, ",")
}

func TestPinsCaseTable(t *testing.T) {
	x := fakeSession(1)
	for _, mode := range []string{"newer-wins", "delete-wins"} {
		pol := openPolicy()
		pol.TombstoneMode = mode
		cases := []struct {
			name                 string
			sBase, sNow          []string
			tBase, tNow          []string
			canon                []string
			sEpoch, tEpoch       uint64
			want                 string
			canonTomb, needsConf bool
		}{
			{name: "1 one-sided add on S", sBase: nil, sNow: []string{x}, want: x},
			{name: "2 one-sided removal on S", sBase: []string{x}, tBase: []string{x}, tNow: []string{x}, canon: []string{x}, want: ""},
			{name: "3 one-sided removal on T", sBase: []string{x}, sNow: []string{x}, tBase: []string{x}, canon: []string{x}, want: ""},
			{name: "4 one-sided add on T", tNow: []string{x}, want: x},
			{name: "5 both add", sNow: []string{x}, tNow: []string{x}, want: x},
			{name: "6 both removed", sBase: []string{x}, tBase: []string{x}, canon: []string{x}, want: ""},
			{name: "7 S removes, T adds: S live later", sBase: []string{x}, tNow: []string{x}, canon: []string{x}, want: ""},
			{name: "8 S adds, T removes: S live later", sNow: []string{x}, tBase: []string{x}, canon: []string{x}, want: x},
			{name: "9 T base invalid: union", sBase: []string{x}, tNow: []string{x}, canon: []string{x}, want: x, needsConf: true},
			{name: "10 S base invalid: union", sNow: []string{x}, tBase: []string{x}, canon: []string{x}, want: x, needsConf: true},
			{name: "11 canonical tombstone keeps x unpinned", sBase: []string{x}, sNow: []string{x}, tBase: []string{x}, tNow: []string{x}, canonTomb: true, want: ""},
			{name: "tie: equal epochs, leaving wins", sBase: []string{x}, tNow: []string{x}, canon: []string{x}, sEpoch: 5, tEpoch: 5, want: ""},
		}
		for _, c := range cases {
			t.Run(mode+"/"+c.name, func(t *testing.T) {
				in := input(model.RowPins, pinSet(t, c.sNow...), pinSet(t, c.sBase...), pinSet(t, c.tNow...), pinSet(t, c.tBase...), pinSet(t, c.canon...), pol)
				if strings.HasPrefix(c.name, "9") {
					in.Entering.BaseValid = false
				}
				if strings.HasPrefix(c.name, "10") {
					in.Leaving.BaseValid = false
				}
				if c.sEpoch != 0 {
					in.Leaving.LiveEpoch, in.Entering.LiveEpoch = c.sEpoch, c.tEpoch
				}
				if c.canonTomb {
					in.Canonical = withTombs(pinSet(t), model.Tombstone{Key: x})
				}
				out := mustMerge(t, model.RowPins, in)
				if got := pinKeys(out.Desired); got != c.want {
					t.Fatalf("desired = %q, want %q", got, c.want)
				}
				if c.needsConf && !out.NeedsConfirmation {
					t.Fatal("union without NeedsConfirmation")
				}
			})
		}
	}
}

func TestPinsTombstoneModesAgree(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := genPinsInput(t)
		var res []string
		for _, mode := range []string{"newer-wins", "delete-wins"} {
			in.Policy.TombstoneMode = mode
			out, err := Registry()[model.RowPins].Merge(in)
			if err != nil {
				t.Fatal(err)
			}
			res = append(res, pinKeys(out.Desired))
		}
		if res[0] != res[1] {
			t.Fatalf("modes differ: %q vs %q", res[0], res[1])
		}
	})
}

func genIDs(t *rapid.T, label string) []string {
	n := rapid.IntRange(0, 4).Draw(t, label+"n")
	var out []string
	for i := 0; i < n; i++ {
		out = append(out, fakeSession(rapid.IntRange(1, 6).Draw(t, label)))
	}
	return out
}

func genPinsInput(t *rapid.T) Input {
	sBase, sNow, tBase, tNow := genIDs(t, "sb"), genIDs(t, "sn"), genIDs(t, "tb"), genIDs(t, "tn")
	canon := genIDs(t, "c")
	in := input(model.RowPins, pinSet(t, sNow...), pinSet(t, sBase...), pinSet(t, tNow...), pinSet(t, tBase...), pinSet(t, canon...), openPolicy())
	in.Leaving.BaseValid = rapid.Bool().Draw(t, "sv")
	in.Entering.BaseValid = rapid.Bool().Draw(t, "tv")
	return in
}

// TestPropPinsMerge is P-MERGE: with valid bases (canonical = the common
// state both bases came from), the result applies each side's own
// changes since its base, the higher live epoch winning a direct
// conflict; with an invalid base the result holds both sides' pins; no
// id outside every input appears.
func TestPropPinsMerge(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		base := genIDs(t, "base")
		sNow, tNow := genIDs(t, "s"), genIDs(t, "t")
		valid := rapid.Bool().Draw(t, "valid")
		in := input(model.RowPins, pinSet(t, sNow...), pinSet(t, base...), pinSet(t, tNow...), pinSet(t, base...), pinSet(t, base...), openPolicy())
		if !valid {
			in.Entering.BaseValid = false
		}
		out, err := Registry()[model.RowPins].Merge(in)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for k := range out.Desired.Items {
			got[k] = true
		}
		has := func(xs []string, x string) bool {
			for _, y := range xs {
				if y == x {
					return true
				}
			}
			return false
		}
		all := append(append(append([]string(nil), base...), sNow...), tNow...)
		for k := range got {
			if !has(all, k) {
				t.Fatalf("id %s from nowhere", k)
			}
		}
		for _, id := range all {
			inB, inS, inT := has(base, id), has(sNow, id), has(tNow, id)
			if !valid {
				if (inS || inT) && !got[id] {
					t.Fatalf("union lost %s", id)
				}
				continue
			}
			sChanged, tChanged := inS != inB, inT != inB
			want := inB
			switch {
			case sChanged:
				want = inS // the leaving side was live later (epoch 7 > 3)
			case tChanged:
				want = inT
			}
			if got[id] != want {
				t.Fatalf("%s: base %v s %v t %v -> %v, want %v", id, inB, inS, inT, got[id], want)
			}
		}
	})
}

func TestExcludedSessionIDs(t *testing.T) {
	a, b, c := fakeSession(1), fakeSession(2), fakeSession(3)
	rec := func(id, cwd string) model.Item { return mk(t, id, `{"cwd":"`+cwd+`"}`, 0) }
	pol := Policy{Exclude: []string{c, "/private/**"}, Fields: map[string][]string{RoleProjectPath: {"cwd"}}}
	got := ExcludedSessionIDs([]*model.RowSnapshot{snap(model.RowSessions, rec(a, "/work/x"), rec(b, "/private/y"))}, pol)
	if got[a] || !got[b] || !got[c] {
		t.Fatalf("excluded = %v", got)
	}
	if len(ExcludedSessionIDs(nil, Policy{})) != 0 {
		t.Fatal("empty policy excludes")
	}
}

func famRec(t fatalHelper, id string, members map[string]any) model.Item {
	t.Helper()
	b, _ := json.Marshal(members)
	return mk(t, id, string(b), 0)
}

func TestAncestors(t *testing.T) {
	s := func(n int) string { return fakeSession(n) }
	fields := []string{"displayParentSessionId", "lineage.sessionId", "spawnedFrom.sessionId", "forkedFromSessionId", "startChain.firstSessionId"}
	recs := snap(model.RowSessions,
		famRec(t, s(1), map[string]any{}),
		famRec(t, s(2), map[string]any{"lineage": map[string]any{"sessionId": s(1), "kind": "hand_off"}}),
		famRec(t, s(3), map[string]any{"displayParentSessionId": s(2)}),
		famRec(t, s(4), map[string]any{"spawnedFrom": map[string]any{"sessionId": s(1), "sideSession": true}}),
		famRec(t, s(5), map[string]any{"forkedFromSessionId": s(4)}),
		famRec(t, s(6), map[string]any{"startChain": map[string]any{"firstSessionId": s(1)}}),
		famRec(t, s(7), map[string]any{"forkedFromSessionId": s(8)}),
		famRec(t, s(8), map[string]any{"forkedFromSessionId": s(7)}),
		famRec(t, s(9), map[string]any{"forkedFromSessionId": s(99)}),
		famRec(t, s(10), map[string]any{"forkedFromSessionId": 5, "lineage": "x"}),
	)
	anc := Ancestors(FamilyRecords([]*model.RowSnapshot{recs}, fields), 64)
	for id, want := range map[string]string{
		s(1): "", s(2): s(1), s(3): s(1) + "," + s(2), s(4): s(1), s(5): s(1) + "," + s(4), s(6): s(1),
		s(7): s(8), s(8): s(7), s(9): "", s(10): "",
	} {
		if got := strings.Join(anc(id), ","); got != want {
			t.Errorf("anc(%s) = %s, want %s", id, got, want)
		}
	}
	if got := strings.Join(Ancestors(FamilyRecords([]*model.RowSnapshot{recs}, fields), 1)(s(3)), ","); got != s(2) {
		t.Errorf("depth 1: %s", got)
	}
	// An earlier snapshot wins for the same id.
	first := snap(model.RowSessions, famRec(t, s(3), map[string]any{}))
	if got := Ancestors(FamilyRecords([]*model.RowSnapshot{first, recs}, fields), 64)(s(3)); len(got) != 0 {
		t.Errorf("union order: %v", got)
	}
}
