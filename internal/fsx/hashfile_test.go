package fsx_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/model"
)

func TestHashFileStreams(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	if err := os.WriteFile(p, []byte("abc"), 0o640); err != nil {
		t.Fatal(err)
	}
	h, mode, err := fsx.HashFile(p)
	if err != nil || h != model.HashBytes([]byte("abc")) || mode != 0o640 {
		t.Fatalf("%s %o %v", h, mode, err)
	}
	// A sparse file far larger than what a test would read into memory.
	big := filepath.Join(dir, "big")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(1 << 30); err != nil {
		t.Fatal(err)
	}
	f.Close()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	if _, _, err := fsx.HashFile(big); err != nil {
		t.Fatal(err)
	}
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if after.TotalAlloc-before.TotalAlloc > 64<<20 {
		t.Errorf("HashFile allocated %d bytes for a 1 GiB file", after.TotalAlloc-before.TotalAlloc)
	}
	if err := os.Symlink(p, filepath.Join(dir, "ln")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fsx.HashFile(filepath.Join(dir, "ln")); err == nil {
		t.Error("followed a symlink")
	}
	if _, _, err := fsx.HashFile(dir); !errors.Is(err, fsx.ErrNotRegular) {
		t.Errorf("directory: %v", err)
	}
}
