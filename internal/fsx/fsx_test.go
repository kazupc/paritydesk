package fsx

import (
	"errors"
	"io/fs"
	"testing"
)

func TestContained(t *testing.T) {
	ok := []string{"a", "a/b.json", "Local Storage/leveldb/000005.ldb", "a/b/"}
	for _, rel := range ok {
		if _, err := Contained("/root", rel); err != nil {
			t.Fatalf("%q: %v", rel, err)
		}
	}
	bad := []string{"", "/abs", "../x", "a/../../x", "a/./b", "a//b", "a\x00b", ".", ".."}
	for _, rel := range bad {
		if _, err := Contained("/root", rel); !errors.Is(err, ErrNotContained) {
			t.Fatalf("%q: expected ErrNotContained, got %v", rel, err)
		}
	}
}

func TestCheckRemovable(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name, root, rel string
		ok              bool
	}{
		{"tool dir", root, "gen/1", true},
		{"empty root", "", "x", false},
		{"relative root", "tmp", "x", false},
		{"slash root", "/", "tmp", false},
		{"unclean root", root + "/./x", "y", false},
		{"escaping rel", root, "../x", false},
		{"app folder", root, "Claude", false},
		{"parked folder", root, "a/Claude-work", false},
		{"empty rel", root, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := CheckRemovable(tc.root, tc.rel)
			if (err == nil) != tc.ok {
				t.Fatalf("CheckRemovable(%q, %q) = %v, want ok=%v", tc.root, tc.rel, err, tc.ok)
			}
		})
	}
}

// recordOps records mutating calls and succeeds.
type recordOps struct {
	readOnly
	calls []string
}

func (r *recordOps) MkdirAll(p string, _ fs.FileMode) error { r.calls = append(r.calls, p); return nil }
func (r *recordOps) Clone(_, dst string) error              { r.calls = append(r.calls, dst); return nil }
func (r *recordOps) RemoveAll(root, rel string) error       { r.calls = append(r.calls, root); return nil }

func TestConfine(t *testing.T) {
	rec := &recordOps{}
	c := Confine(rec, "/tmp/pd/snap")
	if err := c.MkdirAll("/tmp/pd/snap/1", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := c.Clone("/elsewhere/leveldb", "/tmp/pd/snap/1/db"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []func() error{
		func() error { return c.MkdirAll("/tmp/pd/other", 0o700) },
		func() error { return c.Clone("/tmp/pd/snap/a", "/tmp/pd/snapx") },
		func() error { return c.RemoveAll("/tmp/pd", "snap") },
		func() error { return c.SwapDirs("/tmp/pd/snap/a", "/x/b") },
	} {
		if err := f(); !errors.Is(err, ErrOutsideConfinement) {
			t.Fatalf("expected ErrOutsideConfinement, got %v", err)
		}
	}
	if len(rec.calls) != 2 {
		t.Fatalf("calls %v", rec.calls)
	}
}

func TestCheckTestPath(t *testing.T) {
	if err := CheckTestPath("/Applications/Example.app"); !errors.Is(err, ErrRealPathInTest) {
		t.Fatalf("got %v", err)
	}
	if err := CheckTestPath(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}
