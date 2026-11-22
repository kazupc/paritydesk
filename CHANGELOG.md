# Changelog

All notable changes to this project are documented here. The format is
based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the
project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Exit codes and the `--json` schemas (`"v":1`) are part of the public API.

## [Unreleased]

### Added

- The `paritydesk` command line on the standard library flag package:
  `init`, `profile list|adopt|add|rename|remove`, `status`, `plan`,
  `switch`, `sync`, `verify`, `recover`, `rollback`, `rebase`,
  `generations list|show`, `backups prune|purge`, `doctor`, `log`,
  `history`, `config path|show|validate`, `uninstall`, `help`, `version`,
  and the internal `hook session-start`. Every command prints one JSON
  document with `--json`; flags work before and after arguments.
- `plan --json` returns `plan_hash`, `destructive_digest`,
  `needs_confirmation`, `expires_at` and the summary `lines` (the
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

### Defaults

- **The sidebar view is synced by default** (`sync.sidebar_view = true`).
  ADR 0003 showed that pin order and group order, written to Local
  Storage and the config mirror together, survive a relaunch. The
  sidebar view itself was tested only with the Local Storage copy (which
  Claude replaced), and the ADR leaves open whether the server or the
  config mirror wins, so writing the view to both copies is an
  extrapolation until release-checklist step 11 confirms it. v0.1 writes
  the view to both copies while Claude is quit and verifies it after
  launch; values Claude replaces are recorded as drift rather than fought
  over. Set `sync.sidebar_view = false` to leave the view alone.
- `app.late_quit_watch` is 10 minutes: after a vetoed quit times out,
  paritydesk keeps watching and relaunches the profile you were on if
  Claude quits late.

### Sequencing within v0.1

- **WI-9, integrations and release**, starts once the `--json` schemas of
  the command line (this work item, WI-8) are merged: SwiftBar and Raycast
  wrappers and a shell alias around `switch --detach`, a `completion`
  command, `doctor --bundle`, GoReleaser, the Homebrew tap formula, the
  release workflow and the quarantine gate.
- **WI-10, CI extensions**, follows on the same condition: a macOS job
  with a stub app, a differential LevelDB test against the C++ library,
  and a test run inside a network-deny sandbox.

### Deferred to v0.2

- Artifact sync (beta) and `/paritydesk:artifacts`: they need spike S5
  (do hooks fire for the app-injected tools?).
- Pins through the app's own tools: v0.1 carries pin order offline.
- The in-app write path for sections and group-by, online reconcile, the
  capture phase, and the PreToolUse and PostToolUse hooks (spike S5).
- Collapsed sidebar groups, which the first spike round did not cover.
- Seed-only legacy sidebar keys.
- A native file-descriptor scan; v0.1 uses `lsof` with fixed arguments.
- `profile adopt --from-legacy` and legacy archive import.
- Scratch workspaces for worktrees.
- A native multi-account signal from the app's log; v0.1 relies on the
  folder's account history.
- The pinned pairing organization in `doctor`; v0.1 reports the active
  organization only.
- Japanese messages and README.

### Removed

- Any path that sends a signal to the app. Claude is quit only with an
  Apple Event; there is no `--force-quit`.
