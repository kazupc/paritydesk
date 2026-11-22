package merge

import (
	"fmt"
	"testing"

	"pgregory.net/rapid"

	"github.com/kazupc/paritydesk/internal/model"
)

// genVersion draws a random version of key "k" from a small value space so
// that equal and differing hashes both occur.
func genVersion(t *rapid.T, name string) Version {
	val := func(label string) *model.Item {
		if !rapid.Bool().Draw(t, label+"-present") {
			return nil
		}
		v := rapid.IntRange(0, 3).Draw(t, label+"-value")
		st := model.Stamp{
			Epoch:       uint64(rapid.IntRange(0, 3).Draw(t, label+"-epoch")),
			AppTime:     int64(rapid.IntRange(0, 5).Draw(t, label+"-time")),
			ToolWritten: rapid.Bool().Draw(t, label+"-tool"),
		}
		it := item(t, "k", fmt.Sprintf("%d", v), st)
		return it
	}
	v := Version{Item: val(name + "-item"), Base: val(name + "-base"), BaseValid: rapid.Bool().Draw(t, name+"-valid")}
	if rapid.Bool().Draw(t, name+"-tomb") {
		v.Tomb = &model.Tombstone{Key: "k", At: int64(rapid.IntRange(0, 6).Draw(t, name+"-tomb-at"))}
	}
	return v
}

func TestPropScalarInvalidBaseNeverDeletes(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		l, e := genVersion(t, "l"), genVersion(t, "e")
		if l.BaseValid && e.BaseValid {
			return
		}
		o := Scalar("k", l, e, rapid.SampledFrom([]string{TombstoneNewerWins, TombstoneDeleteWins}).Draw(t, "mode"))
		if o.Kind == model.ChangeDeleted {
			t.Fatalf("invalid base produced a deletion: %+v", o)
		}
		if o.Result == nil && (l.Item != nil || e.Item != nil) {
			t.Fatalf("invalid base dropped a present copy: %+v", o)
		}
	})
}

func TestPropScalarTombstoneRule(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		tAt := int64(rapid.IntRange(0, 10).Draw(t, "tomb"))
		appTime := int64(rapid.IntRange(0, 10).Draw(t, "app"))
		base := item(t, "k", `"v"`, model.Stamp{AppTime: appTime})
		l := Version{Base: base, BaseValid: true, Tomb: &model.Tombstone{Key: "k", At: tAt}}
		e := Version{Item: base, Base: base, BaseValid: true}
		if rapid.Bool().Draw(t, "swap") {
			o := Scalar("k", e, l, TombstoneNewerWins)
			checkTomb(t, o, tAt, appTime)
			return
		}
		o := Scalar("k", l, e, TombstoneNewerWins)
		checkTomb(t, o, tAt, appTime)
		if d := Scalar("k", l, e, TombstoneDeleteWins); d.Result != nil {
			t.Fatalf("delete-wins kept the item")
		}
	})
}

func checkTomb(t *rapid.T, o Outcome, tAt, appTime int64) {
	deleted := o.Result == nil
	if deleted != (tAt > appTime) {
		t.Fatalf("tomb %d vs app %d: deleted=%v", tAt, appTime, deleted)
	}
	if !deleted && o.Kind != model.ChangeRevived {
		t.Fatalf("kept copy not reported revived: %q", o.Kind)
	}
}

func TestPropToolWrittenNeverWins(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		good := model.Stamp{AppTime: int64(rapid.IntRange(1, 100).Draw(t, "good"))}
		bad := model.Stamp{AppTime: int64(rapid.IntRange(0, 1000).Draw(t, "bad")), ToolWritten: rapid.Bool().Draw(t, "tool")}
		if !bad.ToolWritten {
			bad.AppTime = 0
		}
		g := Version{Item: item(t, "k", `"good"`, good)}
		b := Version{Item: item(t, "k", `"bad"`, bad)}
		if s, _ := Prefer(g, b); s != Leaving {
			t.Fatalf("unusable entering stamp won")
		}
		if s, _ := Prefer(b, g); s != Entering {
			t.Fatalf("unusable leaving stamp won")
		}
		if o := Scalar("k", b, g, TombstoneNewerWins); o.Result.Hash != g.Item.Hash {
			t.Fatalf("Scalar kept the unusable stamp")
		}
	})
}

func TestPropOrderedList(t *testing.T) {
	alpha := []string{"a", "b", "c", "d", "e", "f"}
	list := func(t *rapid.T, label string) []string {
		return rapid.SliceOfNDistinct(rapid.SampledFrom(alpha), 0, len(alpha), rapid.ID[string]).Draw(t, label)
	}
	rapid.Check(t, func(t *rapid.T) {
		base, w, l := list(t, "base"), list(t, "winner"), list(t, "loser")
		wv, lv := rapid.Bool().Draw(t, "wv"), rapid.Bool().Draw(t, "lv")
		got := OrderedList(base, w, l, wv, lv)
		seen := map[string]bool{}
		for _, x := range got {
			if seen[x] {
				t.Fatalf("duplicate %q in %v", x, got)
			}
			seen[x] = true
		}
		inBase, inL, inW := toSet(base), toSet(l), toSet(w)
		// The winner's survivors keep their relative order.
		var wantSeq []string
		for _, x := range w {
			if !(lv && inBase[x] && !inL[x]) {
				wantSeq = append(wantSeq, x)
				if !seen[x] {
					t.Fatalf("winner item %q lost: %v", x, got)
				}
			}
		}
		if !isSubsequence(wantSeq, got) {
			t.Fatalf("winner order broken: %v not in %v", wantSeq, got)
		}
		for _, x := range l {
			if !inW[x] && !(wv && inBase[x]) && !seen[x] {
				t.Fatalf("loser-only item %q lost: %v", x, got)
			}
		}
		// Invalid bases never remove.
		if !wv && !lv {
			for _, x := range append(append([]string(nil), w...), l...) {
				if !seen[x] {
					t.Fatalf("union lost %q", x)
				}
			}
		}
	})
}

func isSubsequence(sub, s []string) bool {
	i := 0
	for _, x := range s {
		if i < len(sub) && sub[i] == x {
			i++
		}
	}
	return i == len(sub)
}

func TestPropSetInvalidNeverRemoves(t *testing.T) {
	alpha := []string{"a", "b", "c", "d"}
	rapid.Check(t, func(t *rapid.T) {
		gen := rapid.SliceOfN(rapid.SampledFrom(alpha), 0, 4)
		base, l, e := gen.Draw(t, "base"), gen.Draw(t, "l"), gen.Draw(t, "e")
		lv, ev := rapid.Bool().Draw(t, "lv"), rapid.Bool().Draw(t, "ev")
		got := toSet(Set(base, l, e, lv, ev))
		inL, inE := toSet(l), toSet(e)
		for _, x := range append(append([]string(nil), l...), e...) {
			if !got[x] && !((lv && !inL[x]) || (ev && !inE[x])) {
				t.Fatalf("%q removed without a valid-base removal", x)
			}
		}
		for _, x := range base {
			if !got[x] && !((lv && !inL[x]) || (ev && !inE[x])) {
				t.Fatalf("base member %q removed by an invalid side", x)
			}
		}
	})
}
