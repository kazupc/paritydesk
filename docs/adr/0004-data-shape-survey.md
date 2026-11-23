# ADR 0004: Data shape survey of app 6.28899.2

- Status: accepted
- Date: 2026-11-22
- App version: 6.28899.2

## Context

The compatibility manifest named fields, versions and file shapes that
had not been seen on a real Mac. Several were guesses. This ADR records a
read-only survey of one real data folder and the changes made to match
it.

## What was surveyed, and how

- One data folder of the app, on one Mac, on 2026-11-22.
- JSON files were read in place, read-only. Local Storage was read from a
  copy of `Local Storage/leveldb`, never from the original.
- Only key names, nesting and value types were recorded, plus persisted
  version numbers and the members of a few small enum sets. No values,
  ids, titles or paths were recorded. All ids in the code and tests are
  synthetic.
- `main.log` lines were recorded as templates with the ids masked.

## What was found, and what changed

### Session records (`claude-code-sessions/<acct>/<org>/`)

- A tombstone `deleted_<uuid>` holds a bare JSON number: its time in
  epoch ms. There is no `deletedAt` member. `tombstone_time` is now empty,
  the adapter reads the number (falling back to the file's mtime) and
  writes new tombstones as a bare number. The older object form is still
  read, never written.
- `interruptedByQuitAt` does not exist. `run_state` now lists the
  transient fields that were seen: `interruptedAwaitingPerson`,
  `priorErrorMark`, `pendingSystemReminder`, `queuedSideSessionNotes`,
  `lastTurnCutShort`, `steeredByRemoteClient` and `turnBoxMounted`.
  *Update (spike S15, app 6.28899.6):* the field does exist. The app
  writes it only when it is quit while a turn is still open, so no record
  on the surveyed Mac had it. It is now run state, with the fields read
  with it; see the S15 entry in [ADR 0003](0003-online-reconcile.md).
- Run state and busy state are now separate roles. None of the run-state
  fields says that a turn is running (an error mark or a pending reminder
  is not a running turn), so the new `busy` role is empty. Busy detection
  uses `main.log` and the app's own quit veto.
- `cwd` is in every record, so `project_path = cwd` is confirmed.
- Parent links: `forkedFromSessionId`, `spawnedFrom.sessionId` and
  `startChain.firstSessionId`. Parent fields may now be dotted paths into
  nested objects, and only well-formed local session ids count.
- Worktree fields: `worktreeName`, `worktreePath`, `branch` and
  `sourceBranch`. They are a new `worktree` role and move as one group in
  field merges, so a record never pairs one side's worktree name with the
  other side's path. `worktree_path` names the field the loss guard reads.
- `publishedArtifacts`, `artifactsMcpVouchedCalls`, `toolSurfaceSnapshot`
  and `remoteControlUserEnabled` are added to `account_bound`.
- A data folder can hold `claude-code-sessions/<acct>/<org>` directories
  of other accounts, some with many records. Adapters read and write only
  the replica's own directory. Plan validation now also allows file
  writes only below the entering replica's own session directory.

### Tasks and waiting input

- `backlog/tasks.json` is `{"version": 1, "items": [...]}`. Items carry
  `filedAt` and `suggestedAt`, now the row's `max_time`. A new
  `json-version` probe checks the version.
- `scheduled-tasks.json` holds `scheduledTasks`, `recordedSkips` and two
  boolean migration stamps. A new file created by paritydesk holds the
  array and an empty `recordedSkips`; the stamps are left to the app.
- `waiting-input` is a directory. Anything else at that name is now an
  error naming the path, not its content.

### Environments and presets

- `ccd-session-presets.json` `byAccount.<acct>` mirrors the Local Storage
  item `persisted.ccd-session-presets.<acct>` (its state carries
  `fileMirrored`).
- Persisted versions: `persisted.ccd-session-presets.<acct>` is 4,
  `persisted.ccd-started-environments` is 1.
- Presets carry `createdAt` and `lastUsedAt`; there is no `updatedAt`.
  `max_time` now lists `lastUsedAt` and `createdAt`.
- No `env` member was seen in preset settings. The secret paths stay, so
  env values remain secret if they appear.

### Sidebar

- `dframe-store` is `{"version": 1, "shared": <number>, "state": {...}}`.
  The sidebar rows' `persist_version` and their probes are now 1.
- The app keeps a copy of `dframe-store` in Local Storage under
  `dframe-store.backup.v1`.
- The sidebar scope object holds `migratedFrom`, `prefsSeeded` and
  `sections`; each section holds `id`, `kind`, `name`, `order`, `shown`,
  `collapsed`, `groupBy`, `sortBy`, `ascending`, `v` and `show`.

### Other files

- `git-worktrees.json` is `{"schemaVersion": 2, "worktrees": {...}, ...}`
  with entries keyed by worktree name. A `json-version` probe checks
  `schemaVersion`.
- `claude_desktop_config.json` had no `mcpServers` member. The
  `mcp-servers` probe already passes when it is absent; a test now proves
  it.
- `window-state.json` is a flat object with `displayBounds`.

### `main.log` signals

- `active_org` now matches the `[CCDScheduledTasks] Initialized` line
  (and `[ScheduledTasks] Initialized`). Both ids are captured only as
  lowercase UUIDs; the org goes to the named group `org`, which the app
  package reads and validates. The org is only a hint: it is used when
  the active folder's own account has that org.
- `app_started` matches `[startup-perf] main window shown <n> ms after
  launch`.
- No line names a session when a turn starts. `turn_started`,
  `login_required`, `pairing_disabled` and `native_multi_account` stay
  empty, and callers fall back as before.

### Fixtures

testkit now writes these member names and this nesting: session records
with the observed fields, bare-number tombstones, the backlog and
scheduled-task wrappers, an empty `waiting-input` directory, presets with
their file mirror and the per-org item, `dframe-store` with `shared` and
its backup copy, the sidebar scope and section shapes, the worktree
registry keyed by name, other accounts' session directories, and a few
top-level files paritydesk never touches. The adapter tests and the
orchestrator integration tests run on this layout.

## Decisions

1. **Security grants never cross accounts.** `chromePermissionMode`,
   `cuAllowedApps`, `cuGrantFlags`, `sessionPermissionUpdates` and
   `alwaysAllowedReasons` are a new `grant` role. On a record the target
   already knows, the target's own value is kept, and so is its absence.
   On a copy new to the target, they are dropped. They are never listed
   in any other role. `permissionMode` keeps its own cap (D8).

2. **Fields in no role come from the winner.** A record field that no
   role lists is taken from the record that wins the merge (D8), and a
   loser's change to it is kept as a conflict. Dropping unknown fields
   would damage records. The cost is that a new security-relevant field
   is carried until it is added to the manifest. Every field the survey
   flagged as security-relevant or account-bound is now in a role. The
   survey should be repeated on each new app version.

3. **Presets are a mirrored row.** The file copy is read first. A Local
   Storage copy that differs is listed as stale per preset key and is
   never harvested. Writes edit both copies together. The row stays
   `write_path = "off"` in v0.1. The per-org item
   `persisted.ccd-session-presets.<acct>.<org>` was seen but is not
   understood. It is never read and is on the Local Storage deny list.

4. **The dframe-store backup is never written.** The manifest has a new
   `ls_never_touched` list, applied by plan validation on top of the fixed
   fragments in package denylist. It holds `dframe-store.backup.*`, the
   per-org presets item, `ccd-session-store` and `ccd-sync-*`. An override
   may add entries but not remove them. The fixed floor in package
   denylist also holds the fragment `dframe-store.backup`, so the backup
   stays denied even with a broken manifest.
   Verify after launch reads the config mirror only, because LevelDB is
   never opened while the app runs. If the app restores the sidebar
   from its backup and the written value is gone from the mirror, verify
   reports drift (exit 50), not success; a test covers this. A restore
   that changes Local Storage but not the mirror is not visible to verify;
   the next harvest then reads the mirror and lists the Local Storage
   copy as stale.

5. **Unknown entries are never touched.** Every top-level entry the
   survey found is now classified. Credential-like and security-related
   entries (`buddy-tokens.json`, `TransportSecurity`, `Network Persistent
   State`, `SharedStorage*`, `InterestGroups*`, `DIPS*`, `Extension
   State`, `WebStorage`, `ca-bundle.pem`, `extensions-blocklist.json`) are
   never touched. Caches are `cache`. App state whose purpose is not known
   is `never_until_classified`. An entry no list names is treated as never
   touched: plan validation allows file writes only below `synced`.
   `paritydesk doctor` lists unclassified entries by name only, with ids
   in names replaced by `<id>`, so a maintainer can update the manifest.

6. **Version probes are not critical.** A failed version probe skips its
   row (exit 32). That already prevents every write the wrong version
   could make unsafe, so none of the new probes needs swap-only. The
   critical `archived-sessions.idx` probe is unchanged.

7. **Rewind edges are not parent links.** `rewindEdges[].parent` links
   turns inside one record's own history. Treating it as a family link
   could bring deleted sessions back, so it is not listed.

8. **A missing worktree directory is no risk.** The loss guard skips a
   worktree path that no longer exists, since there is nothing to lose.
   Any other path it cannot check still counts as dirty.

## Not observed

- Scheduled task items: the list was empty, so their shape and time
  fields are unknown. `max_time` lists backlog fields only.
- A non-empty `waiting-input` directory.
- `login_required`, `pairing_disabled`, `native_multi_account` and a
  per-session turn-start line in `main.log`.
- Any record field that means a turn is running (the `busy` role).
- The value format of the parent fields; only local session ids are
  followed.
- `env` in preset settings.
- What the app does when `dframe-store` and its backup disagree.
- Whether the per-org presets item matters when an account has several
  organizations.

These are designed behaviour until a real-Mac check confirms them: see
docs/release-checklist.md steps 4 to 6 (log patterns and record fields),
11 (parity of each row) and 19 (worktree guard).

## Amendments after review

A review of this ADR's implementation changed these decisions. None of
the changes has been run against the real app; release checklist steps
6, 11 and 19 check them.

- **Started environments.** `persisted.ccd-started-environments` is one
  item for the whole data folder, while the sessions row reads one
  account and org directory. An entry is removed with its session only
  when its id is a local session id that the sessions row saw. Entries
  of other orgs, other accounts and `session_` ids are left alone.
- **Presets (decision 3).** The file is read first only when the Local
  Storage item's `state.fileMirrored` is true; otherwise the Local
  Storage copy is read. While the two copies disagree, presets are not
  written at all and each change is reported skipped. A disagreement is
  treated as "do not write", not as "the file wins".
- **Grants (decision 1).** `remoteControlUserEnabled` and
  `artifactsMcpVouchedCalls` are opt-ins of one account and moved from
  `account_bound` to `grant`. Worktree registry entries also have
  grants (`autoTrusted`, `creationGrant`) and run state (`leasedBy`,
  `leaseCleared`), handled the same way.
- **Worktree group.** `cwd` moves with the worktree fields, so a record
  never pairs one side's worktree with the other side's working
  directory.
- **Worktree registry.** A same-name entry that registers another
  directory never replaces the target's entry. The loss guard also
  checks registry entries that the worktrees row would delete or
  replace, by directory, and runs even when the sessions row is skipped.
- **Missing worktrees (decision 8).** A worktree path counts as gone only
  when its parent directory exists. A path whose parent is missing too,
  as on an external volume that is not mounted, stays in the guard.
- **Families (decision 7 context).** A parent is revived for a kept child
  only when the child itself was revived or restored, or when the
  parent's deletion is not newer than its last activity.
- **Sidebar scope.** `migratedFrom`, like `prefsSeeded`, is the app's own
  marker and is never carried.
- **Tasks.** `sync.exclude` applies to tasks: by project path (`cwd`) and
  by originating session id.
- **Permission acknowledgements.** The manifest maps permission modes to
  the preferences that acknowledge them (`[permission_acks]`, empty until
  confirmed). A mode the target's own records already use counts as
  acknowledged.
