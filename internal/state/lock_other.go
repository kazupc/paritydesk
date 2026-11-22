//go:build !unix

package state

import (
	"os"

	"github.com/kazupc/paritydesk/internal/fsx"
)

func openLockFile(string) (*os.File, error) { return nil, fsx.ErrUnsupported }

func tryLock(*os.File) error { return fsx.ErrUnsupported }

func releaser(f *os.File) func() error { return f.Close }
