package fsx

import (
	"errors"
	"testing"
)

func TestContained(t *testing.T) {
	ok := []string{"a", "a/b.json", "Local Storage/leveldb/000005.ldb", "a/b/"}
	for _, rel := range ok {
		if _, err := Contained("/root", rel); err != nil {
			t.Fatalf("%q: %v", rel, err)
		}
	}
	bad := []string{"", "/abs", "../x", "a/../../x", "a/./b", "a//b", "a\x00b", ".", ".."}
	for _, rel := range bad {
		if _, err := Contained("/root", rel); !errors.Is(err, ErrNotContained) {
			t.Fatalf("%q: expected ErrNotContained, got %v", rel, err)
		}
	}
}
