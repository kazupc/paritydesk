// Package merge implements the three-way merge primitives and the
// tie-break rule (D7). It does no I/O and is property-tested.
//
// Every primitive compares each side against its own base. A side whose
// base is invalid (BaseValid false) reports no deletions: its missing items
// are "no information", never removals, so an invalid base can only add
// (C1). The loser of a both-sides change is always returned so callers can
// keep it in the generation and in state.
package merge
