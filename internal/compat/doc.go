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
// Probes check names and shapes only, never values. paritydesk never reads
// or hashes the app bundle's code at runtime.
package compat
