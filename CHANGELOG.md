# Changelog

All notable changes to this project are documented here. The format is
based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the
project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Exit codes and the `--json` schemas (`"v":1`) are part of the public API.

## [Unreleased]

### v0.2 (in progress)

- Changed: `root.json` is written as version 2. Every v0.2 field is
  optional and an older root is read as before, but a v0.1 binary refuses
  a version 2 root: do not downgrade paritydesk, and never with an open
  run.

<!-- v0.2 anchor: busy -->
- Added: busy detection from Claude's log. `plan` lists the sessions
  still working (masked ids and ages; titles only in the `busy` member of
  `--json` and in the human output), a switch asks at P0 and waits for
  them at P3 up to `app.quit_timeout`, and the detached job waits instead
  of exiting 12 at P0. It follows the turn start, end, interrupt and stop
  lines and, for Code sessions, Claude's keep-awake hold, across
  `main.log` rotation (`main1.log` … `main4.log`). Claude's own quit
  dialog stays the last guard.
- Added: `app.busy_stale_after` (default `6h`, 10m to 48h): a session
  whose turn started longer ago with no end in the log is shown as stuck
  and not waited for.
- Added: `plan --invoking-session <uuid>` leaves the session running the
  command out of the busy list (the plugin's skill passes it).
- Added: a folder signed in, inside Claude, to another profile's account
  is refused with exit 20 by every command before anything is written
  (found from the folder's marker or Claude's account-change log line).
  Two account changes in one folder since the last switch (Claude's own
  multi-account mode) give swap-only with the cause; `sync` exits 31
  with it. `status` and `doctor` report account changes (masked).
- Added: `doctor` checks `busy-now`, `account-change` and
  `chrome-pairing-org` (the organization Claude in Chrome pairs with, from
  the log, masked), and `chrome-bridge` reports a launch whose local
  pairing Claude turned off. See ADR 0007.
- Changed: compatibility manifest signal patterns match only record
  lines, at the start of the message (after `<timestamp> [<level>] `);
  continuation lines are never matched. Each pattern needs a fixed text
  of at least 8 characters. Manifest overrides must follow these rules;
  an old override that no longer matches only makes paritydesk fall back.
- Deprecated: `signals.native_multi_account` is ignored (account changes
  come from `account_changed`); `doctor` warns when an override sets it.
  It is removed in v0.3.
- Not yet confirmed on a real Mac: the 7.x patterns marked "from code"
  (turn clears, keep-awake release and uncounted lines, account change,
  pairing lines); release checklist step 4 confirms them.

<!-- v0.2 anchor: fdscan -->
- Added: a native open-file scan (libproc, no cgo) replaces `lsof` for
  the quiescence check; `lsof` stays as the fallback. New config key
  `app.open_file_scan` (`auto`, `native`, `lsof`) and doctor check
  `open-file-scan`.
- Changed: thread working directories reported by `lsof` count as working
  directories; folder paths match regardless of case, Unicode
  normalization and symlinks in the folder path.
- Changed: a process of your own user whose open files cannot be read
  completely is never taken as "holds nothing" (`auto` then asks `lsof`
  for that check). When paritydesk runs inside a sandbox that denies
  reading other processes' open files, a switch stops with exit 70
  before writing anything instead of trusting an empty answer; run it
  from Terminal.
- Not yet confirmed on a real Mac: a scan of every process (its time
  against `lsof`, and how many processes of the same user cannot be
  read). It runs only on the CI runners; `paritydesk doctor` shows the
  counts on a Mac.

<!-- v0.2 anchor: sidebar-offline -->
- Added: collapsed sidebar groups and fork families carry between accounts
  (`sync.collapsed_groups`, on by default), written to Local Storage and
  checked after launch; groups of sessions or projects kept back by
  `sync.exclude` stay on their account (row `sidebar-collapse`, ADR 0009).
- Added: the "show empty projects" setting carries between accounts; a
  folder Claude has not yet set up gets the carried Sessions sort direction
  and empty-group setting as its seed (row `sidebar-seed`, 7.x only).
- Added: `verify --json` gains `unverified`, the rows that could not be read
  back from Local Storage while Claude runs; they are checked at the next
  switch (exit 0).
- Changed: the sidebar view merges each section member on its own (sort
  field and direction together), so different edits on two accounts both
  survive.
- Not yet confirmed on a real Mac: that a written collapsed-group list
  survives launch on 7.x (release checklist SO-1). If it does not, the
  release ships `sync.collapsed_groups = false` and says so here.

<!-- v0.2 anchor: scratch -->

<!-- v0.2 anchor: online -->
- Added: changes that only Claude's own tools can make are queued at the
  switch and applied in the next Code session by the new
  `/paritydesk:reconcile` skill: the sidebar's whole-list and section
  group-by and sort (written only when the live value is still the one the
  account had, after a 45 s settle time and a check of the account and
  organization in Claude's log). The queue is derived: dropping it loses
  nothing. See ADR 0005.
- Added: the plugin's PreToolUse guard and PostToolUse recorder for
  Claude's sidebar, session, settings and artifact tools. While a queue is
  armed for a session, only the listed calls with exactly the listed input
  run (plus a short read list); deletes, shares and other sessions' content
  are always blocked there; nothing is ever pre-approved, so Claude's own
  approval cards still appear. Other tools, including the per-turn plan
  tools, are never touched. `online.guard = "always"` blocks the fixed
  floor in every session.
- Added: `paritydesk online status` and `paritydesk online cancel`
  (stops everything at once, also during a run); `online next`,
  `decide`, `retry`, `done` and `capture` are used by the skills. `plan`,
  `switch` and `sync` gain an `online` member and a plan line; `status`
  shows waiting changes (`--short`: `online:<n>`); `doctor` gains the
  check `online`; `uninstall` removes the online folder.
- Added: config `[online]` (`reconcile` prompt/auto/off,
  `capture_max_age`, `queue_max_age`, `lease_ttl`, `guard`,
  `create_sections`) and the SessionStart line that offers
  `/paritydesk:reconcile` (startup and resume only, at most three times
  per queue).
- Changed: `/paritydesk:switch` confirms with buttons (plain-text
  fallback when the question tool is missing) and runs a read-only capture
  of its own session after the confirmation; the plugin is version 0.2.0
  and registers three hook events.
- Not yet confirmed on a real Mac: the shape of PostToolUse payloads for
  MCP tools, what happens after a declined approval card, that
  `${CLAUDE_SESSION_ID}` expands in skills, the question tool in every
  session kind, how soon a sidebar write survives the first reconcile and
  whether every launch logs the account and organization (release
  checklist R-ON-1 to R-ON-9). 6.x gets no online steps.

<!-- v0.2 anchor: pins -->

<!-- v0.2 anchor: artifacts -->

<!-- v0.2 anchor: legacy-adopt -->

<!-- v0.2 anchor: i18n-ja -->

### Added

- The `paritydesk` command line on the standard library flag package:
  `init`, `profile list|adopt|add|rename|remove`, `status`, `plan`,
  `switch`, `sync`, `verify`, `recover`, `rollback`, `rebase`,
  `generations list|show`, `backups prune|purge`, `doctor`, `log`,
  `history`, `config path|show|validate`, `uninstall`, `help`, `version`,
  and the internal `hook session-start`. Every command prints one JSON
  document with `--json`; flags work before and after arguments.
- `plan --json` returns `plan_hash`, `destructive_digest`,
  `needs_confirmation` and the summary `lines` (the
  DESTRUCTIVE line last), which the plugin's skill shows verbatim before
  `switch --detach --confirmed-plan`.
- Exit codes with an error catalog (`PD-E<code>`) and
  [docs/if-something-goes-wrong.md](docs/if-something-goes-wrong.md).
- Read-only mode (`--read-only`, `PARITYDESK_READONLY=1`): plans only and
  writes nothing.
- Output for people and scripts: sanitized text, tables that fit 80
  columns, `--plain`, `--no-color` (also `NO_COLOR`, `TERM=dumb`),
  `--ascii` (automatic when the locale is not UTF-8), NDJSON progress with
  `--json-progress`.
- Config validation: aliases, folder names (reserved names, case and
  Unicode normalization collisions), app preference tags, export and
  import lists, limits, `quit_timeout` (5 s to 10 min) and
  `late_quit_watch` (0 to 1 h). The config file is written atomically
  with mode 0600.
- The Claude Code plugin: the `/paritydesk:switch` skill and a
  SessionStart hook that prints one fixed line when a recover, a sign-in,
  a pending profile add or verify drift needs attention.
- Policy tests: no network packages, external commands only in
  `internal/app/exec.go`, no goleveldb recovery, no advice to end an
  account's session, no credential stores, fake ids only in fixtures,
  ASCII-only text, and a private deny-list check for personal labels.
- CI on Apple silicon and Intel: `go vet`, `gofmt`, `go test -race`,
  `govulncheck`, with actions pinned by commit.
- More CI jobs, all passing on GitHub runners: a Linux job that
  runs `go vet` and the race tests of every package that does not need
  macOS; a checks job for `gofmt`, `go vet` for darwin and linux and the
  network-package check (with and without tests); a second macOS test
  run under a `sandbox-exec` profile that denies all network, with a
  probe test that proves the sandbox works; a differential test against
  the C++ LevelDB library (Snappy and uncompressed, both directions); and
  an end-to-end job that drives the real binary against a stub app with
  a test bundle id in a temporary home. The e2e job refuses to run
  outside CI or next to the real app. See CONTRIBUTING.md.
- `paritydesk completion bash|zsh|fish`: completion scripts generated
  from the command and flag tables, with golden tests per shell and a
  test that every visible command and flag appears. Profile aliases are
  completed from the config file through a hidden `__aliases` command.
- `paritydesk doctor --bundle <file>` (and `--bundle-logs <n>`): a
  support archive (tar.gz, mode 0600, never over an existing file, never
  through a symbolic link, never inside a Claude data folder) holding
  redacted doctor output, paritydesk's config with personal values
  replaced, version and manifest information and the newest run logs.
  Tests plant synthetic ids, home paths, aliases, labels and titles and
  check that none reach the archive. `version --json` also reports the
  commit and build date when they are set at build time.
- Integrations in `integrations/`: a SwiftBar or xbar plugin, a Raycast
  script command and a `pd` shell function, all thin wrappers around
  `paritydesk switch --detach` that find the binary like the plugin's
  hook shim and accept only a valid alias. Tests run them against a fake
  binary, and run shellcheck when it is installed.
- Release tooling: `.goreleaser.yaml` (darwin arm64, amd64 and a
  universal binary; `-trimpath`, CGO off, version, commit and date
  ldflags, commit timestamps so that builds are designed to be
  reproducible, not yet checked; `SHA256SUMS`), a
  Homebrew formula template in `packaging/homebrew/` that builds from
  source, and a release workflow on `v*` tags that runs the tests and
  govulncheck before GoReleaser, with write permission in the release
  job only.
- Documentation: [docs/install.md](docs/install.md) (why paritydesk is
  not notarized, the from-source tap, `go install`, the tarball and its
  checksum), [docs/integrations.md](docs/integrations.md), the support
  archive in [PRIVACY.md](PRIVACY.md#the-support-archive), and release
  checklist steps 25 to 35 (integrations, support archive and the
  quarantine gate for design risk R8).

### Defaults

- **The sidebar view is synced by default** (`sync.sidebar_view = true`).
  ADR 0003 showed that pin order and group order, written to Local
  Storage and the config mirror together, survive a relaunch. Its
  second S1 round found that section sort, group-by and order are kept
  per account on Claude's servers and come back at launch, while section
  collapse is kept on the device. v0.1 writes the view to both local
  copies while Claude is quit and verifies it after launch; values Claude
  replaces from the server are recorded as drift and marked contested
  rather than fought over. Set `sync.sidebar_view = false` to leave the
  view alone.
- `app.late_quit_watch` is 10 minutes: after a vetoed quit times out,
  paritydesk keeps watching and relaunches the profile you were on if
  Claude quits late.
- A compatibility manifest for Claude 7.x, tested on 7.37095.0: the 6.x
  probes and log patterns still hold; the new session fields
  `titleSetAtMs` (max time) and `writtenRepositories` (per field) are
  merged, and eight new top-level entries are never touched until
  classified.

### Changed

- Session records never carry the app's quit-resume marks
  (`interruptedByQuitAt` and the fields read with it, new manifest role
  `resume_mark`) or the send-hold fields to another account, and keep the
  target folder's own `isStarred` (spikes S14 and S15). Carried over, the
  marks would make the app resume one account's turn under the other by
  itself. The target's own marks are dropped once the other account moved
  the session on, so a finished turn is not resumed.
- App 6.28899.6 is listed as tested: the shape probes pass on a copy of
  its data.
- A difference only in fields each folder keeps for itself (run state,
  security grants, account-bound fields, resume marks) is no longer
  reported as a conflict.
- The compatibility manifest for app 6.x follows a read-only survey of
  the on-disk shapes ([ADR 0004](docs/adr/0004-data-shape-survey.md)):
  session tombstones are a bare epoch-ms number; parent links include
  nested fields; a session's worktree fields move as one group; security
  grants in a session record keep the target account's own value and are
  dropped from copies new to it; the presets file is read before its
  Local Storage copy; worktree registrations are keyed by name; persisted
  store versions are probed; unknown top-level entries are never touched
  and `doctor` lists their names; the `active_org` and `app_started`
  log patterns are filled in.
- The app's backup of the sidebar store (`dframe-store.backup.*`) is on
  the fixed Local Storage deny list.

### Fixed

- A switch started in the background (from a Code session, the menu bar
  or Raycast) now posts the notification the docs promised: when Claude
  is up on the other account, or when the switch stops early. It was
  never sent.
- `verify` no longer says Claude in Chrome "did not connect" after a
  switch, and `doctor` no longer warns about it. Claude in Chrome connects
  when it is first used, not when Claude starts, so a log without a
  connection line only means it was not used yet. `verify` still says
  when a connection was seen.
- Planning is about 20 times faster. On a test Mac with about 750 session
  records of up to 600 KB each, one plan took 82 s and a switch planned
  twice; it now takes about 4 s, and the second plan of a switch about
  1 s. The session transform parsed each large record once per field it
  touched; top-level members are now parsed once per document and edits
  that change nothing are skipped. Records and shape probes are read on
  all CPUs, and records and state objects whose files did not change are
  reused within the run.
- A terminal switch hands the post-launch check to a background job, so
  the terminal is free as soon as Claude is up; the SessionStart hook
  reports what the check finds.
- An account in several organizations, parked, resolves to the
  organization of its last run. Before, its org was unknown and every
  per-organization row (sessions among them) was skipped (degraded mode).
  A switch now also records the leaving profile's replica.
- A switch also writes the leaving folder's marker. P5 records that
  folder's harvested rows as its bases, but only the entering folder got a
  marker, so the first switch back was a second first-sync union that
  carried no deletions.
- Fewer stops that protected nothing:
  - A staged Claude update no longer stops a switch with exit 13: the
    switch lets it install and re-checks the new version before syncing
    (what `--allow-update` did; the flag is gone). Exit 13 now means the
    install did not finish in time.
  - A plan saved for `--confirmed-plan` has no time limit (`expires_at`
    is gone from `plan --json`); the check after the quit still refuses
    deletions the confirmed plan did not show.
  - The deletion breaker (exit 22) applies only to `--yes` runs. A plan a
    person confirmed, at the terminal or through `--confirmed-plan`, was
    seen with its DESTRUCTIVE line.
  - A confirmed plan that showed the first-sync line needs no
    `--confirm-first-sync`; the plugin asks once.
  - A switch from Terminal verifies after the launch by itself instead
    of asking you to run `paritydesk verify`.
  - Values Claude keeps per account or device (contested rows: group
    order, pin order, sidebar view) that change after a launch are
    listed as `replaced` and recorded, without exit 50.
  - A stale Local Storage copy that the config mirror overrides is noted
    in the run log only, not on screen.
- Terminal questions have no time limit. The P2 answer expired two
  minutes after the plan was shown, so reading the plan could end in
  exit 3 after answering yes. What runs after an answer checks the data
  again (the final plan against the confirmed one; rollback leaves items
  changed since), so the limit protected nothing. A plan saved for
  `--confirmed-plan` keeps its two-minute limit. A first sync asks once:
  the plan question already shows the first-sync line (`--yes` still asks
  it). Exit 3 now reads "The plan was not confirmed", and a declined
  switch does not print the plan again.
- A first sync no longer plans deletions of the target's stale
  "started environment" entries for sessions it had already removed:
  only a session the merge removes takes its entry with it.
- `plan` and `switch` without an alias before the first switch target
  the only other profile instead of stopping with exit 2, and a run that
  stops before planning no longer prints an empty plan.
- A staged Claude update is no longer reported after it was installed:
  Squirrel leaves its JSON state file behind, so paritydesk now checks
  that the update bundle it names still exists. Before, every switch
  after an app update stopped with exit 13.
- `doctor`'s Claude in Chrome check says it reads the recent Claude log,
  which may reach back before the last launch.
- A quit that Claude refuses (AppleScript error -128, as AppKit answers
  when the app cancels termination) or does not answer in time (-1712)
  is now waited on like the "still working" dialog, watched for a late
  quit, and exits 10. It was reported as missing Automation (exit 15).
  Exit 15 now means only AppleScript error -1743 or -1744; -10004 or any
  other failure exits 10. The message names the error number, and for
  -1712 and -10004 says what to check (an unanswered Automation prompt,
  a sandbox).
- Local Storage reads and writes no longer go wrong when a journal holds
  batches numbered below the manifest's last sequence. The C++ LevelDB
  library, which Chromium and so the app build on, can leave its
  database that way (the differential test's C++ workload does in nearly
  every run). goleveldb dropped those batches without an error in a
  snapshot, so the newest writes were missing, and refused the database
  with "invalid sequence number" when opening it for a sync or restore
  write. The snapshot path now replays such journals itself on its
  private copy, in order. `OpenWritable` first adds a replay journal to
  the database (every batch again, in order, renumbered above every
  sequence number in use; written to a temporary file and renamed) and
  then opens it; both LevelDB libraries read the result, and any state a
  crash leaves, with the same content. It takes the database's `LOCK`
  before it writes the replay journal, so a process holding the database
  stops it with nothing written (`kv.ErrLocked`), and it adds no second
  replay journal when an earlier attempt's open failed after adding one.
  Its checks (manifest, tables, journals) all run before anything is
  written. Batches out of order are refused (`kv.ErrJournalSequence`).
  The manifest is now chosen from `CURRENT`, a pending `CURRENT.<n>` or
  `CURRENT.bak` as goleveldb chooses it.
- The Local Storage format check reads only the tables the manifest
  lists. A table file the C++ LevelDB library leaves unfinished when it
  closes during a compaction (empty, or without its footer; both
  libraries ignore and later remove it) no longer fails every run with
  exit 70 at the format check, or a sync write, until Claude opens the
  database again. A table the manifest lists but the folder lacks is
  refused before anything is written.
- When another process holds a folder's Local Storage lock as a rollback
  or a restore opens it, the run stays open with exit 41 (quit Claude,
  then `paritydesk recover`) instead of exit 70; a switch or sync that
  finds it held when applying writes nothing and exits 40, saying the
  folder is in use.
- A Local Storage snapshot no longer fails when the database holds two
  journal files (after an unclean exit or during a flush): goleveldb's
  read-only open returns `EOF` there, so the private copy is then opened
  read-write. The original is still never opened.
- A value that matches a contested server value is no longer harvested
  as an edit from a side whose sync base is invalid (C2), while such a
  value still survives a removal by the other side (C1).
- Bases of environments, tasks and worktrees are kept after a switch.
  The read-back check compared the item order, which these rows' merges
  do not carry, so every later switch was a first sync and a scripted
  `switch --yes` exited 21.
- A started-environment entry is removed only for a local session the
  sessions row saw. Entries of other orgs' and other accounts' sessions,
  and remote `session_` ids, are no longer deleted.
- A `paritydesk verify` run later than 15 minutes after a switch, or
  after that switch was already verified, only reports differences. It
  no longer takes the user's own edits for app drift.
- Presets are not written while their Local Storage copy and file mirror
  disagree, and the Local Storage copy is read when `fileMirrored` is
  not set.
- `rollback` waits until no process uses the target folder (exit 41
  otherwise), as a switch does.
- The worktree loss guard also checks registry entries that a switch
  would delete or replace, keeps a same-name entry with another
  directory, and keeps paths on volumes that are not mounted.
- The remote-control opt-in, vouched calls and worktree trust grants no
  longer reach other accounts.
- A newer deletion of a chain root or fork parent is no longer undone by
  a child that was merely kept.
- `sync.exclude` applies to backlog and scheduled tasks.
- The active organization from `main.log` counts only when its line
  names the active folder's account.
- The permission-mode cap looks up modes, not preference names; a mode
  the target's own sessions use is kept on new copies.
- `init`, `profile add` and `profile adopt` ask whether the account is
  managed (or need `--managed` / `--unmanaged` without a terminal).
- A downgrade of Claude (exit 30) and the SQLite Local Storage backend
  (exit 31) are detected. `doctor` decides the mode from the same inputs
  as a switch and lists the reasons, skipped rows, failed probes and the
  active organization.
- The support archive replaces aliases of removed profiles in messages
  and keeps only the count of unclassified entries.
- Override manifests can no longer remove `never_until_classified`
  entries, widen `synced`, or bypass the deny lists through another name.
- The bash completion now matches the last value flag of a command.
- `doctor --bundle` refuses a data folder reached through a case variant
  or a symbolic link.
- The SessionStart hook stays usable for `brew install --HEAD` builds.

### Not yet confirmed on a real Mac

- Every item above is covered by automated tests with synthetic data
  only. The release checklist confirms them on a real Mac before v0.1 is
  tagged; until then the README describes them as designed, not as
  confirmed. In particular the integrations (steps 26 to 28), the
  support archive (step 29) and every install path (steps 31 to 35) have
  not been run on a real Mac.
- The tap formula installs from source rather than a prebuilt tarball,
  unlike the original design (D23), so that no unsigned downloaded binary
  is quarantined (R8).

### Deferred to v0.2

- Artifact sync (beta) and `/paritydesk:artifacts`. Spike S5 showed that
  hooks fire for the tools the app injects, so nothing blocks them now.
- Pins through the app's own tools: v0.1 carries pin order offline.
- The in-app write path for sections and group-by, online reconcile, the
  capture phase, and the PreToolUse and PostToolUse hooks (unblocked by
  spike S5).
- Collapsed sidebar groups: device-local and carryable offline (second S1
  round), not yet built.
- Seed-only legacy sidebar keys.
- A native file-descriptor scan; v0.1 uses `lsof` with fixed arguments.
- `profile adopt --from-legacy` and legacy archive import.
- Scratch workspaces for worktrees.
- A native multi-account signal from the app's log; v0.1 relies on the
  folder's account history.
- The pinned pairing organization in `doctor`; v0.1 reports the active
  organization only.
- Japanese messages and README.
- Buttons (Switch / Cancel) for the confirmation that `/paritydesk:switch`
  asks; it asks in plain text for now.

### Removed

- Any path that sends a signal to the app. Claude is quit only with an
  Apple Event; there is no `--force-quit`.
