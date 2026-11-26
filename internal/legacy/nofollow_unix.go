//go:build unix

package legacy

import "syscall"

// openFlags open a file read-only without following a symlink in the last
// component and without blocking on a FIFO.
const openFlags = syscall.O_NOFOLLOW | syscall.O_NONBLOCK
