//go:build darwin

package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// sysctlProcs lists processes with sysctl KERN_PROC_ALL and reads each
// one's executable and arguments from KERN_PROCARGS2.
type sysctlProcs struct{}

// Processes returns every process whose arguments could be read; a process
// that exited meanwhile or belongs to another user is skipped.
func (sysctlProcs) Processes(ctx context.Context) ([]Process, error) {
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, fmt.Errorf("app: list processes: %w", err)
	}
	procs := make([]Process, 0, len(kps))
	for i := range kps {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		kp := &kps[i]
		pid := int(kp.Proc.P_pid)
		if pid <= 0 {
			continue
		}
		buf, err := unix.SysctlRaw("kern.procargs2", pid)
		if err != nil {
			continue // EPERM (other user), EINVAL (exited or zombie)
		}
		exe, argv, err := parseProcArgs(buf)
		if err != nil {
			continue
		}
		p := Process{
			PID:     pid,
			PPID:    int(kp.Eproc.Ppid),
			Exe:     exe,
			Started: time.Unix(kp.Proc.P_starttime.Sec, int64(kp.Proc.P_starttime.Usec)*1000),
		}
		if len(argv) > 1 {
			p.Args = argv[1:]
		}
		procs = append(procs, p)
	}
	return procs, nil
}

// fcntlLocks probes fcntl locks with F_GETLK.
type fcntlLocks struct{}

// LockHeld opens path read-only (never following a symlink) and asks
// whether a write lock could be placed on the whole file. A missing file
// holds no lock. F_GETLK does not report this process's own locks.
func (fcntlLocks) LockHeld(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("app: probe lock: %w", err)
	}
	defer f.Close()
	lk := unix.Flock_t{Type: unix.F_WRLCK, Whence: io.SeekStart}
	if err := unix.FcntlFlock(f.Fd(), unix.F_GETLK, &lk); err != nil {
		return false, fmt.Errorf("app: probe lock %s: %w", path, err)
	}
	return lk.Type != unix.F_UNLCK, nil
}
