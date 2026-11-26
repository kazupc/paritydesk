//go:build darwin && !pd_nofdscan

package fdscan

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// openSelfProbe opens, in this process and with close-on-exec, the probe
// descriptors of the self-check: the own executable read-only (dyld when
// the executable cannot be opened) and /dev/null write-only (nothing is
// created), plus one end of an unbound Unix socketpair (no address, no
// network; SockFD -1 when it fails, for example in a sandbox that denies
// local sockets). It stats the descriptors and the working directory.
func openSelfProbe() (selfProbe, error) {
	p := selfProbe{PID: os.Getpid(), SockFD: -1, Stat: statDevIno}
	var fds []int
	closeAll := func() {
		for _, fd := range fds {
			unix.Close(fd)
		}
	}
	p.Close = closeAll
	open := func(path string, mode int, acc Access) error {
		fd, err := unix.Open(path, mode|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		fds = append(fds, fd)
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			return err
		}
		p.FDs = append(p.FDs, probeFD{FD: int32(fd), Access: acc, devIno: devIno{uint32(st.Dev), st.Ino}})
		return nil
	}
	exe, err := os.Executable()
	if err != nil || open(exe, unix.O_RDONLY, AccessRead) != nil {
		if err := open("/usr/lib/dyld", unix.O_RDONLY, AccessRead); err != nil {
			closeAll()
			return selfProbe{}, err
		}
	}
	if err := open("/dev/null", unix.O_WRONLY, AccessWrite); err != nil {
		closeAll()
		return selfProbe{}, err
	}
	if id, err := statDevIno("."); err == nil {
		p.Cwd = &id
	}
	syscall.ForkLock.RLock()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err == nil {
		unix.CloseOnExec(pair[0])
		unix.CloseOnExec(pair[1])
	}
	syscall.ForkLock.RUnlock()
	if err == nil {
		fds = append(fds, pair[0], pair[1])
		p.SockFD = int32(pair[0])
	}
	return p, nil
}

// statDevIno stats path (following symlinks) and returns its identity.
func statDevIno(path string) (devIno, error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return devIno{}, err
	}
	return devIno{uint32(st.Dev), st.Ino}, nil
}
