//go:build !darwin

package fsx

import "io/fs"

// Default returns an implementation whose every call fails with
// ErrUnsupported. paritydesk supports macOS only; this keeps the module
// buildable elsewhere for vet and pure-package tests.
func Default() Ops { return unsupported{} }

type unsupported struct{}

func (unsupported) SwapDirs(string, string) error   { return ErrUnsupported }
func (unsupported) RenameExcl(string, string) error { return ErrUnsupported }
func (unsupported) Clone(string, string) error      { return ErrUnsupported }
func (unsupported) WriteFileAtomic(string, string, []byte, fs.FileMode, string) error {
	return ErrUnsupported
}
func (unsupported) MoveInto(string, string, string) error         { return ErrUnsupported }
func (unsupported) MkdirAll(string, fs.FileMode) error            { return ErrUnsupported }
func (unsupported) RemoveAll(string) error                        { return ErrUnsupported }
func (unsupported) FullSync(string) error                         { return ErrUnsupported }
func (unsupported) ID(string) (FileID, error)                     { return FileID{}, ErrUnsupported }
func (unsupported) GetXattr(string, string) ([]byte, bool, error) { return nil, false, ErrUnsupported }
func (unsupported) SetXattr(string, string, []byte) error         { return ErrUnsupported }
func (unsupported) FreeBytes(string) (uint64, error)              { return 0, ErrUnsupported }
