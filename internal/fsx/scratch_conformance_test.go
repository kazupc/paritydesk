//go:build darwin

package fsx_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/model"
)

func TestConformCloneFileAtomic(t *testing.T) {
	conform(t, func(t *testing.T, o fsx.Ops) {
		old := syscall.Umask(0o077)
		defer syscall.Umask(old)
		root, tmp := t.TempDir(), filepath.Join(t.TempDir(), "tmp")
		src := filepath.Join(t.TempDir(), "src")
		write(t, src, "new bytes")
		write(t, filepath.Join(root, "d/f"), "old bytes")
		mt := time.Date(2026, 1, 2, 3, 4, 5, 6000, time.UTC)
		want := model.HashBytes([]byte("new bytes"))
		if err := o.CloneFileAtomic(src, root, "d/f", 0o644, mt, want, tmp); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Lstat(filepath.Join(root, "d/f"))
		if err != nil || fi.Mode().Perm() != 0o644 || !fi.ModTime().Equal(mt) || read(t, filepath.Join(root, "d/f")) != "new bytes" {
			t.Fatalf("copy %v %v", fi, err)
		}
		assertNoTemp(t, tmp)
		// A mismatch leaves the target byte-identical and no temp file.
		write(t, src, "changed")
		if err := o.CloneFileAtomic(src, root, "d/f", 0o600, mt, want, tmp); !errors.Is(err, fsx.ErrHashMismatch) {
			t.Fatalf("mismatch: %v", err)
		}
		if read(t, filepath.Join(root, "d/f")) != "new bytes" {
			t.Fatal("target changed on a mismatch")
		}
		assertNoTemp(t, tmp)
		// A symlink source, a missing source.
		ln := filepath.Join(t.TempDir(), "ln")
		if err := os.Symlink(src, ln); err != nil {
			t.Fatal(err)
		}
		if err := o.CloneFileAtomic(ln, root, "d/g", 0o644, mt, want, tmp); !errors.Is(err, fsx.ErrSourceChanged) {
			t.Errorf("symlink source: %v", err)
		}
		if err := o.CloneFileAtomic(filepath.Join(t.TempDir(), "none"), root, "d/g", 0o644, mt, want, tmp); !errors.Is(err, fsx.ErrSourceChanged) {
			t.Errorf("missing source: %v", err)
		}
		// A symlinked component under root, a missing parent.
		other := t.TempDir()
		if err := os.Symlink(other, filepath.Join(root, "l")); err != nil {
			t.Fatal(err)
		}
		write(t, src, "new bytes")
		if err := o.CloneFileAtomic(src, root, "l/x", 0o644, mt, want, tmp); err == nil {
			t.Error("cloned through a symlinked component")
		}
		if err := o.CloneFileAtomic(src, root, "missing/x", 0o644, mt, want, tmp); err == nil {
			t.Error("cloned into a missing parent")
		}
		if _, err := os.Lstat(filepath.Join(other, "x")); err == nil {
			t.Error("wrote outside root")
		}
	})
}

func TestConformMkdirSymlinkRemoveEmptyDir(t *testing.T) {
	conform(t, func(t *testing.T, o fsx.Ops) {
		old := syscall.Umask(0o077)
		defer syscall.Umask(old)
		root := t.TempDir()
		if err := o.Mkdir(root, "a", 0o755); err != nil {
			t.Fatal(err)
		}
		if fi, err := os.Lstat(filepath.Join(root, "a")); err != nil || fi.Mode().Perm() != 0o755 {
			t.Fatalf("mkdir mode %v %v", fi, err)
		}
		if err := o.Mkdir(root, "a", 0o755); !errors.Is(err, fs.ErrExist) {
			t.Errorf("mkdir twice: %v", err)
		}
		if err := o.Mkdir(root, "missing/b", 0o755); err == nil {
			t.Error("mkdir without its parent")
		}
		if err := o.Symlink("../x", root, "a/ln"); err != nil {
			t.Fatal(err)
		}
		if l, _ := os.Readlink(filepath.Join(root, "a/ln")); l != "../x" {
			t.Errorf("link %q", l)
		}
		if err := o.Symlink("y", root, "a/ln"); !errors.Is(err, fs.ErrExist) {
			t.Errorf("symlink twice: %v", err)
		}
		if err := o.RemoveEmptyDir(root, "a"); !errors.Is(err, fsx.ErrNotEmpty) {
			t.Errorf("non-empty: %v", err)
		}
		if err := o.RemoveEmptyDir(root, "a/ln"); err == nil {
			t.Error("removed a symlink as a directory")
		}
		if err := os.Remove(filepath.Join(root, "a/ln")); err != nil {
			t.Fatal(err)
		}
		if err := o.RemoveEmptyDir(root, "a"); err != nil {
			t.Fatalf("empty: %v", err)
		}
		if err := o.BarrierSync(filepath.Join(root)); err != nil {
			t.Errorf("barrier sync: %v", err)
		}
	})
}

func TestScratchOpsRefusedReadOnlyAndConfined(t *testing.T) {
	root := t.TempDir()
	ro := fsx.ReadOnly(fsx.Default())
	for name, err := range map[string]error{
		"clone":   ro.CloneFileAtomic("/x", root, "a", 0o644, time.Now(), "", root),
		"mkdir":   ro.Mkdir(root, "a", 0o755),
		"symlink": ro.Symlink("x", root, "a"),
		"rmdir":   ro.RemoveEmptyDir(root, "a"),
		"barrier": ro.BarrierSync(root),
	} {
		if !errors.Is(err, fsx.ErrReadOnly) {
			t.Errorf("read-only %s: %v", name, err)
		}
	}
	c := fsx.Confine(fsx.Default(), filepath.Join(root, "inside"))
	for name, err := range map[string]error{
		"clone":   c.CloneFileAtomic("/x", root, "a", 0o644, time.Now(), "", root),
		"mkdir":   c.Mkdir(root, "a", 0o755),
		"symlink": c.Symlink("x", root, "a"),
		"rmdir":   c.RemoveEmptyDir(root, "a"),
		"barrier": c.BarrierSync(root),
		// The source is guarded too, even with root and tmpDir inside.
		"clone source": c.CloneFileAtomic("/x", filepath.Join(root, "inside"), "a", 0o644, time.Now(), "", filepath.Join(root, "inside")),
	} {
		if !errors.Is(err, fsx.ErrOutsideConfinement) {
			t.Errorf("confined %s: %v", name, err)
		}
	}
}
