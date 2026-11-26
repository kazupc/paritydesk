package online

import (
	"errors"
	"sync"
)

// ErrNoMonotonic: this platform has no monotonic clock with a boot id;
// every age then counts as stale.
var ErrNoMonotonic = errors.New("online: no monotonic clock on this platform")

// MonoNow is one monotonic reading: the boot session id and nanoseconds
// of a clock that keeps counting during sleep. OK is false when the
// clock could not be read.
type MonoNow struct {
	BootID string
	NS     int64
	OK     bool
}

// MonoClock reads the monotonic clock. Ages compare wall time and this
// clock with the boot id; either failing means stale, so a clock jump
// cannot make a capture or lease fresh.
type MonoClock interface {
	Now() (MonoNow, error)
}

// SystemMono is the platform's monotonic clock (mono_darwin.go,
// mono_other.go).
type SystemMono struct{}

// Read returns m.Now() as a MonoNow whose OK is false on any error.
func Read(m MonoClock) MonoNow {
	if m == nil {
		return MonoNow{}
	}
	n, err := m.Now()
	if err != nil {
		return MonoNow{}
	}
	n.OK = n.BootID != ""
	return n
}

// FakeMono is a manually advanced MonoClock for tests.
type FakeMono struct {
	mu     sync.Mutex
	boot   string
	ns     int64
	Broken bool
}

// NewFakeMono returns a fake clock at ns in boot session boot.
func NewFakeMono(boot string, ns int64) *FakeMono { return &FakeMono{boot: boot, ns: ns} }

// Now implements MonoClock.
func (f *FakeMono) Now() (MonoNow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Broken {
		return MonoNow{}, ErrNoMonotonic
	}
	return MonoNow{BootID: f.boot, NS: f.ns, OK: true}, nil
}

// Advance moves the clock forward by ns nanoseconds.
func (f *FakeMono) Advance(ns int64) {
	f.mu.Lock()
	f.ns += ns
	f.mu.Unlock()
}

// Reboot starts a new boot session at 0.
func (f *FakeMono) Reboot(boot string) {
	f.mu.Lock()
	f.boot, f.ns = boot, 0
	f.mu.Unlock()
}
