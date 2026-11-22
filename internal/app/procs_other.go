//go:build !darwin

package app

import (
	"context"
	"errors"
)

// errUnsupported: paritydesk drives the app on macOS only.
var errUnsupported = errors.New("app: not supported on this platform")

type sysctlProcs struct{}

// Processes fails outside macOS.
func (sysctlProcs) Processes(context.Context) ([]Process, error) { return nil, errUnsupported }

type fcntlLocks struct{}

// LockHeld fails outside macOS.
func (fcntlLocks) LockHeld(string) (bool, error) { return false, errUnsupported }
