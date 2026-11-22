// Package rows holds one merge module per synced row. A module
// takes the leaving and entering snapshots with their bases, the shared
// canonical state and the entering profile's overlay, and returns the new
// canonical state and the desired snapshot of the entering replica
// (canonical ⊕ overlay, invariant G3). rows does no I/O.
//
// Model (D6): canonical holds what every profile shares. Each side's
// changes since its own base are applied over canonical; when both sides
// changed one key, merge.Prefer decides and the loser is reported. Items a
// profile does not export, or does not import, live in that profile's
// overlay instead of canonical (D27). Secrets never cross a managed_by_org
// edge unless allowed per key.
package rows
