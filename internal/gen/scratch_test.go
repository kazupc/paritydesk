package gen

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/plan"
)

const scrWS = "scratch-workspaces/00000000-0000-4000-8000-00000000000a/00000000-0000-4000-8000-00000000000b/scratch-2026-01-02-0a0b0c"

func (f *fixture) readScr(rel string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(f.folder, rel))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// scratchRun simulates a run that created a directory, a file in it, a
// symlink, replaced a file and quarantined one.
func scratchRun(t *testing.T, f *fixture, n uint64) {
	t.Helper()
	f.write(scrWS+"/old.txt", "old")
	f.write(scrWS+"/gone.txt", "gone")
	w, err := f.s.Begin(n, "run-1", "b", testReplica)
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(w.SaveCreated(EntryDir, model.RowScratch, f.folder, scrWS+"/d", plan.DirHash(0o755)))
	must(w.SaveFile(model.RowScratch, f.folder, scrWS+"/d/new.txt", model.HashBytes([]byte("new"))))
	must(w.SaveCreated(EntryLink, model.RowScratch, f.folder, scrWS+"/ln", plan.LinkHash("d/new.txt")))
	must(w.SaveFile(model.RowScratch, f.folder, scrWS+"/old.txt", model.HashBytes([]byte("replaced"))))
	must(w.SaveFile(model.RowScratch, f.folder, scrWS+"/gone.txt", ""))
	if err := w.SaveCreated(EntryDir, model.RowScratch, f.folder, scrWS+"/old.txt", plan.DirHash(0o755)); err == nil {
		t.Fatal("SaveCreated accepted an existing path")
	}
	if _, err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	// Apply.
	ws := filepath.Join(f.folder, scrWS)
	must(os.Mkdir(filepath.Join(ws, "d"), 0o755))
	must(os.WriteFile(filepath.Join(ws, "d/new.txt"), []byte("new"), 0o644))
	must(os.Symlink("d/new.txt", filepath.Join(ws, "ln")))
	must(os.WriteFile(filepath.Join(ws, "old.txt"), []byte("replaced"), 0o644))
	must(f.fs.MoveInto(f.folder, scrWS+"/gone.txt", filepath.Join(f.s.genDir(n), quarantineDir)))
}

func TestScratchRestore(t *testing.T) {
	f := newFixture(t)
	scratchRun(t, f, 1)
	rep, err := f.s.Restore(context.Background(), RestoreInput{N: 1, Folder: f.folder, TmpDir: f.tmp, ScratchRoot: "scratch-workspaces"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Conflicts) != 0 {
		t.Fatalf("conflicts %+v", rep.Conflicts)
	}
	ws := filepath.Join(f.folder, scrWS)
	for _, gone := range []string{"d", "ln", "d/new.txt"} {
		if _, err := os.Lstat(filepath.Join(ws, gone)); err == nil {
			t.Errorf("%s still present", gone)
		}
	}
	if s, _ := f.readScr(scrWS + "/old.txt"); s != "old" {
		t.Errorf("old.txt %q", s)
	}
	if s, _ := f.readScr(scrWS + "/gone.txt"); s != "gone" {
		t.Errorf("gone.txt %q", s)
	}
	// Idempotent.
	if rep, err := f.s.Restore(context.Background(), RestoreInput{N: 1, Folder: f.folder, TmpDir: f.tmp, ScratchRoot: "scratch-workspaces"}); err != nil || len(rep.Conflicts) != 0 {
		t.Fatalf("second restore %+v %v", rep, err)
	}
}

// The generation's recorded scratch root serves a restore without the
// app's manifest (recover or rollback after an app update); without any
// root, a created directory is kept and reported.
func TestScratchRestoreUsesRecordedRoot(t *testing.T) {
	for _, recorded := range []bool{true, false} {
		f := newFixture(t)
		w, err := f.s.Begin(1, "run-1", "b", testReplica)
		if err != nil {
			t.Fatal(err)
		}
		if recorded {
			w.SetScratchRoot("scratch-workspaces")
		}
		if err := w.SaveCreated(EntryDir, model.RowScratch, f.folder, scrWS+"/d", plan.DirHash(0o755)); err != nil {
			t.Fatal(err)
		}
		m, err := w.Finish()
		if err != nil {
			t.Fatal(err)
		}
		if recorded != (m.ScratchRoot == "scratch-workspaces") {
			t.Fatalf("recorded root %q", m.ScratchRoot)
		}
		if err := os.MkdirAll(filepath.Join(f.folder, scrWS, "d"), 0o755); err != nil {
			t.Fatal(err)
		}
		rep, err := f.s.Restore(context.Background(), RestoreInput{N: 1, Folder: f.folder, TmpDir: f.tmp})
		if err != nil {
			t.Fatal(err)
		}
		_, statErr := os.Lstat(filepath.Join(f.folder, scrWS, "d"))
		if recorded && (len(rep.Conflicts) != 0 || statErr == nil) {
			t.Errorf("recorded root: conflicts %+v, dir kept: %v", rep.Conflicts, statErr == nil)
		}
		if !recorded && (len(rep.Conflicts) != 1 || statErr != nil) {
			t.Errorf("no root: conflicts %+v, stat %v", rep.Conflicts, statErr)
		}
	}
}

func TestScratchRestoreKeepsChangedDirsAndLinks(t *testing.T) {
	f := newFixture(t)
	scratchRun(t, f, 1)
	ws := filepath.Join(f.folder, scrWS)
	// The user put another file into the created directory and repointed
	// the link after the run.
	if err := os.WriteFile(filepath.Join(ws, "d/mine.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(ws, "ln"))
	if err := os.Symlink("elsewhere", filepath.Join(ws, "ln")); err != nil {
		t.Fatal(err)
	}
	rep, err := f.s.Restore(context.Background(), RestoreInput{N: 1, Folder: f.folder, TmpDir: f.tmp, ScratchRoot: "scratch-workspaces"})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[EntryKind]int{}
	for _, c := range rep.Conflicts {
		kinds[c.Kind]++
	}
	if kinds[EntryDir] != 1 || kinds[EntryLink] != 1 {
		t.Fatalf("conflicts %+v", rep.Conflicts)
	}
	if _, err := os.Stat(filepath.Join(ws, "d/mine.txt")); err != nil {
		t.Error("a user file was removed")
	}
	// Without the scratch root the directory is never removed.
	g := newFixture(t)
	scratchRun(t, g, 1)
	rep, _ = g.s.Restore(context.Background(), RestoreInput{N: 1, Folder: g.folder, TmpDir: g.tmp})
	if _, err := os.Stat(filepath.Join(g.folder, scrWS, "d")); err != nil || len(rep.Conflicts) != 1 {
		t.Errorf("directory removed without a scratch root: %+v", rep.Conflicts)
	}
}

func TestScratchPutBackVerifiesTheStoredCopy(t *testing.T) {
	f := newFixture(t)
	scratchRun(t, f, 1)
	// Tamper with the stored copy of old.txt after Finish; Restore reads
	// the manifest through Load, which refuses a tampered generation, so
	// check cloneBack directly too.
	m, err := f.s.readManifest(1)
	if err != nil {
		t.Fatal(err)
	}
	var e Entry
	for _, x := range m.Entries {
		if x.Path == scrWS+"/old.txt" {
			e = x
		}
	}
	stored := filepath.Join(f.s.genDir(1), e.Stored)
	if err := os.Chmod(stored, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stored, []byte("evil"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &restorer{s: f.s, m: m, in: RestoreInput{N: 1, Folder: f.folder, TmpDir: f.tmp}, dir: f.s.genDir(1)}
	if err := r.cloneBack(e, stored); !errors.Is(err, ErrTampered) {
		t.Fatalf("tampered copy: %v", err)
	}
	if s, _ := f.readScr(scrWS + "/old.txt"); s != "replaced" {
		t.Errorf("target changed: %q", s)
	}
}

func TestPruneKeepsScratchFiles(t *testing.T) {
	f := newFixture(t)
	scratchRun(t, f, 1)
	for n := uint64(2); n <= 3; n++ {
		w, err := f.s.Begin(n, "run", "b", testReplica)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Finish(); err != nil {
			t.Fatal(err)
		}
	}
	infos, _ := f.s.List()
	var sb int64
	for _, in := range infos {
		if in.N == 1 {
			sb = in.ScratchBytes
		}
	}
	if sb == 0 {
		t.Fatal("ScratchBytes not counted")
	}
	if err := f.s.Prune(PrunePolicy{Keep: 2, LevelDBClones: -1}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.s.genDir(1)); err == nil {
		t.Fatal("generation 1 not pruned")
	}
	kept := filepath.Join(f.s.dir, ScratchKeptDir, "1")
	b, err := os.ReadFile(filepath.Join(kept, filesDir, scrWS, "gone.txt"))
	if err != nil || string(b) != "gone" {
		t.Fatalf("quarantined file not kept: %q %v", b, err)
	}
	var l KeptListing
	raw, err := os.ReadFile(filepath.Join(kept, keptFile))
	if err != nil || json.Unmarshal(raw, &l) != nil || len(l.Entries) != 1 || l.Entries[0].Path != scrWS+"/gone.txt" {
		t.Fatalf("kept.json %s %v", raw, err)
	}
	sets, err := f.s.ScratchKept()
	if err != nil || len(sets) != 1 || !sets[0].Listed || sets[0].Files != 1 {
		t.Fatalf("sets %+v %v", sets, err)
	}
	// Nothing removes kept sets but RemoveScratchKept (and purge).
	if err := f.s.Prune(PrunePolicy{Keep: 1, LevelDBClones: -1}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Fatal("prune removed a kept set")
	}
	n, bytes, err := f.s.RemoveScratchKept(sets[0].Created.Add(-1))
	if err != nil || n != 0 || bytes != 0 {
		t.Fatalf("cutoff before the set removed it: %d %v", n, err)
	}
	if n, _, err := f.s.RemoveScratchKept(sets[0].Created.Add(1)); err != nil || n != 1 {
		t.Fatalf("remove %d %v", n, err)
	}
	if _, err := os.Stat(kept); err == nil {
		t.Fatal("kept set still present")
	}
}

func TestKeepScratchCrashLeavesFilesOnly(t *testing.T) {
	f := newFixture(t)
	scratchRun(t, f, 1)
	// The listing write fails: the files moved, no listing exists.
	f.fs.Fail("WriteFileAtomic", errors.New("crash"))
	if err := f.s.keepScratch(1); err == nil {
		t.Fatal("no error")
	}
	kept := filepath.Join(f.s.dir, ScratchKeptDir, "1")
	if _, err := os.Stat(filepath.Join(kept, filesDir, scrWS, "gone.txt")); err != nil {
		t.Fatal("files not moved first")
	}
	if _, err := os.Stat(filepath.Join(kept, keptFile)); err == nil {
		t.Fatal("a listing exists")
	}
	sets, _ := f.s.ScratchKept()
	if len(sets) != 1 || sets[0].Listed {
		t.Fatalf("sets %+v", sets)
	}
	// The generation is still there (removal comes last); a retry lists.
	if _, err := os.Stat(f.s.genDir(1)); err != nil {
		t.Fatal("generation removed before its files were listed")
	}
}

func TestPruneSizeRuleIgnoresScratchBytes(t *testing.T) {
	f := newFixture(t)
	f.write(scrWS+"/big.bin", string(make([]byte, 4096)))
	w, _ := f.s.Begin(1, "run", "b", testReplica)
	if err := w.SaveFile(model.RowScratch, f.folder, scrWS+"/big.bin", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	w2, _ := f.s.Begin(2, "run", "b", testReplica)
	if _, err := w2.Finish(); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Prune(PrunePolicy{MaxBytes: 3000, LevelDBClones: -1}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.s.genDir(1)); err != nil {
		t.Fatal("a generation was pruned for its scratch bytes")
	}
}
