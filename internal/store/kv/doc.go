// Package kv is the key/value store contract behind which Local Storage
// lives. The LevelDB implementation is in kv/leveldb; MemStore is a fully
// working in-memory implementation for tests of the layers above.
//
// Rules every implementation follows:
//
//   - A live database is never opened. Reads go through a snapshot: a copy
//     of the directory, without LOCK, opened read-only and leniently.
//   - Writes open the original directory only after quiescence was proven
//     by the caller, with strict checks, as one synced batch, followed by a
//     read-back.
//   - Recovery APIs that rewrite a damaged database are never used.
package kv
