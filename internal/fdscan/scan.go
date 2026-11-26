package fdscan

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"
)

// kinfo is what kern.proc.pid says about a pid.
type kinfo struct {
	Started time.Time // p_starttime
	UID     uint32    // e_ucred.cr_uid (effective)
	RUID    uint32    // e_pcred.p_ruid (real)
	Zombie  bool      // p_stat == SZOMB (5)
}

// kstate is the outcome of a kern.proc.pid lookup.
type kstate uint8

const (
	kOK   kstate = iota
	kGone        // no such pid: the process exited
	kErr         // the lookup itself failed: ErrProcInfo for the whole scan
)

// kernel is the process information interface the scan reads. sysKernel
// (darwin) implements it with libproc and sysctl; tests use a fake.
type kernel interface {
	listAllPIDs(buf []int32) int                                      // count, -1 on error
	pidInfo(pid, flavor int, arg uint64, buf []byte) int              // bytes, 0 on error
	pidFDInfo(pid int, fd int32, flavor int, buf []byte) int          // bytes, 0 on error
	pidFileportInfo(pid int, port uint32, flavor int, buf []byte) int // bytes, 0 on error
	kinfo(pid int) (kinfo, kstate)
	euid() uint32
	ruid() uint32
}

// Scan limits.
const (
	// infoBufSize is the buffer of every fixed-size call: at least the
	// largest struct (2352) plus 1024, so a grown struct returns its new,
	// different size instead of a silent 0.
	infoBufSize = 4096
	// maxPIDListCalls bounds proc_listallpids calls, sizing included.
	maxPIDListCalls = 4
	// maxListTries bounds the reads of one descriptor or fileport table.
	maxListTries = 4
	// maxEntries bounds a descriptor or fileport table (8 MiB).
	maxEntries = 1 << 20
	// firstThreads and maxThreads bound the thread list.
	firstThreads = 64
	maxThreads   = 1 << 16
	// regionCtxEvery is how often the region walk checks the context.
	regionCtxEvery = 4096
	// maxReuseAttempts bounds the scans of one pid whose start time keeps
	// changing.
	maxReuseAttempts = 3
	// blindMinProcs is the number of same-user processes a call class
	// must reach before "no success at all" is read as systemic
	// blindness.
	blindMinProcs = 3
)

// maxRegions bounds one process's region walk (a variable so tests can
// reach it).
var maxRegions = 1 << 20

// selfPID is the calling process; its tables never count toward the
// blindness counters.
var selfPID = os.Getpid()

// scanError is a whole-scan failure with a reason that names no path.
type scanError struct {
	err  error
	what string
}

func (e *scanError) Error() string { return e.err.Error() + ": " + e.what }
func (e *scanError) Unwrap() error { return e.err }

func layoutErr(format string, args ...any) error {
	return &scanError{err: ErrLayout, what: fmt.Sprintf(format, args...)}
}

// scanPIDs is the scan with a fixed region flavor. Scan calls it after
// the self-check; selfCheck calls it directly.
func scanPIDs(ctx context.Context, k kernel, opt Options, regionFlavor int) (Result, error) {
	if opt.Match == nil {
		return Result{}, ErrNoMatch
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	start := time.Now()
	stats := Stats{RegionFlavor: regionFlavor}
	pids, calls, err := listPIDs(k, opt)
	stats.Calls += calls
	if err != nil {
		stats.Elapsed = time.Since(start)
		return Result{Stats: stats}, err
	}
	stats.Listed = len(pids)

	workers := opt.Workers
	if workers <= 0 {
		workers = min(8, runtime.GOMAXPROCS(0))
	}
	workers = max(1, min(workers, len(pids)))

	// A worker that fails the whole scan cancels wctx so the others stop.
	// The others then see wctx's cancellation and report a context error
	// that only echoes the real failure, so firstErr keeps the first error
	// that is not a context error, and a context error stands only when
	// the caller's ctx itself is done (checked after wg.Wait).
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		mu       sync.Mutex
		firstErr error // first worker error that is not a context error
		ctxErr   error // first context error a worker saw
		records  []Record
		wg       sync.WaitGroup
	)
	ch := make(chan int)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := newWorker(k, opt.Match, regionFlavor)
			var werr error
			for pid := range ch {
				if werr != nil {
					continue // drain
				}
				if err := wctx.Err(); err != nil {
					werr = err
					continue
				}
				if err := w.scanPID(wctx, pid); err != nil {
					werr = err
					cancel()
				}
			}
			mu.Lock()
			defer mu.Unlock()
			stats.add(w.stats)
			records = append(records, w.records...)
			switch {
			case werr == nil:
			case isContextErr(werr):
				if ctxErr == nil {
					ctxErr = werr
				}
			case firstErr == nil:
				firstErr = werr
			}
		}()
	}
	for _, pid := range pids {
		if wctx.Err() != nil {
			break
		}
		ch <- pid
	}
	close(ch)
	wg.Wait()
	stats.Elapsed = time.Since(start)

	// A cancelled caller context wins over any error it caused.
	if err := ctx.Err(); err != nil {
		return Result{Stats: stats}, err
	}
	if firstErr != nil {
		return Result{Stats: stats}, firstErr
	}
	if ctxErr != nil {
		// Only a worker failure cancels wctx while ctx is live, and that
		// failure is in firstErr; this is unreachable, but a bare context
		// error must never stand for a scan failure (callers read it as
		// their own cancellation), so it is reported as a layout problem.
		return Result{Stats: stats}, &scanError{err: ErrLayout, what: "scan cancelled without a cause: " + ctxErr.Error()}
	}
	for c := range numClasses {
		if stats.Tried[c] >= blindMinProcs && stats.OK[c] == 0 {
			return Result{Stats: stats}, &scanError{err: ErrBlind, what: "no process answered " + className(c)}
		}
	}
	if stats.Unreadable+stats.Partial > 0 {
		return Result{Stats: stats}, ErrPartial
	}
	sortRecords(records)
	return Result{Records: records, Stats: stats}, nil
}

// className names a call class for error messages.
func className(c int) string {
	switch c {
	case classVnodePath:
		return "vnode-path"
	case classFDList:
		return "fd-list"
	case classFDInfo:
		return "fd-info"
	case classThreadList:
		return "thread-list"
	case classThreadPath:
		return "thread-path"
	case classRegion:
		return "region"
	}
	return "unknown"
}

// listPIDs returns the pids to scan (> 0, without SkipPID, deduplicated)
// and the number of libproc calls made.
func listPIDs(k kernel, opt Options) ([]int, int, error) {
	src := opt.PIDs
	calls := 0
	if src == nil {
		n0 := k.listAllPIDs(nil)
		calls++
		if n0 < 0 {
			return nil, calls, ErrListPIDs
		}
		size := n0 + n0/4 + 64
		for {
			if calls >= maxPIDListCalls {
				return nil, calls, ErrListPIDs
			}
			buf := make([]int32, size)
			n := k.listAllPIDs(buf)
			calls++
			if n < 0 {
				return nil, calls, ErrListPIDs
			}
			if n < len(buf) {
				src = make([]int, n)
				for i, p := range buf[:n] {
					src[i] = int(p)
				}
				break
			}
			size *= 2
		}
	}
	seen := make(map[int]bool, len(src))
	out := make([]int, 0, len(src))
	for _, p := range src {
		if p <= 0 || p == opt.SkipPID || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out, calls, nil
}

// worker owns one goroutine's buffers (heap-allocated, reused) and its
// results.
type worker struct {
	k       kernel
	match   func(string) bool
	flavor  int
	info    []byte // infoBufSize bytes, every fixed-size call
	list    []byte // descriptor and fileport tables
	threads []byte // thread handles
	stats   Stats
	records []Record
}

func newWorker(k kernel, match func(string) bool, flavor int) *worker {
	n := infoBufSize
	return &worker{k: k, match: match, flavor: flavor, info: make([]byte, n)}
}

// end is how one attempt at a pid ended before re-validation.
type end uint8

const (
	endDone      end = iota // every step ran
	endExited               // gone or zombie at step 1
	endDenied               // another user's process (early skip)
	endStep2Zero            // PROC_PIDVNODEPATHINFO returned 0
)

// attempt is one pass over a pid's tables.
type attempt struct {
	end      end
	before   kinfo
	recs     []Record
	gap      bool // a confirmed gap: the pid was not read completely
	listZero bool // a list call returned 0 while step 2 had succeeded
	tried    [numClasses]bool
	ok       [numClasses]bool
}

// scanPID scans one pid to exactly one outcome. A non-nil error ends the
// whole scan.
func (w *worker) scanPID(ctx context.Context, pid int) error {
	reuses, step2Retried, gapRetried := 0, false, false
	for {
		a, err := w.attempt(ctx, pid)
		if err != nil {
			return err
		}
		switch a.end {
		case endExited:
			w.stats.Exited++
			return nil
		case endDenied:
			w.stats.Denied++
			return nil
		case endStep2Zero:
			after, st := w.k.kinfo(pid)
			switch {
			case st == kErr:
				return &scanError{err: ErrProcInfo, what: "after an unreadable process"}
			case st == kGone || after.Zombie:
				w.stats.Exited++
				return nil
			case w.otherUserAny(after):
				w.stats.Denied++
				return nil
			case !after.Started.Equal(a.before.Started) && reuses < maxReuseAttempts-1:
				w.stats.Reused++
				reuses++
				continue
			case !step2Retried:
				step2Retried = true
				continue
			}
			w.stats.Unreadable++
			w.commit(pid, a)
			return nil
		}
		// endDone.
		if len(a.recs) == 0 && !a.gap && !a.listZero {
			w.stats.Scanned++
			w.commit(pid, a)
			return nil
		}
		after, st := w.k.kinfo(pid)
		switch {
		case st == kErr:
			return &scanError{err: ErrProcInfo, what: "re-validation"}
		case st == kGone || after.Zombie:
			w.stats.Exited++ // an exited process holds nothing
			return nil
		case !after.Started.Equal(a.before.Started):
			w.stats.Reused++
			reuses++
			if reuses < maxReuseAttempts {
				continue
			}
			// Churn on every attempt: keep the last attempt's records,
			// stamped with the latest start (over-reporting is safe).
			stamp(a.recs, after.Started)
			if a.gap {
				w.stats.Partial++
			} else {
				w.stats.Scanned++
			}
			w.records = append(w.records, a.recs...)
			w.commit(pid, a)
			return nil
		case a.gap && !gapRetried:
			gapRetried = true
			continue
		}
		stamp(a.recs, a.before.Started)
		if a.gap {
			w.stats.Partial++
		} else {
			w.stats.Scanned++
		}
		w.records = append(w.records, a.recs...)
		w.commit(pid, a)
		return nil
	}
}

// stamp sets every record's start time.
func stamp(rs []Record, t time.Time) {
	for i := range rs {
		rs[i].Started = t
	}
}

// commit adds the final attempt's class flags to the counters, for
// same-user processes other than the caller.
func (w *worker) commit(pid int, a attempt) {
	if pid == selfPID {
		return
	}
	for c := range numClasses {
		if a.tried[c] {
			w.stats.Tried[c]++
		}
		if a.ok[c] {
			w.stats.OK[c]++
		}
	}
}

// otherUserBoth reports whether neither of the process's uids is ours
// while we are not root (the kernel denies such a pid under either
// same-user rule); used for the early skip.
func (w *worker) otherUserBoth(ki kinfo) bool {
	eu := w.k.euid()
	return eu != 0 && eu == w.k.ruid() && ki.UID != eu && ki.RUID != eu
}

// otherUserAny reports whether one of the process's uids differs from
// ours while we are not root: a denial is then explained by one of the
// two possible same-user rules, and lsof (not setuid) is denied the same.
func (w *worker) otherUserAny(ki kinfo) bool {
	eu := w.k.euid()
	return eu != 0 && (ki.UID != eu || ki.RUID != eu)
}

// fixed makes one fixed-size call through fn into the worker's info
// buffer. It returns 0 for a failed call, or ErrLayout when the kernel
// answered with a size other than want.
func (w *worker) fixed(want int, what string, fn func(buf []byte) int) (int, error) {
	n := fn(w.info)
	w.stats.Calls++
	switch {
	case n == 0:
		return 0, nil
	case n != want:
		return 0, layoutErr("%s returned %d bytes, want %d", what, n, want)
	}
	return n, nil
}

// emit adds a record when path is non-empty and matches.
func (w *worker) emit(a *attempt, r Record) {
	if r.Path == "" || !w.match(r.Path) {
		return
	}
	a.recs = append(a.recs, r)
}

// attempt runs steps 1 to 6a for one pid.
func (w *worker) attempt(ctx context.Context, pid int) (attempt, error) {
	var a attempt
	before, st := w.k.kinfo(pid)
	switch {
	case st == kErr:
		return a, &scanError{err: ErrProcInfo, what: "process lookup"}
	case st == kGone || before.Zombie:
		a.end = endExited
		return a, nil
	case w.otherUserBoth(before):
		a.end = endDenied
		return a, nil
	}
	a.before = before

	// Step 2: working and root directory.
	a.tried[classVnodePath] = true
	n, err := w.fixed(sizeVnodePathInfo, "vnode path info", func(b []byte) int { return w.k.pidInfo(pid, flavorVnodePath, 0, b) })
	if err != nil {
		return a, err
	}
	if n == 0 {
		a.end = endStep2Zero
		return a, nil
	}
	a.ok[classVnodePath] = true
	cwd, root, ok := decodeVnodePathInfo(w.info[:n])
	if !ok {
		return a, layoutErr("vnode path info")
	}
	w.emit(&a, Record{PID: pid, Kind: KindCwd, FD: -1, Dev: cwd.Dev, Ino: cwd.Ino, Path: cwd.Path})
	if root.Path != "" {
		w.emit(&a, Record{PID: pid, Kind: KindRoot, FD: -1, Dev: root.Dev, Ino: root.Ino, Path: root.Path})
	}

	// Step 3: descriptors.
	a.tried[classFDList] = true
	fdb, err := w.listTable(pid, flavorListFDs)
	if err != nil {
		return a, err
	}
	fds := decodeFDList(fdb)
	if len(fds) == 0 {
		a.listZero = true
	} else {
		a.ok[classFDList] = true
	}
	var zeroFDs []int32
	for _, e := range fds {
		got, err := w.readFD(&a, pid, e)
		if err != nil {
			return a, err
		}
		if !got {
			zeroFDs = append(zeroFDs, e.FD)
		}
	}

	// Step 4: fileports.
	pb, err := w.listTable(pid, flavorListFileports)
	if err != nil {
		return a, err
	}
	ports := decodeFileportList(pb)
	if len(ports) == 0 {
		a.listZero = true
	}
	var zeroPorts []uint32
	for _, e := range ports {
		got, err := w.readFileport(&a, pid, e)
		if err != nil {
			return a, err
		}
		if !got {
			zeroPorts = append(zeroPorts, e.Port)
		}
	}

	// Step 5: threads.
	a.tried[classThreadList] = true
	handles, err := w.listThreads(pid)
	if err != nil {
		return a, err
	}
	if len(handles) == 0 {
		a.gap = true // every live process has a thread
		a.listZero = true
	} else {
		a.ok[classThreadList] = true
	}
	var zeroThreads []uint64
	for _, h := range handles {
		got, err := w.readThread(&a, pid, h)
		if err != nil {
			return a, err
		}
		if !got {
			zeroThreads = append(zeroThreads, h)
		}
	}

	// Step 6: mapped files.
	if err := w.walkRegions(ctx, &a, pid); err != nil {
		return a, err
	}

	// Step 6a: confirm the zeros by listing the tables again.
	if len(zeroFDs) > 0 {
		b, err := w.listTable(pid, flavorListFDs)
		if err != nil {
			return a, err
		}
		now := map[int32]fdEntry{}
		for _, e := range decodeFDList(b) {
			now[e.FD] = e
		}
		for _, fd := range zeroFDs {
			e, listed := now[fd]
			if !listed {
				continue // closed meanwhile
			}
			got, err := w.readFD(&a, pid, e)
			if err != nil {
				return a, err
			}
			if !got {
				a.gap = true
			}
		}
	}
	if len(zeroPorts) > 0 {
		b, err := w.listTable(pid, flavorListFileports)
		if err != nil {
			return a, err
		}
		now := map[uint32]fileportEntry{}
		for _, e := range decodeFileportList(b) {
			now[e.Port] = e
		}
		for _, port := range zeroPorts {
			e, listed := now[port]
			if !listed {
				continue
			}
			got, err := w.readFileport(&a, pid, e)
			if err != nil {
				return a, err
			}
			if !got {
				a.gap = true
			}
		}
	}
	if len(zeroThreads) > 0 {
		hs, err := w.listThreads(pid)
		if err != nil {
			return a, err
		}
		now := map[uint64]bool{}
		for _, h := range hs {
			now[h] = true
		}
		for _, h := range zeroThreads {
			if !now[h] {
				continue // the thread ended meanwhile
			}
			got, err := w.readThread(&a, pid, h)
			if err != nil {
				return a, err
			}
			if !got {
				a.gap = true
			}
		}
	}
	a.end = endDone
	return a, nil
}

// readFD reads one descriptor; got is false when the call returned 0.
// Types other than vnode and socket are ignored without a call.
func (w *worker) readFD(a *attempt, pid int, e fdEntry) (got bool, err error) {
	switch e.Type {
	case fdTypeVnode:
		a.tried[classFDInfo] = true
		n, err := w.fixed(sizeVnodeFDPath, "fd vnode path info", func(b []byte) int { return w.k.pidFDInfo(pid, e.FD, fdFlavorVnodePath, b) })
		if err != nil || n == 0 {
			return false, err
		}
		a.ok[classFDInfo] = true
		flags, dev, ino, path, ok := decodeVnodeFD(w.info[:n])
		if !ok {
			return false, layoutErr("fd vnode path info")
		}
		w.emit(a, Record{PID: pid, Kind: KindFD, FD: e.FD, Access: accessOf(flags), Dev: dev, Ino: ino, Path: path})
		return true, nil
	case fdTypeSocket:
		n, err := w.fixed(sizeSocketInfo, "fd socket info", func(b []byte) int { return w.k.pidFDInfo(pid, e.FD, fdFlavorSocket, b) })
		if err != nil || n == 0 {
			return false, err
		}
		flags, family, kind, path, ok := decodeUnixSocket(w.info[:n])
		if !ok {
			return false, layoutErr("fd socket info")
		}
		if family == afUnix && kind == sockInfoUn {
			w.emit(a, Record{PID: pid, Kind: KindSocket, FD: e.FD, Access: accessOf(flags), Path: path})
		}
		return true, nil
	}
	return true, nil
}

// readFileport reads one fileport; got is false when the call returned 0.
func (w *worker) readFileport(a *attempt, pid int, e fileportEntry) (got bool, err error) {
	if e.Type != fdTypeVnode {
		return true, nil
	}
	n, err := w.fixed(sizeVnodeFDPath, "fileport vnode path info", func(b []byte) int {
		return w.k.pidFileportInfo(pid, e.Port, fileportFlavorVnode, b)
	})
	if err != nil || n == 0 {
		return false, err
	}
	flags, dev, ino, path, ok := decodeVnodeFD(w.info[:n])
	if !ok {
		return false, layoutErr("fileport vnode path info")
	}
	w.emit(a, Record{PID: pid, Kind: KindFileport, FD: -1, Access: accessOf(flags), Dev: dev, Ino: ino, Path: path})
	return true, nil
}

// readThread reads one thread's working directory; got is false when the
// call returned 0.
func (w *worker) readThread(a *attempt, pid int, h uint64) (got bool, err error) {
	a.tried[classThreadPath] = true
	n, err := w.fixed(sizeThreadPath, "thread path info", func(b []byte) int { return w.k.pidInfo(pid, flavorThreadPath, h, b) })
	if err != nil || n == 0 {
		return false, err
	}
	a.ok[classThreadPath] = true
	path, ok := decodeThreadPath(w.info[:n])
	if !ok {
		return false, layoutErr("thread path info")
	}
	w.emit(a, Record{PID: pid, Kind: KindThreadCwd, FD: -1, Path: path})
	return true, nil
}

// walkRegions visits the process's mapped files and emits one record per
// (dev, ino). A 0 on the first call is a gap: a live process maps at least
// its executable and dyld.
func (w *worker) walkRegions(ctx context.Context, a *attempt, pid int) error {
	a.tried[classRegion] = true
	seen := map[devIno]bool{}
	addr := uint64(0)
	for i := 0; i < maxRegions; i++ {
		if i > 0 && i%regionCtxEvery == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		at := addr
		n, err := w.fixed(sizeRegionPath, "region path info", func(b []byte) int { return w.k.pidInfo(pid, w.flavor, at, b) })
		if err != nil {
			return err
		}
		if n == 0 {
			if i == 0 {
				a.gap = true
			}
			return nil
		}
		if i == 0 {
			a.ok[classRegion] = true
		}
		start, size, dev, ino, path, ok := decodeRegion(w.info[:n])
		if !ok {
			return layoutErr("region path info")
		}
		if path != "" {
			id := devIno{dev, ino}
			if !seen[id] {
				before := len(a.recs)
				w.emit(a, Record{PID: pid, Kind: KindMapped, FD: -1, Dev: dev, Ino: ino, Path: path})
				if len(a.recs) > before {
					seen[id] = true
				}
			}
		}
		next := start + size
		if next <= addr {
			return nil // no progress
		}
		addr = next
	}
	return nil
}

// listTable reads a descriptor or fileport table (8-byte entries). A nil
// result means "none or failed"; the caller decides what that means.
func (w *worker) listTable(pid, flavor int) ([]byte, error) {
	need := w.k.pidInfo(pid, flavor, 0, nil)
	w.stats.Calls++
	if need <= 0 {
		return nil, nil
	}
	size := need + 32*sizeFDInfo
	for range maxListTries {
		if size/sizeFDInfo > maxEntries {
			return nil, &scanError{err: ErrTooLarge, what: "descriptor table"}
		}
		if cap(w.list) < size {
			w.list = make([]byte, size)
		}
		buf := w.list[:size]
		n := w.k.pidInfo(pid, flavor, 0, buf)
		w.stats.Calls++
		if n <= 0 {
			return nil, nil
		}
		if n < len(buf) {
			return buf[:n], nil
		}
		size *= 2 // the table grew between sizing and reading
	}
	return nil, &scanError{err: ErrTooLarge, what: "descriptor table kept growing"}
}

// listThreads reads the thread handles. Sizing with a NULL buffer returns
// 0 for this flavor, so it starts at firstThreads entries and grows by 4
// while the result fills the buffer.
func (w *worker) listThreads(pid int) ([]uint64, error) {
	entries := firstThreads
	for {
		if entries > maxThreads {
			return nil, &scanError{err: ErrTooLarge, what: "thread list"}
		}
		size := entries * threadEntrySize
		if cap(w.threads) < size {
			w.threads = make([]byte, size)
		}
		buf := w.threads[:size]
		n := w.k.pidInfo(pid, flavorListThreads, 0, buf)
		w.stats.Calls++
		if n <= 0 {
			return nil, nil
		}
		if n < len(buf) {
			return decodeThreadList(buf[:n]), nil
		}
		entries *= 4
	}
}
