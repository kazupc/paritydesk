package rows

import (
	"reflect"
	"testing"

	"github.com/kazupc/paritydesk/internal/merge"
	"github.com/kazupc/paritydesk/internal/model"
)

// seededInput is an input whose two sides have valid bases seeded by
// profile adopt --from-legacy (Side.Seeded) and no canonical.
func seededInput(row model.RowID, lCur, lBase, eCur, eBase *model.RowSnapshot) Input {
	in := input(row, lCur, lBase, eCur, eBase, nil, openPolicy())
	in.Leaving.Seeded, in.Entering.Seeded = true, true
	return in
}

// A key outside the seeded bases: the leaving side's tombstone, newer
// than the entering copy, does not delete it (first-sync union rule).
func TestSeededUnionKeyNeverDeletes(t *testing.T) {
	row := model.RowAppPrefs
	agreed := mk(t, "x", `{"v":1}`, 5)
	lBase, eBase := snap(row, agreed), snap(row, agreed)
	lCur := withTombs(snap(row, agreed), model.Tombstone{Key: "k", At: 100})
	eCur := snap(row, agreed, mk(t, "k", `{"v":2}`, 50))

	out := mustMerge(t, row, seededInput(row, lCur, lBase, eCur, eBase))
	if hasChange(out, model.ChangeDeleted, "k") {
		t.Fatalf("a union key was deleted: %+v", out.Changes)
	}
	if _, ok := out.Desired.Items["k"]; !ok {
		t.Fatalf("the entering copy of k is gone: %v", out.Desired.Keys())
	}
	if !out.NeedsConfirmation {
		t.Fatal("a union key needs the first-sync confirmation")
	}

	// Without Seeded the same valid bases let the newer tombstone win.
	plain := input(row, lCur, lBase, eCur, eBase, nil, openPolicy())
	out = mustMerge(t, row, plain)
	if _, ok := out.Desired.Items["k"]; ok {
		t.Fatal("control: without Seeded the newer tombstone should delete k")
	}
}

// A key outside the seeded bases changed on both sides: the newer app
// time wins even though the other side has the higher epoch.
func TestSeededUnionKeyIgnoresEpoch(t *testing.T) {
	row := model.RowAppPrefs
	agreed := mk(t, "x", `{"v":1}`, 5)
	lCur := snap(row, agreed, mk(t, "k", `{"v":10}`, 10)) // leaving: epoch 7, older copy
	eCur := snap(row, agreed, mk(t, "k", `{"v":20}`, 20)) // entering: epoch 3, newer copy
	out := mustMerge(t, row, seededInput(row, lCur, snap(row, agreed), eCur, snap(row, agreed)))
	if got := out.Desired.Items["k"]; got.Hash != eCur.Items["k"].Hash {
		t.Fatalf("the newer copy did not win: %s", got.Value)
	}
	if len(out.Conflicts) != 1 || out.Conflicts[0].Reason != merge.ReasonAppTime {
		t.Fatalf("conflicts %+v, want one decided by app time", out.Conflicts)
	}
}

// A key in both seeded bases merges three-way: a removal on one side
// deletes it.
func TestSeededAgreedKeyMergesThreeWay(t *testing.T) {
	row := model.RowAppPrefs
	agreed := mk(t, "x", `{"v":1}`, 5)
	keep := mk(t, "y", `{"v":1}`, 5)
	lCur := snap(row, keep)
	eCur := snap(row, agreed, keep)
	base := snap(row, agreed, keep)
	out := mustMerge(t, row, seededInput(row, lCur, base, eCur, base.Clone()))
	if !hasChange(out, model.ChangeDeleted, "x") {
		t.Fatalf("an agreed key removed on one side was not deleted: %+v", out.Changes)
	}
	if out.NeedsConfirmation {
		t.Fatal("no union key: no confirmation needed")
	}
}

// Seeded bases that hold every key merge exactly like plain valid bases.
func TestSeededWithFullBasesIsUnchanged(t *testing.T) {
	for _, row := range []model.RowID{model.RowAppPrefs, model.RowWindowState} {
		a, b := mk(t, "a", `{"v":1}`, 5), mk(t, "b", `{"v":2}`, 6)
		lBase, eBase := snap(row, a, b), snap(row, a, b)
		lCur := snap(row, mk(t, "a", `{"v":3}`, 9))
		eCur := snap(row, a, b)
		plain := mustMerge(t, row, input(row, lCur, lBase, eCur, eBase, nil, openPolicy()))
		seeded := mustMerge(t, row, seededInput(row, lCur, lBase, eCur, eBase))
		if !reflect.DeepEqual(plain, seeded) {
			t.Fatalf("%s: seeded merge differs from the plain one:\n%+v\n%+v", row, plain, seeded)
		}
	}
}

// Seeded on one side only: a key absent from that side's base is a union
// key for both sides.
func TestSeededOneSide(t *testing.T) {
	row := model.RowAppPrefs
	agreed := mk(t, "x", `{"v":1}`, 5)
	lCur := withTombs(snap(row, agreed), model.Tombstone{Key: "k", At: 100})
	eCur := snap(row, agreed, mk(t, "k", `{"v":2}`, 50))
	in := input(row, lCur, snap(row, agreed), eCur, snap(row, agreed, mk(t, "k", `{"v":2}`, 50)), nil, openPolicy())
	in.Leaving.Seeded = true
	out := mustMerge(t, row, in)
	if _, ok := out.Desired.Items["k"]; !ok || hasChange(out, model.ChangeDeleted, "k") {
		t.Fatalf("k deleted although the seeded leaving base never held it: %+v", out.Changes)
	}
}
