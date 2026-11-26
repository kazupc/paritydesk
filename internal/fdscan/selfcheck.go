package fdscan

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// selfCheckTimeout bounds one self-check. A variable so tests can shorten
// it.
var selfCheckTimeout = 2 * time.Second

// selfProbe is what the self-check opened in its own process and what it
// expects the kernel to report back. openSelfProbe (darwin) fills it;
// tests build one by hand and give the fake kernel the same values.
type selfProbe struct {
	PID    int
	FDs    []probeFD // the read and the write probe descriptors
	Cwd    *devIno   // nil when "." cannot be stat'ed (sub-check skipped)
	SockFD int32     // one end of a socketpair; -1 when socketpair failed
	// Stat returns a path's identity (os.Stat); a seam for tests.
	Stat func(path string) (devIno, error)
	// Close closes everything openSelfProbe opened.
	Close func()
}

// probeFD is one probe descriptor and what the kernel must report for it.
type probeFD struct {
	FD     int32
	Access Access // AccessRead or AccessWrite
	devIno
}

// checkErr is a self-check failure: err is ErrBlind or ErrLayout, what
// names the failed step (never a path).
func checkErr(err error, what string) error {
	return &scanError{err: err, what: "self-check: " + what}
}

// selfCheck verifies, on p.PID only, that the kernel answers at all and
// that its answers decode as the layout constants expect. It returns the
// region flavor to use and whether the socket flavor was verified.
func selfCheck(ctx context.Context, k kernel, p selfProbe) (regionFlavor int, socketOK bool, err error) {
	if p.Close != nil {
		defer p.Close()
	}
	if p.PID <= 0 || len(p.FDs) == 0 || p.Stat == nil {
		return 0, false, checkErr(ErrLayout, "probe")
	}
	info := make([]byte, infoBufSize)
	threads := make([]byte, firstThreads*threadEntrySize)

	// Step 1a: raw permission probes. Each certainly has an answer for a
	// live process that just opened the probe descriptors; a 0 means the
	// interface is denied (the own process included), never a layout
	// problem.
	if k.pidInfo(p.PID, flavorVnodePath, 0, info) == 0 {
		return 0, false, checkErr(ErrBlind, "denied vnode-path")
	}
	if k.pidInfo(p.PID, flavorListFDs, 0, nil) == 0 {
		return 0, false, checkErr(ErrBlind, "denied fd-list")
	}
	if k.pidFDInfo(p.PID, p.FDs[0].FD, fdFlavorVnodePath, info) == 0 {
		return 0, false, checkErr(ErrBlind, "denied fd-info")
	}
	if k.pidInfo(p.PID, flavorListThreads, 0, threads) == 0 {
		return 0, false, checkErr(ErrBlind, "denied thread-list")
	}
	flavor := defaultRegionFlavor
	if k.pidInfo(p.PID, defaultRegionFlavor, 0, info) == 0 {
		// The private flavor gave nothing: the public one, or denied.
		if k.pidInfo(p.PID, fallbackRegionFlavor, 0, info) == 0 {
			return 0, false, checkErr(ErrBlind, "denied region")
		}
		flavor = fallbackRegionFlavor
	}
	if err := ctx.Err(); err != nil {
		return 0, false, fmt.Errorf("fdscan: self-check: %w: %w", ErrLayout, err)
	}

	// Steps 2, 3 and 6: scan the own pid and compare.
	what, err := checkRecords(ctx, k, p, flavor)
	if err != nil {
		return 0, false, err
	}
	if what == "mapped" && flavor == defaultRegionFlavor {
		flavor = fallbackRegionFlavor
		if what, err = checkRecords(ctx, k, p, flavor); err != nil {
			return 0, false, err
		}
	}
	if what != "" {
		return 0, false, checkErr(ErrLayout, what)
	}

	// Step 4: thread flavors.
	n := k.pidInfo(p.PID, flavorListThreads, 0, threads)
	hs := decodeThreadList(threads[:max(0, min(n, len(threads)))])
	if n <= 0 || len(hs) == 0 {
		return 0, false, checkErr(ErrLayout, "thread")
	}
	if k.pidInfo(p.PID, flavorThreadPath, hs[0], info) != sizeThreadPath {
		return 0, false, checkErr(ErrLayout, "thread")
	}

	// Step 5: socket flavor.
	if p.SockFD >= 0 {
		n := k.pidFDInfo(p.PID, p.SockFD, fdFlavorSocket, info)
		if n != sizeSocketInfo {
			return 0, false, checkErr(ErrLayout, "socket")
		}
		_, family, kind, _, ok := decodeUnixSocket(info[:n])
		if !ok || family != afUnix || kind != sockInfoUn {
			return 0, false, checkErr(ErrLayout, "socket")
		}
		socketOK = true
	}
	return flavor, socketOK, nil
}

// checkRecords scans p.PID with the region flavor and returns the first
// failed requirement ("" when all hold). A scan error is returned as an
// error wrapping ErrLayout (and the context error, if any).
func checkRecords(ctx context.Context, k kernel, p selfProbe, flavor int) (string, error) {
	res, err := scanPIDs(ctx, k, Options{PIDs: []int{p.PID}, Match: func(string) bool { return true }, Workers: 1}, flavor)
	if err != nil {
		if errors.Is(err, ErrLayout) {
			return "", &scanError{err: err, what: "self-check: scan"}
		}
		return "", fmt.Errorf("fdscan: self-check: scan: %w: %w", ErrLayout, err)
	}
	byFD := map[int32]Record{}
	mapped, cwdOK := false, p.Cwd == nil
	for _, r := range res.Records {
		switch r.Kind {
		case KindFD:
			byFD[r.FD] = r
		case KindMapped:
			if !mapped {
				if id, err := p.Stat(r.Path); err == nil && id == (devIno{r.Dev, r.Ino}) {
					mapped = true
				}
			}
		case KindCwd:
			if p.Cwd != nil && *p.Cwd == (devIno{r.Dev, r.Ino}) {
				cwdOK = true
			}
		}
	}
	for _, pf := range p.FDs {
		r, ok := byFD[pf.FD]
		switch {
		case !ok:
			return "fd", nil
		case r.Access != pf.Access:
			return "fd access", nil
		case (devIno{r.Dev, r.Ino}) != pf.devIno:
			return "fd identity", nil
		}
		if id, err := p.Stat(r.Path); err != nil || id != pf.devIno {
			return "fd path", nil
		}
	}
	if !cwdOK {
		return "cwd", nil
	}
	if !mapped {
		return "mapped", nil
	}
	return "", nil
}

// sysScanner is the libproc scanner: a kernel, the cached self-check and
// the probe opener. System() returns one on darwin; tests build one over
// a fake kernel.
type sysScanner struct {
	k        kernel
	mu       sync.Mutex // guards the fields below; replaces sync.Once
	checked  bool       // a conclusive self-check outcome is cached
	flavor   int        // 22 or 8 when checked && checkErr == nil
	socketOK bool
	checkErr error
	probe    func() (selfProbe, error) // openSelfProbe; a seam for tests
	linked   func() bool               // trampolinesLinked; nil: linked
}

// errProbeOpen marks a self-check that could not open its probe
// descriptors. It always comes wrapped with ErrLayout and is not a
// conclusive outcome, so ensureChecked does not cache it.
var errProbeOpen = errors.New("probe could not be opened")

// isContextErr reports whether err is (or wraps) a context error.
func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// ensureChecked returns the cached outcome or runs the self-check under
// context.Background() with selfCheckTimeout. It never calls Scan; the
// self-check scans through scanPIDs, which does not touch mu.
func (s *sysScanner) ensureChecked() (int, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.checked {
		return s.flavor, s.socketOK, s.checkErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), selfCheckTimeout)
	defer cancel()
	flavor, sock, err := s.runCheck(ctx)
	if err != nil && isContextErr(err) {
		if !errors.Is(err, ErrLayout) {
			err = fmt.Errorf("fdscan: self-check: %w: %w", ErrLayout, err)
		}
		return 0, false, err // never cached
	}
	if errors.Is(err, errProbeOpen) {
		// Nothing was learned about the kernel's answers (for example the
		// process was briefly out of descriptors): ErrLayout for this call
		// (auto uses lsof), never cached, so the next call tries again.
		return 0, false, err
	}
	s.checked, s.flavor, s.socketOK, s.checkErr = true, flavor, sock, err
	return flavor, sock, err
}

func (s *sysScanner) runCheck(ctx context.Context) (int, bool, error) {
	if s.linked != nil && !s.linked() {
		return 0, false, checkErr(ErrLayout, "trampolines not linked")
	}
	if s.probe == nil {
		return 0, false, checkErr(ErrLayout, "no probe")
	}
	p, err := s.probe()
	if err != nil {
		// The underlying error may name a path; the message does not.
		return 0, false, fmt.Errorf("fdscan: self-check: %w: %w", ErrLayout, errProbeOpen)
	}
	return selfCheck(ctx, s.k, p)
}

// SelfCheck implements Scanner.
func (s *sysScanner) SelfCheck(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	flavor, _, err := s.ensureChecked()
	return flavor, err
}

// Scan implements Scanner.
func (s *sysScanner) Scan(ctx context.Context, opt Options) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if opt.Match == nil {
		return Result{}, ErrNoMatch
	}
	flavor, sock, err := s.ensureChecked()
	if err != nil {
		return Result{}, err
	}
	res, err := scanPIDs(ctx, s.k, opt, flavor)
	res.Stats.RegionFlavor, res.Stats.SocketCheck = flavor, sock
	return res, err
}
