// Package leveldb implements kv.Opener on top of a pinned
// github.com/syndtr/goleveldb (design D5).
//
// Reads: OpenSnapshot clones the directory (APFS clonefile through
// fsx.Ops.Clone, without LOCK) into a private temporary directory and opens
// the clone read-only with lenient checks. The original is never opened.
//
// Writes: OpenWritable opens the original with strict checks
// (opt.StrictAll), refuses table blocks whose compression is not "none" or
// Snappy, and applies each batch with a synced write. After Close the caller
// reopens through OpenSnapshot to read back. leveldb.Recover and
// RecoverFile are banned (a policy test enforces it).
package leveldb
