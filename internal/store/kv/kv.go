package kv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Errors.
var (
	// ErrClosed is returned after Close.
	ErrClosed = errors.New("kv: store closed")
	// ErrReadOnly is returned by a ReadOnly opener's OpenWritable.
	ErrReadOnly = errors.New("kv: read-only mode")
	// ErrUnknownCompression is returned (wrapped, naming the table file but
	// never its content) when a table block uses a compression type other
	// than none or Snappy. The orchestrator maps it to swap-only (exit 31).
	ErrUnknownCompression = errors.New("kv: unknown block compression")
	// ErrJournalSequence is returned (wrapped, naming the journal file but
	// never its content) when a journal the database would replay holds
	// batches out of order: a batch numbered below the end of the batch
	// before it. Neither LevelDB library writes that, so OpenSnapshot and
	// OpenWritable refuse it rather than guess an order. The leveldb
	// package's CheckJournals also returns it for batches numbered below
	// the manifest's last sequence, which the C++ library can leave and
	// both open paths replay.
	ErrJournalSequence = errors.New("kv: journal batch out of sequence")
)

// Reader reads keys. Values returned must not be modified by the caller.
type Reader interface {
	Get(key []byte) (value []byte, found bool, err error)
	// Scan calls fn for every key with the prefix, in key order, until fn
	// returns an error.
	Scan(prefix []byte, fn func(key, value []byte) error) error
	// Close releases the store. Every later call, a second Close
	// included, returns ErrClosed.
	Close() error
}

// Writer adds a synced, atomic batch write to Reader.
type Writer interface {
	Reader
	Write(b *Batch) error
}

// Opener opens stores by directory.
type Opener interface {
	// OpenSnapshot copies dir to a private temporary directory (without
	// LOCK) and opens the copy read-only. The copy is removed on Close.
	// Safe while the app runs; the result may be torn and is labelled
	// approximate by callers (L1).
	OpenSnapshot(ctx context.Context, dir string) (Reader, error)
	// OpenWritable opens dir itself for writing. The caller must have
	// proven quiescence (no process holds LOCK or has files open under the
	// folder). Unknown block compression is refused, and so are journal
	// batches out of order (ErrJournalSequence).
	OpenWritable(ctx context.Context, dir string) (Writer, error)
	// CheckFormat clones dir (as OpenSnapshot does) and checks every table
	// block's compression type without opening the database. It returns
	// an error wrapping ErrUnknownCompression for unknown types. It never
	// writes outside the opener's temp root and works in read-only mode.
	CheckFormat(ctx context.Context, dir string) error
}

// ReadOnly wraps o so that OpenWritable fails with ErrReadOnly. Snapshots
// and format checks pass through. Used with fsx.ReadOnly for --read-only
// and PARITYDESK_READONLY=1.
func ReadOnly(o Opener) Opener { return readOnly{o} }

type readOnly struct{ o Opener }

func (r readOnly) OpenSnapshot(ctx context.Context, dir string) (Reader, error) {
	return r.o.OpenSnapshot(ctx, dir)
}

func (r readOnly) OpenWritable(context.Context, string) (Writer, error) { return nil, ErrReadOnly }

func (r readOnly) CheckFormat(ctx context.Context, dir string) error {
	return r.o.CheckFormat(ctx, dir)
}

// MemOpener is an in-memory Opener for tests: each directory maps to a
// MemStore. OpenSnapshot returns an independent copy, OpenWritable the
// store itself (reopened). Missing directories fail like a missing
// database. FormatErr, if set, is returned by CheckFormat.
type MemOpener struct {
	mu        sync.Mutex
	Stores    map[string]*MemStore
	FormatErr error
}

// NewMemOpener returns an empty MemOpener.
func NewMemOpener() *MemOpener { return &MemOpener{Stores: map[string]*MemStore{}} }

// Put registers store under dir.
func (o *MemOpener) Put(dir string, s *MemStore) {
	o.mu.Lock()
	o.Stores[dir] = s
	o.mu.Unlock()
}

func (o *MemOpener) get(dir string) (*MemStore, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	s, ok := o.Stores[dir]
	if !ok {
		return nil, fmt.Errorf("kv: no database at %s", dir)
	}
	return s, nil
}

// OpenSnapshot implements Opener with a copy of the store.
func (o *MemOpener) OpenSnapshot(_ context.Context, dir string) (Reader, error) {
	s, err := o.get(dir)
	if err != nil {
		return nil, err
	}
	return s.copy(), nil
}

// OpenWritable implements Opener.
func (o *MemOpener) OpenWritable(_ context.Context, dir string) (Writer, error) {
	s, err := o.get(dir)
	if err != nil {
		return nil, err
	}
	s.Reopen()
	return s, nil
}

// CheckFormat implements Opener.
func (o *MemOpener) CheckFormat(_ context.Context, dir string) error {
	if _, err := o.get(dir); err != nil {
		return err
	}
	return o.FormatErr
}

// Op is one batch operation.
type Op struct {
	Key    []byte
	Value  []byte // nil for a delete
	Delete bool
}

// Batch is an ordered list of puts and deletes applied atomically.
type Batch struct{ ops []Op }

// Put adds a put.
func (b *Batch) Put(key, value []byte) {
	b.ops = append(b.ops, Op{Key: append([]byte(nil), key...), Value: append([]byte(nil), value...)})
}

// Delete adds a delete.
func (b *Batch) Delete(key []byte) {
	b.ops = append(b.ops, Op{Key: append([]byte(nil), key...), Delete: true})
}

// Len returns the number of operations.
func (b *Batch) Len() int { return len(b.ops) }

// Ops returns the operations in order.
func (b *Batch) Ops() []Op { return append([]Op(nil), b.ops...) }

// MemStore is an in-memory Writer.
type MemStore struct {
	mu     sync.Mutex
	m      map[string][]byte
	closed bool
	// FailWrite, if set, is returned by Write without applying anything.
	FailWrite error
}

// NewMem returns an empty MemStore.
func NewMem() *MemStore { return &MemStore{m: map[string][]byte{}} }

// Get implements Reader.
func (s *MemStore) Get(key []byte) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, false, ErrClosed
	}
	v, ok := s.m[string(key)]
	return v, ok, nil
}

// Scan implements Reader.
func (s *MemStore) Scan(prefix []byte, fn func(key, value []byte) error) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	keys := make([]string, 0, len(s.m))
	for k := range s.m {
		if bytes.HasPrefix([]byte(k), prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	vals := make([][]byte, len(keys))
	for i, k := range keys {
		vals[i] = s.m[k]
	}
	s.mu.Unlock()
	for i, k := range keys {
		if err := fn([]byte(k), vals[i]); err != nil {
			return err
		}
	}
	return nil
}

// Write implements Writer.
func (s *MemStore) Write(b *Batch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if s.FailWrite != nil {
		return s.FailWrite
	}
	for _, op := range b.ops {
		if op.Delete {
			delete(s.m, string(op.Key))
		} else {
			s.m[string(op.Key)] = append([]byte(nil), op.Value...)
		}
	}
	return nil
}

// Close implements Reader.
func (s *MemStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	s.closed = true
	return nil
}

// copy returns an independent open store with the same contents.
func (s *MemStore) copy() *MemStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := NewMem()
	for k, v := range s.m {
		c.m[k] = append([]byte(nil), v...)
	}
	return c
}

// Reopen clears the closed flag so a test can inspect the store again.
func (s *MemStore) Reopen() {
	s.mu.Lock()
	s.closed = false
	s.mu.Unlock()
}
