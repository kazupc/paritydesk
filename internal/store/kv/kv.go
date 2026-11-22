package kv

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"sync"
)

// ErrClosed is returned after Close.
var ErrClosed = errors.New("kv: store closed")

// Reader reads keys. Values returned must not be modified by the caller.
type Reader interface {
	Get(key []byte) (value []byte, found bool, err error)
	// Scan calls fn for every key with the prefix, in key order, until fn
	// returns an error.
	Scan(prefix []byte, fn func(key, value []byte) error) error
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
	// folder). Unknown block compression is refused.
	OpenWritable(ctx context.Context, dir string) (Writer, error)
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
	s.closed = true
	s.mu.Unlock()
	return nil
}

// Reopen clears the closed flag so a test can inspect the store again.
func (s *MemStore) Reopen() {
	s.mu.Lock()
	s.closed = false
	s.mu.Unlock()
}
