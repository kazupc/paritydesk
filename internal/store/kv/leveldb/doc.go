// Package leveldb implements kv.Opener on top of a pinned
// github.com/syndtr/goleveldb (design D5).
//
// Reads: OpenSnapshot clones the directory (APFS clonefile through
// fsx.Ops.Clone, without LOCK) into a private temporary directory and opens
// the clone read-only with lenient checks. When that fails (goleveldb's
// read-only recovery returns io.EOF for a directory with two or more
// journals), the private clone is opened read-write instead, which
// replays its journals. The original is never opened.
// The opener's filesystem is confined to its temp root (fsx.Confine over
// the unwrapped platform Ops), so snapshots work in read-only mode and can
// never write or remove anything else.
//
// Journals behind the manifest: the C++ LevelDB library often leaves a
// journal whose first batches are numbered below the last sequence number
// its manifest records (a compaction committed after later writes reached
// the journal, and closing does not flush the memtable). goleveldb drops
// such batches without an error when lenient and refuses the database
// when strict. Both paths read the manifest (chosen from CURRENT as
// goleveldb chooses it) and the journals first (journal.go) and replay
// every batch in order, as the C++ library does; batches out of order
// fail with kv.ErrJournalSequence. OpenSnapshot removes the clone's
// journals, opens the clone read-write and writes their batches again.
//
// Writes: OpenWritable opens the original with strict checks
// (opt.StrictAll), refuses table blocks whose compression is not "none" or
// Snappy, and applies each batch with a synced write. For a journal behind
// the manifest it first adds a replay journal to the original (every
// batch again, in order, renumbered; written to a temporary file, synced
// and renamed) and opens with goleveldb's journal check relaxed, so the
// old copies are skipped; a crash at any point leaves a state both
// libraries read with the same content. After Close the caller reopens
// through OpenSnapshot to read back. leveldb.Recover and RecoverFile are
// banned (a policy test enforces it).
package leveldb
