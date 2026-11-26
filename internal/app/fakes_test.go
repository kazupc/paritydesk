package app

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
)

// Synthetic identifiers; none belongs to a real account or session.
const (
	sessA          = "local_00000000-0000-4000-8000-00000000000a"
	sessB          = "local_00000000-0000-4000-8000-00000000000b"
	sessC          = "local_00000000-0000-4000-8000-00000000000c"
	orgX           = "00000000-0000-4000-8000-0000000000f1"
	orgY           = "00000000-0000-4000-8000-0000000000f2"
	acctP          = "00000000-0000-4000-8000-0000000000e1"
	acctQ          = "00000000-0000-4000-8000-0000000000e2"
	testBundle     = "com.example.paritydesk-test"
	testBundlePath = "/Volumes/Test/Apps/Example.app"
)

// testSig holds invented, manifest-style patterns.
var testSig = compat.Signals{
	QuitVetoed:      `\[quit\] refused by interceptor`,
	TurnStarted:     `\[turn\] begin id={session}`,
	TurnEnded:       `\[turn\] done id={session}`,
	AppStarted:      `\[boot\] ready v=[0-9.]+`,
	FocusedSession:  `\[focus\] id={session}`,
	BridgeConnected: `\[bridge\] peer up`,
	ActiveOrg:       `\[acct\] account=(?P<account>[^ ]+) org=(?P<org>[^ ]+) active`,
}

// call is one recorded command.
type call struct {
	t    Template
	args []string
}

// fakeRunner records commands (after validating them with Argv, so a test
// fails on arguments the real runner would refuse) and answers with reply.
type fakeRunner struct {
	t     *testing.T
	mu    sync.Mutex
	calls []call
	reply func(t Template, args []string) ([]byte, error)
}

func (f *fakeRunner) Run(_ context.Context, t Template, args ...string) ([]byte, error) {
	if _, err := Argv(t, args...); err != nil {
		f.t.Errorf("fakeRunner: %s refused: %v", t, err)
		return nil, err
	}
	f.mu.Lock()
	f.calls = append(f.calls, call{t, slices.Clone(args)})
	f.mu.Unlock()
	if f.reply == nil {
		return nil, nil
	}
	return f.reply(t, args)
}

func (f *fakeRunner) templates() []Template {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Template, len(f.calls))
	for i, c := range f.calls {
		out[i] = c.t
	}
	return out
}

// procFunc is a ProcessTable computed on each call.
type procFunc func() []Process

func (f procFunc) Processes(context.Context) ([]Process, error) { return f(), nil }

// fakeLocks reports the listed paths as held.
type fakeLocks map[string]bool

func (f fakeLocks) LockHeld(p string) (bool, error) { return f[p], nil }

// fakeHolders returns fixed holders.
type fakeHolders []Holder

func (f fakeHolders) Holders(context.Context, []string) ([]Holder, error) { return f, nil }

// mainExe is the app's main executable in the test bundle.
func mainExe() string { return testBundlePath + "/Contents/MacOS/Example" }

// appProc returns an app process.
func appProc(pid int) Process {
	return Process{PID: pid, Exe: mainExe(), Started: time.Unix(1, 0)}
}
