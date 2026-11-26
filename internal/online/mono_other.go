//go:build !darwin

package online

// Now returns ErrNoMonotonic: paritydesk runs on macOS only; tests use
// FakeMono.
func (SystemMono) Now() (MonoNow, error) { return MonoNow{}, ErrNoMonotonic }
