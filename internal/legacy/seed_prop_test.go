package legacy

import (
	"fmt"
	"testing"

	"pgregory.net/rapid"

	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/rows"
)

// seedRow describes how the property test builds items of one row.
type seedRow struct {
	row  model.RowID
	keys []string
	val  func(v int) string
}

func seedRows() []seedRow {
	obj := func(v int) string { return fmt.Sprintf(`{"v":%d}`, v) }
	list := func(v int) string { return []string{`["a","b"]`, `["b","a"]`, `["a"]`, `["a","b","c"]`}[v] }
	return []seedRow{
		{model.RowSessions, []string{"local_" + fakeID(0x11), "local_" + fakeID(0x12), "local_" + fakeID(0x13)}, obj},
		{model.RowTasks, []string{"backlog/1", "backlog/2", "backlog/3"}, obj},
		{model.RowEnvironments, []string{"preset/p1", "preset/p2", "preset/p3"}, func(v int) string {
			return fmt.Sprintf(`{"id":"p","name":"n%d","settings":{}}`, v)
		}},
		{model.RowGroupOrder, []string{"order"}, list},
		{model.RowPinnedOrder, []string{"order"}, list},
		{model.RowSidebarView, []string{"field:a", "field:b", "section:s1"}, obj},
	}
}

// genSide draws one side's snapshot: per key presence, value, app time
// and a tombstone (before or after the other side's app time).
func genSide(t *rapid.T, r seedRow, label string) *model.RowSnapshot {
	s := &model.RowSnapshot{Row: r.row, Schema: 1, Items: map[string]model.Item{}, Tombstones: map[string]model.Tombstone{}}
	for _, k := range r.keys {
		switch rapid.IntRange(0, 2).Draw(t, label+"-"+k) {
		case 0:
			v := rapid.IntRange(0, 3).Draw(t, label+"-v-"+k)
			at := int64(rapid.IntRange(1, 50).Draw(t, label+"-t-"+k))
			s.Items[k] = item(t, k, r.val(v), at)
		case 1:
			s.Tombstones[k] = model.Tombstone{Key: k, At: int64(rapid.IntRange(1, 50).Draw(t, label+"-tomb-"+k))}
		}
	}
	return s
}

func mergeOnce(t *rapid.T, r seedRow, l, e *model.RowSnapshot, lBase, eBase *model.RowSnapshot, valid, seeded bool, lEpoch, eEpoch uint64) rows.Output {
	pol := rows.Policy{TombstoneMode: "newer-wins", LeavingExports: true, EnteringExports: true, EnteringImports: true}
	in := rows.Input{Row: r.row, Policy: pol,
		Leaving:  rows.Side{Alias: "alpha", Snapshot: l.Clone(), LiveEpoch: lEpoch, Seeded: seeded},
		Entering: rows.Side{Alias: "beta", Snapshot: e.Clone(), LiveEpoch: eEpoch, Seeded: seeded},
	}
	if valid {
		in.Leaving.Base, in.Leaving.BaseValid = lBase, true
		in.Entering.Base, in.Entering.BaseValid = eBase, true
	}
	out, err := rows.Registry()[r.row].Merge(in)
	if err != nil {
		t.Fatalf("%s: merge: %v", r.row, err)
	}
	return out
}

func winners(out rows.Output) map[string]string {
	m := map[string]string{}
	for _, c := range out.Conflicts {
		m[c.Key] = string(c.Winner)
	}
	return m
}

// The first switch after seeding merges every key outside the seeded
// bases exactly like a first-sync union, whatever the epochs; nothing is
// deleted; every key either side holds is kept.
func TestPropSeededFirstSwitchIsUnionOutsideBases(t *testing.T) {
	for _, r := range seedRows() {
		t.Run(string(r.row), func(t *testing.T) {
			rapid.Check(t, func(t *rapid.T) {
				l, e := genSide(t, r, "l"), genSide(t, r, "e")
				bases, err := Agree([]*model.RowSnapshot{l, e}, nil)
				if err != nil {
					t.Fatal(err)
				}
				lEpoch := uint64(rapid.IntRange(1, 9).Draw(t, "lEpoch"))
				eEpoch := uint64(rapid.IntRange(1, 9).Draw(t, "eEpoch"))
				seeded := mergeOnce(t, r, l, e, bases[0], bases[1], true, true, lEpoch, eEpoch)
				union := mergeOnce(t, r, l, e, nil, nil, false, false, lEpoch, eEpoch)
				for _, c := range seeded.Changes {
					if c.Kind == model.ChangeDeleted {
						t.Fatalf("seeded merge deletes %s", c.Key)
					}
				}
				sw, uw := winners(seeded), winners(union)
				for _, k := range r.keys {
					if _, agreed := bases[0].Items[k]; agreed {
						continue
					}
					si, sok := seeded.Desired.Items[k]
					ui, uok := union.Desired.Items[k]
					if sok != uok || (sok && si.Hash != ui.Hash) {
						t.Fatalf("key %s: seeded %v/%s, union %v/%s", k, sok, si.Value, uok, ui.Value)
					}
					if sw[k] != uw[k] {
						t.Fatalf("key %s: winner %q vs %q", k, sw[k], uw[k])
					}
				}
				for _, s := range []*model.RowSnapshot{l, e} {
					for k := range s.Items {
						if _, ok := seeded.Desired.Items[k]; !ok {
							t.Fatalf("key %s held by a side is not in Desired", k)
						}
					}
				}
			})
		})
	}
}

// Keys both sides hold with equal projections are not edited: Desired
// keeps the entering side's own item.
func TestPropSeededAgreedIsNoOp(t *testing.T) {
	for _, r := range seedRows() {
		t.Run(string(r.row), func(t *testing.T) {
			rapid.Check(t, func(t *rapid.T) {
				l, e := genSide(t, r, "l"), genSide(t, r, "e")
				bases, err := Agree([]*model.RowSnapshot{l, e}, nil)
				if err != nil {
					t.Fatal(err)
				}
				out := mergeOnce(t, r, l, e, bases[0], bases[1], true, true, 3, 5)
				for k := range bases[1].Items {
					if d, ok := out.Desired.Items[k]; !ok || d.Hash != e.Items[k].Hash {
						t.Fatalf("agreed key %s edited", k)
					}
				}
			})
		})
	}
}

// Sessions that differ only in fields of the ignored roles agree, and the
// first switch keeps the entering side's own copy (spec-legacy-adopt 11,
// third point).
func TestSeededIgnoredRolesKeepEnteringCopy(t *testing.T) {
	k := "local_" + fakeID(0x11)
	l := &model.RowSnapshot{Row: model.RowSessions, Schema: 1, Items: map[string]model.Item{
		k: item(t, k, `{"title":"t","acct":"x","busy":true}`, 10)}}
	e := &model.RowSnapshot{Row: model.RowSessions, Schema: 1, Items: map[string]model.Item{
		k: item(t, k, `{"title":"t","acct":"y","busy":false}`, 10)}}
	ignore := []string{"acct", "busy"}
	bases, err := Agree([]*model.RowSnapshot{l, e}, ignore)
	if err != nil || len(bases[0].Items) != 1 {
		t.Fatalf("not agreed: %v %v", bases, err)
	}
	pol := rows.Policy{TombstoneMode: "newer-wins", LeavingExports: true, EnteringExports: true, EnteringImports: true,
		Fields: map[string][]string{rows.RoleAccountBound: {"acct"}, rows.RoleRunState: {"busy"}}}
	out, err := rows.Registry()[model.RowSessions].Merge(rows.Input{Row: model.RowSessions, Policy: pol,
		Leaving:  rows.Side{Alias: "alpha", Snapshot: l, Base: bases[0], BaseValid: true, LiveEpoch: 7, Seeded: true},
		Entering: rows.Side{Alias: "beta", Snapshot: e, Base: bases[1], BaseValid: true, LiveEpoch: 3, Seeded: true}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Desired.Items[k].Hash != e.Items[k].Hash || len(out.Changes) != 0 || out.NeedsConfirmation {
		t.Fatalf("agreed session edited: %s %+v", out.Desired.Items[k].Value, out.Changes)
	}
}
