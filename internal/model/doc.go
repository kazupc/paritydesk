// Package model holds the pure domain types shared by every layer: row and
// category names, replicas, stamps, normalized row snapshots, changes,
// conflicts, contested rows and folder markers.
//
// model does no I/O. A RowSnapshot is the version-independent, normalized
// content of one row on one replica; it is what bases, canonical state and
// overlays are made of (D6, M8).
package model
