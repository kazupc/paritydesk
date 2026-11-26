package fdscan

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Identities the self-check probe and the fake kernel agree on.
var (
	idExe  = devIno{1, 0xa}
	idNull = devIno{3, 0xb}
	idCwd  = devIno{1, 0xc}
)

// echoProc is a process that holds exactly the probe's descriptors: the
// read probe fd 7 (the executable), the write probe fd 8 (/dev/null), the
// socket fd 9, cwd /w, one thread without a working directory and the
// executable mapped.
func echoProc() *fakeProc {
	return &fakeProc{
		started: time.Unix(1, 0), uid: fakeUID, ruid: fakeUID,
		cwd: vnodePath{idCwd, "/w"},
		fds: []fakeFD{
			{fd: 7, typ: fdTypeVnode, flags: 1, dev: idExe.Dev, ino: idExe.Ino, path: "/w/exe"},
			{fd: 8, typ: fdTypeVnode, flags: 2, dev: idNull.Dev, ino: idNull.Ino, path: "/dev/null"},
			{fd: 9, typ: fdTypeSocket, flags: 3, family: afUnix, kind: sockInfoUn},
		},
		threads: []fakeThread{{h: 0x77}},
		regions: []fakeRegion{{addr: 0x1000, size: 0x1000, dev: idExe.Dev, ino: idExe.Ino, path: "/w/exe"}},
	}
}

// probeFor builds the probe of pid by hand; closes counts p.Close calls.
func probeFor(pid int, closes *atomic.Int32) selfProbe {
	cwd := idCwd
	return selfProbe{
		PID:    pid,
		FDs:    []probeFD{{FD: 7, Access: AccessRead, devIno: idExe}, {FD: 8, Access: AccessWrite, devIno: idNull}},
		Cwd:    &cwd,
		SockFD: 9,
		Stat: func(path string) (devIno, error) {
			switch path {
			case "/w/exe":
				return idExe, nil
			case "/dev/null":
				return idNull, nil
			case "/w":
				return idCwd, nil
			}
			return devIno{}, errors.New("no such file")
		},
		Close: func() { closes.Add(1) },
	}
}

func TestSelfCheckFake(t *testing.T) {
	const pid = 4242
	tests := []struct {
		name    string
		setup   func(k *fakeKernel, p *fakeProc, pr *selfProbe)
		flavor  int
		socket  bool
		wantErr error
		what    string
	}{
		{name: "echo", flavor: 22, socket: true},
		{name: "mapped only through flavor 8", setup: func(k *fakeKernel, _ *fakeProc, _ *selfProbe) { k.region22Bad = true }, flavor: 8, socket: true},
		{name: "flavor 22 gone", setup: func(k *fakeKernel, _ *fakeProc, _ *selfProbe) { k.deny["pid:22"] = scopeAll }, flavor: 8, socket: true},
		{name: "fd path offset shifted", setup: func(_ *fakeKernel, _ *fakeProc, pr *selfProbe) {
			stat := pr.Stat
			pr.Stat = func(path string) (devIno, error) {
				if path == "/dev/null" {
					return devIno{99, 99}, nil
				}
				return stat(path)
			}
		}, wantErr: ErrLayout, what: "fd path"},
		{name: "open flags from the wrong place", setup: func(_ *fakeKernel, p *fakeProc, _ *selfProbe) { p.fds[1].flags = 1 }, wantErr: ErrLayout, what: "fd access"},
		{name: "empty thread list", setup: func(_ *fakeKernel, p *fakeProc, _ *selfProbe) { p.threads = nil }, wantErr: ErrBlind, what: "denied thread-list"},
		{name: "thread path size", setup: func(k *fakeKernel, _ *fakeProc, _ *selfProbe) { k.sizes["pid:10"] = 1300 }, wantErr: ErrLayout, what: ""},
		{name: "thread path 0", setup: func(k *fakeKernel, _ *fakeProc, _ *selfProbe) { k.fail["thread:4242:119"] = -1 }, wantErr: ErrLayout, what: ""},
		{name: "socket 0", setup: func(k *fakeKernel, _ *fakeProc, _ *selfProbe) { k.deny["fd:3"] = scopeAll }, wantErr: ErrLayout},
		{name: "socket family 2", setup: func(_ *fakeKernel, p *fakeProc, _ *selfProbe) { p.fds[2].family = 2 }, wantErr: ErrLayout, what: "socket"},
		{name: "no socketpair", setup: func(_ *fakeKernel, _ *fakeProc, pr *selfProbe) { pr.SockFD = -1 }, flavor: 22, socket: false},
		{name: "no cwd", setup: func(_ *fakeKernel, p *fakeProc, pr *selfProbe) {
			pr.Cwd = nil
			p.cwd.Ino = 0x999
		}, flavor: 22, socket: true},
		{name: "wrong cwd", setup: func(_ *fakeKernel, p *fakeProc, _ *selfProbe) { p.cwd.Ino = 0x999 }, wantErr: ErrLayout, what: "cwd"},
		{name: "fd group denied", setup: func(k *fakeKernel, _ *fakeProc, _ *selfProbe) { k.denyFDOp = scopeAll }, wantErr: ErrBlind, what: "denied fd-list"},
		{name: "pidinfo group denied", setup: func(k *fakeKernel, _ *fakeProc, _ *selfProbe) { k.denyPidinfoOp = scopeAll }, wantErr: ErrBlind, what: "denied vnode-path"},
		{name: "regions 0 under both flavors", setup: func(k *fakeKernel, _ *fakeProc, _ *selfProbe) { k.fail["region:4242"] = -1 }, wantErr: ErrBlind, what: "denied region"},
		{name: "vnode path 2400", setup: func(k *fakeKernel, _ *fakeProc, _ *selfProbe) { k.sizes["pid:9"] = 2400 }, wantErr: ErrLayout},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			k := newFake()
			p := k.add(pid, echoProc())
			var closes atomic.Int32
			pr := probeFor(pid, &closes)
			if tc.setup != nil {
				tc.setup(k, p, &pr)
			}
			flavor, sock, err := selfCheck(t.Context(), k, pr)
			if closes.Load() != 1 {
				t.Errorf("Close called %d times", closes.Load())
			}
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err %v, want %v", err, tc.wantErr)
				}
				if tc.wantErr == ErrBlind && errors.Is(err, ErrLayout) || tc.wantErr == ErrLayout && errors.Is(err, ErrBlind) {
					t.Fatalf("err %v is both blind and layout", err)
				}
				if tc.what != "" && !strings.HasSuffix(err.Error(), "self-check: "+tc.what) {
					t.Fatalf("err %q, want the step %q", err, tc.what)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if flavor != tc.flavor || sock != tc.socket {
				t.Fatalf("flavor %d socket %v, want %d %v", flavor, sock, tc.flavor, tc.socket)
			}
		})
	}
}

// echoScanner is a sysScanner over a fake kernel whose own process
// (selfPID) echoes the probe, plus five other same-user processes.
func echoScanner(probes *atomic.Int32) (*sysScanner, *fakeKernel) {
	k := newFake()
	k.add(selfPID, echoProc())
	for pid := 10; pid < 15; pid++ {
		k.add(pid, richProc(1))
	}
	var closes atomic.Int32
	s := &sysScanner{k: k, probe: func() (selfProbe, error) {
		probes.Add(1)
		return probeFor(selfPID, &closes), nil
	}}
	return s, k
}

func TestScanGroupDenial(t *testing.T) {
	for _, tc := range []struct {
		name  string
		deny  func(k *fakeKernel)
		class int
	}{
		{"fd group for others", func(k *fakeKernel) { k.denyFDOp = scopeOthers }, classFDList},
		{"pidinfo group for others", func(k *fakeKernel) { k.denyPidinfoOp = scopeOthers }, classVnodePath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var probes atomic.Int32
			s, k := echoScanner(&probes)
			tc.deny(k)
			if _, err := s.SelfCheck(t.Context()); err != nil {
				t.Fatalf("the self-check must pass (only others are denied): %v", err)
			}
			res, err := s.Scan(t.Context(), Options{Match: matchWatched})
			if !errors.Is(err, ErrBlind) || res.Records != nil {
				t.Fatalf("err %v", err)
			}
			if res.Stats.Tried[tc.class] < blindMinProcs || res.Stats.OK[tc.class] != 0 {
				t.Fatalf("class %s: tried %d ok %d", className(tc.class), res.Stats.Tried[tc.class], res.Stats.OK[tc.class])
			}
		})
	}
}

func TestEnsureChecked(t *testing.T) {
	old := selfCheckTimeout
	t.Cleanup(func() { selfCheckTimeout = old })

	// Two concurrent first callers run the check once.
	var probes atomic.Int32
	s, _ := echoScanner(&probes)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if f, err := s.SelfCheck(t.Context()); err != nil || f != 22 {
				t.Errorf("concurrent: %d %v", f, err)
			}
		})
	}
	wg.Wait()
	if probes.Load() != 1 {
		t.Fatalf("%d checks, want 1", probes.Load())
	}

	// A check that blocks past the timeout is a context error, not
	// cached; the next call runs the check again and passes.
	selfCheckTimeout = 50 * time.Millisecond
	probes.Store(0)
	s, k := echoScanner(&probes)
	k.delay = 100 * time.Millisecond
	_, err := s.SelfCheck(t.Context())
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrLayout) {
		t.Fatalf("timeout: %v", err)
	}
	k.delay = 0
	selfCheckTimeout = old
	if _, err := s.SelfCheck(t.Context()); err != nil || probes.Load() != 2 {
		t.Fatalf("after the timeout: %v (%d checks)", err, probes.Load())
	}

	// A caller whose ctx is already cancelled gets ctx.Err(); the next
	// live caller uses native.
	probes.Store(0)
	s, _ = echoScanner(&probes)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.SelfCheck(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled caller: %v", err)
	}
	if _, err := s.Scan(ctx, Options{Match: matchWatched}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled scan: %v", err)
	}
	if probes.Load() != 0 {
		t.Fatal("a cancelled caller ran the check")
	}

	// Scan -> ensureChecked -> scanPIDs does not deadlock.
	done := make(chan error, 1)
	go func() {
		res, err := s.Scan(t.Context(), Options{Match: matchWatched})
		if err == nil && len(res.Records) == 0 {
			err = errors.New("no records")
		}
		if res.Stats.RegionFlavor != 22 || !res.Stats.SocketCheck {
			err = errors.New("stats lack the self-check outcome")
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Scan deadlocked")
	}

	// A layout failure is cached: one check for many calls.
	probes.Store(0)
	s, k = echoScanner(&probes)
	k.sizes["pid:9"] = 2400
	for range 5 {
		if _, err := s.Scan(t.Context(), Options{Match: matchWatched}); !errors.Is(err, ErrLayout) {
			t.Fatalf("layout: %v", err)
		}
	}
	if probes.Load() != 1 {
		t.Fatalf("%d checks for a cached layout failure", probes.Load())
	}

	// A blind outcome is cached too, and Scan fails with it.
	probes.Store(0)
	s, k = echoScanner(&probes)
	k.denyFDOp = scopeAll
	for range 3 {
		if _, err := s.Scan(t.Context(), Options{Match: matchWatched}); !errors.Is(err, ErrBlind) {
			t.Fatalf("blind: %v", err)
		}
	}
	if probes.Load() != 1 {
		t.Fatalf("%d checks for a cached blind outcome", probes.Load())
	}

	// A probe that cannot be opened (for example out of descriptors) is
	// ErrLayout for that call but not cached: nothing was learned about
	// the kernel, so the next call runs the check again and uses native.
	probes.Store(0)
	s, _ = echoScanner(&probes)
	good := s.probe
	failOnce := true
	s.probe = func() (selfProbe, error) {
		if failOnce {
			failOnce = false
			probes.Add(1)
			return selfProbe{}, errors.New("too many open files")
		}
		return good()
	}
	if _, err := s.SelfCheck(t.Context()); !errors.Is(err, ErrLayout) || errors.Is(err, ErrBlind) {
		t.Fatalf("probe failure: %v", err)
	}
	if res, err := s.Scan(t.Context(), Options{Match: matchWatched}); err != nil || len(res.Records) == 0 || res.Stats.RegionFlavor != 22 {
		t.Fatalf("after a probe failure: %v (%d checks)", err, probes.Load())
	}
	if probes.Load() != 2 {
		t.Fatalf("%d checks, want 2 (the probe failure is not cached)", probes.Load())
	}

	// Unlinked trampolines fail the check before the probe is opened.
	probes.Store(0)
	s, _ = echoScanner(&probes)
	s.linked = func() bool { return false }
	if _, err := s.SelfCheck(t.Context()); !errors.Is(err, ErrLayout) || probes.Load() != 0 {
		t.Fatalf("unlinked: %v", err)
	}
}
