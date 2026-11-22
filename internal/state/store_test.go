package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/clock"
	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/fsx/fsxtest"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
)

// fakeUUID returns the n-th allow-listed fake UUID, in the same format as
// testkit.FakeUUID. testkit is not imported so these tests do not depend
// on its LevelDB fixtures.
func fakeUUID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012x", n) }

// canary is planted in token-bearing fields (the same role as
// testkit.Canary) and must never reach state files.
const canary = "PARITYDESK-CANARY-TOKEN-state"

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// newStore returns a store whose state directory does not exist yet.
func newStore(t *testing.T) (*Store, *fsxtest.FS) {
	t.Helper()
	fsys := fsxtest.New(t)
	base := t.TempDir()
	s, err := Open(filepath.Join(base, "state"), LockFile(filepath.Join(base, "support"), "/data"), fsys, clock.NewFake(t0))
	if err != nil {
		t.Fatal(err)
	}
	return s, fsys
}

// tree maps every path under dir to its sha256 ("dir" for directories).
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

func rep(acct, org int) model.Replica {
	return model.Replica{Account: ids.AccountID(fakeUUID(acct)), Org: ids.OrgID(fakeUUID(org))}
}

func sampleRoot() *Root {
	r := NewRoot("/data")
	r.Epoch = 7
	r.Active = "work"
	ps := &ProfileState{Alias: "work", FolderID: fsx.FileID{Dev: 1, Ino: 2, Birth: t0}, MarkerBase: "abc", LiveEpoch: 7}
	ps.ObserveReplica(rep(1, 10))
	ps.ObserveReplica(rep(1, 11))
	r.Profiles["work"] = ps
	k1 := BaseKey{Alias: "work", Replica: rep(1, 10), Row: model.RowSessions}
	k2 := BaseKey{Alias: "work", Replica: rep(1, 11), Row: model.RowSessions}
	r.Bases[k1.String()] = Ref(strings.Repeat("a", 64))
	r.Bases[k2.String()] = Ref(strings.Repeat("b", 64))
	r.Overlays[k1.String()] = Ref(strings.Repeat("c", 64))
	r.Canonical[model.RowSessions] = Ref(strings.Repeat("d", 64))
	r.Contested = map[string]model.Contested{ContestedKey(k1, "x"): {Row: model.RowSessions, Replica: rep(1, 10), Key: "x", ServerHash: "s", IntendedHash: "i"}}
	r.ToolStamps = map[string]map[string]int64{k1.String(): {"x": 42}}
	r.PendingAdd = &PendingAdd{Alias: "home", Managed: true, ParkedFrom: "work", Started: t0}
	r.Run = &Run{ID: "run-1", Kind: RunSwitch, From: "work", To: "home", Started: t0,
		PendingBases: map[model.RowID]Ref{model.RowTasks: Ref(strings.Repeat("e", 64))}}
	return r
}

func TestCommitLoadRoundTrip(t *testing.T) {
	s, _ := newStore(t)
	r := sampleRoot()
	if err := s.Commit(r); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, r) {
		a, _ := json.Marshal(r)
		b, _ := json.Marshal(got)
		t.Fatalf("round trip differs:\n%s\n%s", a, b)
	}
	if n := len(got.Profiles["work"].Replicas); n != 2 {
		t.Fatalf("replicas = %d, want 2", n)
	}
	for _, p := range []string{s.Dir(), filepath.Join(s.Dir(), rootFile)} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		want := fs.FileMode(filePerm)
		if fi.IsDir() {
			want = dirPerm
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s mode %v, want %v", p, fi.Mode().Perm(), want)
		}
	}
}

func TestBasesPerReplica(t *testing.T) {
	s, _ := newStore(t)
	r := sampleRoot()
	if err := s.Commit(r); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	a := BaseKey{Alias: "work", Replica: rep(1, 10), Row: model.RowSessions}.String()
	b := BaseKey{Alias: "work", Replica: rep(1, 11), Row: model.RowSessions}.String()
	if got.Bases[a] == "" || got.Bases[b] == "" || got.Bases[a] == got.Bases[b] {
		t.Fatalf("two orgs share or lost a base: %v", got.Bases)
	}
}

func TestOpenAndLoadWriteNothing(t *testing.T) {
	s, fsys := newStore(t)
	r, err := s.Load()
	if err != nil || r != nil {
		t.Fatalf("Load on empty = %v, %v", r, err)
	}
	if _, err := os.Lstat(s.Dir()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("state directory created by Open or Load")
	}
	if len(fsys.Calls) != 0 {
		t.Fatalf("calls: %v", fsys.Calls)
	}

	if err := s.Commit(sampleRoot()); err != nil {
		t.Fatal(err)
	}
	before := tree(t, s.Dir())
	fsys.Calls = nil
	if _, err := s.Load(); err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(before, tree(t, s.Dir())) || len(fsys.Calls) != 0 {
		t.Fatalf("Load changed the tree or called fs: %v", fsys.Calls)
	}
}

func TestLoadReadOnly(t *testing.T) {
	s, fsys := newStore(t)
	if err := s.Commit(sampleRoot()); err != nil {
		t.Fatal(err)
	}
	ro, err := Open(s.Dir(), s.lockFile, fsx.ReadOnly(fsys), clock.NewFake(t0))
	if err != nil {
		t.Fatal(err)
	}
	if r, err := ro.Load(); err != nil || r == nil {
		t.Fatalf("read-only Load = %v, %v", r, err)
	}
	if err := ro.Commit(sampleRoot()); !errors.Is(err, fsx.ErrReadOnly) {
		t.Fatalf("read-only Commit = %v", err)
	}
}

func TestFailedCommitKeepsPreviousRoot(t *testing.T) {
	s, fsys := newStore(t)
	r := sampleRoot()
	if err := s.Commit(r); err != nil {
		t.Fatal(err)
	}
	r2 := sampleRoot()
	r2.Epoch = 99
	crash := errors.New("simulated crash")
	fsys.Fail("WriteFileAtomic", crash)
	if err := s.Commit(r2); !errors.Is(err, crash) {
		t.Fatalf("Commit = %v, want crash", err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Epoch != 7 {
		t.Fatalf("epoch = %d, previous root not intact", got.Epoch)
	}
}

func TestNewerVersionRefused(t *testing.T) {
	s, _ := newStore(t)
	if err := os.MkdirAll(s.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	newer := []byte(`{"v": 2, "data_root": "/data", "profiles": {}}`)
	if err := os.WriteFile(filepath.Join(s.Dir(), rootFile), newer, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); !errors.Is(err, ErrNewerRoot) {
		t.Fatalf("Load = %v, want ErrNewerRoot", err)
	}
	if err := s.Commit(NewRoot("/data")); !errors.Is(err, ErrNewerRoot) {
		t.Fatalf("Commit = %v, want ErrNewerRoot", err)
	}
	b, _ := os.ReadFile(filepath.Join(s.Dir(), rootFile))
	if string(b) != string(newer) {
		t.Fatal("newer root overwritten")
	}
}

func TestMigrationInMemoryThenKeptOnce(t *testing.T) {
	s, fsys := newStore(t)
	if err := os.MkdirAll(s.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	v0 := []byte(`{"data_root": "/data", "epoch": 3, "active": "work"}`)
	if err := os.WriteFile(filepath.Join(s.Dir(), rootFile), v0, 0o600); err != nil {
		t.Fatal(err)
	}
	before := tree(t, s.Dir())
	r, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != RootVersion || r.Epoch != 3 || r.Active != "work" || r.Bases == nil || r.Profiles == nil {
		t.Fatalf("migrated root = %+v", r)
	}
	if !maps.Equal(before, tree(t, s.Dir())) {
		t.Fatal("Load wrote the migration")
	}

	pre := filepath.Join(s.Dir(), preMigrationPrefix+"0")
	for i := 0; i < 2; i++ {
		if err := s.Commit(r); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(pre)
		if err != nil || string(b) != string(v0) {
			t.Fatalf("pre-migration copy = %q, %v", b, err)
		}
	}
	writes := 0
	for _, c := range fsys.Calls {
		if c == "WriteFileAtomic "+preMigrationPrefix+"0" {
			writes++
		}
	}
	if writes != 1 {
		t.Fatalf("pre-migration copy written %d times", writes)
	}
	got, err := s.Load()
	if err != nil || got.Version != RootVersion || got.Epoch != 3 {
		t.Fatalf("after migration: %+v, %v", got, err)
	}
}

func TestBeginDone(t *testing.T) {
	s, _ := newStore(t)
	r := NewRoot("/data")
	if err := s.Begin(r, IntentQuit, nil); !errors.Is(err, ErrNoRun) {
		t.Fatalf("Begin without run = %v", err)
	}
	r.Run = &Run{ID: "r", Kind: RunSwitch}
	steps := []struct {
		begin bool
		name  IntentName
	}{
		{true, IntentQuit}, {false, IntentQuit},
		{true, IntentSwapping}, {false, IntentSwapping},
		{true, IntentSwapping},
	}
	for _, st := range steps {
		var err error
		if st.begin {
			err = s.Begin(r, st.name, map[string]string{"k": "v"})
		} else {
			err = s.Done(r, st.name)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	in := got.Run.Intents
	if len(in) != 3 || !in[0].Done || !in[1].Done || in[2].Done || in[2].Name != IntentSwapping || in[2].Data["k"] != "v" {
		t.Fatalf("intents = %+v", in)
	}
	if err := s.Done(got, IntentSwapping); err != nil {
		t.Fatal(err)
	}
	if !got.Run.Intents[2].Done || !got.Run.Intents[1].Done {
		t.Fatal("Done did not mark the last intent of that name")
	}
	if err := s.Done(got, IntentParked); !errors.Is(err, ErrNoIntent) {
		t.Fatalf("Done unknown = %v", err)
	}
}

func TestBeginRevertsOnFailedCommit(t *testing.T) {
	s, fsys := newStore(t)
	r := NewRoot("/data")
	r.Run = &Run{ID: "r", Kind: RunSync}
	fsys.Fail("WriteFileAtomic", errors.New("disk full"))
	if err := s.Begin(r, IntentQuit, nil); err == nil {
		t.Fatal("Begin succeeded")
	}
	if len(r.Run.Intents) != 0 {
		t.Fatal("intent kept in memory after a failed commit")
	}
}

func snapshot(t *testing.T, row model.RowID, items map[string]string) *model.RowSnapshot {
	t.Helper()
	s := &model.RowSnapshot{Row: row, Schema: SnapshotSchema, Items: map[string]model.Item{}, Stale: []string{"stale-key"}}
	for k, v := range items {
		it, err := model.NewItem(k, json.RawMessage(v), []byte(v), model.Stamp{Epoch: 1}, false)
		if err != nil {
			t.Fatal(err)
		}
		s.Items[k] = it
	}
	return s
}

func TestObjectsHashVerified(t *testing.T) {
	s, _ := newStore(t)
	snap := snapshot(t, model.RowTasks, map[string]string{"a": `{"x":1}`, "b": `[1,2]`})
	ref, err := s.PutSnapshot(snap)
	if err != nil {
		t.Fatal(err)
	}
	ref2, err := s.PutSnapshot(snap)
	if err != nil || ref2 != ref {
		t.Fatalf("second put = %v, %v", ref2, err)
	}
	got, err := s.GetSnapshot(ref)
	if err != nil {
		t.Fatal(err)
	}
	if got.Hash() != snap.Hash() || got.Stale != nil || got.Items["a"].Raw != nil {
		t.Fatalf("snapshot changed or memory-only fields persisted: %+v", got)
	}

	path := filepath.Join(s.Dir(), objectsDir, string(ref))
	if err := os.WriteFile(path, []byte(`{"row":"tasks","schema":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSnapshot(ref); !errors.Is(err, ErrCorruptObject) {
		t.Fatalf("tampered = %v", err)
	}
	if _, err := s.GetSnapshot("../root.json"); !errors.Is(err, ErrBadRef) {
		t.Fatalf("bad ref = %v", err)
	}
	if _, err := s.GetSnapshot(Ref(strings.Repeat("0", 64))); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing = %v", err)
	}

	future := snapshot(t, model.RowTasks, nil)
	future.Schema = 2
	fref, err := s.PutSnapshot(future)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSnapshot(fref); !errors.Is(err, ErrUnknownSchema) {
		t.Fatalf("unknown schema = %v", err)
	}
}

func TestSecretRawNeverStored(t *testing.T) {
	s, _ := newStore(t)
	raw := []byte(`{"name":"srv","token":"` + canary + `"}`)
	hashed := json.RawMessage(`{"name":"srv","token":"sha256:` + model.HashBytes([]byte(canary)) + `"}`)
	it, err := model.NewItem("srv", hashed, raw, model.Stamp{}, true)
	if err != nil {
		t.Fatal(err)
	}
	snap := &model.RowSnapshot{Row: model.RowMCPServers, Schema: SnapshotSchema, Items: map[string]model.Item{"srv": it}}
	if _, err := s.PutSnapshot(snap); err != nil {
		t.Fatal(err)
	}
	for rel := range tree(t, s.Dir()) {
		b, err := os.ReadFile(filepath.Join(s.Dir(), rel))
		if err == nil && strings.Contains(string(b), canary) {
			t.Fatalf("canary found in %s", rel)
		}
	}

	leaky := it
	leaky.Value = json.RawMessage(`{"wrapped":` + string(raw) + `}`)
	snap.Items["srv"] = leaky
	if _, err := s.PutSnapshot(snap); !errors.Is(err, ErrSecretInValue) {
		t.Fatalf("leaky secret = %v", err)
	}
}

func TestGCKeepsReferenced(t *testing.T) {
	s, _ := newStore(t)
	put := func(v string) Ref {
		ref, err := s.PutSnapshot(snapshot(t, model.RowTasks, map[string]string{"k": v}))
		if err != nil {
			t.Fatal(err)
		}
		return ref
	}
	base, canon, ovA, ovB, pending, garbage := put(`1`), put(`2`), put(`3`), put(`4`), put(`5`), put(`6`)
	r := NewRoot("/data")
	kA := BaseKey{Alias: "a", Replica: rep(1, 10), Row: model.RowTasks}
	kB := BaseKey{Alias: "b", Replica: rep(2, 20), Row: model.RowTasks}
	r.Bases[kA.String()] = base
	r.Canonical[model.RowTasks] = canon
	r.Overlays[kA.String()] = ovA
	r.Overlays[kB.String()] = ovB
	r.Run = &Run{ID: "r", PendingBases: map[model.RowID]Ref{model.RowTasks: pending}}
	stray := filepath.Join(s.Dir(), objectsDir, "README")
	if err := os.WriteFile(stray, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.GC(r); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []Ref{base, canon, ovA, ovB, pending} {
		if _, err := s.GetSnapshot(ref); err != nil {
			t.Errorf("referenced object removed: %v", err)
		}
	}
	if _, err := s.GetSnapshot(garbage); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("garbage kept: %v", err)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Error("non-object file removed")
	}
}

func TestOpenValidates(t *testing.T) {
	fsys := fsxtest.New(t)
	clk := clock.NewFake(t0)
	tests := []struct {
		name, dir, lock string
	}{
		{"relative dir", "state", "/l/x.lock"},
		{"empty lock", "/s", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Open(tc.dir, tc.lock, fsys, clk); err == nil {
				t.Fatal("Open accepted invalid paths")
			}
		})
	}
}
