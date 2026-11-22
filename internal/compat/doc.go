// Package compat holds the embedded compatibility manifests and decides,
// per run, which rows may be written (D13).
//
// A manifest declares, for a range of app versions: the Local Storage
// backend and allowed compressions; for every row its key templates,
// config-mirror paths, files, write path, verify and contested flags,
// limits and probes; the classification of every top-level entry of a data
// folder; known-bad versions; main.log signal patterns; and footprints of
// other account-switching tools.
//
// Probes check names, shapes and persisted versions only, never other
// values. paritydesk never reads or hashes the app bundle's code at
// runtime.
//
// The classification also bounds writes: file ops are allowed only below
// the synced globs (expanded for the entering replica), so an entry no
// list names is never touched, and ls_never_touched adds Local Storage
// items that are never written. ADR 0004 records the survey behind the
// 6.x manifest.
package compat
