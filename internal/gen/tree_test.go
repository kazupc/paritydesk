package gen

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func (f *fixture) leftoverTree(rel string) {
	f.t.Helper()
	f.write(rel+"/a.json", `{"a":1}`)
	f.write(rel+"/sub/b.json", `{"b":2}`)
}

func fingerprint(t *testing.T, root, rel string) string {
	t.Helper()
	fp, _, _, err := TreeFingerprint(root, rel, 0)
	if err != nil {
		t.Fatal(err)
	}
	return fp
}

func TestTreeFingerprint(t *testing.T) {
	f := newFixture(t)
	f.leftoverTree("x")
	fp, files, bytes, err := TreeFingerprint(f.folder, "x", 0)
	if err != nil || files != 2 || bytes != 14 || len(fp) != len(treePrefix)+64 {
		t.Fatalf("fp %q files %d bytes %d err %v", fp, files, bytes, err)
	}
	if fp2, _, _, _ := TreeFingerprint(f.folder, "missing", 0); fp2 != "" {
		t.Fatalf("absent tree: %q", fp2)
	}
	// Equal after a rename into another directory.
	other := filepath.Join(t.TempDir(), "q")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := f.fs.MoveInto(f.folder, "x", other); err != nil {
		t.Fatal(err)
	}
	if got := fingerprint(t, other, "x"); got != fp {
		t.Fatal("the fingerprint changed with a rename")
	}
	// .DS_Store anywhere does not count.
	for _, p := range []string{"x/.DS_Store", "x/sub/.DS_Store"} {
		if err := os.WriteFile(filepath.Join(other, p), []byte("finder"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := fingerprint(t, other, "x"); got != fp {
		t.Fatal(".DS_Store changed the fingerprint")
	}
	changes := map[string]func(dir string) error{
		"size": func(d string) error { return os.WriteFile(filepath.Join(d, "x/a.json"), []byte(`{"a":12}`), 0o600) },
		"mtime": func(d string) error {
			return os.Chtimes(filepath.Join(d, "x/a.json"), time.Unix(1, 0), time.Unix(1, 0))
		},
		"mode":  func(d string) error { return os.Chmod(filepath.Join(d, "x/a.json"), 0o644) },
		"added": func(d string) error { return os.WriteFile(filepath.Join(d, "x/new.json"), nil, 0o600) },
	}
	for name, change := range changes {
		d := t.TempDir()
		g := &fixture{t: t, folder: d}
		g.leftoverTree("x")
		before := fingerprint(t, d, "x")
		if err := change(d); err != nil {
			t.Fatal(err)
		}
		if fingerprint(t, d, "x") == before {
			t.Errorf("%s: fingerprint unchanged", name)
		}
	}
	// Symlinks are not followed: a link to a big tree counts as one line.
	d := t.TempDir()
	g := &fixture{t: t, folder: d}
	g.leftoverTree("x")
	before := fingerprint(t, d, "x")
	if err := os.Symlink(f.folder, filepath.Join(d, "x/link")); err != nil {
		t.Fatal(err)
	}
	_, files, _, _ = TreeFingerprint(d, "x", 0)
	if fingerprint(t, d, "x") == before || files != 2 {
		t.Errorf("symlink: files %d", files)
	}
	if _, _, _, err := TreeFingerprint(d, "x", 2); !errors.Is(err, ErrTreeTooLarge) {
		t.Errorf("too large: %v", err)
	}
	if _, _, _, err := TreeFingerprint(d, "../x", 0); err == nil {
		t.Error("an escaping rel was accepted")
	}
}

// quarantine makes a pinned quarantine generation n for rels and moves
// the trees named by move into it.
func (f *fixture) quarantine(n uint64, rels, move []string) *Manifest {
	f.t.Helper()
	w, err := f.s.Begin(n, "run-q", "home", testReplica)
	if err != nil {
		f.t.Fatal(err)
	}
	w.Pin(PurposeLegacyQuarantine)
	for _, rel := range rels {
		if _, err := w.SaveTree(f.folder, rel); err != nil {
			f.t.Fatal(err)
		}
	}
	m, err := w.Finish()
	if err != nil {
		f.t.Fatal(err)
	}
	for _, rel := range move {
		if err := f.fs.MoveInto(f.folder, rel, w.QuarantineDir()); err != nil {
			f.t.Fatal(err)
		}
	}
	return m
}

func TestSaveTreeRefusals(t *testing.T) {
	f := newFixture(t)
	f.leftoverTree("x")
	f.write("file", "x")
	if err := os.Symlink(filepath.Join(f.folder, "x"), filepath.Join(f.folder, "link")); err != nil {
		t.Fatal(err)
	}
	w, err := f.s.Begin(1, "run", "home", testReplica)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.SaveTree(f.folder, "x"); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"x", "link", "file", "missing", "../x"} {
		if _, err := w.SaveTree(f.folder, rel); err == nil {
			t.Errorf("SaveTree(%q) accepted", rel)
		}
	}
	if err := w.Abort(); err != nil {
		t.Fatal(err)
	}
}

func TestQuarantineGenerationLifecycle(t *testing.T) {
	f := newFixture(t)
	f.leftoverTree("claude-code-sessions/x")
	f.leftoverTree("claude-code-sessions/y")
	m := f.quarantine(1, []string{"claude-code-sessions/x", "claude-code-sessions/y"}, []string{"claude-code-sessions/x"})
	if !m.Pinned || m.Purpose != PurposeLegacyQuarantine || len(m.Entries) != 2 || m.Entries[0].Kind != EntryQuarantineDir ||
		m.Entries[0].PostHash != "" || !m.Entries[0].Present || m.Entries[0].Row != "" {
		t.Fatalf("manifest %+v", m)
	}
	infos, _ := f.s.List()
	if len(infos) != 1 || !infos[0].Pinned || infos[0].Purpose != PurposeLegacyQuarantine {
		t.Fatalf("info %+v", infos)
	}
	if _, err := os.Stat(filepath.Join(f.folder, "claude-code-sessions/x")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("x was not moved")
	}
	// Something was moved: DeleteUnmoved keeps it.
	if gone, err := f.s.DeleteUnmoved(1); err != nil || gone {
		t.Fatalf("DeleteUnmoved = %v %v", gone, err)
	}
	// Prune keeps the pinned generation, whatever the policy.
	f.simple(2, false, 10)
	f.simple(3, false, 10)
	if err := f.s.Prune(PrunePolicy{Keep: 1, LevelDBClones: 0, MaxBytes: 1}); err != nil {
		t.Fatal(err)
	}
	if got := numbersOf(t, f.s); !slices.Equal(got, []uint64{3, 1}) {
		t.Fatalf("after prune: %v", got)
	}
	// Restore: x moves back (dry run first), y counts as restored (still in place).
	dry, err := f.s.Restore(context.Background(), RestoreInput{N: 1, Folder: f.folder, DryRun: true})
	if err != nil || len(dry.Restored) != 2 {
		t.Fatalf("dry run %+v %v", dry, err)
	}
	if _, err := os.Stat(filepath.Join(f.folder, "claude-code-sessions/x")); err == nil {
		t.Fatal("dry run moved x back")
	}
	rep, err := f.s.Restore(context.Background(), RestoreInput{N: 1, Folder: f.folder})
	if err != nil || len(rep.Restored) != 2 || len(rep.Conflicts) != 0 {
		t.Fatalf("restore %+v %v", rep, err)
	}
	if b, ok := f.read("claude-code-sessions/x/a.json"); !ok || b != `{"a":1}` {
		t.Fatal("x not back")
	}
	// Idempotent.
	rep, err = f.s.Restore(context.Background(), RestoreInput{N: 1, Folder: f.folder})
	if err != nil || len(rep.Restored) != 2 {
		t.Fatalf("second restore %+v %v", rep, err)
	}
}

func TestQuarantineRestoreConflicts(t *testing.T) {
	// A directory recreated since: conflict, nothing overwritten.
	f := newFixture(t)
	f.leftoverTree("d/x")
	f.quarantine(1, []string{"d/x"}, []string{"d/x"})
	f.write("d/x/new.json", "mine")
	rep, err := f.s.Restore(context.Background(), RestoreInput{N: 1, Folder: f.folder})
	if err != nil || len(rep.Conflicts) != 1 {
		t.Fatalf("recreated: %+v %v", rep, err)
	}
	if b, _ := f.read("d/x/new.json"); b != "mine" {
		t.Fatal("the recreated directory was touched")
	}
	if _, ok := f.read("d/x/a.json"); ok {
		t.Fatal("the quarantined copy was merged in")
	}
	// The quarantined copy changed: conflict, nothing moved.
	g := newFixture(t)
	g.leftoverTree("d/x")
	g.quarantine(1, []string{"d/x"}, []string{"d/x"})
	q := filepath.Join(g.s.genDir(1), quarantineDir, "d/x/a.json")
	if err := os.WriteFile(q, []byte("changed!"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err = g.s.Restore(context.Background(), RestoreInput{N: 1, Folder: g.folder})
	if err != nil || len(rep.Conflicts) != 1 {
		t.Fatalf("changed copy: %+v %v", rep, err)
	}
	if _, err := os.Stat(filepath.Join(g.folder, "d/x")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a changed copy was moved back")
	}
}

func TestDeleteUnmoved(t *testing.T) {
	f := newFixture(t)
	f.leftoverTree("d/x")
	f.quarantine(1, []string{"d/x"}, nil)
	if gone, err := f.s.DeleteUnmoved(1); err != nil || !gone {
		t.Fatalf("nothing moved: %v %v", gone, err)
	}
	if got := numbersOf(t, f.s); len(got) != 0 {
		t.Fatalf("left %v", got)
	}
	// An ordinary (unpinned) generation is never deleted by it.
	f.simple(2, false, 10)
	if gone, err := f.s.DeleteUnmoved(2); err != nil || gone {
		t.Fatalf("unpinned: %v %v", gone, err)
	}
	// Pinned for another purpose: kept.
	w, _ := f.s.Begin(3, "run", "home", testReplica)
	w.Pin("other")
	if _, err := w.SaveTree(f.folder, "d/x"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	if gone, _ := f.s.DeleteUnmoved(3); gone {
		t.Fatal("another purpose was deleted")
	}
	if gone, err := f.s.DeleteUnmoved(99); err != nil || gone {
		t.Fatalf("missing: %v %v", gone, err)
	}
}
