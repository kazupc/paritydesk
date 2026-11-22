// Package worktrees adapts git-worktrees.json: worktree registrations
// keyed by path, limited to entries referenced by synced session records.
// GC fields stay per folder. It also computes the worktree loss guard's
// input (row 18, H2).
package worktrees
