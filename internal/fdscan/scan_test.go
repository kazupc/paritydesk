package fdscan

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------- fake kernel

type fakeFD struct {
	fd     int32
	typ    uint32 // fdTypeVnode, fdTypeSocket, or other
	flags  uint32
	dev    uint32
	ino    uint64
	path   string
	family int32 // sockets
	kind   int32 // sockets
}

type fakePort struct {
	port  uint32
	typ   uint32
	flags uint32
	dev   uint32
	ino   uint64
	path  string
}

type fakeThread struct {
	h    uint64
	path string
}

type fakeRegion struct {
	addr, size uint64
	dev        uint32
	ino        uint64
	path       string
}

type fakeProc struct {
	started   time.Time
	uid, ruid uint32
	zombie    bool
	gone      bool
	cwd, root vnodePath
	fds       []fakeFD
	ports     []fakePort
	threads   []fakeThread
	regions   []fakeRegion
}

// fcall is one recorded kernel call.
type fcall struct {
	op     string // "list", "pid", "fd", "port", "kinfo"
	pid    int
	flavor int
	arg    uint64
	buf    int // len(buf); -1 for a nil buffer
}

// scope of a group denial.
type scope uint8

const (
	scopeNone scope = iota
	scopeAll
	scopeOthers
)

const fakeUID = 501

// fakeKernel is a table of processes with the kernel's answers. A denied
// or failed call returns 0, like the kernel.
type fakeKernel struct {
	mu    sync.Mutex
	self  int // the scanning process, for scopeOthers
	procs map[int]*fakeProc
	// pids, when non-nil, is what listAllPIDs reports.
	pids []int32
	// listFull makes the next buffered listAllPIDs calls report a full
	// buffer; listErr makes every call fail.
	listFull int
	listErr  bool
	myEUID   uint32
	myRUID   uint32
	// deny switches per "op:flavor" ("pid:9", "fd:2", "port:2").
	deny map[string]scope
	// The two group switches observed on the real kernel.
	denyFDOp      scope // PROC_PIDLISTFDS and every proc_pidfdinfo flavor
	denyPidinfoOp scope // every proc_pidinfo flavor except PROC_PIDLISTFDS
	kinfoErr      map[int]bool
	// sizes overrides the struct size per "op:flavor" (growth, layout).
	sizes map[string]int
	// fail makes matching calls return 0: "pid:<pid>:<flavor>",
	// "fd:<pid>:<fd>", "port:<pid>:<port>", "thread:<pid>:<handle>",
	// "region:<pid>"; the count is decremented per failure, -1 is forever.
	fail map[string]int
	// region22Bad makes flavor 22 report regions with a wrong identity.
	region22Bad bool
	// hook runs before each call is answered (inside the lock), so
	// scripted events can change the table.
	hook  func(k *fakeKernel, c fcall)
	calls []fcall
	// delay sleeps before answering a vnode path call (TestEnsureChecked).
	delay time.Duration
}

func newFake() *fakeKernel {
	return &fakeKernel{self: selfPID, procs: map[int]*fakeProc{}, myEUID: fakeUID, myRUID: fakeUID,
		deny: map[string]scope{}, kinfoErr: map[int]bool{}, sizes: map[string]int{}, fail: map[string]int{}}
}

// stdProc is a same-user process with an executable mapping, one thread
// and a working directory outside the watched folder.
func stdProc(start int64) *fakeProc {
	return &fakeProc{
		started: time.Unix(start, 0), uid: fakeUID, ruid: fakeUID,
		cwd:     vnodePath{devIno{1, 10}, "/home/u"},
		threads: []fakeThread{{h: 0x100}},
		regions: []fakeRegion{{addr: 0x1000, size: 0x1000, dev: 1, ino: 20, path: "/usr/bin/tool"}},
	}
}

func (k *fakeKernel) add(pid int, p *fakeProc) *fakeProc {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.procs[pid] = p
	return p
}

func (k *fakeKernel) record(c fcall) {
	k.calls = append(k.calls, c)
	if k.hook != nil {
		k.hook(k, c)
	}
}

func (k *fakeKernel) callsOf(op string, pid int) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	n := 0
	for _, c := range k.calls {
		if (op == "" || c.op == op) && c.pid == pid {
			n++
		}
	}
	return n
}

func (k *fakeKernel) denied(op string, flavor, pid int) bool {
	in := func(s scope) bool { return s == scopeAll || (s == scopeOthers && pid != k.self) }
	if in(k.deny[fmt.Sprintf("%s:%d", op, flavor)]) {
		return true
	}
	switch {
	case op == "fd", op == "pid" && flavor == flavorListFDs:
		return in(k.denyFDOp)
	case op == "pid":
		return in(k.denyPidinfoOp)
	}
	return false
}

func (k *fakeKernel) failing(key string) bool {
	n, ok := k.fail[key]
	if !ok || n == 0 {
		return false
	}
	if n > 0 {
		k.fail[key] = n - 1
	}
	return true
}

func (k *fakeKernel) size(op string, flavor, def int) int {
	if s, ok := k.sizes[fmt.Sprintf("%s:%d", op, flavor)]; ok {
		return s
	}
	return def
}

// fixed answers a fixed-size call: the struct's own size when the buffer
// holds it, 0 otherwise (as the kernel does).
func fixed(buf []byte, size int, enc []byte) int {
	if len(buf) < size {
		return 0
	}
	clear(buf[:size])
	copy(buf, enc)
	return size
}

// table answers a list call with 8-byte entries: an upper bound for a nil
// buffer, the entries when they fit, a full buffer when they do not.
func table(buf []byte, entries [][8]byte, bound int) int {
	if buf == nil {
		if len(entries) == 0 && bound == 0 {
			return 0
		}
		return (len(entries) + bound) * 8
	}
	n := 0
	for _, e := range entries {
		if n+8 > len(buf) {
			return len(buf) / 8 * 8
		}
		copy(buf[n:], e[:])
		n += 8
	}
	return n
}

func (k *fakeKernel) listAllPIDs(buf []int32) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.record(fcall{op: "list", buf: len(buf)})
	if k.listErr {
		return -1
	}
	pids := k.pids
	if pids == nil {
		for _, p := range slices.Sorted(maps.Keys(k.procs)) {
			pids = append(pids, int32(p))
		}
	}
	if buf == nil {
		return len(pids)
	}
	if k.listFull > 0 {
		k.listFull--
		return len(buf)
	}
	return copy(buf, pids)
}

func (k *fakeKernel) pidInfo(pid, flavor int, arg uint64, buf []byte) int {
	if flavor == flavorVnodePath && k.delay > 0 {
		time.Sleep(k.delay)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	bl := len(buf)
	if buf == nil {
		bl = -1
	}
	k.record(fcall{op: "pid", pid: pid, flavor: flavor, arg: arg, buf: bl})
	p := k.procs[pid]
	if p == nil || p.gone || k.denied("pid", flavor, pid) || k.failing(fmt.Sprintf("pid:%d:%d", pid, flavor)) {
		return 0
	}
	switch flavor {
	case flavorVnodePath:
		return fixed(buf, k.size("pid", flavor, sizeVnodePathInfo), encVnodePathInfo(p.cwd, p.root))
	case flavorListFDs:
		var es [][8]byte
		for _, f := range p.fds {
			var e [8]byte
			le.PutUint32(e[:], uint32(f.fd))
			le.PutUint32(e[4:], f.typ)
			es = append(es, e)
		}
		return table(buf, es, 4)
	case flavorListFileports:
		var es [][8]byte
		for _, f := range p.ports {
			var e [8]byte
			le.PutUint32(e[:], f.port)
			le.PutUint32(e[4:], f.typ)
			es = append(es, e)
		}
		return table(buf, es, 2)
	case flavorListThreads:
		if buf == nil {
			return 0
		}
		var es [][8]byte
		for _, th := range p.threads {
			var e [8]byte
			le.PutUint64(e[:], th.h)
			es = append(es, e)
		}
		return table(buf, es, 0)
	case flavorThreadPath:
		for _, th := range p.threads {
			if th.h == arg {
				if k.failing(fmt.Sprintf("thread:%d:%d", pid, arg)) {
					return 0
				}
				return fixed(buf, k.size("pid", flavor, sizeThreadPath), encThreadPath(th.path))
			}
		}
		return 0
	case flavorRegionPath, flavorRegionPath2:
		if arg == 0 && k.failing(fmt.Sprintf("region:%d", pid)) {
			return 0
		}
		for _, r := range p.regions {
			if r.addr+r.size <= arg && r.addr < arg {
				continue
			}
			if flavor == flavorRegionPath2 && r.path == "" {
				continue
			}
			dev, ino := r.dev, r.ino
			if flavor == flavorRegionPath2 && k.region22Bad {
				dev, ino = dev+1000, ino+1000
			}
			return fixed(buf, k.size("pid", flavor, sizeRegionPath), encRegion(r.addr, r.size, dev, ino, r.path))
		}
		return 0
	}
	return 0
}

func (k *fakeKernel) pidFDInfo(pid int, fd int32, flavor int, buf []byte) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.record(fcall{op: "fd", pid: pid, flavor: flavor, arg: uint64(uint32(fd)), buf: len(buf)})
	p := k.procs[pid]
	if p == nil || p.gone || k.denied("fd", flavor, pid) || k.failing(fmt.Sprintf("fd:%d:%d", pid, fd)) {
		return 0
	}
	for _, f := range p.fds {
		if f.fd != fd {
			continue
		}
		switch {
		case flavor == fdFlavorVnodePath && f.typ == fdTypeVnode:
			return fixed(buf, k.size("fd", flavor, sizeVnodeFDPath), encVnodeFD(f.flags, f.dev, f.ino, f.path))
		case flavor == fdFlavorSocket && f.typ == fdTypeSocket:
			return fixed(buf, k.size("fd", flavor, sizeSocketInfo), encSocket(f.flags, f.family, f.kind, f.path))
		}
		return 0
	}
	return 0
}

func (k *fakeKernel) pidFileportInfo(pid int, port uint32, flavor int, buf []byte) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.record(fcall{op: "port", pid: pid, flavor: flavor, arg: uint64(port), buf: len(buf)})
	p := k.procs[pid]
	if p == nil || p.gone || k.denied("port", flavor, pid) || k.failing(fmt.Sprintf("port:%d:%d", pid, port)) {
		return 0
	}
	for _, f := range p.ports {
		if f.port == port && f.typ == fdTypeVnode && flavor == fileportFlavorVnode {
			return fixed(buf, k.size("port", flavor, sizeVnodeFDPath), encVnodeFD(f.flags, f.dev, f.ino, f.path))
		}
	}
	return 0
}

func (k *fakeKernel) kinfo(pid int) (kinfo, kstate) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.record(fcall{op: "kinfo", pid: pid})
	if k.kinfoErr[pid] {
		return kinfo{}, kErr
	}
	p := k.procs[pid]
	if p == nil || p.gone {
		return kinfo{}, kGone
	}
	return kinfo{Started: p.started, UID: p.uid, RUID: p.ruid, Zombie: p.zombie}, kOK
}

func (k *fakeKernel) euid() uint32 { return k.myEUID }
func (k *fakeKernel) ruid() uint32 { return k.myRUID }

// ---------------------------------------------------------------- helpers

const watched = "/data/Claude"

func matchWatched(p string) bool { return p == watched || strings.HasPrefix(p, watched+"/") }

func scan(t *testing.T, k *fakeKernel, opt Options) (Result, error) {
	t.Helper()
	if opt.Match == nil {
		opt.Match = matchWatched
	}
	return scanPIDs(t.Context(), k, opt, flavorRegionPath2)
}

// rec is a record without its start time, for comparisons.
type rec struct {
	PID    int
	Kind   Kind
	FD     int32
	Access Access
	Path   string
}

func recs(rs []Record) []rec {
	out := make([]rec, len(rs))
	for i, r := range rs {
		out[i] = rec{r.PID, r.Kind, r.FD, r.Access, r.Path}
	}
	return out
}

// richProc holds the watched folder in every way.
func richProc(start int64) *fakeProc {
	p := stdProc(start)
	p.cwd = vnodePath{devIno{1, 30}, watched}
	p.root = vnodePath{devIno{1, 31}, watched + "/jail"}
	p.fds = []fakeFD{
		{fd: 3, typ: fdTypeVnode, flags: 3, dev: 1, ino: 40, path: watched + "/rw"},
		{fd: 4, typ: fdTypeVnode, flags: 1, dev: 1, ino: 41, path: watched + "/ro"},
		{fd: 5, typ: fdTypeVnode, flags: 2, dev: 1, ino: 42, path: watched + "/wo"},
		{fd: 6, typ: fdTypeVnode, flags: 1, dev: 1, ino: 43, path: "/elsewhere/file"},
		{fd: 7, typ: fdTypeSocket, flags: 3, family: afUnix, kind: sockInfoUn, path: watched + "/s.sock"},
		{fd: 8, typ: fdTypeSocket, flags: 3, family: 2, kind: 2, path: ""},
		{fd: 9, typ: 5}, // a pipe: no call
	}
	p.ports = []fakePort{{port: 0x1303, typ: fdTypeVnode, flags: 2, dev: 1, ino: 44, path: watched + "/port"}}
	p.threads = []fakeThread{{h: 0x100}, {h: 0x101, path: watched + "/twd"}}
	p.regions = []fakeRegion{
		{addr: 0x1000, size: 0x1000, dev: 1, ino: 45, path: watched + "/bin/claude"},
		{addr: 0x2000, size: 0x1000, dev: 1, ino: 45, path: watched + "/bin/claude"},
		{addr: 0x3000, size: 0x1000},
		{addr: 0x4000, size: 0x1000, dev: 1, ino: 46, path: watched + "/map.bin"},
	}
	return p
}

func wantRich(pid int) []rec {
	return []rec{
		{pid, KindFD, 3, AccessReadWrite, watched + "/rw"},
		{pid, KindFD, 4, AccessRead, watched + "/ro"},
		{pid, KindFD, 5, AccessWrite, watched + "/wo"},
		{pid, KindSocket, 7, AccessReadWrite, watched + "/s.sock"},
		{pid, KindFileport, -1, AccessWrite, watched + "/port"},
		{pid, KindCwd, -1, AccessNone, watched},
		{pid, KindRoot, -1, AccessNone, watched + "/jail"},
		{pid, KindThreadCwd, -1, AccessNone, watched + "/twd"},
		{pid, KindMapped, -1, AccessNone, watched + "/bin/claude"},
		{pid, KindMapped, -1, AccessNone, watched + "/map.bin"},
	}
}

// ---------------------------------------------------------------- tests

func TestScanFindsEveryKind(t *testing.T) {
	k := newFake()
	k.add(20, richProc(200))
	k.add(10, richProc(100))
	k.add(30, stdProc(300))
	k.add(40, richProc(400))
	res, err := scan(t, k, Options{SkipPID: 40})
	if err != nil {
		t.Fatal(err)
	}
	want := append(wantRich(10), wantRich(20)...)
	if got := recs(res.Records); !slices.Equal(got, want) {
		t.Fatalf("records\n%+v\nwant\n%+v", got, want)
	}
	for _, r := range res.Records {
		if (r.PID == 10 && !r.Started.Equal(time.Unix(100, 0))) || (r.PID == 20 && !r.Started.Equal(time.Unix(200, 0))) {
			t.Fatalf("start time not stamped: %+v", r)
		}
	}
	if res.Stats.Scanned != 3 || res.Stats.Listed != 3 || res.Stats.Partial != 0 || res.Stats.Unreadable != 0 {
		t.Fatalf("stats %+v", res.Stats)
	}
	if k.callsOf("", 40) != 0 {
		t.Fatal("SkipPID was read")
	}
	// The pipe got no call.
	if n := countCalls(k, func(c fcall) bool { return c.op == "fd" && c.arg == 9 }); n != 0 {
		t.Fatalf("pipe descriptor: %d calls", n)
	}
	// Match filters.
	res, err = scan(t, k, Options{Match: func(p string) bool { return p == watched+"/ro" }})
	if err != nil || len(res.Records) != 3 {
		t.Fatalf("filtered: %+v %v", res.Records, err)
	}
}

func countCalls(k *fakeKernel, f func(fcall) bool) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	n := 0
	for _, c := range k.calls {
		if f(c) {
			n++
		}
	}
	return n
}

func TestScanPIDListGrowth(t *testing.T) {
	k := newFake()
	k.add(10, richProc(1))
	k.listFull = 2
	res, err := scan(t, k, Options{})
	if err != nil || len(res.Records) == 0 {
		t.Fatalf("growth: %v", err)
	}
	if n := countCalls(k, func(c fcall) bool { return c.op == "list" }); n != 4 {
		t.Fatalf("%d list calls, want 4 (sizing, full, full, fit)", n)
	}

	k = newFake()
	k.add(10, richProc(1))
	k.listFull = 100
	if _, err := scan(t, k, Options{}); !errors.Is(err, ErrListPIDs) {
		t.Fatalf("always full: %v", err)
	}
	if n := countCalls(k, func(c fcall) bool { return c.op == "list" }); n != maxPIDListCalls {
		t.Fatalf("%d list calls, want %d", n, maxPIDListCalls)
	}

	k = newFake()
	k.listErr = true
	if _, err := scan(t, k, Options{}); !errors.Is(err, ErrListPIDs) {
		t.Fatalf("-1: %v", err)
	}

	// Explicit pids are deduplicated, non-positive ones dropped, and the
	// pid list is not read.
	k = newFake()
	k.add(10, richProc(1))
	res, err = scan(t, k, Options{PIDs: []int{10, 10, 0, -3}})
	if err != nil || res.Stats.Listed != 1 || countCalls(k, func(c fcall) bool { return c.op == "list" }) != 0 {
		t.Fatalf("explicit pids: %+v %v", res.Stats, err)
	}
}

func TestScanDescriptorTableGrowth(t *testing.T) {
	k := newFake()
	p := k.add(10, richProc(1))
	grown := false
	k.hook = func(k *fakeKernel, c fcall) {
		// Between the sizing call and the read, 100 descriptors appear.
		if c.op == "pid" && c.flavor == flavorListFDs && c.buf > 0 && !grown {
			grown = true
			for i := range 100 {
				p.fds = append(p.fds, fakeFD{fd: int32(100 + i), typ: fdTypeVnode, flags: 1, dev: 2, ino: uint64(i), path: "/other/f"})
			}
		}
	}
	res, err := scan(t, k, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := recs(res.Records); !slices.Equal(got, wantRich(10)) {
		t.Fatalf("records %+v", got)
	}
	if n := countCalls(k, func(c fcall) bool { return c.op == "fd" && c.arg >= 100 }); n != 100 {
		t.Fatalf("grown descriptors read %d times", n)
	}

	k = newFake()
	p = k.add(10, richProc(1))
	for i := range maxEntries {
		p.fds = append(p.fds, fakeFD{fd: int32(100 + i), typ: 9})
	}
	if _, err := scan(t, k, Options{}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("huge table: %v", err)
	}
}

func TestScanThreadListGrowth(t *testing.T) {
	k := newFake()
	p := k.add(10, richProc(1))
	for i := range 300 {
		p.threads = append(p.threads, fakeThread{h: uint64(0x1000 + i)})
	}
	p.threads = append(p.threads, fakeThread{h: 0x9999, path: watched + "/late"})
	res, err := scan(t, k, Options{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range res.Records {
		found = found || (r.Kind == KindThreadCwd && r.Path == watched+"/late")
	}
	if !found {
		t.Fatal("thread beyond the first 64 entries not read")
	}
	var sizes []int
	for _, c := range k.calls {
		if c.op == "pid" && c.flavor == flavorListThreads {
			sizes = append(sizes, c.buf/8)
		}
	}
	if !slices.Equal(sizes, []int{64, 256, 1024}) {
		t.Fatalf("thread list buffers %v", sizes)
	}
}

func TestScanClosedDescriptor(t *testing.T) {
	// 5: closed between list and info; the re-list no longer has it.
	k := newFake()
	p := k.add(10, richProc(1))
	k.hook = func(k *fakeKernel, c fcall) {
		if c.op == "fd" && c.arg == 3 {
			p.fds = slices.DeleteFunc(p.fds, func(f fakeFD) bool { return f.fd == 3 })
		}
	}
	res, err := scan(t, k, Options{})
	if err != nil || res.Stats.Scanned != 1 || res.Stats.Partial != 0 {
		t.Fatalf("closed fd: %+v %v", res.Stats, err)
	}
	if slices.Contains(recs(res.Records), rec{10, KindFD, 3, AccessReadWrite, watched + "/rw"}) {
		t.Fatal("closed descriptor reported")
	}

	// 5a: still listed, 0 on every try -> Partial.
	k = newFake()
	k.add(10, richProc(1))
	k.fail["fd:10:3"] = -1
	res, err = scan(t, k, Options{})
	if !errors.Is(err, ErrPartial) || res.Stats.Partial != 1 || res.Records != nil || res.Stats.Listed != 1 {
		t.Fatalf("persistent zero: %+v %v", res, err)
	}

	// 5b: 0 once, then the retry after the re-list works.
	k = newFake()
	k.add(10, richProc(1))
	k.fail["fd:10:3"] = 1
	res, err = scan(t, k, Options{})
	if err != nil || !slices.Contains(recs(res.Records), rec{10, KindFD, 3, AccessReadWrite, watched + "/rw"}) {
		t.Fatalf("transient zero: %+v %v", recs(res.Records), err)
	}
}

func TestScanClosedFileportAndThread(t *testing.T) {
	// 5c: the same for a fileport and a thread handle.
	for _, tc := range []struct {
		name  string
		fail  string
		close func(p *fakeProc)
		op    string
	}{
		{"fileport", "port:10:4867", func(p *fakeProc) { p.ports = nil }, "port"},
		{"thread", "thread:10:257", func(p *fakeProc) { p.threads = p.threads[:1] }, "thread"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := newFake()
			p := k.add(10, richProc(1))
			k.fail[tc.fail] = 1
			closed := false
			k.hook = func(k *fakeKernel, c fcall) {
				if !closed && (c.op == "port" || (c.op == "pid" && c.flavor == flavorThreadPath && c.arg == 0x101)) && strings.HasPrefix(tc.fail, opName(c)) {
					closed = true
					tc.close(p)
				}
			}
			res, err := scan(t, k, Options{})
			if err != nil || res.Stats.Scanned != 1 {
				t.Fatalf("closed: %+v %v", res.Stats, err)
			}

			k = newFake()
			k.add(10, richProc(1))
			k.fail[tc.fail] = -1
			res, err = scan(t, k, Options{})
			if !errors.Is(err, ErrPartial) || res.Stats.Partial != 1 {
				t.Fatalf("persistent: %+v %v", res.Stats, err)
			}

			k = newFake()
			k.add(10, richProc(1))
			k.fail[tc.fail] = 1
			res, err = scan(t, k, Options{})
			if err != nil || len(res.Records) != len(wantRich(10)) {
				t.Fatalf("transient: %+v %v", recs(res.Records), err)
			}
		})
	}
}

func opName(c fcall) string {
	if c.op == "port" {
		return "port"
	}
	return "thread"
}

func TestScanOtherUsers(t *testing.T) {
	// 6: both uids differ -> Denied without a libproc call.
	k := newFake()
	p := k.add(10, richProc(1))
	p.uid, p.ruid = 0, 0
	res, err := scan(t, k, Options{})
	if err != nil || res.Stats.Denied != 1 || len(res.Records) != 0 {
		t.Fatalf("other user: %+v %v", res, err)
	}
	if n := countCalls(k, func(c fcall) bool { return c.pid == 10 && c.op != "kinfo" }); n != 0 {
		t.Fatalf("%d libproc calls for another user's process", n)
	}

	// A different euid but our ruid is tried; a 0 at step 2 is then
	// Denied (one of the two same-user rules explains it).
	k = newFake()
	p = k.add(10, richProc(1))
	p.uid = 0
	k.fail["pid:10:9"] = -1
	res, err = scan(t, k, Options{})
	if err != nil || res.Stats.Denied != 1 || res.Stats.Unreadable != 0 {
		t.Fatalf("setuid process: %+v %v", res.Stats, err)
	}
	if k.callsOf("pid", 10) == 0 {
		t.Fatal("setuid process was not tried")
	}

	// As root nothing is skipped early.
	k = newFake()
	k.myEUID, k.myRUID = 0, 0
	p = k.add(10, richProc(1))
	p.uid, p.ruid = 7, 7
	res, err = scan(t, k, Options{})
	if err != nil || res.Stats.Scanned != 1 || len(res.Records) == 0 {
		t.Fatalf("root: %+v %v", res.Stats, err)
	}
}

func TestScanUnreadable(t *testing.T) {
	// 7: own user, step 2 returns 0 on both attempts.
	k := newFake()
	k.add(10, richProc(1))
	k.fail["pid:10:9"] = -1
	res, err := scan(t, k, Options{})
	if !errors.Is(err, ErrPartial) || res.Stats.Unreadable != 1 || res.Records != nil {
		t.Fatalf("unreadable: %+v %v", res, err)
	}
	// Once only: the retry works.
	k = newFake()
	k.add(10, richProc(1))
	k.fail["pid:10:9"] = 1
	res, err = scan(t, k, Options{})
	if err != nil || len(res.Records) != len(wantRich(10)) {
		t.Fatalf("transient: %+v %v", res.Stats, err)
	}
}

func TestScanExited(t *testing.T) {
	// 8: a zombie before the scan.
	k := newFake()
	p := k.add(10, richProc(1))
	p.zombie = true
	res, err := scan(t, k, Options{})
	if err != nil || res.Stats.Exited != 1 || len(res.Records) != 0 || k.callsOf("pid", 10) != 0 {
		t.Fatalf("zombie: %+v %v", res.Stats, err)
	}
	// A process that exits after a match: its records are dropped.
	k = newFake()
	p = k.add(10, richProc(1))
	k.hook = func(k *fakeKernel, c fcall) {
		if c.op == "pid" && c.flavor == flavorRegionPath2 {
			p.gone = true
		}
	}
	res, err = scan(t, k, Options{})
	if err != nil || res.Stats.Exited != 1 || len(res.Records) != 0 {
		t.Fatalf("exit during scan: %+v %+v %v", res.Stats, res.Records, err)
	}
	// A pid gone at step 2.
	k = newFake()
	p = k.add(10, richProc(1))
	k.hook = func(k *fakeKernel, c fcall) {
		if c.op == "pid" && c.flavor == flavorVnodePath {
			p.gone = true
		}
	}
	res, err = scan(t, k, Options{})
	if err != nil || res.Stats.Exited != 1 {
		t.Fatalf("gone at step 2: %+v %v", res.Stats, err)
	}
}

func TestScanKinfoError(t *testing.T) {
	// 8a: a failed lookup is never "exited".
	k := newFake()
	k.add(10, richProc(1))
	k.add(11, richProc(1))
	k.kinfoErr[11] = true
	res, err := scan(t, k, Options{})
	if !errors.Is(err, ErrProcInfo) || res.Stats.Exited != 0 || res.Records != nil {
		t.Fatalf("kinfo error: %+v %v", res.Stats, err)
	}
	// Also when it fails at re-validation only.
	k = newFake()
	k.add(10, richProc(1))
	calls := 0
	k.hook = func(k *fakeKernel, c fcall) {
		if c.op == "kinfo" {
			calls++
			if calls == 2 {
				k.kinfoErr[10] = true
			}
		}
	}
	if _, err := scan(t, k, Options{}); !errors.Is(err, ErrProcInfo) {
		t.Fatalf("re-validation kinfo error: %v", err)
	}
}

func TestScanWorkerErrorNotMaskedByCancel(t *testing.T) {
	// A worker that fails the whole scan cancels the others; one of them
	// then sees only the cancellation. The real error must come back every
	// time, never a bare context error while the caller's ctx is live
	// (callers read that as their own cancellation and skip the lsof
	// fallback). The race is scheduling-dependent, so it is run many times.
	const runs = 300
	pids := make([]int, 0, 60)
	for pid := 100; pid < 160; pid++ {
		pids = append(pids, pid)
	}
	for i := range runs {
		k := newFake()
		for _, pid := range pids {
			k.add(pid, richProc(1))
		}
		k.kinfoErr[105] = true
		_, err := scanPIDs(t.Context(), k, Options{PIDs: pids, Workers: 8, Match: matchWatched}, flavorRegionPath2)
		if !errors.Is(err, ErrProcInfo) {
			t.Fatalf("run %d: %v, want ErrProcInfo", i, err)
		}
		if errors.Is(err, context.Canceled) {
			t.Fatalf("run %d: the internal cancellation leaked: %v", i, err)
		}
	}

	// A caller whose ctx is cancelled mid-scan still gets its own error.
	k := newFake()
	for _, pid := range pids {
		k.add(pid, richProc(1))
	}
	ctx, cancel := context.WithCancel(t.Context())
	n := 0
	k.hook = func(_ *fakeKernel, c fcall) {
		if c.op == "kinfo" {
			if n++; n == 10 {
				cancel()
			}
		}
	}
	if _, err := scanPIDs(ctx, k, Options{PIDs: pids, Workers: 8, Match: matchWatched}, flavorRegionPath2); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled caller: %v", err)
	}
}

func TestScanPIDReuse(t *testing.T) {
	// 9: the pid is reused during the scan.
	k := newFake()
	p := k.add(10, richProc(1))
	reused := false
	k.hook = func(k *fakeKernel, c fcall) {
		if !reused && c.op == "pid" && c.flavor == flavorRegionPath2 {
			reused = true
			np := stdProc(2)
			np.fds = []fakeFD{{fd: 3, typ: fdTypeVnode, flags: 1, dev: 1, ino: 50, path: watched + "/new"}}
			*p = *np
		}
	}
	res, err := scan(t, k, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := recs(res.Records); !slices.Equal(got, []rec{{10, KindFD, 3, AccessRead, watched + "/new"}}) {
		t.Fatalf("records %+v", got)
	}
	if res.Stats.Reused != 1 || !res.Records[0].Started.Equal(time.Unix(2, 0)) {
		t.Fatalf("stats %+v, started %v", res.Stats, res.Records[0].Started)
	}

	// Churn on every attempt: 3 attempts, the last records kept with the
	// latest start.
	k = newFake()
	p = k.add(10, richProc(1))
	gen := int64(1)
	k.hook = func(k *fakeKernel, c fcall) {
		if c.op == "pid" && c.flavor == flavorRegionPath2 && c.arg == 0 {
			gen++
			p.started = time.Unix(gen, 0)
		}
	}
	res, err = scan(t, k, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if n := countCalls(k, func(c fcall) bool { return c.op == "pid" && c.flavor == flavorVnodePath }); n != maxReuseAttempts {
		t.Fatalf("%d attempts, want %d", n, maxReuseAttempts)
	}
	if len(res.Records) != len(wantRich(10)) || !res.Records[0].Started.Equal(time.Unix(gen, 0)) {
		t.Fatalf("churn records: %d, started %v", len(res.Records), res.Records[0].Started)
	}
}

func TestScanNoDescriptors(t *testing.T) {
	// 10: a process with no descriptors and no fileports.
	k := newFake()
	k.add(10, stdProc(1))
	res, err := scan(t, k, Options{})
	if err != nil || res.Stats.Scanned != 1 || res.Stats.Unreadable+res.Stats.Partial != 0 {
		t.Fatalf("no descriptors: %+v %v", res.Stats, err)
	}
	if k.callsOf("kinfo", 10) != 2 {
		t.Fatal("an empty list did not re-validate the process")
	}
}

func TestScanGaps(t *testing.T) {
	// 10a: an empty thread list, or a first region call returning 0, on
	// both attempts.
	k := newFake()
	p := k.add(10, richProc(1))
	p.threads = nil
	res, err := scan(t, k, Options{})
	if !errors.Is(err, ErrPartial) || res.Stats.Partial != 1 {
		t.Fatalf("no threads: %+v %v", res.Stats, err)
	}
	k = newFake()
	k.add(10, richProc(1))
	k.fail["region:10"] = -1
	res, err = scan(t, k, Options{})
	if !errors.Is(err, ErrPartial) || res.Stats.Partial != 1 {
		t.Fatalf("no regions: %+v %v", res.Stats, err)
	}
	// Once only: the second attempt is complete.
	k = newFake()
	k.add(10, richProc(1))
	k.fail["region:10"] = 1
	res, err = scan(t, k, Options{})
	if err != nil || len(res.Records) != len(wantRich(10)) {
		t.Fatalf("transient gap: %+v %v", res.Stats, err)
	}
}

func TestScanRegionWalk(t *testing.T) {
	k := newFake()
	p := k.add(10, stdProc(1))
	p.regions = []fakeRegion{
		{addr: 0x1000, size: 0x1000, dev: 1, ino: 5, path: watched + "/a"},
		{addr: 0x2000, size: 0x1000, dev: 1, ino: 5, path: watched + "/a"}, // duplicate (dev, ino)
		{addr: 0x3000, size: 0, dev: 1, ino: 6, path: watched + "/b"},      // no progress: the walk stops
		{addr: 0x4000, size: 0x1000, dev: 1, ino: 7, path: watched + "/c"},
	}
	res, err := scan(t, k, Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := []rec{{10, KindMapped, -1, AccessNone, watched + "/a"}, {10, KindMapped, -1, AccessNone, watched + "/b"}}
	if got := recs(res.Records); !slices.Equal(got, want) {
		t.Fatalf("walk %+v", got)
	}

	// The iteration cap stops an endless walk.
	old := maxRegions
	maxRegions = 50
	t.Cleanup(func() { maxRegions = old })
	k = newFake()
	p = k.add(10, stdProc(1))
	p.regions = nil
	for i := range 200 {
		p.regions = append(p.regions, fakeRegion{addr: uint64(0x1000 * (i + 1)), size: 0x1000, dev: 1, ino: uint64(i), path: "/x"})
	}
	if _, err := scan(t, k, Options{}); err != nil {
		t.Fatal(err)
	}
	if n := countCalls(k, func(c fcall) bool { return c.op == "pid" && c.flavor == flavorRegionPath2 }); n != 50 {
		t.Fatalf("%d region calls, want the cap 50", n)
	}

	// Flavor 8 reports path-less regions too; they are skipped.
	k = newFake()
	p = k.add(10, stdProc(1))
	p.regions = []fakeRegion{{addr: 0x1000, size: 0x1000}, {addr: 0x2000, size: 0x1000, dev: 1, ino: 9, path: watched + "/m"}}
	res, err = scanPIDs(t.Context(), k, Options{Match: matchWatched}, flavorRegionPath)
	if err != nil || !slices.Equal(recs(res.Records), []rec{{10, KindMapped, -1, AccessNone, watched + "/m"}}) {
		t.Fatalf("flavor 8: %+v %v", recs(res.Records), err)
	}
}

func TestScanLayout(t *testing.T) {
	// 12: a positive but wrong byte count from any fixed-size call.
	for key, size := range map[string]int{"pid:9": 2400, "pid:10": 1300, "pid:22": 1300, "fd:2": 1300, "fd:3": 800, "port:2": 1300} {
		k := newFake()
		k.add(10, richProc(1))
		k.sizes[key] = size // a grown struct: never read as "closed"
		if _, err := scan(t, k, Options{}); !errors.Is(err, ErrLayout) {
			t.Errorf("%s: %v", key, err)
		}
	}
	// Every fixed-size call received the 4096-byte buffer.
	k := newFake()
	k.add(10, richProc(1))
	if _, err := scan(t, k, Options{}); err != nil {
		t.Fatal(err)
	}
	for _, c := range k.calls {
		fixedCall := c.op == "fd" || c.op == "port" ||
			(c.op == "pid" && (c.flavor == flavorVnodePath || c.flavor == flavorThreadPath || c.flavor == flavorRegionPath2))
		if fixedCall && c.buf != infoBufSize {
			t.Fatalf("%+v: buffer %d, want %d", c, c.buf, infoBufSize)
		}
	}
}

func TestScanCancelled(t *testing.T) {
	k := newFake()
	k.add(10, richProc(1))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	res, err := scanPIDs(ctx, k, Options{Match: matchWatched}, flavorRegionPath2)
	if !errors.Is(err, context.Canceled) || res.Records != nil {
		t.Fatalf("cancelled: %v", err)
	}
	// Cancelled during the scan.
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	k = newFake()
	for pid := 10; pid < 30; pid++ {
		k.add(pid, richProc(1))
	}
	k.hook = func(k *fakeKernel, c fcall) {
		if c.pid == 12 {
			cancel()
		}
	}
	res, err = scanPIDs(ctx, k, Options{Match: matchWatched, Workers: 1}, flavorRegionPath2)
	if !errors.Is(err, context.Canceled) || res.Records != nil {
		t.Fatalf("cancelled during the scan: %v %d records", err, len(res.Records))
	}
}

func TestScanNoMatch(t *testing.T) {
	if _, err := scanPIDs(t.Context(), newFake(), Options{}, flavorRegionPath2); !errors.Is(err, ErrNoMatch) {
		t.Fatal(err)
	}
}

func TestScanWorkersAgree(t *testing.T) {
	build := func() *fakeKernel {
		k := newFake()
		for pid := 10; pid < 60; pid++ {
			if pid%3 == 0 {
				k.add(pid, stdProc(int64(pid)))
			} else {
				k.add(pid, richProc(int64(pid)))
			}
		}
		return k
	}
	one, err := scan(t, build(), Options{Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	eight, err := scan(t, build(), Options{Workers: 8})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(one.Records, eight.Records) {
		t.Fatal("records differ between 1 and 8 workers")
	}
	one.Stats.Elapsed, eight.Stats.Elapsed = 0, 0
	if one.Stats != eight.Stats {
		t.Fatalf("stats differ:\n%+v\n%+v", one.Stats, eight.Stats)
	}
}

func TestScanSystemicBlindness(t *testing.T) {
	five := func() *fakeKernel {
		k := newFake()
		for pid := 10; pid < 15; pid++ {
			k.add(pid, richProc(1))
		}
		return k
	}
	// pidfdinfo denied for all of them while pidinfo works.
	k := five()
	k.deny["fd:2"] = scopeAll
	res, err := scan(t, k, Options{})
	if !errors.Is(err, ErrBlind) || res.Stats.Tried[classFDInfo] != 5 || res.Stats.OK[classFDInfo] != 0 || res.Records != nil {
		t.Fatalf("fd info denied: %+v %v", res.Stats, err)
	}
	// Step 2 denied for all others.
	k = five()
	k.deny["pid:9"] = scopeOthers
	if _, err := scan(t, k, Options{}); !errors.Is(err, ErrBlind) {
		t.Fatalf("vnode path denied: %v", err)
	}
	// Two processes only: below blindMinProcs, ErrPartial instead.
	k = newFake()
	k.add(10, richProc(1))
	k.add(11, richProc(1))
	k.deny["fd:2"] = scopeAll
	if _, err := scan(t, k, Options{}); !errors.Is(err, ErrPartial) || errors.Is(err, ErrBlind) {
		t.Fatalf("two processes: %v", err)
	}
	// The own pid denied alone never counts toward blindness.
	k = newFake()
	k.add(selfPID, richProc(1))
	k.add(10, richProc(1))
	k.add(11, richProc(1))
	k.add(12, richProc(1))
	k.fail[fmt.Sprintf("pid:%d:9", selfPID)] = -1
	res, err = scan(t, k, Options{})
	if errors.Is(err, ErrBlind) || !errors.Is(err, ErrPartial) || res.Stats.Tried[classVnodePath] != 3 {
		t.Fatalf("own pid: %+v %v", res.Stats, err)
	}
}
