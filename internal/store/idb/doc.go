// Package idb reads one string key from the app's IndexedDB, which
// Chromium keeps in a LevelDB directory with its own comparator
// ("idb_cmp1"). It never opens the database: it replays the manifest,
// walks every live table and scans the logs itself, keeps the newest
// entry of the key, and decodes the Blink/V8 serialized string the value
// holds. Reads run on a private per-file clone of the directory (Cloner),
// so files the app changes while they are read are detected (ErrTorn).
//
// The package is read-only by construction: it opens files O_RDONLY and
// O_NOFOLLOW, regular files only, never opens the directory's lock or
// info-log files, never reads the blob directory, and writes only into
// its own temporary directory through Cloner. Errors carry fixed text and
// base file names, never key or value bytes or absolute paths.
//
// Format facts and the reasoning behind every refusal are in spec-pins
// section 4 and ADR 0005.
package idb
