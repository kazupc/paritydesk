package leveldb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"

	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/store/kv"
)

// AllowedCompressions are the block compression types accepted (0 none,
// 1 Snappy). Anything else is refused before writing.
var AllowedCompressions = []int{0, 1}

// snapshotOptions opens clones read-only and leniently.
func snapshotOptions() *opt.Options {
	return &opt.Options{ReadOnly: true, ErrorIfMissing: true, Strict: opt.NoStrict}
}

// cloneRecoveryOptions opens a private clone read-write and leniently. It
// is used only on a clone, never on an original: goleveldb's read-only
// journal recovery fails with io.EOF when the directory holds two or more
// journal files (its reader reset returns the previous journal's EOF),
// which happens after an unclean exit or during a memtable flush.
func cloneRecoveryOptions() *opt.Options {
	return &opt.Options{ErrorIfMissing: true, Strict: opt.NoStrict}
}

// writableOptions opens the original strictly and never creates a database.
func writableOptions() *opt.Options {
	return &opt.Options{ErrorIfMissing: true, Strict: opt.StrictAll, NoSync: false}
}

// syncWrite makes every batch durable before Write returns.
var syncWrite = &opt.WriteOptions{Sync: true}

// cloneDirName is the database directory inside a snapshot temp directory.
const cloneDirName = "db"

// Opener opens LevelDB directories.
type Opener struct {
	// TempRoot is the parent of snapshot clones; it should be on the same
	// volume as the data root so clonefile works, and mode 0700.
	TempRoot string
	// FS is confined to TempRoot (fsx.Confine): clones, temp directories
	// and their removal can never touch anything outside it. It wraps the
	// unwrapped platform Ops, so snapshots keep working in read-only mode;
	// read-only mode is enforced for this package by kv.ReadOnly, which
	// refuses OpenWritable.
	FS fsx.Ops
}

var _ kv.Opener = (*Opener)(nil)

// New returns an Opener whose filesystem operations are confined to
// tempRoot. fs must be the unwrapped platform implementation (never
// fsx.ReadOnly). Under `go test`, a tempRoot under the real home's app or
// paritydesk folders is refused with fsx.ErrRealPathInTest.
func New(tempRoot string, fs fsx.Ops) (*Opener, error) {
	if err := fsx.CheckTestPath(tempRoot); err != nil {
		return nil, err
	}
	return &Opener{TempRoot: tempRoot, FS: fsx.Confine(fs, tempRoot)}, nil
}

// OpenSnapshot implements kv.Opener. It clones dir (without LOCK) into a
// fresh directory under TempRoot and opens the clone read-only; the clone
// is removed on Close. If the read-only open fails, the clone (never the
// original) is opened read-write once, which replays its journals the
// way the app would on its next start. If a journal holds batches
// goleveldb would drop for their sequence numbers (see journal.go), the
// clone's journals are replayed by this package instead: they are read,
// removed from the clone, and written in order into the clone opened
// read-write. Batches out of order fail with kv.ErrJournalSequence. The
// original directory is never opened.
func (o *Opener) OpenSnapshot(ctx context.Context, dir string) (kv.Reader, error) {
	name, err := o.clone(ctx, dir)
	if err != nil {
		return nil, err
	}
	st, err := inspectJournals(o.clonePath(name), snapshotOptions())
	if err == nil && st.disordered != "" {
		err = st.sequenceErr()
	}
	if err != nil {
		return nil, errors.Join(fmt.Errorf("leveldb: open snapshot: %w", err), o.removeTemp(name))
	}
	var db *leveldb.DB
	if st.behind != "" {
		if db, err = o.replayClone(name, st); err != nil {
			return nil, errors.Join(fmt.Errorf("leveldb: open snapshot: %w", err), o.removeTemp(name))
		}
	} else if db, err = leveldb.OpenFile(o.clonePath(name), snapshotOptions()); err != nil {
		var rwErr error
		db, rwErr = leveldb.OpenFile(o.clonePath(name), cloneRecoveryOptions())
		if rwErr != nil {
			return nil, errors.Join(fmt.Errorf("leveldb: open snapshot: %w", err), fmt.Errorf("leveldb: open snapshot clone for recovery: %w", rwErr), o.removeTemp(name))
		}
	}
	return &store{db: db, cleanup: func() error { return o.removeTemp(name) }}, nil
}

// replayClone replays a clone's journals the way the C++ library does:
// every batch, in order, whatever its sequence number. It removes the
// journals from the clone, opens the clone read-write (so goleveldb has
// nothing to replay) and writes the batches again; goleveldb numbers them
// above everything in the tables, which they are newer than. Only ever
// called on a private clone.
func (o *Opener) replayClone(name string, st *journalState) (*leveldb.DB, error) {
	for _, j := range st.journals {
		if err := o.FS.RemoveAll(filepath.Join(o.TempRoot, name), cloneDirName+"/"+j); err != nil {
			return nil, fmt.Errorf("leveldb: remove clone journal: %w", err)
		}
	}
	db, err := leveldb.OpenFile(o.clonePath(name), cloneRecoveryOptions())
	if err != nil {
		return nil, fmt.Errorf("leveldb: open snapshot clone for journal replay: %w", err)
	}
	for _, b := range st.batches {
		if err := db.Write(b, nil); err != nil {
			return nil, errors.Join(fmt.Errorf("leveldb: journal replay: %w", err), db.Close())
		}
	}
	return db, nil
}

// OpenWritable implements kv.Opener. It checks every table's block
// compression and reads the manifest and journals with strict checks
// first (both only read), then opens dir itself strictly. Batches out of
// order fail with kv.ErrJournalSequence before anything is written. If a
// journal holds batches goleveldb would drop for their sequence numbers
// (journal.go), it first adds a replay journal holding every batch again,
// in order and renumbered, and opens dir with goleveldb's journal check
// relaxed so the old copies are skipped and the replay journal applied;
// every other strict check stays. A failed open may leave the replay
// journal, or goleveldb's partly done journal recovery, behind; either
// holds the same content for both libraries. It never creates a database
// and never runs recovery; errors after the open are returned wrapped so
// the caller restores from its generation.
func (o *Opener) OpenWritable(ctx context.Context, dir string) (kv.Writer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := checkDir(dir); err != nil {
		return nil, err
	}
	if err := CheckCompressions(dir); err != nil {
		return nil, err
	}
	opts := writableOptions()
	st, err := inspectJournals(dir, opts)
	if err != nil {
		return nil, err
	}
	if st.disordered != "" {
		return nil, st.sequenceErr()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if st.behind != "" {
		if _, err := writeReplayJournal(dir, st); err != nil {
			return nil, fmt.Errorf("leveldb: open for writing: %w", err)
		}
		// The journals were just read with strict checks, so the only
		// batches goleveldb skips now are the old copies of the replayed
		// ones.
		opts.Strict &^= opt.StrictJournal
	}
	db, err := leveldb.OpenFile(dir, opts)
	if err != nil {
		return nil, fmt.Errorf("leveldb: open for writing: %w", err)
	}
	return &store{db: db}, nil
}

// CheckFormat implements kv.Opener: it clones dir under TempRoot and runs
// CheckCompressions on the clone, then removes the clone.
func (o *Opener) CheckFormat(ctx context.Context, dir string) error {
	name, err := o.clone(ctx, dir)
	if err != nil {
		return err
	}
	checkErr := CheckCompressions(o.clonePath(name))
	return errors.Join(checkErr, o.removeTemp(name))
}

// clone creates a fresh 0700 directory under TempRoot, clones dir into its
// "db" subdirectory and removes the clone's LOCK. It returns the temp
// directory's name (relative to TempRoot).
func (o *Opener) clone(ctx context.Context, dir string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := checkDir(dir); err != nil {
		return "", err
	}
	name, err := o.newTempDir()
	if err != nil {
		return "", err
	}
	tmp := filepath.Join(o.TempRoot, name)
	if err := o.FS.Clone(dir, filepath.Join(tmp, cloneDirName)); err != nil {
		return "", errors.Join(fmt.Errorf("leveldb: clone database: %w", err), o.removeTemp(name))
	}
	if err := o.FS.RemoveAll(tmp, cloneDirName+"/LOCK"); err != nil {
		return "", errors.Join(fmt.Errorf("leveldb: remove clone lock: %w", err), o.removeTemp(name))
	}
	if err := ctx.Err(); err != nil {
		return "", errors.Join(err, o.removeTemp(name))
	}
	return name, nil
}

// newTempDir creates a uniquely named 0700 directory under TempRoot.
func (o *Opener) newTempDir() (string, error) {
	for range 8 {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", fmt.Errorf("leveldb: temp name: %w", err)
		}
		name := "snapshot-" + hex.EncodeToString(b[:])
		p := filepath.Join(o.TempRoot, name)
		if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err := o.FS.MkdirAll(p, 0o700); err != nil {
			return "", fmt.Errorf("leveldb: create snapshot directory: %w", err)
		}
		return name, nil
	}
	return "", errors.New("leveldb: could not pick a unique snapshot directory")
}

func (o *Opener) clonePath(name string) string {
	return filepath.Join(o.TempRoot, name, cloneDirName)
}

// removeTemp removes a snapshot temp directory.
func (o *Opener) removeTemp(name string) error {
	if err := o.FS.RemoveAll(o.TempRoot, name); err != nil {
		return fmt.Errorf("leveldb: remove snapshot directory: %w", err)
	}
	return nil
}

// checkDir requires dir to be an existing directory, not a symlink.
func checkDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("leveldb: database directory: %w", err)
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("leveldb: database directory: %w", fsx.ErrSymlink)
	}
	if !fi.IsDir() {
		return fmt.Errorf("leveldb: database directory is not a directory")
	}
	return nil
}

// store is a kv.Writer over an open goleveldb database. Snapshot stores
// have a cleanup that removes their clone after the database is closed.
type store struct {
	mu      sync.RWMutex
	db      *leveldb.DB
	cleanup func() error
	closed  bool
}

var _ kv.Writer = (*store)(nil)

// Get implements kv.Reader.
func (s *store) Get(key []byte) ([]byte, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, false, kv.ErrClosed
	}
	v, err := s.db.Get(key, nil)
	switch {
	case errors.Is(err, leveldb.ErrNotFound):
		return nil, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("leveldb: get: %w", mapClosed(err))
	}
	return v, true, nil
}

// Scan implements kv.Reader. Matching pairs are copied while the store is
// locked and fn is called afterwards, so fn may call any method.
func (s *store) Scan(prefix []byte, fn func(key, value []byte) error) error {
	keys, vals, err := s.collect(prefix)
	if err != nil {
		return err
	}
	for i := range keys {
		if err := fn(keys[i], vals[i]); err != nil {
			return err
		}
	}
	return nil
}

func (s *store) collect(prefix []byte) (keys, vals [][]byte, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, nil, kv.ErrClosed
	}
	it := s.db.NewIterator(util.BytesPrefix(prefix), nil)
	defer it.Release()
	for it.Next() {
		keys = append(keys, append([]byte(nil), it.Key()...))
		vals = append(vals, append([]byte(nil), it.Value()...))
	}
	if err := it.Error(); err != nil {
		return nil, nil, fmt.Errorf("leveldb: scan: %w", mapClosed(err))
	}
	return keys, vals, nil
}

// Write implements kv.Writer with one synced, atomic batch.
func (s *store) Write(b *kv.Batch) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return kv.ErrClosed
	}
	lb := new(leveldb.Batch)
	for _, op := range b.Ops() {
		if op.Delete {
			lb.Delete(op.Key)
		} else {
			lb.Put(op.Key, op.Value)
		}
	}
	if err := s.db.Write(lb, syncWrite); err != nil {
		return fmt.Errorf("leveldb: write batch: %w", mapClosed(err))
	}
	return nil
}

// Close implements kv.Reader. It closes the database, then removes a
// snapshot's clone. Later calls, including Close, return kv.ErrClosed.
func (s *store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return kv.ErrClosed
	}
	s.closed = true
	var closeErr error
	if err := s.db.Close(); err != nil {
		closeErr = fmt.Errorf("leveldb: close: %w", err)
	}
	if s.cleanup != nil {
		return errors.Join(closeErr, s.cleanup())
	}
	return closeErr
}

// mapClosed turns goleveldb's closed error into kv.ErrClosed.
func mapClosed(err error) error {
	if errors.Is(err, leveldb.ErrClosed) {
		return errors.Join(kv.ErrClosed, err)
	}
	return err
}
