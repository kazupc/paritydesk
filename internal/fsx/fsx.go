package fsx

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	// ErrNotRemovable: RemoveAll refused a root or path that could hold
	// app data (see CheckRemovable).
	ErrNotRemovable = errors.New("fsx: refusing to remove this path")
	// ErrOutsideConfinement: a Confine wrapper refused a mutating call
	// outside its tool-owned root.
	ErrOutsideConfinement = errors.New("fsx: path outside the confined root")
	// ErrRealPathInTest: a constructor running under `go test` was given a
	// real app, home or bundle path (see CheckTestPath).
	ErrRealPathInTest = errors.New("fsx: real app path used in a test")
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
	// at the destination is refused. Missing parent directories under root
	// are created with mode 0700 (mkdirat, never following a symlink);
	// existing directories keep their modes. tmpDir is created 0700 if
	// missing. Both implementations (darwin and fsxtest) follow exactly
	// these semantics; a shared conformance test checks it.
	WriteFileAtomic(root, rel string, data []byte, perm fs.FileMode, tmpDir string) error
	// MoveInto moves root/rel into dstDir (same volume), keeping rel's
	// directory structure, without following symlinks. Used to quarantine
	// files into a generation instead of deleting them.
	MoveInto(root, rel, dstDir string) error
	// MkdirAll creates a directory tree with perm.
	MkdirAll(path string, perm fs.FileMode) error
	// RemoveAll removes root/rel recursively. root must be a tool-owned
	// directory (snapshot temp root, generations, run dir); the pair must
	// pass CheckRemovable, so no app data folder can ever be removed.
	RemoveAll(root, rel string) error
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
func (r readOnly) RemoveAll(string, string) error             { return ErrReadOnly }
func (r readOnly) FullSync(string) error                      { return nil }
func (r readOnly) ID(p string) (FileID, error)                { return r.o.ID(p) }
func (r readOnly) SetXattr(string, string, []byte) error      { return ErrReadOnly }
func (r readOnly) FreeBytes(p string) (uint64, error)         { return r.o.FreeBytes(p) }
func (r readOnly) GetXattr(p, n string) ([]byte, bool, error) { return r.o.GetXattr(p, n) }

// CheckRemovable validates a RemoveAll target and returns the joined path.
// It refuses: a root that is empty, relative, unclean or "/"; the user's
// home directory or any of its ancestors; an unclean or escaping rel
// (Contained); and any rel component named "Claude" or starting with
// "Claude-" (app data folders and their parked copies). Every RemoveAll
// implementation calls it first.
func CheckRemovable(root, rel string) (string, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" {
		return "", ErrNotRemovable
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if root == home || strings.HasPrefix(home, root+string(filepath.Separator)) {
			return "", ErrNotRemovable
		}
	}
	p, err := Contained(root, rel)
	if err != nil {
		return "", err
	}
	for _, part := range strings.Split(filepath.Clean(rel), string(filepath.Separator)) {
		if part == "Claude" || strings.HasPrefix(part, "Claude-") {
			return "", ErrNotRemovable
		}
	}
	return p, nil
}

// Confine wraps o so that every mutating call whose destination lies
// outside root fails with ErrOutsideConfinement. Reads pass through. The
// LevelDB snapshot opener uses it on an unwrapped Ops, so snapshots work in
// read-only mode while still never writing outside their temp root.
func Confine(o Ops, root string) Ops { return confined{o: o, root: filepath.Clean(root)} }

type confined struct {
	o    Ops
	root string
}

func (c confined) inside(p string) bool {
	p = filepath.Clean(p)
	return filepath.IsAbs(p) && (p == c.root || strings.HasPrefix(p, c.root+string(filepath.Separator)))
}

func (c confined) guard(paths ...string) error {
	for _, p := range paths {
		if !c.inside(p) {
			return ErrOutsideConfinement
		}
	}
	return nil
}

func (c confined) SwapDirs(a, b string) error {
	if err := c.guard(a, b); err != nil {
		return err
	}
	return c.o.SwapDirs(a, b)
}

func (c confined) RenameExcl(from, to string) error {
	if err := c.guard(from, to); err != nil {
		return err
	}
	return c.o.RenameExcl(from, to)
}

// Clone may read from anywhere; only the destination is confined.
func (c confined) Clone(src, dst string) error {
	if err := c.guard(dst); err != nil {
		return err
	}
	return c.o.Clone(src, dst)
}

func (c confined) WriteFileAtomic(root, rel string, data []byte, perm fs.FileMode, tmpDir string) error {
	if err := c.guard(root, tmpDir); err != nil {
		return err
	}
	return c.o.WriteFileAtomic(root, rel, data, perm, tmpDir)
}

func (c confined) MoveInto(root, rel, dstDir string) error {
	if err := c.guard(root, dstDir); err != nil {
		return err
	}
	return c.o.MoveInto(root, rel, dstDir)
}

func (c confined) MkdirAll(path string, perm fs.FileMode) error {
	if err := c.guard(path); err != nil {
		return err
	}
	return c.o.MkdirAll(path, perm)
}

func (c confined) RemoveAll(root, rel string) error {
	if err := c.guard(root); err != nil {
		return err
	}
	return c.o.RemoveAll(root, rel)
}

func (c confined) FullSync(p string) error {
	if err := c.guard(p); err != nil {
		return err
	}
	return c.o.FullSync(p)
}

func (c confined) SetXattr(p, n string, d []byte) error {
	if err := c.guard(p); err != nil {
		return err
	}
	return c.o.SetXattr(p, n, d)
}

func (c confined) ID(p string) (FileID, error)                { return c.o.ID(p) }
func (c confined) GetXattr(p, n string) ([]byte, bool, error) { return c.o.GetXattr(p, n) }
func (c confined) FreeBytes(p string) (uint64, error)         { return c.o.FreeBytes(p) }

// CheckTestPath guards constructors that take real locations (data root,
// bundle path, log path, state and support dirs). Outside `go test` it
// always returns nil. Under `go test` it refuses /Applications and any path
// under the real home's Library/Application Support/Claude*,
// Library/Logs/Claude, Library/Caches/com.anthropic* or
// Library/Application Support/paritydesk, so a mis-wired test can never
// touch the real app or the user's own paritydesk state.
func CheckTestPath(p string) error {
	if !testing.Testing() || p == "" {
		return nil
	}
	p = filepath.Clean(p)
	if p == "/Applications" || strings.HasPrefix(p, "/Applications/") {
		return ErrRealPathInTest
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	lib := filepath.Join(home, "Library")
	for _, bad := range []string{
		filepath.Join(lib, "Application Support", "Claude"),
		filepath.Join(lib, "Logs", "Claude"),
		filepath.Join(lib, "Caches", "com.anthropic"),
		filepath.Join(lib, "Application Support", "paritydesk"),
		filepath.Join(lib, "Logs", "paritydesk"),
	} {
		if strings.HasPrefix(p, bad) {
			return ErrRealPathInTest
		}
	}
	if p == filepath.Join(lib, "Application Support") || p == home {
		return ErrRealPathInTest
	}
	return nil
}
