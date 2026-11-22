package fsx

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"time"
)

// MarkerXattr is the extended attribute that ties a data folder to its
// recorded bases (D4; S17 confirmed the app preserves it).
const MarkerXattr = "dev.paritydesk.marker"

// Errors.
var (
	ErrSwapUnsupported = errors.New("fsx: atomic swap not supported on this volume")
	ErrReadOnly        = errors.New("fsx: read-only mode")
	ErrNotContained    = errors.New("fsx: path escapes its root")
	ErrSymlink         = errors.New("fsx: refusing to follow a symlink")
	ErrUnsupported     = errors.New("fsx: not supported on this platform")
)

// FileID identifies a folder independently of its name (D4).
type FileID struct {
	Dev   uint64    `json:"dev"`
	Ino   uint64    `json:"ino"`
	Birth time.Time `json:"birth"`
}

// Ops are the mutating and platform-specific filesystem operations.
// Plain reads (os.ReadFile, os.ReadDir, os.Stat) are done directly by
// callers; only operations that change state or need platform calls go
// through Ops.
type Ops interface {
	// SwapDirs atomically exchanges two directories on one volume
	// (renamex_np RENAME_SWAP). Returns ErrSwapUnsupported when the volume
	// lacks the capability, so the caller can use the journaled fallback.
	SwapDirs(a, b string) error
	// RenameExcl renames from to to and fails with fs.ErrExist if to exists
	// (renamex_np RENAME_EXCL).
	RenameExcl(from, to string) error
	// Clone copies a file or a directory tree with APFS clones where
	// possible (clonefile), falling back to a byte copy. dst must not exist.
	Clone(src, dst string) error
	// WriteFileAtomic writes data to root/rel: it creates a temporary file
	// in tmpDir (same volume, never next to the target, D30), syncs it with
	// a barrier sync, sets perm, and renames it into place. Every path
	// component under root is opened without following symlinks; a symlink
	// at the destination is refused.
	WriteFileAtomic(root, rel string, data []byte, perm fs.FileMode, tmpDir string) error
	// MoveInto moves root/rel into dstDir (same volume), keeping rel's
	// directory structure, without following symlinks. Used to quarantine
	// files into a generation instead of deleting them.
	MoveInto(root, rel, dstDir string) error
	// MkdirAll creates a directory tree with perm.
	MkdirAll(path string, perm fs.FileMode) error
	// RemoveAll removes a tool-owned path (never an app data folder).
	RemoveAll(path string) error
	// FullSync issues F_FULLFSYNC on a file or directory.
	FullSync(path string) error
	// ID returns the folder's identity.
	ID(path string) (FileID, error)
	// GetXattr returns the named attribute; found is false when absent.
	GetXattr(path, name string) (data []byte, found bool, err error)
	// SetXattr sets the named attribute.
	SetXattr(path, name string, data []byte) error
	// FreeBytes returns the bytes available to the user on path's volume.
	FreeBytes(path string) (uint64, error)
}

// Contained joins root and rel after checking that rel is a clean, relative
// path that stays under root: not absolute, no "..", no NUL, not empty.
// It does not touch the filesystem; symlink checks happen at open time.
func Contained(root, rel string) (string, error) {
	if rel == "" || strings.ContainsRune(rel, 0) || filepath.IsAbs(rel) {
		return "", ErrNotContained
	}
	clean := filepath.Clean(rel)
	if clean != rel && clean+"/" != rel {
		return "", ErrNotContained
	}
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		if part == ".." || part == "." {
			return "", ErrNotContained
		}
	}
	return filepath.Join(root, clean), nil
}

// ReadOnly wraps o so that every mutating call fails with ErrReadOnly.
// Used for --read-only and PARITYDESK_READONLY=1.
func ReadOnly(o Ops) Ops { return readOnly{o} }

type readOnly struct{ o Ops }

func (r readOnly) SwapDirs(string, string) error   { return ErrReadOnly }
func (r readOnly) RenameExcl(string, string) error { return ErrReadOnly }
func (r readOnly) Clone(string, string) error      { return ErrReadOnly }
func (r readOnly) WriteFileAtomic(string, string, []byte, fs.FileMode, string) error {
	return ErrReadOnly
}
func (r readOnly) MoveInto(string, string, string) error      { return ErrReadOnly }
func (r readOnly) MkdirAll(string, fs.FileMode) error         { return ErrReadOnly }
func (r readOnly) RemoveAll(string) error                     { return ErrReadOnly }
func (r readOnly) FullSync(string) error                      { return nil }
func (r readOnly) ID(p string) (FileID, error)                { return r.o.ID(p) }
func (r readOnly) SetXattr(string, string, []byte) error      { return ErrReadOnly }
func (r readOnly) FreeBytes(p string) (uint64, error)         { return r.o.FreeBytes(p) }
func (r readOnly) GetXattr(p, n string) ([]byte, bool, error) { return r.o.GetXattr(p, n) }
