package app

import (
	"io/fs"
	"syscall"
)

// fileIno returns the inode of fi, or 0 when the platform gives none.
func fileIno(fi fs.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino) //nolint:unconvert // uint32 or uint64 by platform
	}
	return 0
}
