// Package worktrees adapts git-worktrees.json: worktree registrations
// under "worktrees", keyed by worktree name on app 6.x (schemaVersion 2).
// Every entry is read and written by its key; schemaVersion, origin
// records and GC fields stay per folder. It also computes the worktree
// loss guard's input from the session records' worktree path field (row
// 18, H2).
package worktrees
