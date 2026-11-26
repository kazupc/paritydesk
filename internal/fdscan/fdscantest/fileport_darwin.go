//go:build darwin && !pd_nofdscan

package fdscantest

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

// syscall6 is package syscall's libc call helper, as in
// internal/fdscan/libproc_darwin.go.
//
//go:linkname syscall6 syscall.syscall6
func syscall6(fn, a1, a2, a3, a4, a5, a6 uintptr) (r1, r2 uintptr, err syscall.Errno)

var libc_fileport_makeport_trampoline_addr uintptr

//go:cgo_import_dynamic libc_fileport_makeport fileport_makeport "/usr/lib/libSystem.B.dylib"

// portBox holds the out parameter on the heap.
type portBox struct{ port uint32 }

// MakeFileport turns the descriptor fd into a fileport held by this
// process (int fileport_makeport(int fd, fileport_t *port), 0 on
// success). The caller may close fd afterwards; the fileport keeps the
// file open.
func MakeFileport(fd int) (uint32, error) {
	if libc_fileport_makeport_trampoline_addr == 0 {
		return 0, fmt.Errorf("fdscantest: fileport_makeport trampoline not linked")
	}
	box := new(portBox)
	r, _, e := syscall6(libc_fileport_makeport_trampoline_addr,
		uintptr(fd), uintptr(unsafe.Pointer(&box.port)), 0, 0, 0, 0)
	runtime.KeepAlive(box)
	if int32(r) != 0 {
		return 0, fmt.Errorf("fdscantest: fileport_makeport: %d (%v)", int32(r), e)
	}
	return box.port, nil
}
