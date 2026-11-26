//go:build darwin

package fsx

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// cloneTmpPrefix names CloneFileAtomic's temporary files (no extension,
// never an app temp-recovery name).
const cloneTmpPrefix = "pdc-"

// CloneFileAtomic implements Ops.CloneFileAtomic.
func (darwinOps) CloneFileAtomic(src, root, rel string, perm fs.FileMode, mtime time.Time, wantHash, tmpDir string) error {
	if _, err := Contained(root, rel); err != nil {
		return err
	}
	if !filepath.IsAbs(src) || filepath.Clean(src) != src {
		return fmt.Errorf("fsx: clone %s: source must be an absolute clean path", rel)
	}
	rel = filepath.Clean(rel)
	in, err := os.OpenFile(src, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, unix.ELOOP) {
			return fmt.Errorf("fsx: clone %s: %w", rel, ErrSourceChanged)
		}
		return fmt.Errorf("fsx: clone %s: %w", rel, err)
	}
	defer in.Close()
	if fi, err := in.Stat(); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("fsx: clone %s: %w", rel, ErrSourceChanged)
	}
	parent, err := openParent(root, rel, false)
	if err != nil {
		return fmt.Errorf("fsx: clone %s: %w", rel, err)
	}
	defer unix.Close(parent)
	base := filepath.Base(rel)
	if err := refuseNonFileAt(parent, base); err != nil {
		return fmt.Errorf("fsx: clone %s: %w", rel, err)
	}
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return fmt.Errorf("fsx: temp dir: %w", err)
	}
	tmpFd, err := unix.Open(tmpDir, dirOpen&^unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("fsx: open temp dir %s: %w", tmpDir, err)
	}
	defer unix.Close(tmpFd)
	name, err := tempName()
	if err != nil {
		return err
	}
	tmp := filepath.Join(tmpDir, name)
	if err := cloneOrCopy(in, src, tmp); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("fsx: clone %s: %w", rel, err)
	}
	if err := finishCopy(tmp, perm, mtime, wantHash); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("fsx: clone %s: %w", rel, err)
	}
	if err := unix.Renameat(tmpFd, name, parent, base); err != nil {
		_ = unix.Unlinkat(tmpFd, name, 0)
		return fmt.Errorf("fsx: clone %s: rename into place: %w", rel, err)
	}
	return nil
}

// tempName returns a fresh temporary file name.
func tempName() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return cloneTmpPrefix + hex.EncodeToString(b[:]), nil
}

// cloneOrCopy clones src to dst (which must not exist), or copies the
// bytes of the already opened source when the volume cannot clone.
func cloneOrCopy(in *os.File, src, dst string) error {
	err := unix.Clonefile(src, dst, cloneFlags)
	if err == nil {
		return nil
	}
	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ELOOP) {
		return ErrSourceChanged
	}
	if !errors.Is(err, unix.EXDEV) && !errors.Is(err, unix.ENOTSUP) {
		return &os.LinkError{Op: "clonefile", Old: src, New: dst, Err: err}
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	if _, err := in.Seek(0, io.SeekStart); err != nil {
		out.Close()
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// finishCopy verifies the copy's hash, sets its mode and mtime, and
// issues a barrier sync.
func finishCopy(tmp string, perm fs.FileMode, mtime time.Time, wantHash string) error {
	f, err := os.OpenFile(tmp, os.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return ErrSourceChanged
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != wantHash {
		return ErrHashMismatch
	}
	if err := f.Chmod(perm.Perm()); err != nil {
		return err
	}
	ts := []unix.Timespec{unix.NsecToTimespec(mtime.UnixNano()), unix.NsecToTimespec(mtime.UnixNano())}
	if err := unix.UtimesNanoAt(unix.AT_FDCWD, tmp, ts, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	return barrierSync(int(f.Fd()))
}

// refuseNonFileAt allows a missing name or a regular file inside dirfd;
// a symlink gives ErrSymlink and anything else an error.
func refuseNonFileAt(dirfd int, name string) error {
	var st unix.Stat_t
	if err := unix.Fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return &os.PathError{Op: "fstatat", Path: name, Err: err}
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		return ErrSymlink
	case unix.S_IFREG:
		return nil
	}
	return &os.PathError{Op: "replace", Path: name, Err: unix.EISDIR}
}

// Mkdir implements Ops.Mkdir.
func (darwinOps) Mkdir(root, rel string, perm fs.FileMode) error {
	if _, err := Contained(root, rel); err != nil {
		return err
	}
	rel = filepath.Clean(rel)
	parent, err := openParent(root, rel, false)
	if err != nil {
		return fmt.Errorf("fsx: mkdir %s: %w", rel, err)
	}
	defer unix.Close(parent)
	base := filepath.Base(rel)
	if err := unix.Mkdirat(parent, base, 0o700); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("fsx: mkdir %s: %w", rel, fs.ErrExist)
		}
		return fmt.Errorf("fsx: mkdir %s: %w", rel, err)
	}
	fd, err := unix.Openat(parent, base, dirOpen, 0)
	if err != nil {
		return fmt.Errorf("fsx: mkdir %s: %w", rel, err)
	}
	defer unix.Close(fd)
	if err := unix.Fchmod(fd, uint32(perm.Perm())); err != nil {
		return fmt.Errorf("fsx: mkdir %s: %w", rel, err)
	}
	return nil
}

// Symlink implements Ops.Symlink.
func (darwinOps) Symlink(target, root, rel string) error {
	if _, err := Contained(root, rel); err != nil {
		return err
	}
	rel = filepath.Clean(rel)
	parent, err := openParent(root, rel, false)
	if err != nil {
		return fmt.Errorf("fsx: symlink %s: %w", rel, err)
	}
	defer unix.Close(parent)
	if err := unix.Symlinkat(target, parent, filepath.Base(rel)); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("fsx: symlink %s: %w", rel, fs.ErrExist)
		}
		return fmt.Errorf("fsx: symlink %s: %w", rel, err)
	}
	return nil
}

// RemoveEmptyDir implements Ops.RemoveEmptyDir.
func (darwinOps) RemoveEmptyDir(root, rel string) error {
	if _, err := Contained(root, rel); err != nil {
		return err
	}
	rel = filepath.Clean(rel)
	parent, err := openParent(root, rel, false)
	if err != nil {
		return fmt.Errorf("fsx: remove dir %s: %w", rel, err)
	}
	defer unix.Close(parent)
	base := filepath.Base(rel)
	var st unix.Stat_t
	if err := unix.Fstatat(parent, base, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("fsx: remove dir %s: %w", rel, err)
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		return fmt.Errorf("fsx: remove dir %s: %w", rel, ErrSymlink)
	case unix.S_IFDIR:
	default:
		return fmt.Errorf("fsx: remove dir %s: %w", rel, ErrNotEmpty)
	}
	if err := unix.Unlinkat(parent, base, unix.AT_REMOVEDIR); err != nil {
		if errors.Is(err, unix.ENOTEMPTY) || errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("fsx: remove dir %s: %w", rel, ErrNotEmpty)
		}
		return fmt.Errorf("fsx: remove dir %s: %w", rel, err)
	}
	return nil
}

// BarrierSync implements Ops.BarrierSync.
func (darwinOps) BarrierSync(path string) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return &os.PathError{Op: "open", Path: path, Err: err}
	}
	defer unix.Close(fd)
	if err := barrierSync(fd); err != nil {
		return &os.PathError{Op: "F_BARRIERFSYNC", Path: path, Err: err}
	}
	return nil
}
