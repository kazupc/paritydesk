package scratch_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/sources/scratch"
)

// The stat cache saves into a fresh state directory through the real
// darwin Ops (whose WriteFileAtomic does not create its root): the cache
// folder is created with mode 0700.
func TestStatCacheSaveFreshStateDir(t *testing.T) {
	state := t.TempDir()
	path := filepath.Join(state, "cache", "scratch-stat.json")
	c := scratch.NewStatCache()
	st := scratch.FileStat{Dev: 1, Ino: 2, Size: 3}
	c.Put("1:2", "a", st, strings.Repeat("a", 64))
	if err := c.Save(fsx.Default(), path, filepath.Join(state, "cache", ".tmp")); err != nil {
		t.Fatalf("save: %v", err)
	}
	fi, err := os.Stat(filepath.Dir(path))
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("cache folder: %v %v", fi, err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("cache file: %v %v", fi, err)
	}
	if _, ok := scratch.LoadStatCache(path).Lookup("1:2", "a", st); !ok {
		t.Error("entry lost")
	}
}
