package all_test

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/localstorage"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/sources/all"
	"github.com/kazupc/paritydesk/internal/testkit"
)

const now int64 = 1800000000000

type env struct {
	m    *compat.Manifest
	root *testkit.DataRoot
}

func newEnv(t *testing.T) *env {
	return &env{m: testkit.Manifest(t), root: testkit.NewDataRoot(t)}
}

func (e *env) rc(t *testing.T, folder string, r model.Replica, row model.RowID) sources.ReadContext {
	return sources.ReadContext{Folder: folder, Replica: r, LS: testkit.LS(t, folder),
		Spec: testkit.Spec(t, e.m, row, r), Manifest: e.m}
}

func read(t *testing.T, a sources.Adapter, rc sources.ReadContext) *model.RowSnapshot {
	t.Helper()
	s, err := a.Read(context.Background(), rc)
	if err != nil {
		t.Fatalf("%s: read: %v", a.Row(), err)
	}
	return s
}

func TestRegistryCoversEveryRow(t *testing.T) {
	reg := all.Registry()
	for _, r := range implementedRows() {
		if a, ok := reg[r]; !ok || a.Row() != r {
			t.Errorf("row %s has no adapter", r)
		}
	}
}

func TestEmptyFolderGivesEmptySnapshots(t *testing.T) {
	e := newEnv(t)
	folder := e.root.AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: testkit.Replica(1)})
	for _, a := range all.Registry() {
		s := read(t, a, e.rc(t, folder, testkit.Replica(1), a.Row()))
		if s.Row != a.Row() || s.Schema != 1 || len(s.Items) != 0 || len(s.Tombstones) != 0 {
			t.Errorf("%s: empty folder gave %+v", a.Row(), s)
		}
	}
}

func TestReadHashesAndIdempotentOps(t *testing.T) {
	e := newEnv(t)
	r := testkit.Replica(1)
	folder := e.root.AddFolder(t, testkit.Rich("Claude", 1))
	before := e.root.Tree(t)
	for _, a := range all.Registry() {
		rc := e.rc(t, folder, r, a.Row())
		s := read(t, a, rc)
		if len(s.Items) == 0 && carried(e.m, a.Row()) {
			t.Errorf("%s: rich folder gave no items", a.Row())
		}
		for k, it := range s.Items {
			h, err := model.ItemHash(it.Value)
			if err != nil || h != it.Hash || k != it.Key {
				t.Errorf("%s/%s: hash %s, want ItemHash(Value) %s", a.Row(), k, it.Hash, h)
			}
		}
		res, err := a.Ops(context.Background(), sources.OpsContext{ReadContext: rc, NowMillis: now}, s, s.Clone())
		if err != nil || len(res.Edits) != 0 || len(res.Skipped) != 0 {
			t.Errorf("%s: Ops(current, current) = %d edits, %d skipped, %v", a.Row(), len(res.Edits), len(res.Skipped), err)
		}
	}
	if after := e.root.Tree(t); !equalTrees(before, after) {
		t.Fatal("Read or Ops wrote to the folder")
	}
}

func equalTrees(a, b map[string]string) bool {
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

// syncRow plans the edits that make target hold source's items for one
// row, applies them, and returns the desired snapshot and the result.
func syncRow(t *testing.T, e *env, a sources.Adapter, src, dst string, rs, rd model.Replica) (*model.RowSnapshot, sources.OpsResult) {
	t.Helper()
	srcRC := e.rc(t, src, rs, a.Row())
	desired := read(t, a, srcRC)
	dstRC := e.rc(t, dst, rd, a.Row())
	current := read(t, a, dstRC)
	res, err := a.Ops(context.Background(), sources.OpsContext{ReadContext: dstRC, NowMillis: now, Source: &srcRC}, current, desired)
	if err != nil {
		t.Fatalf("%s: ops: %v", a.Row(), err)
	}
	return desired, res
}

func TestRoundTripPerRow(t *testing.T) {
	for _, a := range all.Registry() {
		t.Run(string(a.Row()), func(t *testing.T) {
			e := newEnv(t)
			if !carried(e.m, a.Row()) {
				t.Skip("not carried by this manifest")
			}
			rs, rd := testkit.Replica(1), testkit.Replica(2)
			src := e.root.AddFolder(t, richV02("Claude-s", 1))
			dst := e.root.AddFolder(t, richV02("Claude", 2))
			desired, res := syncRow(t, e, a, src, dst, rs, rd)
			if len(res.Edits) == 0 {
				t.Fatal("no edits between different folders")
			}
			if len(res.Skipped) != 0 {
				t.Fatalf("unexpected skips: %+v", res.Skipped)
			}
			checkEdits(t, e.m, a.Row(), res.Edits)
			testkit.Apply(t, dst, res.Edits)
			got := read(t, a, e.rc(t, dst, rd, a.Row()))
			assertSameItems(t, got, desired)
			again, err := a.Ops(context.Background(), sources.OpsContext{ReadContext: e.rc(t, dst, rd, a.Row()), NowMillis: now}, got, desired)
			if err != nil || len(again.Edits) != 0 {
				t.Fatalf("second Ops after apply: %d edits, %v", len(again.Edits), err)
			}
		})
	}
}

func assertSameItems(t *testing.T, got, want *model.RowSnapshot) {
	t.Helper()
	if len(got.Items) != len(want.Items) {
		t.Fatalf("got %d items %v, want %d %v", len(got.Items), got.Keys(), len(want.Items), want.Keys())
	}
	for k, w := range want.Items {
		if g, ok := got.Items[k]; !ok || g.Hash != w.Hash {
			t.Errorf("item %s: hash differs after apply", k)
		}
	}
	if len(got.Tombstones) != len(want.Tombstones) {
		t.Errorf("tombstones: got %v, want %v", got.Tombstones, want.Tombstones)
	}
	for k := range want.Tombstones {
		if _, ok := got.Tombstones[k]; !ok {
			t.Errorf("tombstone %s missing after apply", k)
		}
	}
}

// checkEdits asserts the edit contract: every edit names one location of
// its own row, carries a base hash, no written path matches the tmp
// recovery globs, removals are destructive with an item, and the JSON form
// of an edit never carries the canary.
func checkEdits(t *testing.T, m *compat.Manifest, row model.RowID, edits []plan.Edit) {
	t.Helper()
	for _, ed := range edits {
		if ed.Row != row || (ed.Loc.File == "") == (ed.Loc.LSKey == "") {
			t.Errorf("edit names row %s, location %+v", ed.Row, ed.Loc)
		}
		if ed.Kind != plan.EditRemove && ed.Loc.File != "" {
			for _, g := range m.TmpRecoveryGlobs {
				if ok, _ := path.Match(g, path.Base(ed.Loc.File)); ok {
					t.Errorf("edit writes %s, which matches tmp glob %s", ed.Loc.File, g)
				}
			}
		}
		if ed.Kind == plan.EditRemove && (!ed.Destructive || ed.Item == "") {
			t.Errorf("remove of %+v is not destructive or names no item", ed.Loc)
		}
		b, err := json.Marshal(ed)
		if err != nil || strings.Contains(string(b), testkit.Canary) {
			t.Errorf("edit JSON form carries the canary or fails: %v", err)
		}
		if !ed.Secret && strings.Contains(string(ed.Value), testkit.Canary) {
			t.Errorf("edit on %+v carries the canary without Secret", ed.Loc)
		}
	}
}

func TestCanaryNeverInSnapshotsOrErrors(t *testing.T) {
	e := newEnv(t)
	r := testkit.Replica(1)
	folder := e.root.AddFolder(t, testkit.Rich("Claude", 1))
	for _, a := range all.Registry() {
		s := read(t, a, e.rc(t, folder, r, a.Row()))
		b, err := json.Marshal(s)
		if err != nil || strings.Contains(string(b), testkit.Canary) {
			t.Errorf("%s: snapshot JSON carries the canary (%v)", a.Row(), err)
		}
		for k, it := range s.Items {
			if strings.Contains(string(it.Value), testkit.Canary) {
				t.Errorf("%s/%s: Value carries the canary", a.Row(), k)
			}
		}
	}
}

// TestComposedSharedTargets applies every row's edits together, the way
// plan.Compose folds them: rows sharing claude_desktop_config.json and
// dframe-store must edit disjoint paths (testkit.Apply fails otherwise),
// and each row reads back what was desired.
func TestComposedSharedTargets(t *testing.T) {
	e := newEnv(t)
	rs, rd := testkit.Replica(1), testkit.Replica(2)
	src := e.root.AddFolder(t, testkit.Rich("Claude-s", 1))
	dst := e.root.AddFolder(t, testkit.Rich("Claude", 2))
	reg := all.Registry()
	var edits []plan.Edit
	want := map[model.RowID]*model.RowSnapshot{}
	shared := map[string][]model.RowID{}
	for _, row := range implementedRows() {
		d, res := syncRow(t, e, reg[row], src, dst, rs, rd)
		want[row] = d
		for _, ed := range res.Edits {
			if ed.Loc.File == sources.DesktopConfigFile || ed.Loc.LSKey == "dframe-store" {
				k := ed.Loc.File + ed.Loc.LSKey
				if l := shared[k]; len(l) == 0 || l[len(l)-1] != row {
					shared[k] = append(shared[k], row)
				}
			}
		}
		edits = append(edits, res.Edits...)
	}
	if n := len(shared[sources.DesktopConfigFile]); n < 4 {
		t.Fatalf("expected several rows on the config mirror, got %v", shared[sources.DesktopConfigFile])
	}
	testkit.Apply(t, dst, edits)
	for _, row := range implementedRows() {
		got := read(t, reg[row], e.rc(t, dst, rd, row))
		t.Run(string(row), func(t *testing.T) { assertSameItems(t, got, want[row]) })
	}
}

func TestGroupAndPinnedOrderTouchDisjointConfigPaths(t *testing.T) {
	e := newEnv(t)
	rs, rd := testkit.Replica(1), testkit.Replica(2)
	src := e.root.AddFolder(t, testkit.Rich("Claude-s", 1))
	dst := e.root.AddFolder(t, testkit.Rich("Claude", 2))
	reg := all.Registry()
	paths := map[model.RowID][]string{}
	for _, row := range []model.RowID{model.RowGroupOrder, model.RowPinnedOrder, model.RowSidebarView} {
		_, res := syncRow(t, e, reg[row], src, dst, rs, rd)
		for _, ed := range res.Edits {
			if ed.Loc.File == sources.DesktopConfigFile {
				paths[row] = append(paths[row], strings.Join(ed.Path, "/"))
			}
		}
	}
	rows := []model.RowID{model.RowGroupOrder, model.RowPinnedOrder, model.RowSidebarView}
	for i, a := range rows {
		if len(paths[a]) == 0 {
			t.Fatalf("%s: no config edits", a)
		}
		for _, b := range rows[i+1:] {
			for _, p := range paths[a] {
				for _, q := range paths[b] {
					if p == q || strings.HasPrefix(p, q+"/") || strings.HasPrefix(q, p+"/") {
						t.Errorf("%s path %s overlaps %s path %s", a, p, b, q)
					}
				}
			}
		}
	}
	sort.Strings(paths[model.RowGroupOrder])
}

// TestPlanCompose folds every row's edits with the real plan.Compose: one
// op per location, the config mirror and dframe-store composed from
// several rows.
func TestPlanCompose(t *testing.T) {
	e := newEnv(t)
	rs, rd := testkit.Replica(1), testkit.Replica(2)
	src := e.root.AddFolder(t, testkit.Rich("Claude-s", 1))
	dst := e.root.AddFolder(t, testkit.Rich("Claude", 2))
	reg := all.Registry()
	var edits []plan.Edit
	for _, row := range implementedRows() {
		_, res := syncRow(t, e, reg[row], src, dst, rs, rd)
		edits = append(edits, res.Edits...)
	}
	ls := testkit.LS(t, dst)
	srcFn := func(loc plan.Location) ([]byte, bool, error) {
		if loc.LSKey != "" {
			k, err := localstorage.Key(loc.LSKey)
			if err != nil {
				return nil, false, err
			}
			return ls.Get(k)
		}
		b, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(loc.File)))
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return b, err == nil, err
	}
	ops, err := plan.Compose(edits, srcFn)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	seen := map[string]bool{}
	for _, op := range ops {
		k := op.Path + "\x00" + op.Key
		if seen[k] {
			t.Errorf("two ops for %s%s", op.Path, op.Key)
		}
		seen[k] = true
		if op.Path == sources.DesktopConfigFile && len(op.Rows) < 4 {
			t.Errorf("config mirror op composed from %v only", op.Rows)
		}
		if op.Key == "dframe-store" && len(op.Rows) != 2 {
			t.Errorf("dframe-store op composed from %v", op.Rows)
		}
	}
}

// TestOtherAccountsUntouched: each folder also holds another account's
// session directory, with an id that collides with one of the replica's.
// No adapter reads it, and syncing every row leaves it byte-identical.
func TestOtherAccountsUntouched(t *testing.T) {
	e := newEnv(t)
	rs, rd := testkit.Replica(1), testkit.Replica(2)
	src := e.root.AddFolder(t, testkit.Rich("Claude-s", 1))
	dst := e.root.AddFolder(t, testkit.Rich("Claude", 2))
	decoys := map[string]map[string]string{}
	for _, f := range []struct {
		folder string
		n      int
	}{{src, 1}, {dst, 2}} {
		dir := filepath.Join(f.folder, sources.SessionRel(testkit.DecoyReplica(f.n), ""))
		decoys[dir] = testkit.TreeOf(t, dir)
		if len(decoys[dir]) == 0 {
			t.Fatal("fixture lacks a decoy directory")
		}
	}
	reg := all.Registry()
	var edits []plan.Edit
	for _, row := range implementedRows() {
		desired, res := syncRow(t, e, reg[row], src, dst, rs, rd)
		b, _ := json.Marshal(desired)
		if strings.Contains(string(b), "decoy") || strings.Contains(string(b), testkit.FakeSession(71)) {
			t.Errorf("%s: a decoy record was read", row)
		}
		for _, ed := range res.Edits {
			if strings.HasPrefix(ed.Loc.File, sources.SessionsRoot+"/") && !strings.HasPrefix(ed.Loc.File, sources.SessionRel(rd, "")) {
				t.Errorf("%s: edit outside the replica's directory: %s", row, ed.Loc.File)
			}
		}
		edits = append(edits, res.Edits...)
	}
	testkit.Apply(t, dst, edits)
	for dir, before := range decoys {
		after := testkit.TreeOf(t, dir)
		if !equalTrees(before, after) {
			t.Errorf("decoy directory %s changed", filepath.Base(dir))
		}
	}
}
