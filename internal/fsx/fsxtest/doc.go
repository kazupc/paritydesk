// Package fsxtest provides a working fsx.Ops for tests: real files in temp
// directories, extended attributes kept in memory (keyed by device and
// inode, so they follow a directory across renames), a non-atomic swap, a
// call log and fault injection.
package fsxtest
