package state

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/kazupc/paritydesk/internal/pderr"
)

// lockRetry is the interval between attempts while the lock is busy.
const lockRetry = 100 * time.Millisecond

// Lock is the held run lock.
type Lock struct{ release func() error }

// Release releases the lock. The lock file itself is kept.
func (l *Lock) Release() error {
	if l == nil || l.release == nil {
		return nil
	}
	return l.release()
}

// Lock takes the run lock (flock LOCK_EX|LOCK_NB on the 0600 lock file,
// creating its directory 0700). While the lock is held elsewhere it
// retries every 100 ms (clock-driven) for up to wait, then returns
// *pderr.Error with code RunLockBusy. wait is 0 for interactive commands
// and a few seconds for the detached job, which may start while its
// parent is still exiting. The artifacts commands of later versions share
// this lock.
func (s *Store) Lock(ctx context.Context, wait time.Duration) (*Lock, error) {
	if err := s.fs.MkdirAll(filepath.Dir(s.lockFile), dirPerm); err != nil {
		return nil, fmt.Errorf("state: create lock directory: %w", err)
	}
	f, err := openLockFile(s.lockFile)
	if err != nil {
		return nil, fmt.Errorf("state: open lock file: %w", err)
	}
	deadline := s.clock.Now().Add(wait)
	for {
		err := tryLock(f)
		if err == nil {
			return &Lock{release: releaser(f)}, nil
		}
		if !errors.Is(err, errBusy) {
			f.Close()
			return nil, fmt.Errorf("state: lock: %w", err)
		}
		if !s.clock.Now().Before(deadline) {
			f.Close()
			return nil, pderr.New(pderr.RunLockBusy, "state/lock", "another paritydesk run holds the lock for this data root")
		}
		if err := s.clock.Sleep(ctx, lockRetry); err != nil {
			f.Close()
			return nil, fmt.Errorf("state: lock: %w", err)
		}
	}
}

// errBusy means the lock is held by another open file description.
var errBusy = errors.New("state: lock busy")
