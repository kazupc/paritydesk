//go:build darwin && !pd_nofdscan

package fdscan

import (
	"errors"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// syscall6 is package syscall's libc call helper
// (src/syscall/syscall_darwin.go), the one golang.org/x/sys/unix uses.
// The runtime reads errno after every call, but syscall6 returns it only
// when the C function returned -1 (syscall.errno); libproc returns 0 on
// most failures, so callers never rely on err.
//
// Declared without a body: the compiler treats a uintptr(unsafe.Pointer(p))
// conversion written directly in the argument list of such a call as
// keeping p alive and unmoved until the call returns (package unsafe,
// rule 4). That guarantee covers only conversions written in the call
// expression itself.
//
//go:linkname syscall6 syscall.syscall6
func syscall6(fn, a1, a2, a3, a4, a5, a6 uintptr) (r1, r2 uintptr, err syscall.Errno)

var (
	libc_proc_listallpids_trampoline_addr     uintptr
	libc_proc_pidinfo_trampoline_addr         uintptr
	libc_proc_pidfdinfo_trampoline_addr       uintptr
	libc_proc_pidfileportinfo_trampoline_addr uintptr
)

//go:cgo_import_dynamic libc_proc_listallpids proc_listallpids "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_proc_pidinfo proc_pidinfo "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_proc_pidfdinfo proc_pidfdinfo "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_proc_pidfileportinfo proc_pidfileportinfo "/usr/lib/libSystem.B.dylib"

// int proc_listallpids(void *buffer, int buffersize): a pid count, -1 on error.
func procListAllPIDs(buf []int32) int {
	r, _, _ := syscall6(libc_proc_listallpids_trampoline_addr,
		uintptr(unsafe.Pointer(unsafe.SliceData(buf))), // in the call expression: keep-alive applies
		uintptr(len(buf)*4), 0, 0, 0, 0)
	runtime.KeepAlive(buf)
	return int(int32(r)) // a C int: the upper 32 bits are not defined
}

// int proc_pidinfo(int pid, int flavor, uint64_t arg, void *buffer, int buffersize): bytes, 0 on error.
func procPIDInfo(pid, flavor int, arg uint64, buf []byte) int {
	r, _, _ := syscall6(libc_proc_pidinfo_trampoline_addr,
		uintptr(pid), uintptr(flavor), uintptr(arg),
		uintptr(unsafe.Pointer(unsafe.SliceData(buf))), uintptr(len(buf)), 0)
	runtime.KeepAlive(buf)
	return int(int32(r))
}

// int proc_pidfdinfo(int pid, int fd, int flavor, void *buffer, int buffersize): bytes, 0 on error.
func procPIDFDInfo(pid int, fd int32, flavor int, buf []byte) int {
	r, _, _ := syscall6(libc_proc_pidfdinfo_trampoline_addr,
		uintptr(pid), uintptr(uint32(fd)), uintptr(flavor),
		uintptr(unsafe.Pointer(unsafe.SliceData(buf))), uintptr(len(buf)), 0)
	runtime.KeepAlive(buf)
	return int(int32(r))
}

// int proc_pidfileportinfo(int pid, uint32_t fileport, int flavor, void *buffer, int buffersize): bytes, 0 on error.
func procPIDFileportInfo(pid int, port uint32, flavor int, buf []byte) int {
	r, _, _ := syscall6(libc_proc_pidfileportinfo_trampoline_addr,
		uintptr(pid), uintptr(port), uintptr(flavor),
		uintptr(unsafe.Pointer(unsafe.SliceData(buf))), uintptr(len(buf)), 0)
	runtime.KeepAlive(buf)
	return int(int32(r))
}

// trampolinesLinked reports whether every trampoline address is set (an
// arch without its .s file compiles but leaves them zero).
func trampolinesLinked() bool {
	return libc_proc_listallpids_trampoline_addr != 0 && libc_proc_pidinfo_trampoline_addr != 0 &&
		libc_proc_pidfdinfo_trampoline_addr != 0 && libc_proc_pidfileportinfo_trampoline_addr != 0
}

// sysKernel is the real kernel: libproc through the trampolines and
// kern.proc.pid through sysctl.
type sysKernel struct{}

func (sysKernel) listAllPIDs(buf []int32) int { return procListAllPIDs(buf) }
func (sysKernel) pidInfo(pid, flavor int, arg uint64, buf []byte) int {
	return procPIDInfo(pid, flavor, arg, buf)
}
func (sysKernel) pidFDInfo(pid int, fd int32, flavor int, buf []byte) int {
	return procPIDFDInfo(pid, fd, flavor, buf)
}
func (sysKernel) pidFileportInfo(pid int, port uint32, flavor int, buf []byte) int {
	return procPIDFileportInfo(pid, port, flavor, buf)
}

// kinfo reads kern.proc.pid. x/sys returns EIO when the returned length
// is not one kinfo_proc (a missing pid gives length 0) and passes other
// sysctl errors through; only EIO and ESRCH mean "gone".
func (sysKernel) kinfo(pid int) (kinfo, kstate) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	switch {
	case err == nil:
	case errors.Is(err, unix.EIO), errors.Is(err, unix.ESRCH):
		return kinfo{}, kGone
	default:
		return kinfo{}, kErr
	}
	if int(kp.Proc.P_pid) != pid {
		return kinfo{}, kGone
	}
	return kinfo{
		Started: time.Unix(kp.Proc.P_starttime.Sec, int64(kp.Proc.P_starttime.Usec)*1000),
		UID:     kp.Eproc.Ucred.Uid,
		RUID:    kp.Eproc.Pcred.P_ruid,
		Zombie:  kp.Proc.P_stat == zombieState,
	}, kOK
}

func (sysKernel) euid() uint32 { return uint32(unix.Geteuid()) }
func (sysKernel) ruid() uint32 { return uint32(unix.Getuid()) }

// system is the process-wide scanner.
var system = &sysScanner{k: sysKernel{}, probe: openSelfProbe, linked: trampolinesLinked}

// System returns the libproc scanner. Each call returns the same
// process-wide value.
func System() Scanner { return system }
