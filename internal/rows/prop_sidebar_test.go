package rows

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"pgregory.net/rapid"

	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
)

// The sidebar-offline rows get their own property worlds: the app-prefs
// convergence world (TestPropConvergence) is not reused for them.

var (
	collapseKeys     = []string{colGroups, colFamilies}
	collapseAlphabet = []string{"m1", "m2", "m3", "m4", "m5"}
)

func genSet(t *rapid.T, label string) []string {
	var out []string
	for _, m := range collapseAlphabet {
		if rapid.Bool().Draw(t, label+"-"+m) {
			out = append(out, m)
		}
	}
	return out
}

func genCollapseSnap(t *rapid.T, label string) *model.RowSnapshot {
	s := snap(model.RowSidebarCollapse)
	for _, k := range collapseKeys {
		if rapid.IntRange(0, 4).Draw(t, label+"-has-"+k) > 0 {
			s.Items[k] = setIt(t, k, 0, genSet(t, label+"-"+k)...)
		}
	}
	return s
}

// secondMergeEmpty re-runs a merge with the state after applying out: the
// result must change nothing.
func secondMergeEmpty(t fatalHelper, row model.RowID, in Input, out Output) {
	t.Helper()
	in2 := in
	in2.Leaving.Base, in2.Leaving.BaseValid = in.Leaving.Snapshot.Clone(), true
	in2.Entering.Snapshot = out.Desired.Clone()
	in2.Entering.Base, in2.Entering.BaseValid = out.Desired.Clone(), true
	in2.Canonical, in2.LeavingOverlay, in2.EnteringOverlay = out.Canonical, out.LeavingOverlay, out.EnteringOverlay
	in2.Leaving.Contested, in2.Entering.Contested = nil, nil
	out2, err := Registry()[row].Merge(in2)
	if err != nil {
		t.Fatalf("second merge: %v", err)
	}
	if len(out2.Changes) != 0 {
		t.Fatalf("second merge without edits changes %+v", out2.Changes)
	}
}

func TestPropCollapse(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		w := &world{row: model.RowSidebarCollapse}
		for i := 0; i < 3; i++ {
			w.reps = append(w.reps, &replica{alias: ids.Alias(fmt.Sprintf("p%d", i)), cur: genCollapseSnap(t, fmt.Sprintf("r%d", i))})
		}
		open := policyFlags{true, true, true}
		steps := rapid.IntRange(1, 8).Draw(t, "steps")
		for s := 0; s < steps; s++ {
			r := w.reps[rapid.IntRange(0, len(w.reps)-1).Draw(t, "editor")]
			if rapid.Bool().Draw(t, "edit") {
				k := rapid.SampledFrom(collapseKeys).Draw(t, "edit-key")
				c := r.cur.Clone()
				c.Items[k] = setIt(t, k, 0, genSet(t, "edit-set")...)
				r.cur = c
			}
			if rapid.IntRange(0, 4).Draw(t, "lose-base") == 0 {
				r.valid, r.base = false, nil
			}
			next := rapid.IntRange(0, len(w.reps)-1).Draw(t, "next")
			if next == w.active {
				continue
			}
			in := w.input(next, open)
			out := w.merge(t, in)
			checkCollapseMerge(t, in, out)
			secondMergeEmpty(t, w.row, in, out)
			w.apply(next, out)
		}
		// Two rounds of switches without edits converge.
		for round := 0; round < 2; round++ {
			for i := 1; i <= len(w.reps); i++ {
				next := (w.active + 1) % len(w.reps)
				in := w.input(next, open)
				out := w.merge(t, in)
				checkCollapseMerge(t, in, out)
				w.apply(next, out)
			}
		}
		want := hashes(w.canon)
		for _, r := range w.reps {
			got := hashes(r.cur)
			for _, k := range collapseKeys {
				if got[k] != want[k] {
					t.Fatalf("%s: %s %s differs from canonical %v", r.alias, k, r.cur.Items[k].Value, w.canon.Items[k].Value)
				}
			}
		}
	})
}

// checkCollapseMerge asserts the per-merge properties: no deletion, no
// confirmation, and with an invalid base Desired ⊇ entering ∪ leaving.
func checkCollapseMerge(t fatalHelper, in Input, out Output) {
	t.Helper()
	for _, c := range out.Changes {
		if c.Kind == model.ChangeDeleted {
			t.Fatalf("deletion %+v", c)
		}
	}
	if out.NeedsConfirmation {
		t.Fatalf("confirmation needed")
	}
	if in.Leaving.BaseValid && in.Entering.BaseValid {
		return
	}
	for _, k := range collapseKeys {
		got := membersOf(t, out.Desired, k)
		for _, side := range []*model.RowSnapshot{in.Leaving.Snapshot, in.Entering.Snapshot} {
			for _, m := range membersOf(t, side, k) {
				if !slices.Contains(got, m) {
					t.Fatalf("invalid base: %s lost member %s (desired %v)", k, m, got)
				}
			}
		}
	}
}

func TestPropSeed(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		gen := func(label string) *model.RowSnapshot {
			s := snap(model.RowSidebarSeed)
			for _, k := range []string{seedSort, seedEmpty} {
				switch rapid.IntRange(0, 2).Draw(t, label+"-"+k) {
				case 1:
					s.Items[k] = mk(t, k, "true", 0)
				case 2:
					s.Items[k] = mk(t, k, "false", 0)
				}
			}
			return s
		}
		l, lb, e, eb, canon := gen("l"), gen("lb"), gen("e"), gen("eb"), gen("canon")
		seeded := rapid.Bool().Draw(t, "seeded")
		from := map[string]json.RawMessage{}
		for _, k := range []string{seedSort, seedEmpty} {
			switch rapid.IntRange(0, 2).Draw(t, "from-"+k) {
			case 1:
				from[k] = json.RawMessage("true")
			case 2:
				from[k] = json.RawMessage("false")
			}
		}
		in := input(model.RowSidebarSeed, l, lb, e, eb, canon, seedPolicy(seeded, from))
		in.Row = model.RowSidebarSeed
		in.Leaving.BaseValid = rapid.Bool().Draw(t, "lv")
		in.Entering.BaseValid = rapid.Bool().Draw(t, "ev")
		out, err := Registry()[model.RowSidebarSeed].Merge(in)
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		checkHashes(t, out)
		for _, c := range out.Changes {
			if c.Kind == model.ChangeDeleted {
				t.Fatalf("deletion %+v", c)
			}
		}
		if out.NeedsConfirmation {
			t.Fatal("confirmation needed")
		}
		d, cur := hashes(out.Desired), hashes(e)
		if seeded && d[seedSort] != cur[seedSort] {
			t.Fatalf("seeded: sort key %q, entering %q", d[seedSort], cur[seedSort])
		}
		for k := range d {
			if k != seedSort && k != seedEmpty {
				t.Fatalf("key %s outside the seed keys", k)
			}
		}
		for k := range cur {
			if _, ok := d[k]; !ok {
				t.Fatalf("entering's %s dropped", k)
			}
		}
		for k, v := range from {
			if seeded && k == seedSort {
				continue
			}
			if string(out.Desired.Items[k].Value) != string(v) {
				t.Fatalf("%s = %s, want the carried %s", k, out.Desired.Items[k].Value, v)
			}
		}
		secondMergeEmpty(t, model.RowSidebarSeed, in, out)
	})
}

// TestPropCollapsePrivate runs the collapse world with sync.exclude set
// (every family member and the two project members below are private),
// random import boundaries and random limits. Per merge: the entering
// folder never loses a private member of its own; a private member of the
// leaving side never reaches canonical or Desired unless the entering side
// holds it; a profile that does not import, and every key the target keeps,
// get Desired equal to the entering read; the plan's changes list exactly
// the keys whose Desired differs from the entering read.
func TestPropCollapsePrivate(t *testing.T) {
	// Sorted, so a set drawn in this order is already normalized.
	alphabet := []string{"code:" + fakeSession(1), "m1", "m2", "project-/tmp/client/a", "project-example/repo"}
	private := map[string]bool{alphabet[0]: true, alphabet[3]: true, alphabet[4]: true}
	gen := func(t *rapid.T, label string) *model.RowSnapshot {
		s := snap(model.RowSidebarCollapse)
		for _, k := range collapseKeys {
			if rapid.IntRange(0, 4).Draw(t, label+"-has-"+k) == 0 {
				continue
			}
			var ms []string
			for _, m := range alphabet {
				if rapid.Bool().Draw(t, label+"-"+k+"-"+m) {
					ms = append(ms, m)
				}
			}
			s.Items[k] = setIt(t, k, 0, ms...)
		}
		return s
	}
	rapid.Check(t, func(t *rapid.T) {
		w := &world{row: model.RowSidebarCollapse}
		for i := 0; i < 3; i++ {
			w.reps = append(w.reps, &replica{alias: ids.Alias(fmt.Sprintf("p%d", i)), cur: gen(t, fmt.Sprintf("r%d", i))})
		}
		steps := rapid.IntRange(1, 8).Draw(t, "steps")
		for s := 0; s < steps; s++ {
			r := w.reps[rapid.IntRange(0, len(w.reps)-1).Draw(t, "editor")]
			if rapid.Bool().Draw(t, "edit") {
				r.cur = gen(t, "edit")
			}
			if rapid.IntRange(0, 4).Draw(t, "lose-base") == 0 {
				r.valid, r.base = false, nil
			}
			next := rapid.IntRange(0, len(w.reps)-1).Draw(t, "next")
			if next == w.active {
				continue
			}
			in := w.input(next, policyFlags{rapid.Bool().Draw(t, "lExp"), rapid.Bool().Draw(t, "eExp"), rapid.Bool().Draw(t, "eImp")})
			pol := collapsePolicy()
			pol.LeavingExports, pol.EnteringExports, pol.EnteringImports = in.Policy.LeavingExports, in.Policy.EnteringExports, in.Policy.EnteringImports
			pol.Exclude = []string{"/tmp/client/**"}
			pol.Limits = map[string]int{limitMaxMembers: rapid.IntRange(0, 4).Draw(t, "max")}
			in.Policy = pol
			out := w.merge(t, in)
			checkPrivateMerge(t, in, out, private)
			w.apply(next, out)
		}
	})
}

func checkPrivateMerge(t fatalHelper, in Input, out Output, private map[string]bool) {
	t.Helper()
	if out.NeedsConfirmation {
		t.Fatalf("confirmation needed")
	}
	if out.Desired == nil {
		return
	}
	own := in.Entering.Snapshot
	for _, k := range collapseKeys {
		ownMs, got := membersOf(t, own, k), membersOf(t, out.Desired, k)
		if _, had := own.Items[k]; had {
			if _, has := out.Desired.Items[k]; !has {
				t.Fatalf("%s: entering's item dropped", k)
			}
		}
		for _, m := range ownMs {
			if private[m] && !slices.Contains(got, m) {
				t.Fatalf("%s: entering lost its own private member %s (own %v, desired %v)", k, m, ownMs, got)
			}
		}
		for _, m := range membersOf(t, in.Leaving.Snapshot, k) {
			if !private[m] {
				continue
			}
			if slices.Contains(membersOf(t, out.Canonical, k), m) {
				t.Fatalf("%s: leaving's private member %s reached canonical", k, m)
			}
			if slices.Contains(got, m) && !slices.Contains(ownMs, m) {
				t.Fatalf("%s: leaving's private member %s reached Desired", k, m)
			}
		}
	}
	d, cur := hashes(out.Desired), hashes(own)
	if !in.Policy.EnteringImports && !sameMap(d, cur) {
		t.Fatalf("no import: desired %v differs from the entering read %v", d, cur)
	}
	changed := map[string]bool{}
	for _, c := range out.Changes {
		switch c.Kind {
		case model.ChangeDeleted:
			t.Fatalf("deletion %+v", c)
		case model.ChangeSkipped:
			if d[c.Key] != cur[c.Key] {
				t.Fatalf("skipped %s, but Desired differs from the target", c.Key)
			}
		default:
			changed[c.Key] = true
		}
	}
	for _, k := range collapseKeys {
		if differs := d[k] != cur[k]; differs != changed[k] {
			t.Fatalf("%s: Desired differs from the target = %v, plan lists a change = %v (changes %+v)", k, differs, changed[k], out.Changes)
		}
	}
}
