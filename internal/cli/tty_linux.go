package cli

import (
	"os"

	"golang.org/x/sys/unix"
)

// isTTY reports whether f is a terminal: the terminal driver answers a
// termios request on it (a character device like /dev/null does not).
func isTTY(f any) bool {
	file, ok := f.(*os.File)
	if !ok {
		return false
	}
	_, err := unix.IoctlGetTermios(int(file.Fd()), unix.TCGETS)
	return err == nil
}
