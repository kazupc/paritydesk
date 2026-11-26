//go:build !darwin || pd_nofdscan

package fdscan

import "context"

// stubScanner is the scanner where the native scan is not built: every
// call fails with ErrUnsupported, and the caller uses lsof.
type stubScanner struct{}

func (stubScanner) Scan(context.Context, Options) (Result, error) { return Result{}, ErrUnsupported }
func (stubScanner) SelfCheck(context.Context) (int, error)        { return 0, ErrUnsupported }

// System returns the stub scanner. Each call returns the same value.
func System() Scanner { return stubScanner{} }

// trampolinesLinked is true: the stub has no trampolines to miss.
func trampolinesLinked() bool { return true }
