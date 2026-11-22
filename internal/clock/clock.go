package clock

import (
	"context"
	"sync"
	"time"
)

// Clock is the time source used by every package that waits or stamps.
type Clock interface {
	Now() time.Time
	// Sleep waits for d or until ctx is done, whichever comes first.
	Sleep(ctx context.Context, d time.Duration) error
}

// System is the real clock.
type System struct{}

// Now returns time.Now().
func (System) Now() time.Time { return time.Now() }

// Sleep waits for d or until ctx is done.
func (System) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Fake is a manually advanced clock for tests. Sleep advances the clock
// immediately instead of blocking.
type Fake struct {
	mu  sync.Mutex
	now time.Time
}

// NewFake returns a Fake set to t.
func NewFake(t time.Time) *Fake { return &Fake{now: t} }

// Now returns the fake time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Advance moves the fake time forward by d.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

// Sleep advances the fake time by d unless ctx is already done.
func (f *Fake) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.Advance(d)
	return nil
}
