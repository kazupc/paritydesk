package gen

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/clock"
	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/fsx/fsxtest"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/localstorage"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/store/kv"
)

// fakeUUID returns the n-th allow-listed fake UUID, in the same format as
// testkit.FakeUUID. testkit is not imported so these tests do not depend
// on its LevelDB fixtures.
func fakeUUID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012x", n) }

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

var testReplica = model.Replica{Account: ids.AccountID(fakeUUID(1)), Org: ids.OrgID(fakeUUID(2))}

type fixture struct {
	t      *testing.T
	fs     *fsxtest.FS
	s      *Store
	folder string
	tmp    string
	ls     *kv.MemStore
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	fsys := fsxtest.New(t)
	base := t.TempDir()
	f := &fixture{
		t:      t,
		fs:     fsys,
		s:      Open(filepath.Join(base, "generations"), fsys, clock.NewFake(t0)),
		folder: filepath.Join(base, "data", "Claude-test"),
		tmp:    filepath.Join(base, "tmp"),
		ls:     kv.NewMem(),
	}
	if err := os.MkdirAll(f.folder, 0o700); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) write(rel, content string) {
	f.t.Helper()
	p := filepath.Join(f.folder, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) read(rel string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(f.folder, rel))
	if err != nil {
		return "", false
	}
	return string(b), true
}

func lsKey(t *testing.T, name string) []byte {
	t.Helper()
	k, err := localstorage.Key(name)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func (f *fixture) lsPut(name, value string) {
	f.t.Helper()
	b := &kv.Batch{}
	b.Put(lsKey(f.t, name), localstorage.EncodeString(value))
	if err := f.ls.Write(b); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) lsGet(name string) (string, bool) {
	f.t.Helper()
	raw, ok, err := f.ls.Get(lsKey(f.t, name))
	if err != nil {
		f.t.Fatal(err)
	}
	if !ok {
		return "", false
	}
	s, err := localstorage.DecodeText(raw)
	if err != nil {
		f.t.Fatal(err)
	}
	return s, true
}

func (f *fixture) lsRaw(name string) []byte {
	raw, _, _ := f.ls.Get(lsKey(f.t, name))
	return append([]byte(nil), raw...)
}

func hashOf(s string) string { return model.HashBytes([]byte(s)) }

func lsHashOf(s string) string { return model.HashBytes(localstorage.EncodeString(s)) }

// tree maps every path under dir to its content hash ("dir" for
// directories).
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			out[rel] = "dir"
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = model.HashBytes(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// simple makes a complete generation n holding one file entry.
func (f *fixture) simple(n uint64, withClone bool, size int) {
	f.t.Helper()
	f.write("w.json", strings.Repeat("x", size))
	w, err := f.s.Begin(n, "run", "work", testReplica)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := w.SaveFile(model.RowWindowState, f.folder, "w.json", hashOf("new")); err != nil {
		f.t.Fatal(err)
	}
	if withClone {
		ldb := filepath.Join(f.t.TempDir(), "leveldb")
		if err := os.MkdirAll(ldb, 0o700); err != nil {
			f.t.Fatal(err)
		}
		for _, n := range []string{"CURRENT", "LOCK", "000001.log"} {
			if err := os.WriteFile(filepath.Join(ldb, n), []byte(n+strings.Repeat("y", size)), 0o600); err != nil {
				f.t.Fatal(err)
			}
		}
		if err := w.CloneLevelDB(ldb); err != nil {
			f.t.Fatal(err)
		}
	}
	if _, err := w.Finish(); err != nil {
		f.t.Fatal(err)
	}
}

func numbersOf(t *testing.T, s *Store) []uint64 {
	t.Helper()
	infos, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	var out []uint64
	for _, in := range infos {
		out = append(out, in.N)
	}
	return out
}

func TestBeginSaveFinishListLoad(t *testing.T) {
	f := newFixture(t)
	f.write("a.json", `{"a":1}`)
	f.write("sub/b.json", `{"b":2}`)
	f.lsPut("present", "old value")

	w, err := f.s.Begin(1, "run-1", "work", testReplica)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(w.Dir()); err != nil || fi.Mode().Perm() != dirPerm {
		t.Fatalf("generation dir: %v, %v", fi, err)
	}
	if w.QuarantineDir() != filepath.Join(w.Dir(), quarantineDir) {
		t.Fatal(w.QuarantineDir())
	}
	steps := []func() error{
		func() error { return w.SaveFile(model.RowAppPrefs, f.folder, "a.json", hashOf("new a")) },
		func() error { return w.SaveFile(model.RowWorktrees, f.folder, "sub/b.json", hashOf("new b")) },
		func() error { return w.SaveFile(model.RowWorktrees, f.folder, "new.json", hashOf("created")) },
		func() error {
			return w.SaveLSValue(model.RowGroupOrder, "present", f.lsRaw("present"), true, lsHashOf("new"))
		},
		func() error { return w.SaveLSValue(model.RowPinnedOrder, "absent", nil, false, lsHashOf("x")) },
		func() error { return w.SaveConflict(model.RowSessions, "local_x", []byte(`{"loser":true}`)) },
	}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	if err := w.SaveFile(model.RowAppPrefs, f.folder, "a.json", ""); err == nil {
		t.Fatal("duplicate SaveFile accepted")
	}
	if err := w.SaveFile(model.RowAppPrefs, f.folder, "../escape", ""); !errors.Is(err, fsx.ErrNotContained) {
		t.Fatalf("escaping SaveFile = %v", err)
	}
	ldb := filepath.Join(t.TempDir(), "leveldb")
	if err := os.MkdirAll(ldb, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"CURRENT", "LOCK", "000003.log", "MANIFEST-000001"} {
		if err := os.WriteFile(filepath.Join(ldb, n), []byte(n), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.CloneLevelDB(ldb); err != nil {
		t.Fatal(err)
	}
	m, err := w.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if !m.Complete || m.N != 1 || len(m.Entries) != 6 || m.LevelDB != levelDBDir || !m.Created.Equal(t0) {
		t.Fatalf("manifest = %+v", m)
	}
	if _, err := os.Stat(filepath.Join(w.Dir(), levelDBDir, lockName)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("LOCK was cloned")
	}
	if err := w.SaveConflict(model.RowSessions, "k", nil); !errors.Is(err, ErrFinished) {
		t.Fatalf("save after finish = %v", err)
	}
	if _, err := f.s.Begin(1, "run-2", "work", testReplica); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("Begin on existing n = %v", err)
	}

	infos, err := f.s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || !infos[0].Complete || !infos[0].LevelDB || infos[0].Entries != 6 || infos[0].Bytes == 0 || infos[0].RunID != "run-1" {
		t.Fatalf("infos = %+v", infos)
	}
	got, err := f.s.Load(1)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]Entry{}
	for _, e := range got.Entries {
		byPath[e.Path+e.Key] = e
	}
	if e := byPath["a.json"]; !e.Present || e.PreHash != hashOf(`{"a":1}`) || e.PostHash != hashOf("new a") {
		t.Fatalf("a.json entry = %+v", e)
	}
	if e := byPath["new.json"]; e.Present || e.PreHash != "" || e.Stored != "" {
		t.Fatalf("new.json entry = %+v", e)
	}
	if e := byPath["present"]; !e.Present || e.PreHash != lsHashOf("old value") || !strings.HasPrefix(e.Stored, lsDir+"/") {
		t.Fatalf("LS entry = %+v", e)
	}
}

func TestTamperedGenerationFailsLoad(t *testing.T) {
	tests := []struct {
		name   string
		tamper func(dir string) error
	}{
		{"changed file", func(dir string) error {
			return os.WriteFile(filepath.Join(dir, filesDir, "w.json"), []byte("evil"), 0o600)
		}},
		{"removed file", func(dir string) error { return os.Remove(filepath.Join(dir, filesDir, "w.json")) }},
		{"changed clone", func(dir string) error {
			return os.WriteFile(filepath.Join(dir, levelDBDir, "CURRENT"), []byte("evil"), 0o600)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.simple(1, true, 4)
			if _, err := f.s.Load(1); err != nil {
				t.Fatal(err)
			}
			if err := tc.tamper(f.s.genDir(1)); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.Load(1); !errors.Is(err, ErrTampered) {
				t.Fatalf("Load = %v, want ErrTampered", err)
			}
		})
	}
}

func TestPrune(t *testing.T) {
	tests := []struct {
		name       string
		gens       int
		policy     PrunePolicy
		wantGens   []uint64
		wantClones []uint64
	}{
		{"by count", 5, PrunePolicy{Keep: 3, LevelDBClones: -1}, []uint64{5, 4, 3}, []uint64{5, 4, 3}},
		{"clones", 4, PrunePolicy{Keep: 10, LevelDBClones: 2}, []uint64{4, 3, 2, 1}, []uint64{4, 3}},
		{"no clones", 2, PrunePolicy{LevelDBClones: 0}, []uint64{2, 1}, nil},
		// Each generation is about 4.6 KB with its clone and 1.6 KB
		// without: after dropping clones, 4000 bytes keeps two.
		{"size", 4, PrunePolicy{LevelDBClones: 0, MaxBytes: 4000}, []uint64{4, 3}, nil},
		{"size keeps newest", 3, PrunePolicy{LevelDBClones: -1, MaxBytes: 1}, []uint64{3}, []uint64{3}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			for n := 1; n <= tc.gens; n++ {
				if err := os.Remove(filepath.Join(f.folder, "w.json")); err != nil && !errors.Is(err, fs.ErrNotExist) {
					t.Fatal(err)
				}
				f.simple(uint64(n), true, 1000)
			}
			if err := f.s.Prune(tc.policy); err != nil {
				t.Fatal(err)
			}
			if got := numbersOf(t, f.s); !slices.Equal(got, tc.wantGens) {
				t.Fatalf("generations = %v, want %v", got, tc.wantGens)
			}
			infos, _ := f.s.List()
			var clones []uint64
			for _, in := range infos {
				if in.LevelDB {
					clones = append(clones, in.N)
				}
				if _, err := f.s.Load(in.N); err != nil {
					t.Fatalf("pruned generation %d no longer loads: %v", in.N, err)
				}
			}
			if !slices.Equal(clones, tc.wantClones) {
				t.Fatalf("clones = %v, want %v", clones, tc.wantClones)
			}
			for _, c := range f.fs.Calls {
				if strings.HasPrefix(c, "RemoveAll ") && strings.Contains(c, "..") {
					t.Fatalf("unexpected removal %q", c)
				}
			}
		})
	}
}

func TestDeleteIncompleteAbortPurge(t *testing.T) {
	f := newFixture(t)
	f.simple(1, false, 4)
	if _, err := f.s.Begin(2, "run-2", "work", testReplica); err != nil {
		t.Fatal(err)
	}
	w3, err := f.s.Begin(3, "run-3", "work", testReplica)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Load(2); err == nil {
		t.Fatal("incomplete generation loaded")
	}
	for _, tc := range []struct {
		n    uint64
		gone bool
	}{{1, false}, {9, false}, {3, true}, {2, true}} {
		gone, err := f.s.DeleteIncomplete(tc.n)
		if err != nil || gone != tc.gone {
			t.Fatalf("DeleteIncomplete(%d) = %v, %v; want %v", tc.n, gone, err, tc.gone)
		}
	}
	if got := numbersOf(t, f.s); !slices.Equal(got, []uint64{1}) {
		t.Fatalf("left %v", got)
	}
	if err := w3.Abort(); err != nil {
		t.Fatalf("abort of a removed generation: %v", err)
	}

	w4, err := f.s.Begin(4, "run-4", "work", testReplica)
	if err != nil {
		t.Fatal(err)
	}
	if err := w4.Abort(); err != nil {
		t.Fatal(err)
	}
	if got := numbersOf(t, f.s); !slices.Equal(got, []uint64{1}) {
		t.Fatalf("after abort %v", got)
	}
	if err := f.s.Purge(); err != nil {
		t.Fatal(err)
	}
	if got := numbersOf(t, f.s); len(got) != 0 {
		t.Fatalf("after purge %v", got)
	}
	for _, c := range f.fs.Calls {
		if strings.HasPrefix(c, "RemoveAll ") {
			rel := strings.TrimPrefix(c, "RemoveAll ")
			if _, ok := parseName(strings.Split(rel, "/")[0]); !ok && rel != tmpDirName {
				t.Fatalf("removal outside a generation: %q", c)
			}
		}
	}
}

// restoreFixture runs a backup and a simulated apply, then a user edit
// after the generation:
//
//	keep.json     changed by apply             -> restored
//	user.json     changed by apply, then user  -> conflict
//	created.json  created by apply             -> quarantined
//	gone.json     quarantined by apply         -> moved back
//	ls "k1"       changed by apply             -> restored
//	ls "k2"       created by apply             -> deleted again
//	ls "k3"       changed by apply, then user  -> conflict
func restoreFixture(t *testing.T) (*fixture, Writer) {
	f := newFixture(t)
	f.write("keep.json", "keep-old")
	f.write("user.json", "user-old")
	f.write("gone.json", "gone-old")
	f.lsPut("k1", "v1-old")
	f.lsPut("k3", "v3-old")

	w, err := f.s.Begin(7, "run-7", "work", testReplica)
	if err != nil {
		t.Fatal(err)
	}
	saves := []error{
		w.SaveFile(model.RowAppPrefs, f.folder, "keep.json", hashOf("keep-new")),
		w.SaveFile(model.RowAppPrefs, f.folder, "user.json", hashOf("user-new")),
		w.SaveFile(model.RowWorktrees, f.folder, "created.json", hashOf("created")),
		w.SaveFile(model.RowSessions, f.folder, "gone.json", ""),
		w.SaveLSValue(model.RowGroupOrder, "k1", f.lsRaw("k1"), true, lsHashOf("v1-new")),
		w.SaveLSValue(model.RowGroupOrder, "k2", nil, false, lsHashOf("v2-new")),
		w.SaveLSValue(model.RowGroupOrder, "k3", f.lsRaw("k3"), true, lsHashOf("v3-new")),
	}
	for i, err := range saves {
		if err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
	if _, err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	// Apply.
	f.write("keep.json", "keep-new")
	f.write("user.json", "user-new")
	f.write("created.json", "created")
	if err := f.fs.MoveInto(f.folder, "gone.json", w.QuarantineDir()); err != nil {
		t.Fatal(err)
	}
	f.lsPut("k1", "v1-new")
	f.lsPut("k2", "v2-new")
	f.lsPut("k3", "v3-new")
	// User edits after the generation.
	f.write("user.json", "user-edited")
	f.lsPut("k3", "v3-edited")
	return f, w
}

func keysOf(es []Entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Path+e.Key)
	}
	slices.Sort(out)
	return out
}

func mutatingCalls(calls []string) []string {
	var out []string
	for _, c := range calls {
		op := strings.SplitN(c, " ", 2)[0]
		switch op {
		case "ID", "GetXattr", "FreeBytes", "FullSync":
		default:
			out = append(out, c)
		}
	}
	return out
}

func TestRestoreConditionalAndIdempotent(t *testing.T) {
	f, w := restoreFixture(t)
	in := RestoreInput{N: 7, Folder: f.folder, LS: f.ls, TmpDir: f.tmp}
	rep, err := f.s.Restore(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	wantRestored := []string{"created.json", "gone.json", "k1", "k2", "keep.json"}
	wantConflicts := []string{"k3", "user.json"}
	if got := keysOf(rep.Restored); !slices.Equal(got, wantRestored) {
		t.Fatalf("restored = %v, want %v", got, wantRestored)
	}
	if got := keysOf(rep.Conflicts); !slices.Equal(got, wantConflicts) {
		t.Fatalf("conflicts = %v, want %v", got, wantConflicts)
	}
	files := []struct {
		rel, want string
		present   bool
	}{
		{"keep.json", "keep-old", true},
		{"user.json", "user-edited", true},
		{"gone.json", "gone-old", true},
		{"created.json", "", false},
	}
	for _, c := range files {
		got, ok := f.read(c.rel)
		if ok != c.present || got != c.want {
			t.Errorf("%s = %q (%v), want %q (%v)", c.rel, got, ok, c.want, c.present)
		}
	}
	if b, err := os.ReadFile(filepath.Join(w.QuarantineDir(), "created.json")); err != nil || string(b) != "created" {
		t.Errorf("created file not quarantined: %q, %v", b, err)
	}
	ls := []struct {
		name, want string
		present    bool
	}{
		{"k1", "v1-old", true},
		{"k2", "", false},
		{"k3", "v3-edited", true},
	}
	for _, c := range ls {
		got, ok := f.lsGet(c.name)
		if ok != c.present || got != c.want {
			t.Errorf("LS %s = %q (%v), want %q (%v)", c.name, got, ok, c.want, c.present)
		}
	}

	// Second run: nothing new is written; already-restored entries are not
	// conflicts.
	f.fs.Calls = nil
	lsBefore := maps.Clone(map[string]string{"k1": string(f.lsRaw("k1")), "k3": string(f.lsRaw("k3"))})
	rep2, err := f.s.Restore(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if got := keysOf(rep2.Conflicts); !slices.Equal(got, wantConflicts) {
		t.Fatalf("second conflicts = %v", got)
	}
	if got := keysOf(rep2.Restored); !slices.Equal(got, wantRestored) {
		t.Fatalf("second restored = %v", got)
	}
	if m := mutatingCalls(f.fs.Calls); len(m) != 0 {
		t.Fatalf("second run wrote: %v", m)
	}
	if string(f.lsRaw("k1")) != lsBefore["k1"] || string(f.lsRaw("k3")) != lsBefore["k3"] {
		t.Fatal("second run changed Local Storage")
	}
}

func TestRestoreDryRunWritesNothing(t *testing.T) {
	f, _ := restoreFixture(t)
	beforeFolder := tree(t, f.folder)
	beforeGen := tree(t, f.s.dir)
	beforeLS := map[string]string{}
	for _, k := range []string{"k1", "k2", "k3"} {
		beforeLS[k] = string(f.lsRaw(k))
	}
	f.fs.Calls = nil
	rep, err := f.s.Restore(context.Background(), RestoreInput{N: 7, Folder: f.folder, LS: f.ls, TmpDir: f.tmp, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Restored) != 5 || len(rep.Conflicts) != 2 {
		t.Fatalf("dry run report = %v / %v", keysOf(rep.Restored), keysOf(rep.Conflicts))
	}
	if m := mutatingCalls(f.fs.Calls); len(m) != 0 {
		t.Fatalf("dry run wrote: %v", m)
	}
	if !maps.Equal(beforeFolder, tree(t, f.folder)) || !maps.Equal(beforeGen, tree(t, f.s.dir)) {
		t.Fatal("dry run changed files")
	}
	for k, v := range beforeLS {
		if string(f.lsRaw(k)) != v {
			t.Fatalf("dry run changed LS %s", k)
		}
	}
}

func TestRestoreLevelDB(t *testing.T) {
	for _, noSwap := range []bool{false, true} {
		t.Run(map[bool]string{false: "swap", true: "fallback"}[noSwap], func(t *testing.T) {
			f := newFixture(t)
			f.fs.NoSwap = noSwap
			ldb := filepath.Join(f.folder, levelDBRel)
			f.write(filepath.Join(levelDBRel, "CURRENT"), "MANIFEST-1")
			f.write(filepath.Join(levelDBRel, "LOCK"), "")
			f.write(filepath.Join(levelDBRel, "000001.log"), "old")
			w, err := f.s.Begin(1, "run", "work", testReplica)
			if err != nil {
				t.Fatal(err)
			}
			if err := w.CloneLevelDB(ldb); err != nil {
				t.Fatal(err)
			}
			if _, err := w.Finish(); err != nil {
				t.Fatal(err)
			}
			f.write(filepath.Join(levelDBRel, "000001.log"), "broken by apply")
			f.write(filepath.Join(levelDBRel, "000002.ldb"), "new table")

			for i := 0; i < 2; i++ {
				if err := f.s.RestoreLevelDB(context.Background(), 1, f.folder); err != nil {
					t.Fatal(err)
				}
				got := tree(t, ldb)
				want := map[string]string{".": "dir", "CURRENT": hashOf("MANIFEST-1"), "000001.log": hashOf("old")}
				if !maps.Equal(got, want) {
					t.Fatalf("restored LevelDB = %v", got)
				}
			}
			replaced := filepath.Join(w.Dir(), replacedDir+"-1", "000002.ldb")
			if b, err := os.ReadFile(replaced); err != nil || string(b) != "new table" {
				t.Fatalf("replaced directory not kept: %q, %v", b, err)
			}
			if _, err := f.s.Load(1); err != nil {
				t.Fatalf("generation damaged: %v", err)
			}
		})
	}
}

func TestRestoreLevelDBWithoutClone(t *testing.T) {
	f := newFixture(t)
	f.simple(1, false, 4)
	if err := f.s.RestoreLevelDB(context.Background(), 1, f.folder); !errors.Is(err, ErrNoLevelDBClone) {
		t.Fatalf("RestoreLevelDB = %v", err)
	}
}

// TestDeleteIncompleteKeepsUnreadable checks that recover's cleanup never
// removes a generation it cannot read: a newer paritydesk's manifest or a
// damaged one.
func TestDeleteIncompleteKeepsUnreadable(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
	}{
		{"newer manifest", `{"v":99,"n":5,"complete":false}`},
		{"damaged manifest", `{"v":1,"n":`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			dir := f.s.genDir(5)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, manifestFile), []byte(tc.manifest), 0o600); err != nil {
				t.Fatal(err)
			}
			gone, err := f.s.DeleteIncomplete(5)
			if gone {
				t.Fatalf("generation removed (err %v)", err)
			}
			if _, statErr := os.Stat(dir); statErr != nil {
				t.Fatalf("generation directory gone: %v", statErr)
			}
		})
	}
}
