package kv

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
)

func TestReadOnlyPassesFormatErrors(t *testing.T) {
	o := NewMemOpener()
	o.Put("/db", NewMem())
	o.FormatErr = fmt.Errorf("table 000005.ldb: %w", ErrUnknownCompression)
	if err := ReadOnly(o).CheckFormat(context.Background(), "/db"); !errors.Is(err, ErrUnknownCompression) {
		t.Fatalf("got %v", err)
	}
	if _, err := ReadOnly(o).OpenSnapshot(context.Background(), "/missing"); err == nil {
		t.Fatal("missing database opened")
	}
}

func TestMemStoreBatch(t *testing.T) {
	tests := []struct {
		name      string
		failWrite error
		want      []string
	}{
		{"applied in order", nil, []string{"a=2", "c=3"}},
		{"failed write applies nothing", errors.New("disk full"), []string{"a=1", "b=1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := NewMem()
			var seed Batch
			seed.Put([]byte("a"), []byte("1"))
			seed.Put([]byte("b"), []byte("1"))
			if err := s.Write(&seed); err != nil {
				t.Fatal(err)
			}
			var b Batch
			b.Put([]byte("a"), []byte("2"))
			b.Delete([]byte("b"))
			b.Put([]byte("c"), []byte("3"))
			if b.Len() != 3 || !b.Ops()[1].Delete {
				t.Fatalf("ops: %+v", b.Ops())
			}
			s.FailWrite = tc.failWrite
			if err := s.Write(&b); !errors.Is(err, tc.failWrite) {
				t.Fatalf("write: %v", err)
			}
			var got []string
			if err := s.Scan(nil, func(k, v []byte) error {
				got = append(got, string(k)+"="+string(v))
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestBatchCopiesInputs(t *testing.T) {
	k, v := []byte("k"), []byte("v")
	var b Batch
	b.Put(k, v)
	k[0], v[0] = 'x', 'x'
	if op := b.Ops()[0]; string(op.Key) != "k" || string(op.Value) != "v" {
		t.Fatalf("batch aliases caller memory: %+v", op)
	}
}

func TestMemStoreScanPrefixAndClose(t *testing.T) {
	s := NewMem()
	var b Batch
	for _, k := range []string{"p2", "q", "p1", "p"} {
		b.Put([]byte(k), []byte("v"))
	}
	if err := s.Write(&b); err != nil {
		t.Fatal(err)
	}
	var keys []string
	if err := s.Scan([]byte("p"), func(k, _ []byte) error { keys = append(keys, string(k)); return nil }); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(keys, []string{"p", "p1", "p2"}) {
		t.Fatalf("got %v", keys)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Get([]byte("p")); !errors.Is(err, ErrClosed) {
		t.Fatalf("get after close: %v", err)
	}
	if err := s.Scan(nil, func(_, _ []byte) error { return nil }); !errors.Is(err, ErrClosed) {
		t.Fatalf("scan after close: %v", err)
	}
	if err := s.Close(); !errors.Is(err, ErrClosed) {
		t.Fatalf("second close: %v", err)
	}
	if err := s.Write(&b); !errors.Is(err, ErrClosed) {
		t.Fatalf("write after close: %v", err)
	}
}
