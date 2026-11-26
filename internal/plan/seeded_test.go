package plan

import "testing"

// Seeded enters the hash only when set: hashes of plans without it are
// those of v0.1.
func TestHashSeeded(t *testing.T) {
	a := samplePlan()
	if a.Hash() == "" {
		t.Fatal("empty hash")
	}
	b := samplePlan()
	b.Seeded = true
	if b.Hash() == a.Hash() {
		t.Fatal("Seeded did not change the hash")
	}
	c := samplePlan()
	c.Seeded = false
	if c.Hash() != a.Hash() {
		t.Fatal("an unset Seeded changed the hash")
	}
}
