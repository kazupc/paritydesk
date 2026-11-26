package artifacts

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Stage copies a blob into the outbox (5.8) and returns the copy's
// absolute path: an exclusive create of outbox/<sha><suffix> (0600),
// re-hashed afterwards; an existing file is re-hashed and reused. The
// outbox holds only pages of the publish round being built, so a granted
// folder never exposes the manifest, history or other pages.
func (a *Archive) Stage(sha string) (string, error) {
	content, err := a.ReadBlob(sha)
	if err != nil {
		return "", err
	}
	if !ValidSHA(sha) {
		return "", ErrBadName
	}
	rel := filepath.Join(dirOutbox, sha+a.outboxSuffix())
	if err := a.createExcl(rel, []byte(content)); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	if err := a.checkStaged(rel, sha); err != nil {
		return "", err
	}
	return filepath.Join(a.Root, rel), nil
}

// checkStaged checks a staged copy: a regular 0600 file whose bytes hash
// to sha.
func (a *Archive) checkStaged(rel, sha string) error {
	fi, err := os.Lstat(filepath.Join(a.Root, rel))
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		return ErrSymlink
	}
	b, err := a.read(rel, a.maxContent())
	if err != nil {
		return err
	}
	if b == nil || shaHex(b) != sha {
		return ErrBlobCorrupt
	}
	return nil
}

// Unstage removes a staged copy (missing is fine).
func (a *Archive) Unstage(sha string) error {
	if !ValidSHA(sha) {
		return ErrBadName
	}
	return a.remove(filepath.Join(dirOutbox, sha+a.outboxSuffix()))
}
