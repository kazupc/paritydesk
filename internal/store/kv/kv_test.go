package kv

import (
	"context"
	"errors"
	"testing"
)

func TestReadOnlyRefusesWritable(t *testing.T) {
	o := NewMemOpener()
	s := NewMem()
	var b Batch
	b.Put([]byte("k"), []byte("v"))
	if err := s.Write(&b); err != nil {
		t.Fatal(err)
	}
	o.Put("/db", s)
	ro := ReadOnly(o)
	if _, err := ro.OpenWritable(context.Background(), "/db"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("got %v", err)
	}
	r, err := ro.OpenSnapshot(context.Background(), "/db")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if v, ok, err := r.Get([]byte("k")); err != nil || !ok || string(v) != "v" {
		t.Fatalf("get: %q %v %v", v, ok, err)
	}
	if err := ro.CheckFormat(context.Background(), "/db"); err != nil {
		t.Fatal(err)
	}
}

func TestMemOpenerSnapshotIsIndependent(t *testing.T) {
	o := NewMemOpener()
	o.Put("/db", NewMem())
	snap, err := o.OpenSnapshot(context.Background(), "/db")
	if err != nil {
		t.Fatal(err)
	}
	w, err := o.OpenWritable(context.Background(), "/db")
	if err != nil {
		t.Fatal(err)
	}
	var b Batch
	b.Put([]byte("k"), []byte("v"))
	if err := w.Write(&b); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := snap.Get([]byte("k")); ok {
		t.Fatal("snapshot saw a later write")
	}
	if _, err := o.OpenSnapshot(context.Background(), "/missing"); err == nil {
		t.Fatal("missing database opened")
	}
	o.FormatErr = ErrUnknownCompression
	if err := o.CheckFormat(context.Background(), "/db"); !errors.Is(err, ErrUnknownCompression) {
		t.Fatalf("got %v", err)
	}
}
