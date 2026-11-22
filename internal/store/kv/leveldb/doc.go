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
// Writes: OpenWritable opens the original with strict checks
// (opt.StrictAll), refuses table blocks whose compression is not "none" or
// Snappy, and applies each batch with a synced write. After Close the caller
// reopens through OpenSnapshot to read back. leveldb.Recover and
// RecoverFile are banned (a policy test enforces it).
package leveldb
