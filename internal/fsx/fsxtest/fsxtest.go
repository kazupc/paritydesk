package fsxtest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/fsx"
)

type inode struct{ dev, ino uint64 }

// FS is an fsx.Ops for tests. It is bound to the test's temporary
// directory tree: any path argument outside it fails the test and returns
// fsx.ErrOutsideConfinement, so a mis-wired test can never touch real
// folders.
type FS struct {
	t      testing.TB
	root   string
	mu     sync.Mutex
	xattrs map[inode]map[string][]byte
	births map[inode]time.Time
	faults map[string][]error
	// Calls logs every call as "Op path".
	Calls []string
	// NoSwap makes SwapDirs return fsx.ErrSwapUnsupported.
	NoSwap bool
	// Free is returned by FreeBytes.
	Free uint64
	// Now stamps new birth times.
	Now func() time.Time
	// CloneUnsupported makes CloneFileOnly return fsx.ErrUnsupported, as
	// on a volume that cannot clone.
	CloneUnsupported bool
}

var (
	_ fsx.Ops        = (*FS)(nil)
	_ fsx.FileCloner = (*FS)(nil)
)

// New returns an FS with 1 TiB free, bound to the parent directory of
// t.TempDir(), which holds every directory t.TempDir() returns for this
// test.
func New(t testing.TB) *FS {
	t.Helper()
	return &FS{
		t:      t,
		root:   filepath.Dir(t.TempDir()),
		xattrs: map[inode]map[string][]byte{},
		births: map[inode]time.Time{},
		faults: map[string][]error{},
		Free:   1 << 40,
		Now:    time.Now,
	}
}

// Fail makes the next call of op (for example "SwapDirs") return err.
// Several calls queue several faults.
func (f *FS) Fail(op string, err error) {
	f.mu.Lock()
	f.faults[op] = append(f.faults[op], err)
	f.mu.Unlock()
}

// Root returns the directory every path must stay under.
func (f *FS) Root() string { return f.root }

func (f *FS) inside(p string) bool {
	p = filepath.Clean(p)
	return filepath.IsAbs(p) && (p == f.root || strings.HasPrefix(p, f.root+string(filepath.Separator)))
}

// check fails the test for absolute paths outside the bound root.
func (f *FS) check(op string, paths ...string) error {
	for _, p := range paths {
		if !f.inside(p) {
			f.t.Errorf("fsxtest: %s outside the test root: %s", op, p)
			return fsx.ErrOutsideConfinement
		}
	}
	return nil
}

// enter checks paths, logs "op logArg" and pops a queued fault.
func (f *FS) enter(op, logArg string, paths ...string) error {
	if err := f.check(op, paths...); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, op+" "+logArg)
	if q := f.faults[op]; len(q) > 0 {
		f.faults[op] = q[1:]
		return q[0]
	}
	return nil
}

func statInode(path string) (inode, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return inode{}, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return inode{}, errors.New("fsxtest: no stat_t")
	}
	return inode{dev: uint64(st.Dev), ino: uint64(st.Ino)}, nil //nolint:unconvert // Dev differs by platform
}

// SwapDirs exchanges a and b with three renames (not atomic; tests only).
func (f *FS) SwapDirs(a, b string) error {
	if err := f.enter("SwapDirs", a, a, b); err != nil {
		return err
	}
	if f.NoSwap {
		return fsx.ErrSwapUnsupported
	}
	tmp := a + ".fsxtest-swap"
	if err := os.Rename(a, tmp); err != nil {
		return err
	}
	if err := os.Rename(b, a); err != nil {
		_ = os.Rename(tmp, a)
		return err
	}
	return os.Rename(tmp, b)
}

// RenameExcl renames unless to exists.
func (f *FS) RenameExcl(from, to string) error {
	if err := f.enter("RenameExcl", from, from, to); err != nil {
		return err
	}
	if _, err := os.Lstat(to); err == nil {
		return fs.ErrExist
	}
	return os.Rename(from, to)
}

// Clone copies a file or tree.
func (f *FS) Clone(src, dst string) error {
	if err := f.enter("Clone", src, src, dst); err != nil {
		return err
	}
	if _, err := os.Lstat(dst); err == nil {
		return fs.ErrExist
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm())
		case d.Type()&fs.ModeSymlink != 0:
			l, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(l, target)
		default:
			return copyFile(p, target, info.Mode().Perm())
		}
	})
}

// CloneFileOnly copies one regular file (a test stand-in for clonefile)
// unless CloneUnsupported is set. The call is logged as
// "CloneFileOnly <src>", so tests can check which sources were opened.
func (f *FS) CloneFileOnly(src, dst string) error {
	if err := f.enter("CloneFileOnly", src, src, dst); err != nil {
		return err
	}
	if f.CloneUnsupported {
		return fsx.ErrUnsupported
	}
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fsx.ErrNotRegular
	}
	if _, err := os.Lstat(dst); err == nil {
		return fs.ErrExist
	}
	return copyFile(src, dst, fi.Mode().Perm())
}

func copyFile(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func noSymlinks(root, rel string) error {
	cur := root
	parts := []string{}
	for d := filepath.Dir(rel); d != "." && d != "/"; d = filepath.Dir(d) {
		parts = append([]string{filepath.Base(d)}, parts...)
	}
	for _, p := range parts {
		cur = filepath.Join(cur, p)
		fi, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fsx.ErrSymlink
		}
	}
	return nil
}

// WriteFileAtomic writes through a temp file in tmpDir and renames.
func (f *FS) WriteFileAtomic(root, rel string, data []byte, perm fs.FileMode, tmpDir string) error {
	if err := f.enter("WriteFileAtomic", rel, root, tmpDir); err != nil {
		return err
	}
	target, err := fsx.Contained(root, rel)
	if err != nil {
		return err
	}
	if err := noSymlinks(root, rel); err != nil {
		return err
	}
	if fi, err := os.Lstat(target); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
		return fsx.ErrSymlink
	}
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(tmpDir, "w-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}

// MoveInto moves root/rel to dstDir/rel. Symlinked components are
// refused and an existing destination gives fs.ErrExist.
func (f *FS) MoveInto(root, rel, dstDir string) error {
	if err := f.enter("MoveInto", rel, root, dstDir); err != nil {
		return err
	}
	src, err := fsx.Contained(root, rel)
	if err != nil {
		return err
	}
	dst, err := fsx.Contained(dstDir, rel)
	if err != nil {
		return err
	}
	if err := noSymlinks(root, rel); err != nil {
		return err
	}
	if err := noSymlinks(dstDir, rel); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if _, err := os.Lstat(dst); err == nil {
		return fs.ErrExist
	}
	return os.Rename(src, dst)
}

// MkdirAll creates directories.
func (f *FS) MkdirAll(path string, perm fs.FileMode) error {
	if err := f.enter("MkdirAll", path, path); err != nil {
		return err
	}
	return os.MkdirAll(path, perm)
}

// RemoveAll removes root/rel after fsx.CheckRemovable, refusing
// symlinked components on the way to rel.
func (f *FS) RemoveAll(root, rel string) error {
	if err := f.enter("RemoveAll", rel, root); err != nil {
		return err
	}
	p, err := fsx.CheckRemovable(root, rel)
	if err != nil {
		return err
	}
	if err := noSymlinks(root, rel); err != nil {
		return err
	}
	return os.RemoveAll(p)
}

// FullSync only logs the call.
func (f *FS) FullSync(path string) error { return f.enter("FullSync", path, path) }

// ID returns device, inode and a stable fake birth time.
func (f *FS) ID(path string) (fsx.FileID, error) {
	if err := f.enter("ID", path, path); err != nil {
		return fsx.FileID{}, err
	}
	in, err := statInode(path)
	if err != nil {
		return fsx.FileID{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.births[in]
	if !ok {
		b = f.Now().UTC().Truncate(time.Second)
		f.births[in] = b
	}
	return fsx.FileID{Dev: in.dev, Ino: in.ino, Birth: b}, nil
}

// GetXattr reads an in-memory attribute.
func (f *FS) GetXattr(path, name string) ([]byte, bool, error) {
	if err := f.enter("GetXattr", path, path); err != nil {
		return nil, false, err
	}
	in, err := statInode(path)
	if err != nil {
		return nil, false, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.xattrs[in][name]
	return append([]byte(nil), v...), ok, nil
}

// SetXattr writes an in-memory attribute.
func (f *FS) SetXattr(path, name string, data []byte) error {
	if err := f.enter("SetXattr", path, path); err != nil {
		return err
	}
	in, err := statInode(path)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.xattrs[in] == nil {
		f.xattrs[in] = map[string][]byte{}
	}
	f.xattrs[in][name] = append([]byte(nil), data...)
	return nil
}

// FreeBytes returns f.Free.
func (f *FS) FreeBytes(path string) (uint64, error) {
	if err := f.enter("FreeBytes", path, path); err != nil {
		return 0, err
	}
	return f.Free, nil
}

// String summarizes the call log for test failure messages.
func (f *FS) String() string { return fmt.Sprintf("fsxtest.FS{%d calls}", len(f.Calls)) }

// CloneFileAtomic copies src through a temp file in tmpDir, verifies its
// hash, sets perm and mtime, and renames it into place (parent must
// exist). Symlinks in the source's last component or under root are
// refused.
func (f *FS) CloneFileAtomic(src, root, rel string, perm fs.FileMode, mtime time.Time, wantHash, tmpDir string) error {
	if err := f.enter("CloneFileAtomic", rel, src, root, tmpDir); err != nil {
		return err
	}
	target, err := fsx.Contained(root, rel)
	if err != nil {
		return err
	}
	if fi, err := os.Lstat(src); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("fsxtest: clone %s: %w", rel, fsx.ErrSourceChanged)
	}
	if err := noSymlinks(root, rel); err != nil {
		return err
	}
	if fi, err := os.Lstat(filepath.Dir(target)); err != nil || !fi.IsDir() {
		return fmt.Errorf("fsxtest: clone %s: parent missing", rel)
	}
	if fi, err := os.Lstat(target); err == nil && !fi.Mode().IsRegular() {
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fsx.ErrSymlink
		}
		return fmt.Errorf("fsxtest: clone %s: target is not a regular file", rel)
	}
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(tmpDir, "c-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		_ = os.Remove(name)
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return fail(fsx.ErrSourceChanged)
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(tmp, h), in)
	in.Close()
	if err != nil {
		return fail(err)
	}
	if hex.EncodeToString(h.Sum(nil)) != wantHash {
		return fail(fmt.Errorf("fsxtest: clone %s: %w", rel, fsx.ErrHashMismatch))
	}
	if err := tmp.Chmod(perm.Perm()); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Chtimes(name, mtime, mtime); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Rename(name, target)
}

// Mkdir creates root/rel with exactly perm; the parent must exist.
func (f *FS) Mkdir(root, rel string, perm fs.FileMode) error {
	if err := f.enter("Mkdir", rel, root); err != nil {
		return err
	}
	target, err := fsx.Contained(root, rel)
	if err != nil {
		return err
	}
	if err := noSymlinks(root, rel); err != nil {
		return err
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		return err
	}
	return os.Chmod(target, perm.Perm())
}

// Symlink creates root/rel -> target.
func (f *FS) Symlink(target, root, rel string) error {
	if err := f.enter("Symlink", rel, root); err != nil {
		return err
	}
	p, err := fsx.Contained(root, rel)
	if err != nil {
		return err
	}
	if err := noSymlinks(root, rel); err != nil {
		return err
	}
	return os.Symlink(target, p)
}

// RemoveEmptyDir removes root/rel only when it is an empty directory.
func (f *FS) RemoveEmptyDir(root, rel string) error {
	if err := f.enter("RemoveEmptyDir", rel, root); err != nil {
		return err
	}
	p, err := fsx.Contained(root, rel)
	if err != nil {
		return err
	}
	if err := noSymlinks(root, rel); err != nil {
		return err
	}
	fi, err := os.Lstat(p)
	switch {
	case err != nil:
		return err
	case fi.Mode()&fs.ModeSymlink != 0:
		return fsx.ErrSymlink
	case !fi.IsDir():
		return fsx.ErrNotEmpty
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fsx.ErrNotEmpty
	}
	return os.Remove(p)
}

// BarrierSync only logs the call.
func (f *FS) BarrierSync(path string) error { return f.enter("BarrierSync", path, path) }
