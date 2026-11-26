//go:build !darwin && !linux

package scratch

import (
	"io/fs"
	"os"
)

// StatOf is unsupported on this platform: every lookup misses.
func StatOf(fs.FileInfo) (FileStat, bool) { return FileStat{}, false }

// openNoFollow opens a file for reading.
func openNoFollow(p string) (*os.File, error) { return os.Open(p) }
