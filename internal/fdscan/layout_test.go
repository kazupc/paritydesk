package fdscan

import (
	"slices"
	"strings"
	"testing"
)

// Encoders that put values at the layout offsets, shared with the fake
// kernel.

func putPath(b []byte, off, n int, path string) {
	copy(b[off:off+n], path)
}

func encVnodeFD(flags, dev uint32, ino uint64, path string) []byte {
	b := make([]byte, sizeVnodeFDPath)
	le.PutUint32(b[offOpenFlags:], flags)
	le.PutUint32(b[offFDDev:], dev)
	le.PutUint64(b[offFDIno:], ino)
	putPath(b, offFDPath, maxPathLen, path)
	return b
}

func encVnodePathInfo(cwd, root vnodePath) []byte {
	b := make([]byte, sizeVnodePathInfo)
	le.PutUint32(b[offCdirDev:], cwd.Dev)
	le.PutUint64(b[offCdirIno:], cwd.Ino)
	putPath(b, offCdirPath, maxPathLen, cwd.Path)
	le.PutUint32(b[offRdirDev:], root.Dev)
	le.PutUint64(b[offRdirIno:], root.Ino)
	putPath(b, offRdirPath, maxPathLen, root.Path)
	return b
}

func encRegion(addr, size uint64, dev uint32, ino uint64, path string) []byte {
	b := make([]byte, sizeRegionPath)
	le.PutUint64(b[offRegionAddr:], addr)
	le.PutUint64(b[offRegionSize:], size)
	le.PutUint32(b[offRegionDev:], dev)
	le.PutUint64(b[offRegionIno:], ino)
	putPath(b, offRegionPath, maxPathLen, path)
	return b
}

func encThreadPath(path string) []byte {
	b := make([]byte, sizeThreadPath)
	putPath(b, offThreadPath, maxPathLen, path)
	return b
}

func encSocket(flags uint32, family, kind int32, path string) []byte {
	b := make([]byte, sizeSocketInfo)
	le.PutUint32(b[offOpenFlags:], flags)
	le.PutUint32(b[offSockFamily:], uint32(family))
	le.PutUint32(b[offSockKind:], uint32(kind))
	putPath(b, offSockPath, sockPathLen, path)
	return b
}

func TestDecodeGolden(t *testing.T) {
	flags, dev, ino, path, ok := decodeVnodeFD(encVnodeFD(3, 0x1000004, 0x1122334455, "/data/Claude/x"))
	if !ok || flags != 3 || dev != 0x1000004 || ino != 0x1122334455 || path != "/data/Claude/x" {
		t.Fatalf("vnode fd: %d %x %x %q %v", flags, dev, ino, path, ok)
	}
	for _, n := range []int{0, sizeVnodeFDPath - 1, sizeVnodeFDPath + 1} {
		if _, _, _, _, ok := decodeVnodeFD(make([]byte, n)); ok {
			t.Fatalf("vnode fd of %d bytes decoded", n)
		}
	}

	cwd, root, ok := decodeVnodePathInfo(encVnodePathInfo(vnodePath{devIno{1, 2}, "/data/Claude"}, vnodePath{devIno{3, 4}, "/jail"}))
	if !ok || cwd != (vnodePath{devIno{1, 2}, "/data/Claude"}) || root != (vnodePath{devIno{3, 4}, "/jail"}) {
		t.Fatalf("vnode path info: %+v %+v %v", cwd, root, ok)
	}
	if _, root, _ := decodeVnodePathInfo(encVnodePathInfo(vnodePath{devIno{1, 2}, "/a"}, vnodePath{})); root.Path != "" {
		t.Fatalf("empty root decoded as %q", root.Path)
	}
	if _, _, ok := decodeVnodePathInfo(make([]byte, 2400)); ok {
		t.Fatal("vnode path info of 2400 bytes decoded")
	}

	addr, size, rdev, rino, rpath, ok := decodeRegion(encRegion(0x100000000, 0x4000, 9, 77, "/data/Claude/bin/claude"))
	if !ok || addr != 0x100000000 || size != 0x4000 || rdev != 9 || rino != 77 || rpath != "/data/Claude/bin/claude" {
		t.Fatalf("region: %x %x %d %d %q", addr, size, rdev, rino, rpath)
	}
	if _, _, _, _, _, ok := decodeRegion(make([]byte, sizeRegionPath-8)); ok {
		t.Fatal("short region decoded")
	}

	if p, ok := decodeThreadPath(encThreadPath("/data/Claude/twd")); !ok || p != "/data/Claude/twd" {
		t.Fatalf("thread path %q %v", p, ok)
	}
	if p, ok := decodeThreadPath(encThreadPath("")); !ok || p != "" {
		t.Fatalf("empty thread path %q %v", p, ok)
	}
	if _, ok := decodeThreadPath(make([]byte, 1300)); ok {
		t.Fatal("thread path of 1300 bytes decoded")
	}

	// A path filling all 1024 bytes without a NUL decodes to all of it.
	long := strings.Repeat("a", maxPathLen)
	if _, _, _, p, _ := decodeVnodeFD(encVnodeFD(1, 1, 1, long)); p != long {
		t.Fatalf("full-length path: %d bytes", len(p))
	}
	if cString([]byte("ab\x00cd")) != "ab" || cString([]byte("abc")) != "abc" || cString(nil) != "" {
		t.Fatal("cString")
	}

	var list []byte
	for _, e := range []fdEntry{{3, 1}, {4, 2}, {-1, 9}} {
		list = le.AppendUint32(list, uint32(e.FD))
		list = le.AppendUint32(list, e.Type)
	}
	if got := decodeFDList(append(list, 1, 2, 3)); !slices.Equal(got, []fdEntry{{3, 1}, {4, 2}, {-1, 9}}) {
		t.Fatalf("fd list %+v", got)
	}
	if got := decodeFileportList(list[:16]); !slices.Equal(got, []fileportEntry{{3, 1}, {4, 2}}) {
		t.Fatalf("fileport list %+v", got)
	}
	var th []byte
	th = le.AppendUint64(th, 0xabc)
	th = le.AppendUint64(th, 0xdef)
	if got := decodeThreadList(append(th, 0)); !slices.Equal(got, []uint64{0xabc, 0xdef}) {
		t.Fatalf("thread list %x", got)
	}
}

func TestAccessOf(t *testing.T) {
	for flags, want := range map[uint32]Access{0: AccessNone, 1: AccessRead, 2: AccessWrite, 3: AccessReadWrite, 0x8001: AccessRead} {
		if got := accessOf(flags); got != want {
			t.Errorf("accessOf(%#x) = %d, want %d", flags, got, want)
		}
	}
}

func TestKindString(t *testing.T) {
	want := map[Kind]string{KindFD: "fd", KindSocket: "socket", KindFileport: "fp.", KindCwd: "cwd", KindRoot: "rtd",
		KindThreadCwd: "twd", KindMapped: "txt", 0: "unknown"}
	for k, s := range want {
		if k.String() != s {
			t.Errorf("%d: %q, want %q", k, k.String(), s)
		}
	}
}

func TestDecodeUnixSocket(t *testing.T) {
	flags, family, kind, path, ok := decodeUnixSocket(encSocket(2, afUnix, sockInfoUn, "/data/Claude/s.sock"))
	if !ok || flags != 2 || family != afUnix || kind != sockInfoUn || path != "/data/Claude/s.sock" {
		t.Fatalf("socket: %d %d %d %q %v", flags, family, kind, path, ok)
	}
	if accessOf(flags) != AccessWrite {
		t.Fatal("socket flags are not the descriptor's access")
	}
	full := strings.Repeat("s", sockPathLen)
	b := encSocket(3, afUnix, sockInfoUn, full)
	b[offSockPath+sockPathLen] = 'X' // the byte after sun_path is not part of the path
	if _, _, _, p, _ := decodeUnixSocket(b); p != full {
		t.Fatalf("104-byte path: %q", p)
	}
	if _, _, _, _, ok := decodeUnixSocket(make([]byte, 800)); ok {
		t.Fatal("socket of 800 bytes decoded")
	}
}
