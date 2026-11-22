//go:build darwin

package fsx_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/fsx/fsxtest"
)

// impls are the Ops implementations that must follow the same contract.
// Each constructor runs inside the subtest so fsxtest binds to its tree.
var impls = []struct {
	name string
	new  func(t *testing.T) fsx.Ops
}{
	{"darwin", func(*testing.T) fsx.Ops { return fsx.Default() }},
	{"fsxtest", func(t *testing.T) fsx.Ops { return fsxtest.New(t) }},
}

// conform runs f against every implementation.
func conform(t *testing.T, f func(t *testing.T, o fsx.Ops)) {
	for _, impl := range impls {
		t.Run(impl.name, func(t *testing.T) { f(t, impl.new(t)) })
	}
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// assertNoTemp fails if tmpDir holds any entry (a missing tmpDir is fine).
func assertNoTemp(t *testing.T, tmpDir string) {
	t.Helper()
	entries, err := os.ReadDir(tmpDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temp files left behind: %d", len(entries))
	}
}

func TestConformWriteFileAtomic(t *testing.T) {
	conform(t, func(t *testing.T, o fsx.Ops) {
		root, tmp := t.TempDir(), filepath.Join(t.TempDir(), "tmp")
		if err := o.WriteFileAtomic(root, "a/b/c.json", []byte("{}"), 0o640, tmp); err != nil {
			t.Fatal(err)
		}
		if got := read(t, filepath.Join(root, "a/b/c.json")); got != "{}" {
			t.Fatalf("content %q", got)
		}
		fi, err := os.Stat(filepath.Join(root, "a/b/c.json"))
		if err != nil || fi.Mode().Perm() != 0o640 {
			t.Fatalf("mode: %v %v", fi, err)
		}
		for _, d := range []string{"a", "a/b"} {
			fi, err := os.Stat(filepath.Join(root, d))
			if err != nil || fi.Mode().Perm() != 0o700 {
				t.Fatalf("parent %s: %v %v", d, fi, err)
			}
		}
		// Existing directories keep their modes.
		if err := os.Mkdir(filepath.Join(root, "open"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(root, "open"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := o.WriteFileAtomic(root, "open/f", []byte("x"), 0o600, tmp); err != nil {
			t.Fatal(err)
		}
		if fi, _ := os.Stat(filepath.Join(root, "open")); fi.Mode().Perm() != 0o755 {
			t.Fatalf("existing dir mode changed to %v", fi.Mode().Perm())
		}
		assertNoTemp(t, tmp)
	})
}

func TestConformWriteFileAtomicRefusals(t *testing.T) {
	conform(t, func(t *testing.T, o fsx.Ops) {
		root, outside := t.TempDir(), t.TempDir()
		tmp := filepath.Join(t.TempDir(), "tmp")
		write(t, filepath.Join(outside, "target.json"), "orig")
		if err := os.Symlink(outside, filepath.Join(root, "linkdir")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(outside, "target.json"), filepath.Join(root, "link.json")); err != nil {
			t.Fatal(err)
		}
		tests := []struct {
			name, rel string
			want      error
		}{
			{"symlinked parent", "linkdir/target.json", fsx.ErrSymlink},
			{"symlinked parent, new file", "linkdir/new/x.json", fsx.ErrSymlink},
			{"symlinked destination", "link.json", fsx.ErrSymlink},
			{"dot dot", "../escape.json", fsx.ErrNotContained},
			{"inner dot dot", "a/../../escape.json", fsx.ErrNotContained},
			{"absolute", "/tmp/escape.json", fsx.ErrNotContained},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				err := o.WriteFileAtomic(root, tc.rel, []byte("evil"), 0o600, tmp)
				if !errors.Is(err, tc.want) {
					t.Fatalf("got %v, want %v", err, tc.want)
				}
			})
		}
		if got := read(t, filepath.Join(outside, "target.json")); got != "orig" {
			t.Fatalf("symlink target changed: %q", got)
		}
		if _, err := os.Lstat(filepath.Join(outside, "new")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatal("directory created through a symlink")
		}
		if _, err := os.Lstat(filepath.Join(filepath.Dir(root), "escape.json")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatal("file written outside root")
		}
		assertNoTemp(t, tmp)
	})
}

func TestConformMoveInto(t *testing.T) {
	conform(t, func(t *testing.T, o fsx.Ops) {
		root, dst := t.TempDir(), t.TempDir()
		write(t, filepath.Join(root, "x", "y", "f.json"), "f")
		write(t, filepath.Join(root, "top"), "t")
		if err := o.MoveInto(root, "x/y/f.json", dst); err != nil {
			t.Fatal(err)
		}
		if err := o.MoveInto(root, "top", dst); err != nil {
			t.Fatal(err)
		}
		if got := read(t, filepath.Join(dst, "x", "y", "f.json")); got != "f" {
			t.Fatalf("got %q", got)
		}
		if got := read(t, filepath.Join(dst, "top")); got != "t" {
			t.Fatalf("got %q", got)
		}
		if _, err := os.Lstat(filepath.Join(root, "x", "y", "f.json")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatal("source left in place")
		}
		write(t, filepath.Join(root, "top"), "again")
		if err := o.MoveInto(root, "top", dst); !errors.Is(err, fs.ErrExist) {
			t.Fatalf("overwrite: got %v, want fs.ErrExist", err)
		}
		if err := o.MoveInto(root, "../x", dst); !errors.Is(err, fsx.ErrNotContained) {
			t.Fatalf("got %v", err)
		}
		outside := t.TempDir()
		write(t, filepath.Join(outside, "v"), "v")
		if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
			t.Fatal(err)
		}
		if err := o.MoveInto(root, "link/v", dst); !errors.Is(err, fsx.ErrSymlink) {
			t.Fatalf("got %v, want ErrSymlink", err)
		}
		if got := read(t, filepath.Join(outside, "v")); got != "v" {
			t.Fatal("moved through a symlink")
		}
	})
}

func TestConformRemoveAllGuards(t *testing.T) {
	conform(t, func(t *testing.T, o fsx.Ops) {
		root, outside := t.TempDir(), t.TempDir()
		write(t, filepath.Join(root, "Claude", "keep"), "k")
		write(t, filepath.Join(root, "Claude-work", "keep"), "k")
		write(t, filepath.Join(root, "gen", "1", "f"), "f")
		write(t, filepath.Join(outside, "victim"), "v")
		if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
			t.Fatal(err)
		}
		for _, rel := range []string{"Claude", "Claude-work", "gen/Claude-x"} {
			if err := o.RemoveAll(root, rel); !errors.Is(err, fsx.ErrNotRemovable) {
				t.Fatalf("%s: got %v", rel, err)
			}
		}
		if err := o.RemoveAll(root, "../"+filepath.Base(outside)); !errors.Is(err, fsx.ErrNotContained) {
			t.Fatalf("escape: got %v", err)
		}
		if err := o.RemoveAll(root, "link/victim"); !errors.Is(err, fsx.ErrSymlink) {
			t.Fatalf("symlink: got %v", err)
		}
		if err := o.RemoveAll(root, "link"); err != nil {
			t.Fatalf("removing the link itself: %v", err)
		}
		if err := o.RemoveAll(root, "gen/1"); err != nil {
			t.Fatal(err)
		}
		if err := o.RemoveAll(root, "gen/missing"); err != nil {
			t.Fatalf("missing: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(root, "gen", "1")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatal("gen/1 not removed")
		}
		for _, p := range []string{filepath.Join(root, "Claude", "keep"), filepath.Join(root, "Claude-work", "keep"), filepath.Join(outside, "victim")} {
			if _, err := os.Stat(p); err != nil {
				t.Fatalf("%s: %v", p, err)
			}
		}
	})
}

func TestConformRenameExcl(t *testing.T) {
	conform(t, func(t *testing.T, o fsx.Ops) {
		dir := t.TempDir()
		a, b, c := filepath.Join(dir, "a"), filepath.Join(dir, "b"), filepath.Join(dir, "c")
		write(t, filepath.Join(a, "f"), "a")
		write(t, filepath.Join(b, "f"), "b")
		if err := o.RenameExcl(a, b); !errors.Is(err, fs.ErrExist) {
			t.Fatalf("got %v, want fs.ErrExist", err)
		}
		if err := o.RenameExcl(a, c); err != nil {
			t.Fatal(err)
		}
		if got := read(t, filepath.Join(c, "f")); got != "a" {
			t.Fatalf("got %q", got)
		}
	})
}

func TestConformClone(t *testing.T) {
	conform(t, func(t *testing.T, o fsx.Ops) {
		dir := t.TempDir()
		src := filepath.Join(dir, "src")
		write(t, filepath.Join(src, "a"), "aa")
		write(t, filepath.Join(src, "d", "b"), "bb")
		if err := o.Clone(src, filepath.Join(dir, "dst")); err != nil {
			t.Fatal(err)
		}
		if got := read(t, filepath.Join(dir, "dst", "d", "b")); got != "bb" {
			t.Fatalf("got %q", got)
		}
		if err := o.Clone(filepath.Join(src, "a"), filepath.Join(dir, "a2")); err != nil {
			t.Fatal(err)
		}
		if got := read(t, filepath.Join(dir, "a2")); got != "aa" {
			t.Fatalf("got %q", got)
		}
		if err := o.Clone(src, filepath.Join(dir, "dst")); !errors.Is(err, fs.ErrExist) {
			t.Fatalf("existing destination: got %v", err)
		}
	})
}
