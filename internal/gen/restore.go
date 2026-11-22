package gen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/localstorage"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/store/kv"
)

// RestoreInput describes a conditional restore.
type RestoreInput struct {
	N      uint64
	Folder string    // the folder to restore into; the app must be stopped
	LS     kv.Writer // the folder's Local Storage, opened writable
	TmpDir string    // for atomic file writes
	DryRun bool
}

// RestoreReport lists what was restored and what conflicted.
type RestoreReport struct {
	Restored  []Entry `json:"restored"`
	Conflicts []Entry `json:"conflicts"` // current hash != post-hash
}

// notRegular is a current hash that matches no recorded hash: the path
// holds something other than a regular file.
const notRegular = "!not-a-regular-file"

// decision is what Restore does with one entry.
type decision int

const (
	atPre    decision = iota // already at its pre-run state: restored, nothing written
	atPost                   // at the generation's post state: restore it
	conflict                 // changed after the generation: never overwritten
)

func decide(e Entry, current string) decision {
	switch current {
	case e.PreHash:
		return atPre
	case e.PostHash:
		return atPost
	}
	return conflict
}

// restorer carries one Restore call.
type restorer struct {
	s     *Store
	m     *Manifest
	in    RestoreInput
	dir   string
	batch kv.Batch
}

// Restore restores every entry whose current hash equals its post-hash
// (files through WriteFileAtomic, LS values in one batch, quarantined
// files moved back). An entry with Present=false (created by apply) is
// restored by moving the new file into the generation's quarantine, never
// by deleting it. An entry whose current hash already equals its pre-hash
// counts as restored without writing, so Restore is idempotent and recover
// can re-run an interrupted rollback. Any other hash is a conflict and is
// never overwritten. With DryRun nothing is written and the report says
// what a real run would do.
func (s *Store) Restore(ctx context.Context, in RestoreInput) (RestoreReport, error) {
	rep := RestoreReport{Restored: []Entry{}, Conflicts: []Entry{}}
	if in.Folder == "" || !filepath.IsAbs(in.Folder) {
		return rep, errors.New("gen: restore: folder must be an absolute path")
	}
	m, err := s.Load(in.N)
	if err != nil {
		return rep, err
	}
	r := &restorer{s: s, m: m, in: in, dir: s.genDir(in.N)}
	for _, e := range m.Entries {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		var d decision
		switch e.Kind {
		case EntryFile, EntryQuarantine:
			d, err = r.file(e)
		case EntryLSValue:
			d, err = r.lsValue(e)
		default:
			continue
		}
		if err != nil {
			return rep, err
		}
		if d == conflict {
			rep.Conflicts = append(rep.Conflicts, e)
		} else {
			rep.Restored = append(rep.Restored, e)
		}
	}
	if r.batch.Len() > 0 && !in.DryRun {
		if err := in.LS.Write(&r.batch); err != nil {
			return rep, fmt.Errorf("gen: restore Local Storage values: %w", err)
		}
	}
	return rep, nil
}

// currentFileHash hashes folder/rel: "" when absent, notRegular for a
// symlink, directory or other non-regular entry.
func currentFileHash(path string) (string, error) {
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", nil
	case err != nil:
		return "", err
	case !fi.Mode().IsRegular():
		return notRegular, nil
	}
	return hashFile(path)
}

// file handles an EntryFile or EntryQuarantine entry.
func (r *restorer) file(e Entry) (decision, error) {
	target, err := fsx.Contained(r.in.Folder, e.Path)
	if err != nil {
		return conflict, fmt.Errorf("gen: restore %s: %w", e.Path, err)
	}
	cur, err := currentFileHash(target)
	if err != nil {
		return conflict, fmt.Errorf("gen: restore %s: %w", e.Path, err)
	}
	d := decide(e, cur)
	if d != atPost || r.in.DryRun {
		return d, nil
	}
	if !e.Present {
		return r.quarantineNew(e)
	}
	return atPost, r.putBack(e, cur == "")
}

// quarantineNew moves a file the run created into the generation's
// quarantine. If the quarantine already holds that path, the entry is a
// conflict rather than an overwrite.
func (r *restorer) quarantineNew(e Entry) (decision, error) {
	qdir := filepath.Join(r.dir, quarantineDir)
	if _, err := os.Lstat(filepath.Join(qdir, e.Path)); err == nil {
		return conflict, nil
	}
	if err := r.s.fs.MoveInto(r.in.Folder, e.Path, qdir); err != nil {
		return conflict, fmt.Errorf("gen: quarantine %s: %w", e.Path, err)
	}
	return atPost, nil
}

// putBack restores a file that existed before the run: moved back from
// the quarantine when apply moved it there (and the path is now free),
// otherwise rewritten from the stored clone.
func (r *restorer) putBack(e Entry, absent bool) error {
	qdir := filepath.Join(r.dir, quarantineDir)
	if absent {
		if h, err := currentFileHash(filepath.Join(qdir, e.Path)); err == nil && h == e.PreHash {
			if err := r.s.fs.MoveInto(qdir, e.Path, r.in.Folder); err != nil {
				return fmt.Errorf("gen: restore %s from quarantine: %w", e.Path, err)
			}
			return nil
		}
	}
	if e.Kind != EntryFile || e.Stored == "" {
		return fmt.Errorf("gen: restore %s: no stored copy", e.Path)
	}
	if r.in.TmpDir == "" {
		return errors.New("gen: restore: a temp directory is required for file writes")
	}
	src := filepath.Join(r.dir, e.Stored)
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("gen: restore %s: %w", e.Path, err)
	}
	if model.HashBytes(data) != e.PreHash {
		return fmt.Errorf("gen: restore %s: %w", e.Path, ErrTampered)
	}
	perm := fs.FileMode(filePerm)
	if fi, err := os.Stat(src); err == nil {
		perm = fi.Mode().Perm()
	}
	if err := r.s.fs.WriteFileAtomic(r.in.Folder, e.Path, data, perm, r.in.TmpDir); err != nil {
		return fmt.Errorf("gen: restore %s: %w", e.Path, err)
	}
	return nil
}

// lsValue handles an EntryLSValue entry; writes are queued in r.batch.
func (r *restorer) lsValue(e Entry) (decision, error) {
	if r.in.LS == nil {
		return conflict, errors.New("gen: restore: Local Storage is required for Local Storage entries")
	}
	key, err := localstorage.Key(e.Key)
	if err != nil {
		return conflict, fmt.Errorf("gen: restore Local Storage value: %w", err)
	}
	raw, found, err := r.in.LS.Get(key)
	if err != nil {
		return conflict, fmt.Errorf("gen: restore Local Storage value: %w", err)
	}
	cur := ""
	if found {
		cur = model.HashBytes(raw)
	}
	d := decide(e, cur)
	if d != atPost || r.in.DryRun {
		return d, nil
	}
	if !e.Present {
		r.batch.Delete(key)
		return atPost, nil
	}
	rec, err := r.readLS(e)
	if err != nil {
		return conflict, err
	}
	r.batch.Put(key, rec.Raw)
	return atPost, nil
}

// readLS reads a stored Local Storage record and checks it belongs to e.
func (r *restorer) readLS(e Entry) (*lsRecord, error) {
	b, err := os.ReadFile(filepath.Join(r.dir, e.Stored))
	if err != nil {
		return nil, fmt.Errorf("gen: restore Local Storage value: %w", err)
	}
	rec := &lsRecord{}
	if err := json.Unmarshal(b, rec); err != nil {
		return nil, fmt.Errorf("gen: restore Local Storage value: %w", err)
	}
	if rec.Name != e.Key || !rec.Present || model.HashBytes(rec.Raw) != e.PreHash {
		return nil, fmt.Errorf("gen: restore Local Storage value: %w", ErrTampered)
	}
	return rec, nil
}

// RestoreLevelDB replaces the folder's LevelDB directory with the
// generation's full clone (used when apply fails after a writable open).
// The caller guarantees quiescence. The clone is copied to a staging
// directory and swapped in, so the generation keeps its clone; the
// replaced directory is kept inside the generation, never deleted.
func (s *Store) RestoreLevelDB(ctx context.Context, n uint64, folder string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m, err := s.Load(n)
	if err != nil {
		return err
	}
	if m.LevelDB == "" {
		return fmt.Errorf("gen: generation %d: %w", n, ErrNoLevelDBClone)
	}
	target, err := fsx.Contained(folder, levelDBRel)
	if err != nil || !filepath.IsAbs(folder) {
		return errors.New("gen: restore LevelDB: folder must be an absolute path")
	}
	dir := s.genDir(n)
	staging := filepath.Join(dir, stagingDir)
	if err := s.remove(filepath.Join(name(n), stagingDir)); err != nil {
		return err
	}
	if err := s.fs.Clone(filepath.Join(dir, m.LevelDB), staging); err != nil {
		return fmt.Errorf("gen: restore LevelDB: stage clone: %w", err)
	}
	if err := s.swapIn(staging, target, dir); err != nil {
		return err
	}
	for _, p := range []string{filepath.Dir(target), dir} {
		if err := s.fs.FullSync(p); err != nil {
			return fmt.Errorf("gen: restore LevelDB: %w", err)
		}
	}
	return nil
}

// swapIn puts staging at target. An existing target is moved into the
// generation directory (as leveldb-replaced-<k>).
func (s *Store) swapIn(staging, target, genDir string) error {
	if _, err := os.Lstat(target); errors.Is(err, fs.ErrNotExist) {
		if err := s.fs.RenameExcl(staging, target); err != nil {
			return fmt.Errorf("gen: restore LevelDB: %w", err)
		}
		return nil
	}
	keep, err := freeName(genDir, replacedDir)
	if err != nil {
		return err
	}
	err = s.fs.SwapDirs(target, staging)
	switch {
	case err == nil:
		if err := s.fs.RenameExcl(staging, keep); err != nil {
			return fmt.Errorf("gen: restore LevelDB: keep replaced directory: %w", err)
		}
		return nil
	case errors.Is(err, fsx.ErrSwapUnsupported):
		if err := s.fs.RenameExcl(target, keep); err != nil {
			return fmt.Errorf("gen: restore LevelDB: move replaced directory: %w", err)
		}
		if err := s.fs.RenameExcl(staging, target); err != nil {
			return fmt.Errorf("gen: restore LevelDB: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("gen: restore LevelDB: swap: %w", err)
	}
}

// freeName returns dir/<base>-<k> for the first unused k.
func freeName(dir, base string) (string, error) {
	for k := 1; k < 1000; k++ {
		p := filepath.Join(dir, base+"-"+strconv.Itoa(k))
		if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
			return p, nil
		}
	}
	return "", errors.New("gen: restore LevelDB: too many replaced directories")
}
