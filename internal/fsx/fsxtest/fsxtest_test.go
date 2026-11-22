package fsxtest

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kazupc/paritydesk/internal/fsx"
)

// recorder captures Errorf so a test can assert that FS reported a
// violation without failing itself.
type recorder struct {
	testing.TB
	failed bool
}

func (r *recorder) Errorf(string, ...any) { r.failed = true }

func TestBoundToTestRoot(t *testing.T) {
	rec := &recorder{TB: t}
	f := New(rec)
	inside := filepath.Join(t.TempDir(), "a")
	if err := f.MkdirAll(inside, 0o700); err != nil || rec.failed {
		t.Fatalf("inside: %v failed=%v", err, rec.failed)
	}
	outside := filepath.Join(os.TempDir(), "paritydesk-fsxtest-must-not-exist")
	if err := f.MkdirAll(outside, 0o700); !errors.Is(err, fsx.ErrOutsideConfinement) || !rec.failed {
		t.Fatalf("outside: %v failed=%v", err, rec.failed)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("outside directory was created")
	}
}

func TestRemoveAllRefusesAppFolders(t *testing.T) {
	f := New(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := f.RemoveAll(root, "Claude"); !errors.Is(err, fsx.ErrNotRemovable) {
		t.Fatalf("got %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "gen", "1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := f.RemoveAll(root, "gen/1"); err != nil {
		t.Fatal(err)
	}
}

func TestWriteFileAtomicCreatesParents(t *testing.T) {
	f := New(t)
	root, tmp := t.TempDir(), filepath.Join(t.TempDir(), "tmp")
	if err := f.WriteFileAtomic(root, "a/b/c.json", []byte("{}"), 0o600, tmp); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(root, "a", "b"))
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("parent: %v %v", fi, err)
	}
}
