# ADR 0008: Scratch workspaces are mirrored at the same path

- Status: accepted (R1, R6, R7 unverified on a real Mac: release checklist S-1, S-5, S-6)
- Date: 2026-11-26
- App version: 7.37095.0 (code reading of the app bundle only)

## Context

A Code session started with "Start without a folder" lives in a scratch
workspace, `<data folder>/scratch-workspaces/<acct>/<org>/scratch-YYYY-MM-DD-<6 hex>/`.
Its record names that absolute path in `cwd`, `originCwd`,
`gitAnchorsFolderRealpath` and a few other members. After a folder swap
the carried record points at a directory only the other account's folder
holds, and the app reports "folder not found".

## Facts (read in the app's code, 7.37095.0)

| Id | Fact |
|---|---|
| A1 | The scratch root is `<userData>/scratch-workspaces`; a scope is `<root>/<accountId>/<orgId>`. |
| A2 | New workspaces are minted only under the current account and org, named `scratch-<ISO date>-<3 random bytes hex>`, and trusted on creation. |
| A3 | Workspace trust is stored per absolute path in `~/.claude.json`, outside the data folders and shared by every account; a path is trusted when any parent is. |
| A4 | Starting or resuming a session asserts trust of its `originCwd` (else `cwd`). |
| A5 | The app re-trusts a scratch workspace by itself only when the data folder moved, not when the account or org part differs. |
| A6 | Looking a session's scratch folder up "in the current scope" fails for a workspace of another scope: the scratch file preview, the Browser pane's local previews and the side "temporary query". The session start path does not use it. |
| A7 | The app's sweep scans only the current scope; it keeps a directory any record names by full path or leaf, and removes others after 7 untouched days. |
| A8 | Owed removals after a session delete (`scratchowed_<leaf>` notes) remove only `<current scope>/<leaf>`. |
| A9 | At session delete, workspaces count only inside the current scope. |
| A10 | Path-bearing record members: `cwd`, `originCwd`, `gitAnchors[]`, `gitAnchorsFolderRealpath`, `folderPlace`, `scratchFilesLeftIn`, `shellFolders[]`, `scratchCarried`, `stagedTranscriptPath`, `sessionPermissionUpdates[]`, `pendingFirstStart`. |
| A11 | Record validation accepts a scratch path of any scope. |
| A12 | The CLI transcript folder is `~/.claude/projects/<sanitized path>`, looked up for `cwd`, `worktreePath`, `originCwd`; the first holding a conversation wins. |
| A13 | "That folder is in the Claude app's own data" refuses a folder only when no scratch member of the record names the same scratch root, whatever its scope. |
| A14 | The sidebar marks a session as scratch from `originCwd`, scope-free. |
| A15 | When the app carries a workspace itself it skips `node_modules`, `.venv`, `vendor`, folders of other VCSs and git directories, `settings.local.json` and `.cc-writes`, with 1 GiB per file and 2 GiB in all. |
| A16 | `scratch_cwd_unreachable` comes from the CLI's stderr, not from a scope check. |
| A17 | Nothing else creates, renames or removes scratch directories. |
| A18 | `homeTenancy` is reset to the current scope when sessions load, so delete flows look only at the current scope. |
| A19 | The app makes no extra-folder worktrees for a scratch session. |

Not verifiable here, with the safe fallback:

| Item | Fallback |
|---|---|
| R1 that a mirrored session opens on the other account | Release checklist S-1; a later manifest without the row if the app adds a scope check. |
| R2 the "scratch" entries of `git-worktrees.json` | `.claude/worktrees/**` inside a workspace is never read, written or removed. |
| R3 the size of real workspaces | Configurable limits; an oversize workspace is reported and not carried. |
| R4 whether cloning works for a pair of files | A streamed byte copy; the free-space check assumes no cloning. |
| R5 directory modes of the root, account and org folders | Copied from the leaving folder, never guessed. |
| R6 an org's allowed-workspace-folders policy | Unverified; S-6. `scratch.cross_managed` keeps the row off a managed edge by default. |
| R7 whether `preview_start` is refused | Documented as a limitation; S-5. |

## Decision

Mirror under the same path. paritydesk copies (APFS clone) each scratch
workspace a carried record names from the leaving folder into the
entering folder at `scratch-workspaces/<acct of origin>/<org of origin>/<leaf>/`
and never rewrites a session record. The directory has the same relative
path in every folder, so later switches merge the two copies file by file
(three-way, per-folder bases, newest change wins) in both directions.

| Concern | Mirror (chosen) | Rewrite into the target scope |
|---|---|---|
| Transcript continuity (A12) | Same path, same transcript. | Two transcripts; the other account's turns disappear on the way back. Fixing it needs writes under `~/.claude/projects`: refused. |
| Trust (A3 to A5) | Same path, already trusted. | Untrusted on first open; pre-trusting needs writes to `~/.claude.json`: refused. |
| App delete flows (A8, A9, A18) | Never touch mirrors. | The copy is removed when the session is deleted on that account. |
| App sweep (A7) | Never scans mirrors. | Removed after 7 untouched days once no record names its leaf. |
| Record bytes | Unchanged. | 10+ members rewritten. |
| Previews and the side query (A6, R7) | Not available on the other account. | Available. |

The rewrite wins on previews; it loses on history and trust for every
mirrored session and cannot be made whole without writing outside the
data folders. The preview limitation is stated in the plan
(`scratch.preview_limit`), in doctor, in the README and checked in S-5.

## Rules

- The carry set is the workspaces the sessions row's Desired names that
  exist in the leaving folder as real directories. It is recorded in the
  planned intent (`scratch_refs`), so recover re-reads exactly those; a
  re-read without refs never happens (the base is dropped instead).
- Items are keyed below the scratch root; values hold content only (sha256,
  size, mode, link target), the file's mtime is its stamp.
- Excluded everywhere: `.DS_Store`, `.claude/settings.local.json`,
  `.claude/.cc-writes/`, `.claude/worktrees/`, git lock files, the app's
  tmp recovery names. Held back: `node_modules`, `.venv`, `vendor`, other
  VCS folders and `[scratch] cache_dirs`. A change of these rules never
  looks like a deletion, also where a file was replaced by a held-back
  folder.
- Directories and symlinks are create-only; a link that leaves its
  workspace is never created. A path that is a file on one side and a
  folder (held-back ones included) or a link on the other keeps the
  entering side's form.
- A `.git` folder is one unit with a digest over its members. A one-sided
  change carries that side's unit whole (a canonical value from a third
  profile alone does not make it two-sided); a two-sided change keeps each
  account's repository and marks the unit diverged in canonical, so later
  runs never mix or overwrite either until both copies are equal again.
- Conflicts: the newer change wins (epoch with valid bases, else mtime,
  then the leaving side); the loser is kept next to the winner as
  "<stem> (conflict from <profile> <date>)<ext>" and enters canonical, so
  the next switch back carries it. An entering-side loser is copied to its
  sibling from the entering folder before the winner replaces it (copies
  from the entering folder run before those from the leaving folder).
- Deletions: a file removed against a valid base is quarantined into the
  backup generation; nothing is removed with an invalid base; a removal
  first seen after the preview is deferred to a later switch;
  `scratch.propagate_deletions = false` turns removals off. No workspace,
  directory or link is ever removed. The breaker uses `[scratch]`
  limits, counts files outside git units and whole units.
- A copy from the leaving folder outside a git unit is soft: when its
  source changed between the plan and apply, the target is left as it
  was and the change is carried next time. Every other mismatch rolls
  back.
- Retention: quarantined scratch files of a pruned generation move to
  `generations/scratch-kept/<n>/` (files first, `kept.json` second, the
  generation removed last) and stay until `backups prune --scratch`.
- Limits: 20,000 files or 2 GiB per workspace, 8 GiB or 50,000 files per
  read, free space for a byte copy plus 500 MiB; an oversize workspace is
  reported and not carried (its listing stops at the limit). A workspace
  with a folder its owner cannot write into is not carried either. `sync` never carries the row; a managed edge
  carries it only with `scratch.cross_managed`.

## Consequences

- A mirrored session opens on the other account with its history and
  trust (S-1 confirms); its previews work only on the account that
  created the workspace.
- A mirror is never removed by the entering account's app. If its session
  is deleted there it becomes an orphan; doctor lists orphans and nobody
  removes them automatically.
- Out of scope: rewriting record paths, writes to `~/.claude.json` or
  `~/.claude/projects`, removing workspaces, merging a diverged `.git`,
  carrying dependency and cache folders, xattrs, ACLs and hard links,
  Cowork workspaces, app 6.x.
