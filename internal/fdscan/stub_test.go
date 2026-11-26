//go:build !darwin || pd_nofdscan

package fdscan

import (
	"errors"
	"testing"
)

func TestStubUnsupported(t *testing.T) {
	if _, err := System().Scan(t.Context(), Options{Match: func(string) bool { return true }}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Scan: %v", err)
	}
	if _, err := System().SelfCheck(t.Context()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("SelfCheck: %v", err)
	}
	if !trampolinesLinked() {
		t.Fatal("the stub reports missing trampolines")
	}
}
