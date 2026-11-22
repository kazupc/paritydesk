// Package state is paritydesk's single source of truth (D21, M1): one
// atomically replaced root.json plus content-addressed objects/, under
// ~/Library/Application Support/paritydesk/state/ (0700; files 0600).
//
// root.json is written as: temp file in the same directory, F_FULLFSYNC,
// rename, F_FULLFSYNC of the directory. An intent is recorded before each
// irreversible action of a run and confirmed after it, so recover can
// decide what happened without a separate journal. Objects hold row
// snapshots (bases, canonical state, overlays) keyed by sha256.
package state
