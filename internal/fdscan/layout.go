package fdscan

import "encoding/binary"

// Layout of the libproc structures (sys/proc_info.h), the same on arm64
// and x86_64 (little-endian, verified with _Static_assert for both
// architectures; scripts/dev/libproc-layout.c keeps the check).
const (
	// struct proc_fdinfo {int32 proc_fd; uint32 proc_fdtype}.
	sizeFDInfo = 8
	// struct proc_fileportinfo {uint32 proc_fileport; uint32 proc_fdtype}.
	sizeFileportInfo = 8
	// struct proc_fileinfo: fi_openflags at 0 (also pfi of socket_fdinfo).
	offOpenFlags = 0

	// struct vnode_fdinfowithpath (PROC_PIDFDVNODEPATHINFO,
	// PROC_PIDFILEPORTVNODEPATHINFO): pvip at 24.
	sizeVnodeFDPath = 1200
	offFDDev        = 24
	offFDIno        = 32
	offFDPath       = 176

	// struct proc_vnodepathinfo (PROC_PIDVNODEPATHINFO): pvi_cdir at 0,
	// pvi_rdir at 1176; inside each vnode_info_path, dev at 0, ino at 8,
	// path at 152.
	sizeVnodePathInfo = 2352
	offCdirDev        = 0
	offCdirIno        = 8
	offCdirPath       = 152
	offRdirDev        = 1176
	offRdirIno        = 1184
	offRdirPath       = 1328

	// struct proc_regionwithpathinfo (flavors 8 and 22): pri_address at
	// 80, pri_size at 88, prp_vip at 96.
	sizeRegionPath  = 1272
	offRegionAddr   = 80
	offRegionSize   = 88
	offRegionDev    = 96
	offRegionIno    = 104
	offRegionPath   = 248
	sizeThreadPath  = 1288 // struct proc_threadwithpathinfo
	offThreadPath   = 264
	sizeSocketInfo  = 792 // struct socket_fdinfo (PROC_PIDFDSOCKETINFO)
	offSockFamily   = 184
	offSockKind     = 256
	offSockPath     = 282
	sockPathLen     = 104 // sizeof(sun_path)
	maxPathLen      = 1024
	threadEntrySize = 8 // uint64 thread handles
)

// libproc flavors and constants.
const (
	flavorListFDs        = 1  // PROC_PIDLISTFDS
	flavorListThreads    = 6  // PROC_PIDLISTTHREADS
	flavorRegionPath     = 8  // PROC_PIDREGIONPATHINFO (public)
	flavorVnodePath      = 9  // PROC_PIDVNODEPATHINFO
	flavorThreadPath     = 10 // PROC_PIDTHREADPATHINFO
	flavorListFileports  = 14 // PROC_PIDLISTFILEPORTS
	flavorRegionPath2    = 22 // PROC_PIDREGIONPATHINFO2 (private, vnode-backed regions only)
	fdFlavorVnodePath    = 2  // PROC_PIDFDVNODEPATHINFO
	fdFlavorSocket       = 3  // PROC_PIDFDSOCKETINFO
	fileportFlavorVnode  = 2  // PROC_PIDFILEPORTVNODEPATHINFO
	fdTypeVnode          = 1  // PROX_FDTYPE_VNODE
	fdTypeSocket         = 2  // PROX_FDTYPE_SOCKET
	afUnix               = 1  // AF_UNIX
	sockInfoUn           = 3  // SOCKINFO_UN
	openFlagRead         = 1  // FREAD
	openFlagWrite        = 2  // FWRITE
	zombieState          = 5  // SZOMB, kinfo_proc p_stat
	defaultRegionFlavor  = flavorRegionPath2
	fallbackRegionFlavor = flavorRegionPath
)

var le = binary.LittleEndian

// fdEntry is one struct proc_fdinfo.
type fdEntry struct {
	FD   int32
	Type uint32
}

// fileportEntry is one struct proc_fileportinfo.
type fileportEntry struct {
	Port uint32
	Type uint32
}

// devIno identifies a vnode.
type devIno struct {
	Dev uint32
	Ino uint64
}

// vnodePath is one vnode_info_path: the vnode's identity and its path.
type vnodePath struct {
	devIno
	Path string
}

// cString returns b up to the first NUL, or all of b. The result is a
// copy, so b can be reused.
func cString(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// decodeFDList decodes 8-byte proc_fdinfo entries; trailing bytes that do
// not fill an entry are ignored.
func decodeFDList(b []byte) []fdEntry {
	out := make([]fdEntry, 0, len(b)/sizeFDInfo)
	for i := 0; i+sizeFDInfo <= len(b); i += sizeFDInfo {
		out = append(out, fdEntry{FD: int32(le.Uint32(b[i:])), Type: le.Uint32(b[i+4:])})
	}
	return out
}

// decodeFileportList decodes 8-byte proc_fileportinfo entries.
func decodeFileportList(b []byte) []fileportEntry {
	out := make([]fileportEntry, 0, len(b)/sizeFileportInfo)
	for i := 0; i+sizeFileportInfo <= len(b); i += sizeFileportInfo {
		out = append(out, fileportEntry{Port: le.Uint32(b[i:]), Type: le.Uint32(b[i+4:])})
	}
	return out
}

// decodeThreadList decodes uint64 thread handles.
func decodeThreadList(b []byte) []uint64 {
	out := make([]uint64, 0, len(b)/threadEntrySize)
	for i := 0; i+threadEntrySize <= len(b); i += threadEntrySize {
		out = append(out, le.Uint64(b[i:]))
	}
	return out
}

// decodeVnodeFD decodes a struct vnode_fdinfowithpath.
func decodeVnodeFD(b []byte) (flags uint32, dev uint32, ino uint64, path string, ok bool) {
	if len(b) != sizeVnodeFDPath {
		return 0, 0, 0, "", false
	}
	return le.Uint32(b[offOpenFlags:]), le.Uint32(b[offFDDev:]), le.Uint64(b[offFDIno:]),
		cString(b[offFDPath : offFDPath+maxPathLen]), true
}

// decodeVnodePathInfo decodes a struct proc_vnodepathinfo: the working
// directory and the root directory (Path empty when not chrooted).
func decodeVnodePathInfo(b []byte) (cwd, root vnodePath, ok bool) {
	if len(b) != sizeVnodePathInfo {
		return vnodePath{}, vnodePath{}, false
	}
	cwd = vnodePath{devIno{le.Uint32(b[offCdirDev:]), le.Uint64(b[offCdirIno:])}, cString(b[offCdirPath : offCdirPath+maxPathLen])}
	root = vnodePath{devIno{le.Uint32(b[offRdirDev:]), le.Uint64(b[offRdirIno:])}, cString(b[offRdirPath : offRdirPath+maxPathLen])}
	return cwd, root, true
}

// decodeRegion decodes a struct proc_regionwithpathinfo.
func decodeRegion(b []byte) (addr, size uint64, dev uint32, ino uint64, path string, ok bool) {
	if len(b) != sizeRegionPath {
		return 0, 0, 0, 0, "", false
	}
	return le.Uint64(b[offRegionAddr:]), le.Uint64(b[offRegionSize:]), le.Uint32(b[offRegionDev:]),
		le.Uint64(b[offRegionIno:]), cString(b[offRegionPath : offRegionPath+maxPathLen]), true
}

// decodeThreadPath decodes a struct proc_threadwithpathinfo's path (empty
// when the thread has no working directory of its own).
func decodeThreadPath(b []byte) (path string, ok bool) {
	if len(b) != sizeThreadPath {
		return "", false
	}
	return cString(b[offThreadPath : offThreadPath+maxPathLen]), true
}

// decodeUnixSocket decodes a struct socket_fdinfo: the descriptor's open
// flags, the family and kind, and the bound path of a Unix socket.
func decodeUnixSocket(b []byte) (flags uint32, family, kind int32, path string, ok bool) {
	if len(b) != sizeSocketInfo {
		return 0, 0, 0, "", false
	}
	return le.Uint32(b[offOpenFlags:]), int32(le.Uint32(b[offSockFamily:])), int32(le.Uint32(b[offSockKind:])),
		cString(b[offSockPath : offSockPath+sockPathLen]), true
}

// accessOf maps fi_openflags to an Access: FREAD|FWRITE -> ReadWrite;
// FREAD -> Read; FWRITE -> Write; neither -> AccessNone. Other bits
// (O_EVTONLY and the like) are ignored.
func accessOf(openflags uint32) Access {
	switch openflags & (openFlagRead | openFlagWrite) {
	case openFlagRead | openFlagWrite:
		return AccessReadWrite
	case openFlagRead:
		return AccessRead
	case openFlagWrite:
		return AccessWrite
	}
	return AccessNone
}
