// Package gen manages numbered backup generations (D16) under
// ~/Library/Application Support/paritydesk/generations/<n>/ (0700).
//
// A generation holds, for one run: the old bytes of every Local Storage
// value it changes (key-level JSON), clones of the plain files it touches,
// files quarantined instead of deleted, a full clone of the LevelDB
// directory (kept only in the most recent backups.leveldb_clones_keep
// generations), and manifest.json with sha256 of everything. Restores are
// conditional: an item is restored only if its current hash equals the
// generation's post-hash; anything else is a conflict (M6).
package gen
