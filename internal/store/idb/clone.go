package idb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"github.com/kazupc/paritydesk/internal/fsx"
)

// tempDirRE names the clone directories this package makes under its
// temporary root.
var tempDirRE = regexp.MustCompile(`^idb-[0-9a-f]{16}$`)

// cloneDirName is the database directory inside one temporary directory.
const cloneDirName = "db"

// sweepAge is how old a leftover clone directory must be before
// SweepTemp removes it.
const sweepAge = time.Hour

// Cloner reads from a private per-file clone of the database directory.
type Cloner struct {
	tempRoot string
	fs       fsx.Ops // confined to tempRoot
	now      func() time.Time
}

// NewCloner confines ops to tempRoot (fsx.Confine) and refuses a real-home
// tempRoot under `go test` (fsx.CheckTestPath), like leveldb.New. ops must
// be the unwrapped platform implementation: clones are paritydesk's own
// temporary files, so they work in read-only mode too.
func NewCloner(tempRoot string, ops fsx.Ops) (*Cloner, error) {
	if err := fsx.CheckTestPath(tempRoot); err != nil {
		return nil, err
	}
	return &Cloner{tempRoot: filepath.Clean(tempRoot), fs: fsx.Confine(ops, tempRoot), now: time.Now}, nil
}

// fileStamp is one recorded source file.
type fileStamp struct {
	size  int64
	mtime time.Time
}

// listSource records every regular file of dir except LOCK and LOG*. A
// subdirectory or symlink is ErrUnsupported; more than maxFiles entries
// ErrTooLarge.
func listSource(dir string, maxFiles int) (map[string]fileStamp, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("idb: list: %w", stripPath(err))
	}
	if maxFiles > 0 && len(entries) > maxFiles {
		return nil, fail("list", ErrTooLarge)
	}
	out := map[string]fileStamp{}
	for _, e := range entries {
		if skipFile(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, fail("list", ErrTorn)
			}
			return nil, fmt.Errorf("idb: list: %w", stripPath(err))
		}
		if !fi.Mode().IsRegular() {
			return nil, fail("list "+e.Name(), ErrUnsupported)
		}
		out[e.Name()] = fileStamp{size: fi.Size(), mtime: fi.ModTime()}
	}
	return out, nil
}

// Read clones folder/spec.Dir into <tempRoot>/idb-<16 hex>/db, reads the
// clone with ReadString, and removes the temporary directory (also on
// error and on ctx cancellation). It satisfies Reader.
func (c *Cloner) Read(ctx context.Context, folder string, spec Spec) (v Value, err error) {
	src, err := checkDir(folder, spec)
	if err != nil {
		return Value{}, err
	}
	before, err := listSource(src, spec.MaxFiles)
	if err != nil {
		return Value{}, err
	}
	var total int64
	for _, st := range before {
		total += st.size
	}
	if spec.MaxCloneBytes > 0 && total > spec.MaxCloneBytes {
		return Value{}, fail("clone", ErrTooLarge)
	}
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return Value{}, fmt.Errorf("idb: temp name: %w", err)
	}
	name := "idb-" + hex.EncodeToString(rnd[:])
	tmp := filepath.Join(c.tempRoot, name)
	dst := filepath.Join(tmp, cloneDirName)
	if err := c.fs.MkdirAll(dst, 0o700); err != nil {
		return Value{}, errors.Join(fmt.Errorf("idb: temp dir: %w", stripPath(err)), c.fs.RemoveAll(c.tempRoot, name))
	}
	defer func() {
		if rerr := c.fs.RemoveAll(c.tempRoot, name); rerr != nil {
			err = errors.Join(err, fmt.Errorf("idb: remove temp dir: %w", stripPath(rerr)))
		}
	}()
	copied := false
	first := true
	for fname := range before {
		if err := ctx.Err(); err != nil {
			return Value{}, err
		}
		from, to := filepath.Join(src, fname), filepath.Join(dst, fname)
		if !copied {
			cerr := fsx.CloneFileOnly(c.fs, from, to)
			switch {
			case cerr == nil:
				first = false
				continue
			case first && errors.Is(cerr, fsx.ErrUnsupported):
				if spec.MaxCopyBytes > 0 && total > spec.MaxCopyBytes {
					return Value{}, fail("copy", ErrTooLarge)
				}
				copied = true
			case errors.Is(cerr, fs.ErrNotExist):
				return Value{}, fail("clone "+fname, ErrTorn)
			default:
				return Value{}, fmt.Errorf("idb: clone %s: %w", fname, pathFree(cerr))
			}
		}
		if err := c.fs.Clone(from, to); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return Value{}, fail("copy "+fname, ErrTorn)
			}
			return Value{}, fmt.Errorf("idb: copy %s: %w", fname, pathFree(err))
		}
	}
	after, err := listSource(src, spec.MaxFiles)
	if err != nil {
		if errors.Is(err, ErrTooLarge) {
			return Value{}, fail("list", ErrTorn)
		}
		return Value{}, err
	}
	if !sameStamps(before, after) {
		return Value{}, fail("clone", ErrTorn)
	}
	cs := spec
	cs.Dir, cs.BackendPrefix = cloneDirName, ""
	v, err = ReadString(ctx, tmp, cs)
	if err != nil {
		return Value{}, err
	}
	v.Copied = copied
	return v, nil
}

// pathFree returns a clone or copy failure without absolute paths in its
// text (spec-pins 4.1): the base names of an *os.LinkError or
// *fs.PathError (stripPath); any other error keeps its chain for
// errors.Is and errors.As under a fixed text (the errno when there is
// one), since fsx may have put the source path into it.
func pathFree(err error) error {
	var le *os.LinkError
	var pe *fs.PathError
	if errors.As(err, &le) || errors.As(err, &pe) {
		return stripPath(err)
	}
	return &quietError{err: err}
}

// quietError hides the text of an error, keeping its chain.
type quietError struct{ err error }

func (e *quietError) Error() string {
	var en syscall.Errno
	if errors.As(e.err, &en) {
		return en.Error()
	}
	return "failed"
}

func (e *quietError) Unwrap() error { return e.err }

func sameStamps(a, b map[string]fileStamp) bool {
	if len(a) != len(b) {
		return false
	}
	for k, x := range a {
		y, ok := b[k]
		if !ok || x.size != y.size || !x.mtime.Equal(y.mtime) {
			return false
		}
	}
	return true
}

// SweepTemp removes leftover idb-* directories older than an hour (a
// crash between clone and removal leaves one).
func (c *Cloner) SweepTemp() error {
	entries, err := os.ReadDir(c.tempRoot)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("idb: sweep: %w", stripPath(err))
	}
	var errs []error
	for _, e := range entries {
		if !e.IsDir() || !tempDirRE.MatchString(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil || c.now().Sub(fi.ModTime()) < sweepAge {
			continue
		}
		if err := c.fs.RemoveAll(c.tempRoot, e.Name()); err != nil {
			errs = append(errs, fmt.Errorf("idb: sweep %s: %w", e.Name(), stripPath(err)))
		}
	}
	return errors.Join(errs...)
}
