//go:build unix

package state

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// openLockFile opens (creating 0600) the lock file without following a
// symlink.
func openLockFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, filePerm)
}

// tryLock takes an exclusive, non-blocking flock on f.
func tryLock(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.EWOULDBLOCK):
			return errBusy
		default:
			return err
		}
	}
}

// releaser unlocks and closes f once.
func releaser(f *os.File) func() error {
	done := false
	return func() error {
		if done {
			return nil
		}
		done = true
		uerr := syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		cerr := f.Close()
		if uerr != nil {
			return fmt.Errorf("state: unlock: %w", uerr)
		}
		if cerr != nil {
			return fmt.Errorf("state: close lock file: %w", cerr)
		}
		return nil
	}
}
