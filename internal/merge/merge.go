package merge

import (
	"github.com/kazupc/paritydesk/internal/model"
)

// Side identifies a replica in a merge.
type Side int

// Sides.
const (
	Leaving  Side = iota // the account being left (S); live most recently
	Entering             // the account being switched to (T)
)

// Version is one side's view of one key.
type Version struct {
	Item      *model.Item // nil: absent on this side
	Base      *model.Item // nil: absent in this side's base
	BaseValid bool
	// Tomb is this side's tombstone for the key, if any.
	Tomb *model.Tombstone
}

// Changed reports whether the side changed the key since its base. With an
// invalid base, presence counts as a change and absence does not.
func (v Version) Changed() bool { return false }

// Outcome is the merge result for one key.
type Outcome struct {
	Key    string
	Result *model.Item // nil: absent (deleted) after the merge
	Winner Side
	Kind   model.ChangeKind // relative to the entering side
	// Loser holds the losing version of a both-sides change.
	Loser *model.Item
	// Reason is a constant string for summaries ("epoch", "app-time",
	// "leaving-side", "tombstone-newer", "revived").
	Reason string
}

// Prefer applies the D7 tie-break between two changed versions: (1) the
// higher epoch when both bases are valid; (2) the later app time from this
// Mac that paritydesk did not write, when a base is missing; (3) the
// leaving side.
func Prefer(leaving, entering Version) (Side, string) { return Leaving, "leaving-side" }

// Scalar merges one key (D7 rules, D9 tombstones). tombstoneMode is
// "newer-wins" or "delete-wins".
func Scalar(key string, leaving, entering Version, tombstoneMode string) Outcome {
	return Outcome{Key: key}
}

// FieldMerger merges a record field by field. It returns the merged record
// value, or ok=false to fall back to whole-record last-writer-wins.
type FieldMerger func(base, leaving, entering []byte) (merged []byte, ok bool)

// Keyed merges keyed records: Scalar per key, with optional field-level
// merge for keys changed on both sides.
func Keyed(keys []string, leaving, entering func(key string) Version, field FieldMerger, tombstoneMode string) []Outcome {
	return nil
}

// Set merges set members against bases (adds and removes tracked).
func Set(base, leaving, entering []string, leavingBaseValid, enteringBaseValid bool) []string {
	return nil
}

// OrderedList keeps the winner's order, inserts items only the loser has
// next to their nearest surviving neighbour, and drops items removed
// relative to the base (only when that side's base is valid).
func OrderedList(base, winner, loser []string, winnerBaseValid, loserBaseValid bool) []string {
	return nil
}
