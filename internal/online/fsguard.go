package online

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/kazupc/paritydesk/internal/fsx"
)

// errReadOnlyHook is returned by every mutating call a hook may not make.
var errReadOnlyHook = errors.New("online: hooks only create result, marker, denial and counter files")

// createOnlyDirs are the directories under the online folder in which
// hooks may create files.
var createOnlyDirs = []string{dirInflight, dirResults, dirCaptures, dirDenials, dirShown}

// CreateOnly wraps o for the hooks: WriteFileAtomic only into
// <dir>/.tmp/, RenameExcl only from <dir>/.tmp/ into the in-flight,
// results, captures, denials and shown directories, MkdirAll only of
// those directories (and .tmp); every other mutating method returns
// errReadOnlyHook. Reads pass through.
func CreateOnly(o fsx.Ops, dir string) fsx.Ops {
	return createOnly{o: o, dir: filepath.Clean(dir)}
}

type createOnly struct {
	o   fsx.Ops
	dir string
}

// under reports whether p lies inside dir/sub (sub itself excluded unless
// self is set).
func (c createOnly) under(p, sub string, self bool) bool {
	p = filepath.Clean(p)
	base := filepath.Join(c.dir, sub)
	return (self && p == base) || strings.HasPrefix(p, base+string(filepath.Separator))
}

func (c createOnly) inCreateDir(p string, self bool) bool {
	for _, d := range createOnlyDirs {
		if c.under(p, d, self) {
			return true
		}
	}
	return false
}

func (c createOnly) SwapDirs(string, string) error { return errReadOnlyHook }

func (c createOnly) RenameExcl(from, to string) error {
	if !c.under(from, dirTmp, false) || !c.inCreateDir(to, false) {
		return errReadOnlyHook
	}
	return c.o.RenameExcl(from, to)
}

func (c createOnly) Clone(string, string) error { return errReadOnlyHook }

// CloneFileAtomic, Mkdir, Symlink and RemoveEmptyDir write the data
// folders' scratch trees; hooks never do.
func (c createOnly) CloneFileAtomic(string, string, string, fs.FileMode, time.Time, string, string) error {
	return errReadOnlyHook
}
func (c createOnly) Mkdir(string, string, fs.FileMode) error { return errReadOnlyHook }
func (c createOnly) Symlink(string, string, string) error    { return errReadOnlyHook }
func (c createOnly) RemoveEmptyDir(string, string) error     { return errReadOnlyHook }

// BarrierSync is refused: hooks write through WriteFileAtomic, which
// syncs on its own.
func (c createOnly) BarrierSync(string) error { return errReadOnlyHook }

func (c createOnly) WriteFileAtomic(root, rel string, data []byte, perm fs.FileMode, tmpDir string) error {
	target := filepath.Join(root, rel)
	if !c.under(target, dirTmp, false) || !c.under(tmpDir, dirTmp, true) {
		return errReadOnlyHook
	}
	return c.o.WriteFileAtomic(root, rel, data, perm, tmpDir)
}

func (c createOnly) MoveInto(string, string, string) error { return errReadOnlyHook }

func (c createOnly) MkdirAll(p string, perm fs.FileMode) error {
	if filepath.Clean(p) != c.dir && !c.under(p, dirTmp, true) && !c.inCreateDir(p, true) {
		return errReadOnlyHook
	}
	return c.o.MkdirAll(p, perm)
}

func (c createOnly) RemoveAll(string, string) error             { return errReadOnlyHook }
func (c createOnly) FullSync(string) error                      { return nil }
func (c createOnly) ID(p string) (fsx.FileID, error)            { return c.o.ID(p) }
func (c createOnly) GetXattr(p, n string) ([]byte, bool, error) { return c.o.GetXattr(p, n) }
func (c createOnly) SetXattr(string, string, []byte) error      { return errReadOnlyHook }
func (c createOnly) FreeBytes(p string) (uint64, error)         { return c.o.FreeBytes(p) }
