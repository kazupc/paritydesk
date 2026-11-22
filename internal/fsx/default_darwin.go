//go:build darwin

package fsx

import (
	"io/fs"

	"golang.org/x/sys/unix"

	"github.com/kazupc/paritydesk/internal/pderr"
)

// Default returns the macOS implementation.
func Default() Ops { return darwinOps{} }

type darwinOps struct{}

// Flags used by the implementation (documented here so the contract is
// visible in the skeleton).
const (
	renameSwap     = unix.RENAME_SWAP
	renameExcl     = unix.RENAME_EXCL
	fFullFsync     = unix.F_FULLFSYNC
	fBarrierFsync  = unix.F_BARRIERFSYNC
	openNoFollow   = unix.O_NOFOLLOW
	cloneNoOwnerCp = unix.CLONE_NOOWNERCOPY
)

func (darwinOps) SwapDirs(a, b string) error       { return pderr.ErrNotImplemented }
func (darwinOps) RenameExcl(from, to string) error { return pderr.ErrNotImplemented }
func (darwinOps) Clone(src, dst string) error      { return pderr.ErrNotImplemented }
func (darwinOps) WriteFileAtomic(root, rel string, data []byte, perm fs.FileMode, tmpDir string) error {
	return pderr.ErrNotImplemented
}
func (darwinOps) MoveInto(root, rel, dstDir string) error      { return pderr.ErrNotImplemented }
func (darwinOps) MkdirAll(path string, perm fs.FileMode) error { return pderr.ErrNotImplemented }
func (darwinOps) RemoveAll(path string) error                  { return pderr.ErrNotImplemented }
func (darwinOps) FullSync(path string) error                   { return pderr.ErrNotImplemented }
func (darwinOps) ID(path string) (FileID, error)               { return FileID{}, pderr.ErrNotImplemented }
func (darwinOps) GetXattr(path, name string) ([]byte, bool, error) {
	return nil, false, pderr.ErrNotImplemented
}
func (darwinOps) SetXattr(path, name string, data []byte) error { return pderr.ErrNotImplemented }
func (darwinOps) FreeBytes(path string) (uint64, error)         { return 0, pderr.ErrNotImplemented }

var _ = []any{renameSwap, renameExcl, fFullFsync, fBarrierFsync, openNoFollow, cloneNoOwnerCp}
