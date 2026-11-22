# paritydesk v0.1 implementation plan

Status: accepted for implementation. Audience: contributors and the agents
that implement the work items below. This plan is binding where it fixes a
contract (package APIs in the skeleton, row snapshot schemas, exit codes);
everything else is guidance.

Related records: [ADR 0003](../adr/0003-online-reconcile.md) holds the
runtime spike results; where older notes disagree with it, ADR 0003 wins.

## 1. What v0.1 is

One static Go binary (macOS, arm64 and amd64, no cgo) plus a small Claude
Code plugin. It switches the Claude desktop app between accounts by swapping
data folders, and carries everything except the account between them, newest
change first, in both directions. It never touches credentials and never
opens a network connection.

### In scope

| Area | v0.1 content |
|---|---|
| Switch engine | P0 preflight, P2 preview, P3 quit, P4 quiescence, P5 harvest and final plan, P6 generation, P7 apply, P8 swap, P9 park, P10 commit, P11 launch and reopen, P12 verify |
| Safety | Intents in `root.json`, `recover`, `rollback`, `rebase`, deletion breaker, worktree loss guard, read-only mode, probe gating |
| Rows | sessions, tasks, waiting-input, environments, group-order, pinned-order, app-prefs, window-state, worktrees, mcp-servers (opt-in), sidebar-view (opt-in, OFF + verify) |
| Storage | Local Storage over pinned goleveldb (copies for reads, quiescence for writes), Chromium key/value encoding, literal-path JSON splicing |
| Merge | Three-way per (profile, row) bases, canonical state plus per-profile overlays, tombstones, revival, field merges, tie-break by epoch |
| CLI | `init`, `profile list/adopt/add/rename/remove`, `status`, `plan`, `switch`, `sync`, `verify`, `recover`, `rollback`, `rebase`, `generations`, `backups`, `doctor`, `log`, `history`, `config`, `uninstall`, `hook session-start`, `version`; `--json` everywhere; exit codes of section 6 |
| Plugin | `/paritydesk:switch` skill (detached switch), SessionStart hook (one fixed line when something is pending) |
| Runner | Detached `gui/<uid>` launchd one-shot job for switches started inside the app |

### Deferred

| Item | Why it waits |
|---|---|
| Artifact sync (beta), `/paritydesk:artifacts` | Needs spike S5 (do hooks fire for the app-injected tools?); the guard and capture depend on it |
| Pins through the app's tools (capture, `set_pinned`) | Needs S5 and S6; v0.1 carries pin order offline (pinned-order row) |
| ON write path for sections and group-by, online reconcile (P13), P1 capture, PreToolUse guard, PostToolUse hook | Needs S5; v0.1 offers sidebar-view as an opt-in OFF + verify row |
| Collapsed groups | Not covered by the first S1 round |
| Seed-only legacy sidebar keys | Read by the app only for unseeded folders; no effect on existing ones |
| SwiftBar, Raycast, Starship, shell alias, menu bar app | Front ends only exec the binary; ship once `--json` schemas settle |
| `doctor --bundle`, `doctor --export-schema` | Need the redaction layer proven with planted-PII tests |
| Native libproc fd scan | The fixed-argv `lsof` path is correct, only slower |
| `profile adopt --from-legacy`, legacy artifact archive import | One-off migration; generic `profile adopt` covers folders |
| Differential LevelDB CI against the C++ library, stub-app macOS job, network-deny sandbox run | CI infrastructure after the core lands |
| GoReleaser, tap formula, release workflow, quarantine check (R8) | Release work after the core lands |
| Scratch-workspaces handling for worktrees | Shape not yet observed |
| Native multi-account signal from main.log | Signal not confirmed (S11); v0.1 uses folder account history only |

Dropped rather than deferred: any SIGTERM path (`--force-quit`); ADR 0003
showed SIGTERM is an unclean exit.

## 2. Runtime facts that shape the code

These come from ADR 0003 and the legacy tooling. Each row says where the
fact is applied.

| Fact | Applied in |
|---|---|
| A value written to Local Storage only is replaced by the config mirror within seconds of launch; values written to both stay. | `sources/sidebar`: every key mirrored under `preferences.epitaxyPrefs` is written to Local Storage **and** `claude_desktop_config.json` in the same plan. Rows group-order, pinned-order and sidebar-view are `off-verify` in the manifest; P12 marks drift as contested. |
| The mirror holds the bare value; Local Storage holds an envelope `{"value", "tabId", "timestamp"}` for `LSS-persisted.*` keys, and `{"state", "version"}` for persisted stores. | `localstorage` envelope helpers (implemented), `sources/*` adapters. |
| The config mirror is written at once by the app; Local Storage later. After an unclean exit the app rebuilds Local Storage from the mirror. | P12 reads only the JSON mirror (never LevelDB) to verify. |
| Quit is vetoed while any session is working; a queued message restarts work. | `app.QuitAndWait` reports the veto through a callback and waits up to `app.quit_timeout`; a timeout is exit 10 with nothing written. In-app switches first wait for the invoking session's turn to end (`app.WaitTurnEnded`), and the skill ends its turn and queues nothing. |
| SIGTERM is not a clean quit. | No signal is ever sent to the app. There is no `--force-quit`. |
| Chrome's native messaging host, the updater and the crash handler live in the bundle but are not the app. | `app.IsAppProcess` excludes them from the app's process set. |
| A staged update installs on quit. | P0 refuses with exit 13 unless `--allow-update`, which waits for the install and re-runs probes on the new version; P5 and P11 re-check the bundle identity. |
| The resume link is `claude://claude.ai/epitaxy/<local session id>`; it is confirmed by a focus line in `main.log`. | `app.SessionURL` (implemented) rebuilds it from an id matching `^local_[0-9a-f-]{36}$`; P11 opens it and P12 looks for the focus signal. |
| An xattr on the data folder survives restarts. | `fsx.MarkerXattr` marker ties bases to folders. |
| Claude in Chrome follows the Chrome profile's sign-in, not the folder. | `doctor` reports whether an extension connected after the last launch and explains the one-Chrome-profile-per-account setup. |
| The Apple Event quit and the launchd job need no extra prompt beyond Automation. | `init` and `doctor --grant-automation` send a harmless `get name` event while the user is present. |
| Local Storage keys are `"_https://claude.ai\0" + encoded(name)`; values are a 1-byte flag (0x01 Latin-1, 0x00 UTF-16LE) plus text. | `localstorage` (implemented): raw bytes kept, edits only when `encode(decode(x)) == x`. |
| goleveldb's lock conflicts with Chromium's lock; opening a live database is refused; opening even read-only rewrites manifest files. | Reads always go through a clone without `LOCK`; writes only after P4. |
| main.log line patterns (quit veto, turn end, focus, bridge connection, pairing disabled, unclean exit) | Patterns live in the compat manifest (`[signals]`), not in code. |

## 3. Packages

Arrows are imports. Pure packages do no I/O.

```
cmd/paritydesk ─► cli ─► orchestrator ─► (interfaces only) ──────────────┐
                   │                                                    │
                   └─ wire.go builds real deps:                         ▼
   config  state  gen  profile  app  runner  compat  sources/*  rows  plan  apply
     │       │     │      │      │      │       │        │        │     │     │
     └───────┴─────┴──────┴──────┴──────┴───────┴────────┴────────┴─────┴─────┘
                    shared foundations (implemented in the skeleton):
          model  ids  pderr  clock  jsonx  localstorage  store/kv  fsx(+fsxtest)
```

| Package | Purpose | Pure |
|---|---|---|
| `pderr` | Exit codes, `*Error`, catalog `PD-E<code>`, `ErrNotImplemented` | yes |
| `clock` | `Clock` with a fake | yes |
| `ids` | UUID, session id, alias and folder validation; masking | yes |
| `model` | Rows, categories, replicas, stamps, `RowSnapshot`, changes, conflicts, contested, marker | yes |
| `jsonx` | Literal-path byte-splice JSON editor; top-level span scan | yes |
| `localstorage` | Key and value codec; envelope helpers | yes |
| `store/kv` | `Reader`/`Writer`/`Opener`; `MemStore` | yes |
| `store/kv/leveldb` | goleveldb snapshot and writable opens; compression check | no |
| `fsx` | `Ops` (swap, exclusive rename, clone, contained atomic write, quarantine move, syncs, identity, xattr, free space); `ReadOnly`; `Contained`; `fsxtest` fake | no |
| `config` | TOML config, defaults, validation, paths | no |
| `compat` | Embedded manifests, probes, mode decision, `Gate` | mostly |
| `sources/*` | On-disk adapters per row; identity scan; session directory helpers | no |
| `merge` | Three-way primitives and tie-break | yes |
| `rows` | One merge module per row; canonical and overlays | yes |
| `plan` | Ops, plan, hash, summary, validator | yes |
| `apply` | Executes a plan on the entering folder | no |
| `gen` | Generations, conditional restore, prune | no |
| `state` | `root.json`, objects, intents, run lock | no |
| `profile` | Folder scan, identity, binding, markers, other tools | no |
| `app` | Processes, holders, LOCK probe, bundle and updater, Apple Event quit, launch, main.log; `exec.go` is the only exec site | no |
| `runner` | Detached launchd job, result file | no |
| `orchestrator` | Phases and commands over interfaces | no (via deps) |
| `render`, `i18n`, `redact` | Output, sanitizer, strings, redaction | yes |
| `hook` | `hook session-start` | no |
| `cli` | cobra commands and wiring | no |
| `testkit` | Synthetic data roots with real goleveldb databases | test only |
| `policy` | Tests for project-wide rules | test only |

### Dependencies

| Module | Version | Use |
|---|---|---|
| `github.com/syndtr/goleveldb` | `v1.0.1-0.20220721030215-126854af5e6d` (pinned) | Local Storage |
| `github.com/pelletier/go-toml/v2` | v2.4.3 | config file |
| `golang.org/x/sys` | v0.50.0 | renamex_np, clonefile, fcntl, xattr, sysctl |
| `golang.org/x/text` | v0.44.0 | NFC folding of folder names |
| `pgregory.net/rapid` | v1.3.0 | property tests only |

The CLI is built on the standard library `flag` package instead of cobra:
cobra's flag library imports `net`, and the zero-network rule is enforced
as "no package in `go list -deps ./...` is `net`, `net/http` or
`crypto/tls`". All five modules above pass that check.

Code comments cite decision ids (D1–D30), review findings (C, H, M, L,
K) and spike ids (S1–S18). WI-8 publishes `docs/architecture.md` with a
short, freshly written index of those ids so the references resolve for
readers of the repository.

## 4. Data flow of a switch

```
P0  lock run; load config, state; recover first if a run is open
    locate bundle (identity tuple); staged update? -> 13 (or --allow-update)
    scan folders -> identities, markers -> profile.Resolve (20 on mismatch)
    manifest + probes per folder -> compat.Decide -> mode (30/31/32)
    other tools -> 14; free space; Automation; outside-switch detection
    inside the app? -> runner.Detacher.Submit, return (job continues)
P2  snapshots (kv OpenSnapshot) of S and T -> adapters Read -> rows Merge
    -> adapters Ops -> plan (approximate) -> summary, DESTRUCTIVE line last
    -> confirm (unless --yes)
P3  intent quit; [in-app: WaitTurnEnded]; QuitAndWait (veto callback) -> 10
P4  CheckQuiescence until quiet (or abort, relaunch S)
P5  bundle identity unchanged? read S and T originals (kv OpenWritable is
    NOT used for reads; reads still use snapshots, now of a quiet folder)
    -> Read -> Merge (bases from state; contested rules) -> Ops -> plan
    -> plan.Validate (breaker 22, worktree guard 23, secrets, denylist)
    -> compare with P2: new destructive changes re-confirm or abort
    -> commit: objects, canonical, overlays, S bases (harvested rows only),
       run.PostHashes, intent planned
P6  gen.Begin -> SaveFile / SaveLSValue / SaveConflict / CloneLevelDB
    -> Finish -> intent backed_up                (skipped for an empty plan)
P7  apply.Apply on T (parked) -> marker on T -> intent applied
    any error: restore LevelDB clone + files from generation, relaunch S, 40
P8  CheckQuiescence on Claude/; intent swapping; SwapDirs(Claude, Claude-T)
    (fallback: journaled two renames); FullSync parent; intent swapped
    post-swap check: new app process or holder since t4 -> swap back or 41
P9  RenameExcl(Claude-T, Claude-S); intent parked
P10 one commit: T bases := applied snapshots, epoch+1, active T,
    previous S, generation committed, tool stamps, run closed
P11 launch (only if I4 holds); reopen session link
P12 detached: wait for LOCK and a stable config mirror; read JSON;
    drift -> contested + base := observed; main.log signals; result file
```

`sync` runs P3–P7 and P10–P12 on the active folder without a swap.
`plan` runs P0 and P2 only. Read-only mode stops after P2.

### Recover decision table (with a journal)

| Last intent | Folder facts | Action |
|---|---|---|
| none / quit | — | Relaunch S if paritydesk quit it; close run |
| planned | — | Close run; canonical already valid; relaunch S |
| backed_up (not done) | incomplete generation | Delete generation; relaunch S |
| applied (not done) or applied | T post-hashes match | Roll T back from generation; relaunch S |
| applied | some mismatch | Mark T's bases invalid; relaunch S |
| swapping | `Claude/` holds S | As "applied" |
| swapping / swapped | `Claude/` holds T | Continue at P9 |
| parked | — | Continue at P10 (bases from post-hashes only if all match) |
| committed / launched | — | Launch if not running; close run |

Without a journal (`state` missing or unreadable): list `Claude` and
`Claude-*`, read identities and markers, expect one folder per profile; if
`Claude/` is missing, propose moving the most recently active profile's
folder into place with an exclusive rename and wait for confirmation. Never
create, merge or delete data folders.

## 5. Row snapshot schemas (binding contract between WI-2 and WI-3)

Every adapter returns a `model.RowSnapshot` with `Schema: 1` and these
shapes. `Value` is JSON. `Raw` (memory only) holds the original bytes the
item came from. `Hash` is sha256 of `Raw`. Secret values inside `Value` are
replaced by the string `"sha256:<hex>"`; `Secret` is then true.

| Row | Item key | Value | Stamp.AppTime | Order / Tombstones |
|---|---|---|---|---|
| sessions | session id `local_<uuid>` | the record object as stored (raw JSON) | `lastActivityAt` | Tombstones: key = session id, `At` = tombstone time (content if it carries one, else file mtime in ms) |
| tasks | `backlog/<id>`, `scheduled/<id>` | the item object | max of known run-time fields | — |
| waiting-input | file name relative to `waiting-input/` | `{"sha256": "<hex>", "kind": "<subdir or extension>"}` | file mtime ms | — |
| environments | `preset/<id>`; `started/<session id>`; `placeDefaults` | preset object with env-var values hashed; environment id string; object | preset's own update time if present | — |
| group-order | `order` | JSON array as stored | envelope `timestamp` | `Order` = the array's string elements |
| pinned-order | `order` | JSON array as stored | envelope `timestamp` | `Order` = session ids |
| sidebar-view | `field:<name>` for top-level members of the scope object except `prefsSeeded`; `section:<id>` per section | member value; section object | envelope `timestamp` | `Order` = section ids |
| app-prefs | preference name (allow-list only) | value | — | — |
| window-state | `window-state.json` | `{"sha256": "<hex>"}` | file mtime ms | — |
| mcp-servers | server name | server object with `env` values hashed | — | — |
| worktrees | absolute worktree path | entry object | — | — |

Rules both sides rely on:

- An absent file or key yields an empty snapshot, never an error.
- `Ops(current, desired)` with `desired == current` returns no ops.
- Items whose `Value` contains hashed secrets are written by copying the
  source's `Raw` bytes for that item (from `OpsContext.Source`); if the
  source no longer has matching bytes (hash differs), the op is refused.
- Envelope timestamps paritydesk writes are returned in
  `OpsResult.WrittenStamps` and stored in `state.Root.ToolStamps`; the merge
  marks them `ToolWritten` so they never win a tie-break.
- Scope keys of sidebar-view (`<acct>/<org>`) are rewritten to the target
  replica; values are never rewritten (no global id replacement).

## 6. Exit codes

Implemented in `pderr` (catalog text included). 0 ok, 2 usage, 3 declined,
10 app did not quit, 11 run lock busy, 12 sessions busy, 13 update pending,
14 other tool, 15 Automation missing, 20 identity mismatch, 21 base invalid,
22 deletion breaker, 23 worktree risk, 30 compat refused, 31 swap-only,
32 degraded, 40 apply rolled back, 41 needs recover, 50 verify drift,
51 needs sign-in, 70 internal (also `ErrNotImplemented`).

## 7. Skeleton conventions

- Every package has `doc.go`. Bodies that are pending return
  `pderr.ErrNotImplemented` (or a zero value where the signature has no
  error); a work item replaces them.
- Already implemented and tested: `pderr`, `clock`, `ids`, `model`,
  `jsonx`, `localstorage`, `store/kv` (interfaces and `MemStore`), `fsx`
  (`Ops`, `Contained`, `ReadOnly`, `fsxtest`), `sources/configjson`,
  `app.SessionURL`, `app.Argv`, `config.Default/Load/ResolvePaths`,
  `cli version`.
- `orchestrator/deps.go` asserts at compile time that every real
  implementation satisfies the orchestrator's interfaces. Changing a
  contract means changing both sides in one reviewed change, coordinated by
  the lead; work items must not change another item's exported API.
- Fixtures use only `testkit.FakeUUID(n)` ids
  (`00000000-0000-4000-8000-<n>`) and never real names, emails, folders or
  ids. Tests never touch `/Applications/Claude.app`,
  `~/Library/Application Support/Claude*` or `~/Library/Logs/Claude`, and
  never run `open`, `osascript` or `launchctl` for real; the command runner
  is faked.

## 8. Work items

Eight items with disjoint paths. Each depends only on the skeleton.

| ID | Title | Owns |
|---|---|---|
| WI-1 | LevelDB store and format hardening | `internal/store/kv/leveldb/`, `internal/localstorage/`, `internal/jsonx/`, `internal/sources/configjson/` |
| WI-2 | Merge primitives and row modules | `internal/merge/`, `internal/rows/` |
| WI-3 | On-disk adapters, compat manifest, test fixtures | `internal/sources/` (except `configjson/`), `internal/compat/`, `internal/testkit/` |
| WI-4 | Platform: filesystem and app control | `internal/fsx/`, `internal/app/` |
| WI-5 | State, generations, profiles | `internal/state/`, `internal/gen/`, `internal/profile/` |
| WI-6 | Plan validation and apply | `internal/plan/`, `internal/apply/` |
| WI-7 | Orchestrator and detached runner | `internal/orchestrator/`, `internal/runner/` |
| WI-8 | CLI, config, output, hook, plugin, policy, CI, docs | `cmd/`, `internal/cli/`, `internal/config/`, `internal/render/`, `internal/i18n/`, `internal/redact/`, `internal/hook/`, `internal/policy/`, `plugin/`, `.claude-plugin/`, `.github/`, root `*.md` except `README.md` capability claims |

Detailed specs and required tests are in the structured result that
accompanies this plan and are repeated in each item's PR description.

After all items merge, the lead runs one integration pass: wire real
implementations in `cli/wire.go`, add golden end-to-end tests in
`internal/orchestrator/e2e_test.go` over `testkit` data roots with a fake
app, and run the recovery drill (crash after every intent, then `recover`,
then assert I1–I5).

## 9. Test strategy

- **Unit tests** in every package; pure packages aim for full branch
  coverage.
- **Property tests** (`pgregory.net/rapid`) for merge and rows: convergence,
  idempotence, order independence, the tombstone rule, "missing without a
  tombstone is restored", no orphan forks, no loss, an invalid base never
  deletes, contested server values are not harvested as edits, bases advance
  only for harvested or applied rows. A state-machine test over N replicas
  with random edits, switches and crashes runs in the orchestrator's
  integration pass.
- **Fuzzing**: the LS codec, `jsonx` (`Set`, `Delete`, `TopLevelString`),
  and the hook payload parser.
- **Byte preservation**: adapters and `jsonx` assert that every byte
  outside an edited span is unchanged.
- **Canary**: `testkit.Canary` in token-bearing fields must never appear in
  logs, state, generations, errors or `--json` output.
- **Platform tests** run on macOS in temp directories: `RENAME_SWAP`,
  clonefile, xattrs, `F_GETLK` with a helper process, symlink refusal.
- **Policy tests**: imports of `net`, `net/http`, `crypto/tls` only in an
  allowlisted, unused package; `exec.Command` only in `internal/app/exec.go`
  (test files exempt); no `leveldb.Recover`; no user-facing text advising
  signing out; PII patterns.
- **No real app**: every app interaction in tests is a fake; the real-app
  checks live in `docs/release-checklist.md` and run by hand before a
  release.

## 10. Definition of done for v0.1

- `go vet ./...`, `go test ./...`, `gofmt -l .` clean on macOS arm64 and
  amd64; policy tests pass.
- Every command in section 1 works against `testkit` data roots with a fake
  app, including crash-and-recover at every intent.
- The manual release checklist passes on a real Mac for the tested app
  version, and ADR 0003 records any new spike before the README claims it.

## 11. Open questions

1. A general "no session is working" signal for switches started from
   Terminal. v0.1 relies on the app's own quit veto (waited on, never
   overridden) and, for in-app switches, on the invoking session's turn-end
   signal.
2. Whether plugin skills expand `${CLAUDE_SESSION_ID}`; without it the
   switch skips the reopen and the turn-end wait falls back to a fixed
   settle delay plus the veto.
3. Where a tombstone's time comes from (file content or mtime); WI-3
   settles it from the observed file shape, documented in the manifest.
4. RENAME_SWAP on FileVault and firmlinked volumes (S7); the journaled
   two-rename fallback ships either way.
5. What the app's worktree sweep does with directories registered only in
   the other folder (S13); the loss guard ships either way.
6. Format of the installed bundle's Info.plist and the updater state path
   on current versions; confirmed by a read-only check on a real Mac
   before release.
7. Whether sidebar-view should default to on, given that ADR 0003 allows
   OFF writes to both copies with verification.
8. Which copy wins when the server and the config mirror disagree (second
   S1 round); affects contested handling, not the write path.
