package rows

import (
	"fmt"
	"testing"

	"pgregory.net/rapid"

	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
)

// The property tests simulate N replicas of one keyed row switching
// between each other: each switch merges the leaving (active) replica and
// an entering one, applies Desired to the entering replica and advances
// bases, canonical and overlays the way the orchestrator does.

var propKeys = []string{"a", "b", "c", "d"}

type replica struct {
	alias     ids.Alias
	cur, base *model.RowSnapshot
	valid     bool
	live      uint64
	overlay   *model.RowSnapshot
	contested map[string]model.Contested
}

type world struct {
	row    model.RowID
	reps   []*replica
	canon  *model.RowSnapshot
	epoch  uint64
	active int
	clock  int64
}

func genSnapshot(t *rapid.T, w *world, label string) *model.RowSnapshot {
	s := snap(w.row)
	for _, k := range propKeys {
		if rapid.Bool().Draw(t, label+"-has-"+k) {
			s.Items[k] = w.newItem(t, k, rapid.IntRange(0, 3).Draw(t, label+"-val-"+k))
		}
	}
	return s
}

func (w *world) newItem(t fatalHelper, k string, v int) model.Item {
	w.clock++
	return mk(t, k, fmt.Sprintf(`{"v":%d}`, v), w.clock)
}

func newWorld(t *rapid.T, row model.RowID, n int) *world {
	w := &world{row: row}
	for i := 0; i < n; i++ {
		w.reps = append(w.reps, &replica{alias: ids.Alias(fmt.Sprintf("p%d", i)), cur: genSnapshot(t, w, fmt.Sprintf("r%d", i))})
	}
	return w
}

// edit changes one key of replica r: a new value or a removal.
func (w *world) edit(t *rapid.T, r *replica) {
	k := rapid.SampledFrom(propKeys).Draw(t, "edit-key")
	c := r.cur.Clone()
	if rapid.Bool().Draw(t, "edit-remove") {
		delete(c.Items, k)
	} else {
		c.Items[k] = w.newItem(t, k, rapid.IntRange(0, 3).Draw(t, "edit-val"))
	}
	r.cur = c
}

type policyFlags struct{ lExp, eExp, eImp bool }

func (w *world) input(next int, f policyFlags) Input {
	l, e := w.reps[w.active], w.reps[next]
	pol := openPolicy()
	pol.LeavingExports, pol.EnteringExports, pol.EnteringImports = f.lExp, f.eExp, f.eImp
	return Input{
		Row:             w.row,
		Leaving:         Side{Alias: l.alias, Snapshot: l.cur, Base: l.base, BaseValid: l.valid, LiveEpoch: w.epoch, Contested: l.contested},
		Entering:        Side{Alias: e.alias, Snapshot: e.cur, Base: e.base, BaseValid: e.valid, LiveEpoch: e.live, Contested: e.contested},
		Canonical:       w.canon,
		LeavingOverlay:  l.overlay,
		EnteringOverlay: e.overlay,
		Policy:          pol,
	}
}

func (w *world) merge(t fatalHelper, in Input) Output {
	t.Helper()
	out, err := Registry()[w.row].Merge(in)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	checkHashes(t, out)
	return out
}

// apply advances the world as P7–P10 would after a merge into next.
func (w *world) apply(next int, out Output) {
	l, e := w.reps[w.active], w.reps[next]
	l.base, l.valid = l.cur.Clone(), true
	if out.Desired != nil {
		e.cur = out.Desired.Clone()
	}
	e.base, e.valid = e.cur.Clone(), true
	w.canon = out.Canonical
	l.overlay, e.overlay = out.LeavingOverlay, out.EnteringOverlay
	l.contested, e.contested = nil, nil
	w.epoch++
	e.live = w.epoch
	w.active = next
}

// view returns canonical ⊕ overlay as key → hash.
func view(canon, ov *model.RowSnapshot) map[string]string {
	out := map[string]string{}
	for k, it := range canon.Items {
		if _, hidden := ov.Tombstones[k]; hidden {
			continue
		}
		out[k] = it.Hash
	}
	for k, it := range ov.Items {
		out[k] = it.Hash
	}
	return out
}

func hashes(s *model.RowSnapshot) map[string]string {
	out := map[string]string{}
	if s == nil {
		return out
	}
	for k, it := range s.Items {
		out[k] = it.Hash
	}
	return out
}

func sameMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestPropConvergence(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		w := newWorld(t, model.RowAppPrefs, rapid.IntRange(2, 4).Draw(t, "n"))
		open := policyFlags{true, true, true}
		for round := 0; round < 2; round++ {
			for step := 1; step <= len(w.reps); step++ {
				next := (w.active + 1) % len(w.reps)
				out := w.merge(t, w.input(next, open))
				if !sameMap(hashes(out.Desired), view(out.Canonical, out.EnteringOverlay)) {
					t.Fatalf("desired != canonical ⊕ overlay")
				}
				w.apply(next, out)
			}
		}
		want := hashes(w.canon)
		for i, r := range w.reps {
			if !sameMap(hashes(r.cur), want) {
				t.Fatalf("replica %d did not converge: %v vs %v", i, hashes(r.cur), want)
			}
			if r.overlay != nil && len(r.overlay.Items) != 0 {
				t.Fatalf("replica %d kept overlay items with an open boundary", i)
			}
		}
	})
}

// TestPropRandomWalk runs random edits, drift, base losses and switches
// with random boundaries and checks the per-merge invariants.
func TestPropRandomWalk(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		w := newWorld(t, model.RowAppPrefs, rapid.IntRange(2, 4).Draw(t, "n"))
		steps := rapid.IntRange(1, 14).Draw(t, "steps")
		for i := 0; i < steps; i++ {
			switch rapid.SampledFrom([]string{"edit", "drift", "invalidate", "contest", "switch", "switch"}).Draw(t, "op") {
			case "edit":
				w.edit(t, w.reps[w.active])
			case "drift":
				w.edit(t, w.reps[rapid.IntRange(0, len(w.reps)-1).Draw(t, "drift-rep")])
			case "invalidate":
				w.reps[rapid.IntRange(0, len(w.reps)-1).Draw(t, "inv-rep")].valid = false
			case "contest":
				w.contest(t)
			default:
				w.checkedSwitch(t)
			}
		}
	})
}

// contest marks a key of the active replica as contested with its current
// value as the server value.
func (w *world) contest(t *rapid.T) {
	r := w.reps[w.active]
	k := rapid.SampledFrom(propKeys).Draw(t, "contest-key")
	it, ok := r.cur.Items[k]
	if !ok {
		return
	}
	r.contested = map[string]model.Contested{k: {Row: w.row, Key: k, ServerHash: it.Hash, IntendedHash: "intended"}}
}

func (w *world) checkedSwitch(t *rapid.T) {
	next := rapid.IntRange(0, len(w.reps)-1).Draw(t, "next")
	if next == w.active {
		return
	}
	f := policyFlags{rapid.Bool().Draw(t, "l-exp"), rapid.Bool().Draw(t, "e-exp"), rapid.Bool().Draw(t, "e-imp")}
	in := w.input(next, f)
	out := w.merge(t, in)
	checkInvariants(t, in, out)
	w.apply(next, out)
	again := w.merge(t, w.input(w.active, f))
	if len(again.Changes) != 0 || len(again.Conflicts) != 0 {
		t.Fatalf("merging again changed things: %+v %+v", again.Changes, again.Conflicts)
	}
	if !sameMap(hashes(again.Canonical), hashes(out.Canonical)) {
		t.Fatal("merging again changed canonical")
	}
}

// checkInvariants checks one merge against its input.
func checkInvariants(t *rapid.T, in Input, out Output) {
	// Desired is canonical ⊕ overlay when importing.
	if in.Policy.EnteringImports && !sameMap(hashes(out.Desired), view(out.Canonical, out.EnteringOverlay)) {
		t.Fatalf("desired != canonical ⊕ overlay")
	}
	invalid := !in.Leaving.BaseValid || !in.Entering.BaseValid
	if invalid != out.NeedsConfirmation {
		t.Fatalf("NeedsConfirmation = %v with invalid base %v", out.NeedsConfirmation, invalid)
	}
	for _, c := range out.Changes {
		if c.Kind == model.ChangeDeleted && invalid {
			t.Fatalf("invalid base produced a deletion of %s", c.Key)
		}
	}
	kept := keptHashes(out)
	for _, s := range []Side{in.Leaving, in.Entering} {
		for k, it := range s.Snapshot.Items {
			if changedOn(s, k) && !kept[it.Hash] {
				t.Fatalf("changed value of %s lost", k)
			}
		}
	}
	for k, c := range itemsOf(in.Canonical) {
		if !kept[c.Hash] && !changedOn(in.Leaving, k) && !changedOn(in.Entering, k) {
			t.Fatalf("canonical value of %s lost without an edit", k)
		}
	}
	checkPrivateEdits(t, in.Leaving, in.Policy.LeavingExports, in, out)
	checkPrivateEdits(t, in.Entering, in.Policy.EnteringExports, in, out)
	checkOverlayKept(t, in.Leaving, in.LeavingOverlay, out.LeavingOverlay)
	checkOverlayKept(t, in.Entering, in.EnteringOverlay, out.EnteringOverlay)
	for k, c := range in.Leaving.Contested {
		if it, ok := in.Leaving.Snapshot.Items[k]; ok && it.Hash == c.ServerHash && !changedOn(in.Entering, k) && canonKnows(in.Canonical, k) {
			// While a base is invalid the union keeps the entering copy
			// (C1); that copy may hold the same value.
			if e, ok := in.Entering.Snapshot.Items[k]; ok && invalid && in.Policy.EnteringImports && e.Hash == canonHash(out.Canonical, k) {
				continue
			}
			if canonHash(in.Canonical, k) != canonHash(out.Canonical, k) {
				t.Fatalf("contested server value of %s harvested as an edit", k)
			}
		}
	}
}

func itemsOf(s *model.RowSnapshot) map[string]model.Item {
	if s == nil {
		return nil
	}
	return s.Items
}

// canonKnows reports whether canonical holds an item or a tombstone for k.
func canonKnows(s *model.RowSnapshot, k string) bool {
	if s == nil {
		return false
	}
	_, item := s.Items[k]
	_, tomb := s.Tombstones[k]
	return item || tomb
}

func canonHash(s *model.RowSnapshot, k string) string {
	if s == nil {
		return ""
	}
	return s.Items[k].Hash
}

// changedOn mirrors the engine's change rule for generic rows. A contested
// value is never a change; on a side with an invalid base the engine still
// keeps it when the other side removed the key (C1), which this check
// does not need to model.
func changedOn(s Side, k string) bool {
	if s.Snapshot == nil {
		return false
	}
	it, has := s.Snapshot.Items[k]
	if c, ok := s.Contested[k]; ok && has && (it.Hash == c.ServerHash || it.Hash == c.IntendedHash) {
		return false
	}
	if !s.BaseValid || s.Base == nil {
		return has
	}
	b, bhas := s.Base.Items[k]
	return has != bhas || (has && it.Hash != b.Hash)
}

func keptHashes(out Output) map[string]bool {
	kept := map[string]bool{}
	for _, s := range []*model.RowSnapshot{out.Desired, out.Canonical, out.LeavingOverlay, out.EnteringOverlay} {
		if s == nil {
			continue
		}
		for _, it := range s.Items {
			kept[it.Hash] = true
		}
	}
	for _, c := range out.Conflicts {
		kept[c.LoserHash] = true
	}
	return kept
}

// checkPrivateEdits: a side that does not export never puts its edited
// value into canonical (unless another copy holds the same value).
func checkPrivateEdits(t *rapid.T, s Side, exports bool, in Input, out Output) {
	if exports || s.Snapshot == nil {
		return
	}
	for k, it := range s.Snapshot.Items {
		if !changedOn(s, k) || out.Canonical.Items[k].Hash != it.Hash {
			continue
		}
		other := in.Entering
		if s.Alias == in.Entering.Alias {
			other = in.Leaving
		}
		if canonHash(in.Canonical, k) == it.Hash || canonHash(other.Snapshot, k) == it.Hash {
			continue
		}
		if s.Base != nil && s.Base.Items[k].Hash == it.Hash {
			continue
		}
		t.Fatalf("non-exported edit of %s by %s reached canonical", k, s.Alias)
	}
}

// checkOverlayKept: an overlay keeps every key unless its own side removed
// it against a valid base.
func checkOverlayKept(t *rapid.T, s Side, before, after *model.RowSnapshot) {
	if before == nil {
		return
	}
	for k := range before.Items {
		if _, ok := after.Items[k]; ok {
			continue
		}
		removed := s.Snapshot != nil && s.BaseValid && s.Base != nil
		if removed {
			_, inBase := s.Base.Items[k]
			_, inCur := s.Snapshot.Items[k]
			removed = inBase && !inCur
		}
		if !removed {
			t.Fatalf("overlay of %s lost %s", s.Alias, k)
		}
	}
}

func TestPropOrderIndependence(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		w := &world{row: model.RowAppPrefs}
		canon := genSnapshot(t, w, "canon")
		a, b := canon.Clone(), canon.Clone()
		for _, k := range propKeys {
			switch rapid.IntRange(0, 2).Draw(t, "owner-"+k) {
			case 1:
				editKey(t, w, a, k)
			case 2:
				editKey(t, w, b, k)
			}
		}
		merge := func(l, e *model.RowSnapshot) map[string]string {
			out := w.merge(t, input(w.row, l, canon, e, canon, canon, openPolicy()))
			return hashes(out.Canonical)
		}
		if ab, ba := merge(a, b), merge(b, a); !sameMap(ab, ba) {
			t.Fatalf("result depends on direction: %v vs %v", ab, ba)
		}
	})
}

func editKey(t *rapid.T, w *world, s *model.RowSnapshot, k string) {
	if rapid.Bool().Draw(t, "rm-"+k) {
		delete(s.Items, k)
		return
	}
	s.Items[k] = w.newItem(t, k, rapid.IntRange(4, 6).Draw(t, "v-"+k))
}

func TestPropToolStampsNeverWin(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		row := model.RowAppPrefs
		lt := int64(rapid.IntRange(1, 1000).Draw(t, "lt"))
		et := int64(rapid.IntRange(1, 1000).Draw(t, "et"))
		l, e := mk(t, "k", `"l"`, lt), mk(t, "k", `"e"`, et)
		in := input(row, snap(row, l), nil, snap(row, e), nil, nil, openPolicy())
		in.Leaving.BaseValid, in.Entering.BaseValid = rapid.Bool().Draw(t, "lv"), false
		in.Leaving.ToolStamps = map[string]int64{"k": lt}
		out, err := Registry()[row].Merge(in)
		if err != nil {
			t.Fatal(err)
		}
		if out.Desired.Items["k"].Hash != e.Hash {
			t.Fatalf("tool-written stamp %d beat %d", lt, et)
		}
	})
}

func TestPropSessionsTombstoneRule(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		row := model.RowSessions
		at := int64(rapid.IntRange(0, 20).Draw(t, "tomb"))
		act := int64(rapid.IntRange(0, 20).Draw(t, "activity"))
		e := rec(t, 1, `"title":"t"`, act)
		lCur := withTombs(snap(row), model.Tombstone{Key: e.Key, At: at})
		out, err := Registry()[row].Merge(input(row, lCur, snap(row, e), snap(row, e), snap(row, e), snap(row, e), sessPolicy()))
		if err != nil {
			t.Fatal(err)
		}
		_, present := out.Desired.Items[e.Key]
		if present == (at > act) {
			t.Fatalf("tomb %d activity %d: present %v", at, act, present)
		}
	})
}

// TestPropSessionsMissingRestoredAndFamilies: sessions missing without a
// tombstone are restored, and no record in Desired loses a parent that
// any input still holds, unless that parent's deletion is newer than its
// last activity (D9: the newer change wins).
func TestPropSessionsMissingRestoredAndFamilies(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		row := model.RowSessions
		const n = 5
		var all []model.Item
		for i := 1; i <= n; i++ {
			body := `"title":"t"`
			if p := rapid.IntRange(0, n).Draw(t, fmt.Sprintf("parent-%d", i)); p != 0 && p != i {
				body += fmt.Sprintf(`,"parentSessionId":%q`, fakeSession(p))
			}
			all = append(all, rec(t, i, body, int64(rapid.IntRange(1, 20).Draw(t, fmt.Sprintf("act-%d", i)))))
		}
		act := map[string]int64{}
		for _, it := range all {
			act[it.Key] = it.Stamp.AppTime
		}
		tombAt := map[string]int64{}
		pick := func(label string) *model.RowSnapshot {
			s := snap(row)
			for _, it := range all {
				if rapid.Bool().Draw(t, label+it.Key) {
					s.Items[it.Key] = it
				}
			}
			return s
		}
		base := snap(row, all...)
		lCur, eCur := pick("l"), pick("e")
		tombed := map[string]bool{}
		for _, it := range all {
			_, inL := lCur.Items[it.Key]
			if !inL && rapid.Bool().Draw(t, "tomb"+it.Key) {
				at := int64(rapid.IntRange(0, 25).Draw(t, "at"+it.Key))
				withTombs(lCur, model.Tombstone{Key: it.Key, At: at})
				tombed[it.Key] = true
				tombAt[it.Key] = at
			}
		}
		in := input(row, lCur, base, eCur, base, base, sessPolicy())
		out, err := Registry()[row].Merge(in)
		if err != nil {
			t.Fatal(err)
		}
		checkHashes(t, out)
		for _, it := range all {
			if _, ok := out.Desired.Items[it.Key]; !ok && !tombed[it.Key] {
				t.Fatalf("%s missing without a tombstone was not restored", it.Key)
			}
		}
		for k, it := range out.Desired.Items {
			if p, ok := getString(it.Value, "parentSessionId"); ok {
				if _, ok := out.Desired.Items[p]; !ok && !(tombed[p] && tombAt[p] > act[p]) {
					t.Fatalf("orphan fork %s: parent %s missing", k, p)
				}
			}
		}
	})
}

func TestPropStaleOrNilNeverRemoves(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		w := &world{row: model.RowWorktrees}
		canon := genSnapshot(t, w, "canon")
		e := genSnapshot(t, w, "e")
		eBase := e
		if rapid.Bool().Draw(t, "e-edited") {
			eBase = genSnapshot(t, w, "e-base")
		}
		var l, lBase *model.RowSnapshot
		lValid := false
		switch rapid.IntRange(0, 1).Draw(t, "leaving") {
		case 0: // nil snapshot (row skipped)
			lValid = rapid.Bool().Draw(t, "nil-valid")
			lBase = genSnapshot(t, w, "l-base")
		case 1: // stale capture: an invalid base
			l, lBase = genSnapshot(t, w, "l"), genSnapshot(t, w, "l-base")
		}
		in := input(w.row, l, lBase, e, eBase, canon, openPolicy())
		in.Leaving.BaseValid = lValid
		out := w.merge(t, in)
		for _, c := range out.Changes {
			if c.Kind == model.ChangeDeleted {
				t.Fatalf("stale or missing leaving snapshot removed %s", c.Key)
			}
		}
		for k := range e.Items {
			if _, ok := out.Desired.Items[k]; !ok {
				t.Fatalf("entering item %s removed", k)
			}
		}
	})
}
