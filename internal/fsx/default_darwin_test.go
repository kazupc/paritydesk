//go:build darwin

package fsx

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func mustWrite(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSwapDirsKeepsXattrs(t *testing.T) {
	o := Default()
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	mustWrite(t, filepath.Join(a, "f"), "A")
	mustWrite(t, filepath.Join(b, "f"), "B")
	if err := o.SetXattr(a, MarkerXattr, []byte("marker-a")); err != nil {
		t.Fatal(err)
	}
	idA, err := o.ID(a)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.SwapDirs(a, b); err != nil {
		if errors.Is(err, ErrSwapUnsupported) {
			t.Skip("volume has no RENAME_SWAP")
		}
		t.Fatal(err)
	}
	if got := mustRead(t, filepath.Join(a, "f")); got != "B" {
		t.Fatalf("a/f = %q", got)
	}
	if got := mustRead(t, filepath.Join(b, "f")); got != "A" {
		t.Fatalf("b/f = %q", got)
	}
	v, found, err := o.GetXattr(b, MarkerXattr)
	if err != nil || !found || string(v) != "marker-a" {
		t.Fatalf("xattr on b: %q %v %v", v, found, err)
	}
	if _, found, err := o.GetXattr(a, MarkerXattr); err != nil || found {
		t.Fatalf("xattr on a: found=%v err=%v", found, err)
	}
	idB, err := o.ID(b)
	if err != nil {
		t.Fatal(err)
	}
	if idA != idB {
		t.Fatalf("identity did not move with the directory: %+v vs %+v", idA, idB)
	}
}

func TestSwapDirsMissing(t *testing.T) {
	dir := t.TempDir()
	if err := Default().SwapDirs(filepath.Join(dir, "x"), filepath.Join(dir, "y")); err == nil {
		t.Fatal("swap of missing directories succeeded")
	}
}

func TestRenameExcl(t *testing.T) {
	o := Default()
	dir := t.TempDir()
	from, to := filepath.Join(dir, "from"), filepath.Join(dir, "to")
	mustWrite(t, from, "1")
	mustWrite(t, to, "2")
	if err := o.RenameExcl(from, to); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("got %v, want fs.ErrExist", err)
	}
	if got := mustRead(t, to); got != "2" {
		t.Fatalf("destination changed: %q", got)
	}
	free := filepath.Join(dir, "free")
	if err := o.RenameExcl(from, free); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, free); got != "1" {
		t.Fatalf("got %q", got)
	}
}

func TestCloneFileAndTree(t *testing.T) {
	o := Default()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	mustWrite(t, filepath.Join(src, "a.json"), `{"a":1}`)
	mustWrite(t, filepath.Join(src, "sub", "b.ldb"), "bbbb")
	if err := os.Symlink("a.json", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	if err := o.Clone(filepath.Join(src, "a.json"), filepath.Join(dir, "copy.json")); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, filepath.Join(dir, "copy.json")); got != `{"a":1}` {
		t.Fatalf("file clone %q", got)
	}
	dst := filepath.Join(dir, "dst")
	if err := o.Clone(src, dst); err != nil {
		t.Fatal(err)
	}
	assertTreesEqual(t, src, dst)
	if err := o.Clone(src, dst); err == nil {
		t.Fatal("clone onto an existing destination succeeded")
	}
}

func TestCopyTreeFallback(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	mustWrite(t, filepath.Join(src, "a"), "aa")
	mustWrite(t, filepath.Join(src, "d", "b"), "bb")
	if err := os.Chmod(filepath.Join(src, "a"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(src, "d"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nonexistent-target", filepath.Join(src, "l")); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst")
	if err := copyTree(src, dst); err != nil {
		t.Fatal(err)
	}
	assertTreesEqual(t, src, dst)
	for _, name := range []string{"a", "d"} {
		s, _ := os.Stat(filepath.Join(src, name))
		d, _ := os.Stat(filepath.Join(dst, name))
		if s.Mode().Perm() != d.Mode().Perm() {
			t.Fatalf("%s mode %v, want %v", name, d.Mode().Perm(), s.Mode().Perm())
		}
	}
}

// assertTreesEqual compares names, kinds, file contents and link targets.
func assertTreesEqual(t *testing.T, a, b string) {
	t.Helper()
	err := filepath.WalkDir(a, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(a, p)
		q := filepath.Join(b, rel)
		qi, err := os.Lstat(q)
		if err != nil {
			return err
		}
		if qi.Mode().Type() != d.Type() {
			t.Errorf("%s: type %v vs %v", rel, d.Type(), qi.Mode().Type())
			return nil
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			l1, _ := os.Readlink(p)
			l2, _ := os.Readlink(q)
			if l1 != l2 {
				t.Errorf("%s: link %q vs %q", rel, l1, l2)
			}
		case d.Type().IsRegular():
			x, _ := os.ReadFile(p)
			y, _ := os.ReadFile(q)
			if !bytes.Equal(x, y) {
				t.Errorf("%s: content differs", rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWriteFileAtomicDarwin(t *testing.T) {
	o := Default()
	root, tmp := t.TempDir(), filepath.Join(t.TempDir(), "tmp")
	if err := o.WriteFileAtomic(root, "x/y/z.json", []byte("{}"), 0o640, tmp); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(root, "x/y/z.json"))
	if err != nil || fi.Mode().Perm() != 0o640 {
		t.Fatalf("file mode: %v %v", fi, err)
	}
	for _, d := range []string{"x", "x/y"} {
		fi, err := os.Stat(filepath.Join(root, d))
		if err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("%s mode: %v %v", d, fi, err)
		}
	}
	ti, err := os.Stat(tmp)
	if err != nil || ti.Mode().Perm() != 0o700 {
		t.Fatalf("tmp dir: %v %v", ti, err)
	}
	// Overwrite keeps atomic semantics and the new mode.
	if err := o.WriteFileAtomic(root, "x/y/z.json", []byte(`{"v":2}`), 0o600, tmp); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, filepath.Join(root, "x/y/z.json")); got != `{"v":2}` {
		t.Fatalf("got %q", got)
	}
	entries, _ := os.ReadDir(tmp)
	if len(entries) != 0 {
		t.Fatalf("temp files left: %v", entries)
	}
}

func TestMoveIntoDarwin(t *testing.T) {
	o := Default()
	root, gen := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(root, "Local Storage", "leveldb", "000005.ldb"), "data")
	if err := o.MoveInto(root, "Local Storage/leveldb/000005.ldb", gen); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, filepath.Join(gen, "Local Storage", "leveldb", "000005.ldb")); got != "data" {
		t.Fatalf("got %q", got)
	}
	if _, err := os.Lstat(filepath.Join(root, "Local Storage", "leveldb", "000005.ldb")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("source still present: %v", err)
	}
}

func TestRemoveAllGuards(t *testing.T) {
	o := Default()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "Claude", "keep"), "k")
	mustWrite(t, filepath.Join(root, "gen", "Claude-work", "keep"), "k")
	const missing = "paritydesk-removeall-guard-test-missing"
	tests := []struct {
		name, root, rel string
	}{
		{"app folder", root, "Claude"},
		{"parked folder", root, "gen/Claude-work"},
		{"home", home, missing},
		{"home parent", filepath.Dir(home), missing},
		{"slash", "/", missing},
		{"escape", root, "../" + missing},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := o.RemoveAll(tc.root, tc.rel)
			if !errors.Is(err, ErrNotRemovable) && !errors.Is(err, ErrNotContained) {
				t.Fatalf("got %v", err)
			}
		})
	}
	if got := mustRead(t, filepath.Join(root, "Claude", "keep")); got != "k" {
		t.Fatal("app folder touched")
	}
}

func TestRemoveAllDoesNotFollowSymlinks(t *testing.T) {
	o := Default()
	root, outside := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(outside, "victim"), "v")
	mustWrite(t, filepath.Join(outside, "dir", "victim"), "v")
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "tree"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "dir"), filepath.Join(root, "tree", "inner")); err != nil {
		t.Fatal(err)
	}
	if err := o.RemoveAll(root, "link/victim"); !errors.Is(err, ErrSymlink) {
		t.Fatalf("got %v, want ErrSymlink", err)
	}
	if err := o.RemoveAll(root, "tree"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "tree")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("tree not removed: %v", err)
	}
	for _, p := range []string{"victim", "dir/victim"} {
		if got := mustRead(t, filepath.Join(outside, p)); got != "v" {
			t.Fatalf("%s touched", p)
		}
	}
	if err := o.RemoveAll(root, "missing/deeper"); err != nil {
		t.Fatalf("missing path: %v", err)
	}
}

func TestIDStableAcrossRename(t *testing.T) {
	o := Default()
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if err := os.Mkdir(a, 0o700); err != nil {
		t.Fatal(err)
	}
	before, err := o.ID(a)
	if err != nil {
		t.Fatal(err)
	}
	if before.Ino == 0 || before.Birth.IsZero() {
		t.Fatalf("incomplete id %+v", before)
	}
	if err := os.Rename(a, b); err != nil {
		t.Fatal(err)
	}
	after, err := o.ID(b)
	if err != nil {
		t.Fatal(err)
	}
	if !before.Birth.Equal(after.Birth) || before.Ino != after.Ino || before.Dev != after.Dev {
		t.Fatalf("id changed: %+v -> %+v", before, after)
	}
}

func TestXattrRoundTrip(t *testing.T) {
	o := Default()
	dir := t.TempDir()
	if _, found, err := o.GetXattr(dir, MarkerXattr); err != nil || found {
		t.Fatalf("absent: found=%v err=%v", found, err)
	}
	for _, v := range [][]byte{[]byte("v1"), bytes.Repeat([]byte("x"), 4096), {}} {
		if err := o.SetXattr(dir, MarkerXattr, v); err != nil {
			t.Fatal(err)
		}
		got, found, err := o.GetXattr(dir, MarkerXattr)
		if err != nil || !found || !bytes.Equal(got, v) {
			t.Fatalf("round trip %d bytes: found=%v err=%v len=%d", len(v), found, err, len(got))
		}
	}
}

func TestSyncAndFreeBytes(t *testing.T) {
	o := Default()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "f"), "x")
	for _, p := range []string{dir, filepath.Join(dir, "f")} {
		if err := o.FullSync(p); err != nil {
			t.Fatalf("FullSync %s: %v", p, err)
		}
	}
	n, err := o.FreeBytes(dir)
	if err != nil || n == 0 {
		t.Fatalf("FreeBytes = %d, %v", n, err)
	}
}

func TestReadOnlyAndConfineRefuse(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "a"), "a")
	ro := ReadOnly(Default())
	for name, f := range map[string]func() error{
		"write":  func() error { return ro.WriteFileAtomic(dir, "b", nil, 0o600, filepath.Join(dir, "t")) },
		"rename": func() error { return ro.RenameExcl(filepath.Join(dir, "a"), filepath.Join(dir, "c")) },
		"mkdir":  func() error { return ro.MkdirAll(filepath.Join(dir, "d"), 0o700) },
		"remove": func() error { return ro.RemoveAll(dir, "a") },
		"xattr":  func() error { return ro.SetXattr(dir, MarkerXattr, []byte("x")) },
		"swap":   func() error { return ro.SwapDirs(dir, dir) },
		"clone":  func() error { return ro.Clone(filepath.Join(dir, "a"), filepath.Join(dir, "e")) },
		"move":   func() error { return ro.MoveInto(dir, "a", filepath.Join(dir, "m")) },
	} {
		if err := f(); !errors.Is(err, ErrReadOnly) {
			t.Fatalf("%s: got %v", name, err)
		}
	}
	if _, err := ro.ID(dir); err != nil {
		t.Fatalf("reads pass through: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("read-only mode changed the tree: %v", entries)
	}

	snap := filepath.Join(dir, "snap")
	c := Confine(Default(), snap)
	if err := c.MkdirAll(filepath.Join(snap, "1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := c.Clone(filepath.Join(dir, "a"), filepath.Join(snap, "1", "a")); err != nil {
		t.Fatal(err)
	}
	if err := c.WriteFileAtomic(dir, "z", []byte("z"), 0o600, snap); !errors.Is(err, ErrOutsideConfinement) {
		t.Fatalf("got %v", err)
	}
	if err := c.Clone(filepath.Join(dir, "a"), filepath.Join(dir, "out")); !errors.Is(err, ErrOutsideConfinement) {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "out")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("confined clone wrote outside its root")
	}
}
