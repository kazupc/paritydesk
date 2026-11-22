//go:build darwin

package fsx

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Default returns the macOS implementation.
func Default() Ops { return darwinOps{} }

type darwinOps struct{}

// Flags used by the implementation.
const (
	renameSwap    = unix.RENAME_SWAP
	renameExcl    = unix.RENAME_EXCL
	fFullFsync    = unix.F_FULLFSYNC
	fBarrierFsync = unix.F_BARRIERFSYNC
	// cloneFlags: never copy the owner, and clone a symlink itself rather
	// than its target.
	cloneFlags = unix.CLONE_NOOWNERCOPY | unix.CLONE_NOFOLLOW
	// dirOpen opens one path component as a directory, never following a
	// symlink.
	dirOpen = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	// tmpPrefix names WriteFileAtomic's temporary files. It has no
	// extension and never matches an app temp-recovery glob (D30).
	tmpPrefix = "pdw-"
)

// SwapDirs exchanges a and b with renamex_np(RENAME_SWAP).
func (darwinOps) SwapDirs(a, b string) error {
	err := unix.RenamexNp(a, b, renameSwap)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, unix.ENOTSUP), errors.Is(err, unix.EINVAL), errors.Is(err, unix.ENOTTY):
		return fmt.Errorf("fsx: swap %s and %s: %w", a, b, ErrSwapUnsupported)
	default:
		return &os.LinkError{Op: "renamex_np swap", Old: a, New: b, Err: err}
	}
}

// RenameExcl renames with renamex_np(RENAME_EXCL). An existing destination
// gives an error matching fs.ErrExist (EEXIST does).
func (darwinOps) RenameExcl(from, to string) error {
	if err := unix.RenamexNp(from, to, renameExcl); err != nil {
		return &os.LinkError{Op: "renamex_np excl", Old: from, New: to, Err: err}
	}
	return nil
}

// Clone clones src to dst with clonefile, falling back to a byte copy that
// keeps modes when the volume cannot clone (EXDEV, ENOTSUP).
func (darwinOps) Clone(src, dst string) error {
	err := unix.Clonefile(src, dst, cloneFlags)
	if err == nil {
		return nil
	}
	if !errors.Is(err, unix.EXDEV) && !errors.Is(err, unix.ENOTSUP) {
		return &os.LinkError{Op: "clonefile", Old: src, New: dst, Err: err}
	}
	if err := copyTree(src, dst); err != nil {
		_ = os.RemoveAll(dst) // dst did not exist before; drop the partial copy
		return fmt.Errorf("fsx: copy %s: %w", src, err)
	}
	return nil
}

// copyTree copies a file, symlink or directory tree without following
// symlinks, keeping permission bits. dst must not exist.
func copyTree(src, dst string) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	case fi.IsDir():
		if err := os.Mkdir(dst, fi.Mode().Perm()|0o700); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return os.Chmod(dst, fi.Mode().Perm())
	case fi.Mode().IsRegular():
		return copyFile(src, dst, fi.Mode().Perm())
	default:
		return fmt.Errorf("fsx: cannot copy special file %s", src)
	}
}

func copyFile(src, dst string, perm fs.FileMode) error {
	in, err := os.OpenFile(src, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Chmod(perm); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// WriteFileAtomic implements Ops.WriteFileAtomic.
func (darwinOps) WriteFileAtomic(root, rel string, data []byte, perm fs.FileMode, tmpDir string) error {
	if _, err := Contained(root, rel); err != nil {
		return err
	}
	rel = filepath.Clean(rel)
	parent, err := openParent(root, rel, true)
	if err != nil {
		return fmt.Errorf("fsx: write %s: %w", rel, err)
	}
	defer unix.Close(parent)
	base := filepath.Base(rel)
	if err := refuseSymlinkAt(parent, base); err != nil {
		return fmt.Errorf("fsx: write %s: %w", rel, err)
	}
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return fmt.Errorf("fsx: temp dir: %w", err)
	}
	tmpFd, err := unix.Open(tmpDir, dirOpen&^unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("fsx: open temp dir %s: %w", tmpDir, err)
	}
	defer unix.Close(tmpFd)
	name, err := writeTemp(tmpDir, data, perm)
	if err != nil {
		return fmt.Errorf("fsx: write %s: %w", rel, err)
	}
	if err := unix.Renameat(tmpFd, name, parent, base); err != nil {
		_ = unix.Unlinkat(tmpFd, name, 0)
		return fmt.Errorf("fsx: write %s: rename into place: %w", rel, err)
	}
	return nil
}

// writeTemp creates a temporary file in dir, writes data, sets perm and
// issues a barrier sync. It returns the file's base name; on error the
// file is removed.
func writeTemp(dir string, data []byte, perm fs.FileMode) (string, error) {
	f, err := os.CreateTemp(dir, tmpPrefix+"*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	fail := func(err error) (string, error) {
		f.Close()
		_ = os.Remove(name)
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Chmod(perm.Perm()); err != nil {
		return fail(err)
	}
	if err := barrierSync(int(f.Fd())); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return filepath.Base(name), nil
}

// barrierSync orders the file's writes before later writes (F_BARRIERFSYNC),
// falling back to fsync where the volume does not support it.
func barrierSync(fd int) error {
	if _, err := unix.FcntlInt(uintptr(fd), fBarrierFsync, 0); err != nil {
		if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTTY) {
			return unix.Fsync(fd)
		}
		return err
	}
	return nil
}

// openParent opens root and walks rel's parent components without
// following symlinks, returning an fd of rel's parent directory. With
// create, missing components are made with mode 0700.
func openParent(root, rel string, create bool) (int, error) {
	fd, err := unix.Open(root, dirOpen&^unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, &os.PathError{Op: "open", Path: root, Err: err}
	}
	dir := filepath.Dir(rel)
	if dir == "." {
		return fd, nil
	}
	for _, comp := range strings.Split(dir, string(filepath.Separator)) {
		next, err := openDirAt(fd, comp, create)
		unix.Close(fd)
		if err != nil {
			return -1, err
		}
		fd = next
	}
	return fd, nil
}

// openDirAt opens the directory name inside dirfd without following a
// symlink, creating it with mode 0700 when create is set and it is missing.
func openDirAt(dirfd int, name string, create bool) (int, error) {
	fd, err := unix.Openat(dirfd, name, dirOpen, 0)
	if err == nil {
		return fd, nil
	}
	if errors.Is(err, unix.ENOENT) && create {
		if err := unix.Mkdirat(dirfd, name, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
			return -1, &os.PathError{Op: "mkdirat", Path: name, Err: err}
		}
		fd, err = unix.Openat(dirfd, name, dirOpen, 0)
		if err == nil {
			return fd, nil
		}
	}
	if serr := refuseSymlinkAt(dirfd, name); serr != nil {
		return -1, serr
	}
	return -1, &os.PathError{Op: "openat", Path: name, Err: err}
}

// refuseSymlinkAt returns ErrSymlink if name inside dirfd is a symlink.
// A missing name is fine.
func refuseSymlinkAt(dirfd int, name string) error {
	var st unix.Stat_t
	if err := unix.Fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return &os.PathError{Op: "fstatat", Path: name, Err: err}
	}
	if st.Mode&unix.S_IFMT == unix.S_IFLNK {
		return ErrSymlink
	}
	return nil
}

// MoveInto implements Ops.MoveInto. The destination must not exist.
func (darwinOps) MoveInto(root, rel, dstDir string) error {
	if _, err := Contained(root, rel); err != nil {
		return err
	}
	if _, err := Contained(dstDir, rel); err != nil {
		return err
	}
	rel = filepath.Clean(rel)
	src, err := openParent(root, rel, false)
	if err != nil {
		return fmt.Errorf("fsx: move %s: %w", rel, err)
	}
	defer unix.Close(src)
	dst, err := openParent(dstDir, rel, true)
	if err != nil {
		return fmt.Errorf("fsx: move %s: %w", rel, err)
	}
	defer unix.Close(dst)
	base := filepath.Base(rel)
	if err := unix.RenameatxNp(src, base, dst, base, renameExcl); err != nil {
		return fmt.Errorf("fsx: move %s: %w", rel, err)
	}
	return nil
}

// MkdirAll creates path and its parents with perm.
func (darwinOps) MkdirAll(path string, perm fs.FileMode) error {
	return os.MkdirAll(path, perm)
}

// RemoveAll removes root/rel after CheckRemovable. The walk to rel refuses
// symlinks, and the removal itself works on directory fds and never
// follows a symlink, so nothing outside the tree can be removed.
func (darwinOps) RemoveAll(root, rel string) error {
	if _, err := CheckRemovable(root, rel); err != nil {
		return err
	}
	rel = filepath.Clean(rel)
	parent, err := openParent(root, rel, false)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return fmt.Errorf("fsx: remove %s: %w", rel, err)
	}
	defer unix.Close(parent)
	if err := removeAt(parent, filepath.Base(rel)); err != nil {
		return fmt.Errorf("fsx: remove %s: %w", rel, err)
	}
	return nil
}

// removeAt removes name inside dirfd recursively without following
// symlinks. A missing name is not an error.
func removeAt(dirfd int, name string) error {
	err := unix.Unlinkat(dirfd, name, 0)
	if err == nil || errors.Is(err, unix.ENOENT) {
		return nil
	}
	fd, oerr := unix.Openat(dirfd, name, dirOpen, 0)
	if oerr != nil {
		// Not a directory (or not openable): report the unlink error.
		return &os.PathError{Op: "unlinkat", Path: name, Err: err}
	}
	if err := removeChildren(fd, name); err != nil {
		return err
	}
	if err := unix.Unlinkat(dirfd, name, unix.AT_REMOVEDIR); err != nil && !errors.Is(err, unix.ENOENT) {
		return &os.PathError{Op: "rmdir", Path: name, Err: err}
	}
	return nil
}

// removeChildren empties the directory fd and closes it.
func removeChildren(fd int, name string) error {
	dupFd, err := unix.Dup(fd)
	if err != nil {
		unix.Close(fd)
		return &os.PathError{Op: "dup", Path: name, Err: err}
	}
	f := os.NewFile(uintptr(dupFd), name)
	defer unix.Close(fd)
	defer f.Close()
	names, err := f.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, n := range names {
		if err := removeAt(fd, n); err != nil {
			return err
		}
	}
	return nil
}

// FullSync issues F_FULLFSYNC on a file or directory, falling back to
// fsync where the volume does not support it.
func (darwinOps) FullSync(path string) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return &os.PathError{Op: "open", Path: path, Err: err}
	}
	defer unix.Close(fd)
	if _, err := unix.FcntlInt(uintptr(fd), fFullFsync, 0); err != nil {
		if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTTY) {
			if err := unix.Fsync(fd); err != nil {
				return &os.PathError{Op: "fsync", Path: path, Err: err}
			}
			return nil
		}
		return &os.PathError{Op: "F_FULLFSYNC", Path: path, Err: err}
	}
	return nil
}

// ID returns device, inode and birth time (lstat).
func (darwinOps) ID(path string) (FileID, error) {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return FileID{}, &os.PathError{Op: "lstat", Path: path, Err: err}
	}
	return FileID{
		Dev:   uint64(uint32(st.Dev)),
		Ino:   st.Ino,
		Birth: time.Unix(st.Btim.Sec, st.Btim.Nsec).UTC(),
	}, nil
}

// GetXattr reads an extended attribute without following symlinks.
func (darwinOps) GetXattr(path, name string) ([]byte, bool, error) {
	for range 4 {
		n, err := unix.Lgetxattr(path, name, nil)
		if err != nil {
			if errors.Is(err, unix.ENOATTR) {
				return nil, false, nil
			}
			return nil, false, &os.PathError{Op: "getxattr", Path: path, Err: err}
		}
		buf := make([]byte, n)
		m, err := unix.Lgetxattr(path, name, buf)
		switch {
		case err == nil:
			return buf[:m], true, nil
		case errors.Is(err, unix.ERANGE):
			continue // grew between the calls
		case errors.Is(err, unix.ENOATTR):
			return nil, false, nil
		default:
			return nil, false, &os.PathError{Op: "getxattr", Path: path, Err: err}
		}
	}
	return nil, false, &os.PathError{Op: "getxattr", Path: path, Err: unix.ERANGE}
}

// SetXattr writes an extended attribute without following symlinks.
func (darwinOps) SetXattr(path, name string, data []byte) error {
	if err := unix.Lsetxattr(path, name, data, 0); err != nil {
		return &os.PathError{Op: "setxattr", Path: path, Err: err}
	}
	return nil
}

// FreeBytes returns Bavail*Bsize of path's volume.
func (darwinOps) FreeBytes(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, &os.PathError{Op: "statfs", Path: path, Err: err}
	}
	return st.Bavail * uint64(st.Bsize), nil
}
