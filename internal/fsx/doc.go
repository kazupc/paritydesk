// Package fsx holds the filesystem operations that need platform support or
// must be faultable in tests: the atomic folder swap (renamex_np with
// RENAME_SWAP), exclusive renames, APFS clones, full and barrier syncs,
// file identity, xattr markers, contained atomic writes and free space.
//
// Only apply/, gen/, state/ and fsx/ write into app-owned or tool-owned
// folders, and they do it through Ops so that tests can inject faults and
// read-only mode can be enforced in one place (ReadOnly).
package fsx
