//go:build darwin

package scratch

import (
	"io/fs"
	"os"
	"syscall"
)

// StatOf returns the identity and change times of a file's lstat result.
func StatOf(fi fs.FileInfo) (FileStat, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return FileStat{}, false
	}
	return FileStat{
		Dev: uint64(uint32(st.Dev)), Ino: st.Ino, Size: st.Size,
		MtimeNs: st.Mtimespec.Nano(), CtimeNs: st.Ctimespec.Nano(),
	}, true
}

// openNoFollow opens a regular file for reading without following a
// symlink in its last component.
func openNoFollow(p string) (*os.File, error) {
	return os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
}
