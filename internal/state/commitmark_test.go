package state

import (
	"testing"
	"time"
)

// TestCommitMark: LastCommitAt wins; a root written before it existed
// falls back to its newest generation; nothing known gives zero.
func TestCommitMark(t *testing.T) {
	t1 := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	var nilRoot *Root
	if !nilRoot.CommitMark().IsZero() {
		t.Error("nil root")
	}
	if !(&Root{}).CommitMark().IsZero() {
		t.Error("empty root")
	}
	old := &Root{Generations: []GenerationRef{{N: 1, At: t2}, {N: 2, At: t1}}}
	if got := old.CommitMark(); !got.Equal(t2) {
		t.Errorf("v0.1 root: %v, want the newest generation %v", got, t2)
	}
	old.LastCommitAt = t3
	if got := old.CommitMark(); !got.Equal(t3) {
		t.Errorf("with LastCommitAt: %v, want %v", got, t3)
	}
}
