package idb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/fsx/fsxtest"
	"github.com/kazupc/paritydesk/internal/store/idb/idbtest"
)

func newCloner(t *testing.T) (*Cloner, *fsxtest.FS, string) {
	t.Helper()
	fs := fsxtest.New(t)
	root := filepath.Join(t.TempDir(), ".paritydesk-tmp")
	c, err := NewCloner(root, fs)
	if err != nil {
		t.Fatal(err)
	}
	return c, fs, root
}

func tempLeft(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func withLogFiles(t *testing.T, folder string) {
	t.Helper()
	for _, n := range []string{"LOG", "LOG.old", "LOCK"} {
		if err := os.WriteFile(filepath.Join(folder, idbtest.Dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClonerReads(t *testing.T) {
	c, fs, root := newCloner(t)
	f := build(t, idbtest.PinDB(idbtest.PinValue([]string{a1})))
	withLogFiles(t, f)
	v, err := c.Read(context.Background(), f, testSpec())
	if err != nil || v.Text != pinText(a1) || v.Copied {
		t.Fatalf("v = %+v err = %v", v, err)
	}
	for _, call := range fs.Calls {
		base := filepath.Base(strings.TrimPrefix(call, "CloneFileOnly "))
		if (strings.HasPrefix(call, "CloneFileOnly ") || strings.HasPrefix(call, "Clone ")) && skipFile(base) {
			t.Fatalf("source file opened: %s", call)
		}
	}
	if left := tempLeft(t, root); len(left) != 0 {
		t.Fatalf("temp left: %v", left)
	}
}

func TestClonerByteCopy(t *testing.T) {
	c, fs, root := newCloner(t)
	fs.CloneUnsupported = true
	f := build(t, idbtest.PinDB(idbtest.PinValue([]string{a1})))
	withLogFiles(t, f)
	v, err := c.Read(context.Background(), f, testSpec())
	if err != nil || !v.Copied || v.Text != pinText(a1) {
		t.Fatalf("v = %+v err = %v", v, err)
	}
	for _, call := range fs.Calls {
		if strings.HasPrefix(call, "Clone ") && skipFile(filepath.Base(strings.TrimPrefix(call, "Clone "))) {
			t.Fatalf("source file copied: %s", call)
		}
	}
	s := testSpec()
	s.MaxCopyBytes = 10
	if _, err := c.Read(context.Background(), f, s); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("copy budget: %v", err)
	}
	if left := tempLeft(t, root); len(left) != 0 {
		t.Fatalf("temp left: %v", left)
	}
}

func TestClonerCloneIgnoresCopyBudget(t *testing.T) {
	c, _, _ := newCloner(t)
	f := build(t, idbtest.PinDB(idbtest.PinValue([]string{a1})))
	s := testSpec()
	// The copy budget bounds one log read (memory), not the clone: the
	// folder is larger than the budget, its log is not.
	fi, err := os.Stat(filepath.Join(f, idbtest.Dir, "000003.log"))
	if err != nil {
		t.Fatal(err)
	}
	s.MaxCopyBytes = fi.Size()
	if _, err := c.Read(context.Background(), f, s); err != nil {
		t.Fatal(err)
	}
	s.MaxCloneBytes = 10
	if _, err := c.Read(context.Background(), f, s); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("clone budget: %v", err)
	}
}

func TestClonerRefusals(t *testing.T) {
	c, _, root := newCloner(t)
	if _, err := c.Read(context.Background(), t.TempDir(), testSpec()); !errors.Is(err, ErrDirAbsent) {
		t.Fatalf("absent: %v", err)
	}
	backend := t.TempDir()
	if err := os.MkdirAll(filepath.Join(backend, "IndexedDB", "https_claude.ai_0.indexeddb.sqlite"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Read(context.Background(), backend, testSpec()); !errors.Is(err, ErrBackend) {
		t.Fatalf("backend: %v", err)
	}
	f := build(t, idbtest.PinDB(idbtest.PinValue([]string{a1})))
	link := filepath.Join(f, idbtest.Dir, "000009.ldb")
	if err := os.Symlink(filepath.Join(f, idbtest.Dir, "CURRENT"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Read(context.Background(), f, testSpec()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("symlink: %v", err)
	}
	os.Remove(link)
	if err := os.Mkdir(filepath.Join(f, idbtest.Dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Read(context.Background(), f, testSpec()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("subdirectory: %v", err)
	}
	os.Remove(filepath.Join(f, idbtest.Dir, "sub"))
	s := testSpec()
	s.MaxFiles = 2
	if _, err := c.Read(context.Background(), f, s); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("max files: %v", err)
	}
	linkedDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(linkedDir, "IndexedDB"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f, idbtest.Dir), filepath.Join(linkedDir, idbtest.Dir)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Read(context.Background(), linkedDir, testSpec()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("symlinked dir: %v", err)
	}
	if left := tempLeft(t, root); len(left) != 0 {
		t.Fatalf("temp left: %v", left)
	}
}

// changingFS changes the source while it is cloned.
type changingFS struct {
	*fsxtest.FS
	path string
	done bool
}

func (c *changingFS) CloneFileOnly(src, dst string) error {
	if !c.done {
		c.done = true
		if err := os.WriteFile(c.path, []byte("new"), 0o600); err != nil {
			return err
		}
	}
	return c.FS.CloneFileOnly(src, dst)
}

func TestClonerTorn(t *testing.T) {
	f := build(t, idbtest.PinDB(idbtest.PinValue([]string{a1})))
	fs := &changingFS{FS: fsxtest.New(t), path: filepath.Join(f, idbtest.Dir, "000004.log")}
	root := filepath.Join(t.TempDir(), "tmp")
	c, err := NewCloner(root, fs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Read(context.Background(), f, testSpec()); !errors.Is(err, ErrTorn) {
		t.Fatalf("torn: %v", err)
	}
	if left := tempLeft(t, root); len(left) != 0 {
		t.Fatalf("temp left: %v", left)
	}
}

func TestClonerCancelled(t *testing.T) {
	c, _, root := newCloner(t)
	f := build(t, idbtest.PinDB(idbtest.PinValue([]string{a1})))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Read(ctx, f, testSpec()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	if left := tempLeft(t, root); len(left) != 0 {
		t.Fatalf("temp left: %v", left)
	}
}

func TestSweepTemp(t *testing.T) {
	c, _, root := newCloner(t)
	for _, n := range []string{"idb-0123456789abcdef", "idb-fedcba9876543210", "other"} {
		if err := os.MkdirAll(filepath.Join(root, n, "db"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, "idb-0123456789abcdef"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := c.SweepTemp(); err != nil {
		t.Fatal(err)
	}
	left := strings.Join(tempLeft(t, root), ",")
	if left != "idb-fedcba9876543210,other" {
		t.Fatalf("left = %s", left)
	}
}
