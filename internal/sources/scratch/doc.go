// Package scratch is the adapter of row scratch: scratch workspaces
// ("Start without a folder" sessions) mirrored at the same path in every
// data folder (ADR 0008, spec-scratch).
//
// A session record names its workspace by absolute path,
// <data root>/Claude/scratch-workspaces/<acct>/<org>/<leaf>. Refs finds
// the workspaces the sessions row's Desired names; ReadRefs reads only
// those (the adapter is a sources.RefReader, and Read refuses), listing
// before hashing, never following a symlink, with per-workspace and
// per-read limits and a stat cache. Items are keyed relative to the
// scratch root: "<acct>/<org>/<leaf>/" (root marker), ".../<rel>/"
// (directory or git unit), ".../<rel>" (file or symlink). Values hold
// content only (sha256, size, mode, link target); a file's mtime is its
// stamp. A ".git" folder is one git unit whose digest covers its
// members, so a repository is never merged file by file.
//
// Ops turns T's snapshot into Desired with plan.EditMkdir, plan.EditCopy
// (a clone verified against its hash, from T's own content when it has
// it, else from S), plan.EditSymlink and plan.EditRemove (a quarantine).
// Session records are never edited; no workspace or directory is ever
// removed.
package scratch
