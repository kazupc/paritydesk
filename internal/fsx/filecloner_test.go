package fsx

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// cloneSpy implements FileCloner on top of the unsupported stub.
type cloneSpy struct {
	Ops
	calls int
}

func (c *cloneSpy) CloneFileOnly(src, dst string) error { c.calls++; return nil }

func TestCloneFileOnlyWrappers(t *testing.T) {
	root := t.TempDir()
	if err := CloneFileOnly(nil, "a", "b"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("no capability: %v", err)
	}
	if err := ReadOnly(nil).(FileCloner).CloneFileOnly("a", "b"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("read-only: %v", err)
	}
	spy := &cloneSpy{}
	c := Confine(spy, root)
	if err := CloneFileOnly(c, "/anywhere/src", filepath.Join(root, "dst")); err != nil || spy.calls != 1 {
		t.Fatalf("confined inside: %v (%d calls)", err, spy.calls)
	}
	if err := CloneFileOnly(c, filepath.Join(root, "src"), "/outside/dst"); !errors.Is(err, ErrOutsideConfinement) || spy.calls != 1 {
		t.Fatalf("confined outside: %v", err)
	}
	type plain struct{ Ops }
	if err := CloneFileOnly(Confine(plain{}, root), "a", filepath.Join(root, "b")); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("confined without capability: %v", err)
	}
}

func TestDefaultCloneFileOnly(t *testing.T) {
	fc, ok := Default().(FileCloner)
	if !ok {
		t.Skip("this platform's Ops has no FileCloner")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := fc.CloneFileOnly(src, filepath.Join(dir, "dst"))
	if errors.Is(err, ErrUnsupported) {
		t.Skip("volume cannot clone")
	}
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "dst")); string(b) != "data" {
		t.Fatalf("clone = %q", b)
	}
	if err := os.Symlink(src, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := fc.CloneFileOnly(filepath.Join(dir, "link"), filepath.Join(dir, "dst2")); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("symlink source: %v", err)
	}
}
