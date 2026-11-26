//go:build unix

package fsx

import "syscall"

// oNoFollow makes an open fail on a symlink in the last component.
const oNoFollow = syscall.O_NOFOLLOW
