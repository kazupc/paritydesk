# paritydesk v0.1 implementation plan

Status: accepted for implementation (revision 2, after the skeleton
review). Audience: contributors and the agents that implement the work
items below. This plan is binding where it fixes a contract (package APIs
in the skeleton, row snapshot schemas, the edit and hash rules, intents and
recover tables, exit codes); everything else is guidance.

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
| Switch engine | P0 preflight (busy list), P2 preview, P3 busy wait and quit, P4 quiescence, P5 harvest and final plan, P6 generation, P7 apply, P8 swap, P9 park, P10 commit, P11 launch and reopen, P12 verify |
| Safety | Intents in `root.json`, `recover` (per run kind), `rollback`, `rebase`, deletion breaker, worktree loss guard, read-only mode, probe gating, fixed deny floor, confirmed-plan check for detached runs |
| Rows | sessions, tasks, waiting-input, environments, group-order, pinned-order, sidebar-view (on by default, OFF + verify), app-prefs, window-state, worktrees, mcp-servers (opt-in) |
| Storage | Local Storage over pinned goleveldb (copies for reads, quiescence for writes), Chromium key/value encoding, literal-path JSON splicing composed per target |
| Merge | Three-way per (profile, replica, row) bases, canonical state plus per-profile overlays, tombstones, revival, field merges, tie-break by epoch |
| CLI | `init`, `profile list/adopt/add/rename/remove`, `status`, `plan`, `switch`, `sync`, `verify`, `recover`, `rollback`, `rebase`, `generations`, `backups`, `doctor`, `log`, `history`, `config`, `uninstall`, `hook session-start`, `version`; `--json` everywhere; exit codes of section 6 |
| Plugin | `/paritydesk:switch` skill (detached switch with a confirmed plan), SessionStart hook (one fixed line when something is pending) |
| Runner | Detached `gui/<uid>` launchd one-shot job for switches started inside the app |
| After the core (sequenced, still v0.1) | WI-9 integrations and release: SwiftBar and Raycast wrappers and a shell alias (thin wrappers around `switch --detach`), `completion` command, `doctor --bundle`, GoReleaser, tap formula, release workflow and the quarantine gate (R8). WI-10 CI: stub-app macOS job, differential LevelDB test against the C++ library, network-deny sandbox run. Both start once the `--json` schemas of WI-8 are merged. |

### Deferred to v0.2 (decided)

| Item | Status |
|---|---|
| Artifact sync (beta), `/paritydesk:artifacts` | Planned for v0.2: unit U7 (`spec-artifacts`) |
| Pins through the app's tools (capture, `set_pinned`) | Planned for v0.2: units U5 and U6 (`spec-online`, `spec-pins`) |
| ON write path for sections and group-by, online reconcile (P13), P1 capture, PreToolUse guard, PostToolUse hook | Planned for v0.2: unit U5 (`spec-online`) |
| Collapsed groups | Planned for v0.2: unit U3 (`spec-sidebar-offline`) |
| Seed-only legacy sidebar keys | Planned for v0.2: unit U3 (`spec-sidebar-offline`) |
| Native libproc fd scan | Planned for v0.2: unit U2 (`spec-fdscan`) |
| `profile adopt --from-legacy`, legacy archive import | Planned for v0.2: unit U8 (`spec-legacy-adopt`); legacy archive import: unit U7 (`spec-artifacts`) |
| Scratch-workspaces handling for worktrees | Planned for v0.2: unit U4 (`spec-scratch`) |
| Buttons for the switch confirmation | Planned for v0.2: unit U5 (`spec-online`) |
| Native multi-account signal from main.log | Planned for v0.2: unit U1 (`spec-busy`) |
| Pinned pairing org shown in `doctor` | Planned for v0.2: unit U1 (`spec-busy`) |

Dropped rather than deferred: any SIGTERM path (`--force-quit`); ADR 0003
showed SIGTERM is an unclean exit.

## 2. Runtime facts that shape the code

These come from ADR 0003 and the legacy tooling. Each row says where the
fact is applied.

| Fact | Applied in |
|---|---|
| A value written to Local Storage only is replaced by the config mirror within seconds of launch; values written to both stay. | `sources/sidebar`: every key mirrored under `preferences.epitaxyPrefs` is edited in Local Storage **and** `claude_desktop_config.json` in the same plan. Rows group-order, pinned-order and sidebar-view are `off-verify` in the manifest; P12 marks drift as contested. |
| The config mirror is written at once by the app; Local Storage later. After an unclean exit the app rebuilds Local Storage from the mirror. | The mirror is authoritative: adapters harvest mirrored values from the mirror; a differing LS copy is reported stale and never harvested (section 5). P12 reads the JSON mirror, and Local Storage only through a snapshot copy for Local-Storage-only rows (v0.2: sidebar-collapse). |
| The mirror holds the bare value; Local Storage holds an envelope `{"value", "tabId", "timestamp"}` for `LSS-persisted.*` keys, and `{"state", "version"}` for persisted stores. | `localstorage` envelope helpers (implemented). Item hashes cover the bare value only (section 5). |
| Several rows share one target: `claude_desktop_config.json` holds the mirror of three rows and `mcpServers`; the `dframe-store` value holds both the sidebar view and the pin order. | Adapters return literal-path edits; `plan.Compose` applies them in row order and emits one op per target. |
| Quit is vetoed while any session is working; a queued message restarts work; a vetoed quit can still happen later if the user picks "Wait for Claude". | P0 lists busy sessions (session records plus main.log turn lines); P3 waits until nothing is busy before sending the quit; after a vetoed timeout the run keeps watching (`late_quit_watch`) and relaunches the leaving profile if the app quits late. In-app switches first wait for the invoking session's turn to end, from a log offset recorded before the hand-off. |
| SIGTERM is not a clean quit. | No signal is ever sent to the app. There is no `--force-quit`. The detached job never boots itself out (launchd would SIGTERM it). |
| Chrome's native messaging host, the updater and the crash handler live in the bundle but are not the app. | `app.IsAppProcess` excludes them from the app's process set. |
| A staged update installs on quit. | P0 lets it install: quit intent and `QuitByUs` first, wait for the install, new identity baseline and probes, P3 again if the updater relaunched the app; an abort relaunches (I4). |
| The resume link is `claude://claude.ai/epitaxy/<local session id>`; it is confirmed by a focus line in `main.log`. | `app.SessionURL` (implemented) rebuilds it from an id matching `^local_[0-9a-f-]{36}$`; `app.Argv` accepts only that exact form; P11 opens it only if the session exists in the entering replica; P12 looks for the focus signal. |
| An xattr on the data folder survives restarts. | `fsx.MarkerXattr` marker ties bases to folders. |
| Claude in Chrome follows the Chrome profile's sign-in, not the folder. | `init`, `profile add` and the README explain one Chrome profile per account; `doctor` and `verify` report an extension connection when the Claude log shows one. Claude in Chrome connects on first use, not at launch, so no connection line is not reported as a problem. |
| The Apple Event quit and the launchd job need no extra prompt beyond Automation. | `init` and `doctor --grant-automation` send a harmless `get name` event while the user is present. |
| Local Storage keys are `"_https://claude.ai\0" + encoded(name)`; values are a 1-byte flag (0x01 Latin-1, 0x00 UTF-16LE) plus text. | `localstorage` (implemented): raw bytes kept, edits only when `encode(decode(x)) == x`. |
| goleveldb's lock conflicts with Chromium's lock; opening a live database is refused; opening even read-only rewrites manifest files. | Reads always go through a clone without `LOCK`; writes only after P4. The opener's filesystem is confined to its temp root. |
| main.log line patterns (quit veto, turn start and end, app start, focus, bridge connection, pairing disabled, unclean exit, login required, active org) | Patterns live in the compat manifest (`[signals]`), not in code. An empty pattern means "unknown"; callers fall back. |

## 3. Packages

Arrows are imports. Pure packages do no I/O.

```
cmd/paritydesk ─► cli ─► orchestrator (Service) ─► (interfaces only) ─────┐
                   │                                                      │
                   └─ wire.go builds real deps:                           ▼
   config  state  gen  profile  app  runner  compat  sources/*  rows  plan  apply
     │       │     │      │      │      │       │        │        │     │     │
     └───────┴─────┴──────┴──────┴──────┴───────┴────────┴────────┴─────┴─────┘
                    shared foundations (implemented in the skeleton):
     model  ids  pderr  clock  jsonx  localstorage  store/kv  fsx(+fsxtest)  denylist
```

| Package | Purpose | Pure |
|---|---|---|
| `pderr` | Exit codes, `*Error`, catalog `PD-E<code>`, `ErrNotImplemented` | yes |
| `clock` | `Clock` with a fake | yes |
| `ids` | UUID, session id, alias (never starts with `-`) and folder validation; masking | yes |
| `model` | Rows, categories, replicas, stamps, `RowSnapshot`, `Canonical`/`ItemHash`/`NewItem`, changes, conflicts, contested, marker | yes |
| `jsonx` | Literal-path byte-splice JSON editor; top-level span scan | yes |
| `localstorage` | Key and value codec; envelope helpers | yes |
| `store/kv` | `Reader`/`Writer`/`Opener` (with `CheckFormat`); `ReadOnly`; `MemStore`, `MemOpener` | yes |
| `store/kv/leveldb` | goleveldb snapshot and writable opens; compression check; confined filesystem | no |
| `fsx` | `Ops` (swap, exclusive rename, clone, contained atomic write, quarantine move, guarded `RemoveAll(root, rel)`, syncs, identity, xattr, free space); `ReadOnly`; `Confine`; `Contained`; `CheckRemovable`; `CheckTestPath`; `fsxtest` fake bound to the test's temp tree | no |
| `denylist` | Fixed, non-overridable floor of paths and LS key fragments no plan may touch | yes |
| `config` | TOML config, defaults, validation, paths | no |
| `compat` | Embedded manifests, probes, mode decision, `Gate` | mostly |
| `sources/*` | On-disk adapters per row (edits, mirror reader); identity scan; session directory helpers | no |
| `merge` | Three-way primitives and tie-break | yes |
| `rows` | One merge module per row; canonical and both overlays; `PostMerge` | yes |
| `plan` | Edits, `Compose`, ops, plan, hash, summary, destructive digest, validator | yes |
| `apply` | Executes a plan on the entering folder | no |
| `gen` | Generations, conditional and idempotent restore, prune | no |
| `state` | `root.json`, objects, intents, run lock keyed to the data root | no |
| `profile` | Folder scan, identity, binding, markers, other tools | no |
| `app` | Processes, holders, LOCK probe, bundle and updater, Apple Event quit, launch, main.log (busy, turn end, active org); `exec.go` is the only exec site and validates every argument | no |
| `runner` | Detached launchd job, result file, reaping, confirmed-plan store | no |
| `orchestrator` | Phases and commands over interfaces; `Service`; job argv (`JobArgs`) | no (via deps) |
| `render`, `i18n`, `redact` | Output, sanitizer, strings, redaction | yes |
| `hook` | `hook session-start` | no |
| `cli` | Standard library `flag` command tree and wiring | no |
| `online` | (v0.2, planned) Online reconcile after a launch: queue, captures, PreToolUse guard, PostToolUse interpretation, providers (sidebar; pins) | mostly |
| `store/idb` | (v0.2, planned) Read-only IndexedDB-on-LevelDB reader for one string key, over a private clone | no |
| `sources/pins` | (v0.2, planned) Adapter for row pins (which Code sessions are pinned) and the reader of the app's own mirror of that list | no |
| `sources/scratch` | (v0.2, planned) Adapter for row scratch: the scratch workspaces that session records reference, carried at the same path | no |
| `artifacts` | (v0.2, planned) Artifact sync: archive, rounds, judge, ingest, the online provider, legacy archive import | mostly |
| `legacy` | (v0.2, planned) Importer for folders left by an earlier folder-swap script (`profile adopt --from-legacy`); every input untrusted, writes nothing | mostly |
| `fdscan` | (v0.2, planned) Native open-file scan through libproc without cgo; `lsof` stays the fallback | no |
| `testkit` | Synthetic data roots with real goleveldb databases | test only |
| `policy` | Tests for project-wide rules | test only |

### Dependencies

| Module | Version | Use |
|---|---|---|
| `github.com/syndtr/goleveldb` | `v1.0.1-0.20220721030215-126854af5e6d` (pinned) | Local Storage |
| `github.com/golang/snappy` | v0.0.4 (indirect, via goleveldb) | table blocks |
| `github.com/pelletier/go-toml/v2` | v2.4.3 | config file |
| `golang.org/x/sys` | v0.50.0 | renamex_np, clonefile, fcntl, xattr, sysctl |
| `golang.org/x/text` | v0.44.0 | NFC folding of folder names |
| `pgregory.net/rapid` | v1.3.0 | property tests only |

`go.mod` and `go.sum` belong to the lead and are frozen for every work
item; a new module needs a lead change. **Pending lead step before WI-1 and
WI-3 start:** importing `github.com/syndtr/goleveldb/leveldb` needs the
`github.com/golang/snappy v0.0.4` requirement and its `go.sum` hashes
(`go mod tidy` once, verified with `go list -deps ./...`). It was prepared
but not applied in the skeleton step because changing `go.mod` needs the
maintainer's approval.

The CLI is built on the standard library `flag` package instead of cobra:
cobra's flag library imports `net`, and the zero-network rule is enforced
as "no package in `go list -deps ./...` is `net`, `net/http` or
`crypto/tls`". All modules above pass that check. Shell completion comes
from a small hand-written `completion` command (WI-9).

Code comments cite decision ids (D1–D30), review findings (C, H, M, L,
K) and spike ids (S1–S18). WI-8 publishes `docs/architecture.md` with a
short, freshly written index of those ids so the references resolve for
readers of the repository.

## 4. Data flow of a switch

```
P0  record main.log offset (LogOffset) first, while the invoking turn runs
    detach? (--detach, or inside the app's process tree, and not --from-runner)
      -> no lock is taken by the parent
      -> interactive parent: read-only preflight + P2 preview, confirm,
         save the plan (PlanStore), then Submit with --confirmed-plan
      -> non-interactive parent (the skill): requires --confirmed-plan or
         --yes; runner.Reap, then Submit(SwitchOptions.JobArgs); return
    take the run lock (the job retries for DetachedLockWait)
    load config, state; open run -> 41 unless recover; data-root binding
    locate bundle (identity tuple); staged update? -> quit, wait for the install, re-probe
    scan folders -> identities, markers -> profile.Resolve(hints: active
      org from main.log) (20 on mismatch; PendingAdd -> 41)
    manifest + probes per folder + kv CheckFormat on S and T copies
      -> compat.Decide (30/31/32; unknown compression and native
         multi-account -> swap-only; unknown org with several -> per-org
         rows skipped)
    other tools -> 14; free space; Automation; outside-switch detection
    busy list: records (SessionInfo.Busy) + App.BusySessions, minus the
      invoking session -> ConfirmBusy, or 12 when non-interactive
P2  snapshots (kv OpenSnapshot) of S and T -> adapters Read -> rows Merge
    (AllRows order; Policy from config, manifest field lists, T's
    permission acks) -> rows.PostMerge -> adapters Ops (edits)
    -> plan.Compose (source: T's files and T's LS snapshot) -> plan
    (approximate) -> summary, DESTRUCTIVE line last
    -> with --confirmed-plan: load it (missing or older than
       ConfirmedPlanTTL -> 3); NewDestructive(confirmed, this) non-empty
       -> 3 before quitting; NeedsConfirmation without --confirm-first-sync
       -> 21
    -> otherwise confirm (unless --yes; expires after ConfirmedPlanTTL)
P3  [in-app: WaitTurnEnded from LogOffset]; wait until the busy list is
    empty (detached: up to quit_timeout, then 12 without quitting;
    interactive: ask again if it grew); intent quit + QuitByUs committed;
    QuitAndWait (veto callback)
    timeout: not vetoed -> close run, 10; vetoed (Pending) -> WaitExited up
    to late_quit_watch: exited -> relaunch S (I4), close run, 10 ("quit
    late; relaunched"); still running -> close run, 10 with quit_pending
P4  CheckQuiescence until quiet (or abort, relaunch S)
P5  bundle identity unchanged? read S and T originals (reads still use
    snapshots, now of a quiet folder) -> Read -> Merge (bases from state
    per replica; contested rules) -> PostMerge -> Ops -> Compose -> plan
    -> plan.Validate (deny floor + manifest, LS allow-list, tmp globs,
       destructive-op mapping, breaker 22, worktree guard 23, secrets)
    -> compare with P2: new destructive changes re-confirm, or abort and
       relaunch S when detached
    === from the commit below to P10, and on every abort path, the
        context is context.WithoutCancel(ctx) with CommitTimeout ===
    -> commit: objects, canonical, both overlays, S bases (harvested rows
       only), run.PostHashes, intent planned
P6  gen.Begin -> SaveFile / SaveLSValue / SaveConflict / CloneLevelDB
    -> Finish -> intent backed_up                (skipped for an empty plan)
P7  apply.Apply on T (parked) -> marker on T -> intent applied
    any error: restore LevelDB clone + files from generation, relaunch S, 40
P8  CheckQuiescence on Claude/; intent swapping; SwapDirs(Claude, Claude-T)
    (ErrSwapUnsupported: fallback_s_parked = RenameExcl(Claude, Claude-S),
     fallback_t_placed = RenameExcl(Claude-T, Claude)); FullSync parent;
    intent swapped
    post-swap check: new app process or holder since t4 -> intent
    swap_back, swap back (no fds into moved folders) and abort as P7, else 41
P9  RenameExcl(Claude-T, Claude-S); intent parked    (not needed after the
    fallback, which parks S in its first step)
P10 re-read T (adapters over a snapshot of T, app still stopped); T bases
    := re-read snapshots for rows whose re-read hashes equal Desired
    (others: base invalid, warning); one commit: T bases, epoch+1, active
    T, previous S, generation committed, tool stamps, run closed
P11 launch (only if I4 holds); reopen the session link only if the
    session id is in T's sessions snapshot (else warn and skip)
P12 detached: wait for LOCK and a stable config mirror; read JSON;
    compare items via sources.MirrorReader hashes, and (v0.2) Local
    Storage through a snapshot copy for Local-Storage-only rows
    (sources.LSVerifier; unreadable -> unverified, exit 0); drift ->
    contested + base := observed; T identity check; main.log signals;
    result file
```

`sync` runs P0, P2–P7 and P10–P12 on the active folder without a swap; it
refuses to run inside the app's process tree (usage error telling the user
to run it from Terminal), since it quits the app. `plan` runs P0 and P2 only (no lock) and saves the plan unless
read-only. Read-only mode stops after P2 and writes nothing: no lock, no
state commit, no config save, no plan save, no runner submit; Local Storage
is read through `kv.ReadOnly` over the confined LevelDB opener.

Cancellation: the root context is cancelled by SIGINT and SIGTERM. Up to
the P5 commit the orchestrator checks it at phase boundaries and aborts
cleanly (relaunching S if it quit the app). From the P5 commit to P10, and
on every abort, rollback and relaunch path, it uses
`context.WithoutCancel(ctx)` with `CommitTimeout`.

Staged update: at P0 the run records the quit
intent and `QuitByUs` before the quit (data `reason=update`), waits for the
busy list, quits, waits with `WaitUpdateInstalled`, re-runs `Locate` (new
baseline for P5 and P11) and the probes on the new version, and continues;
if the updater relaunched the app, P3 runs again. A decline or abort after
the update quit relaunches S (I4).

### Invariants

- I1: after any crash and `recover`, `Claude/` holds exactly one identified,
  registered account. Exception: between a profile add's park step and the
  new account's sign-in, `Claude/` may be empty or unidentified; this is
  recorded in `root.PendingAdd`, shown by `status` and the SessionStart hook,
  and `switch`/`sync` exit 41 until `recover` binds or undoes it.
- I2: no data folder is ever created, merged or deleted by paritydesk.
- I3: every write is preceded by a complete generation.
- I4: the app is launched only if paritydesk quit it in this run
  (`QuitByUs`) and `Claude/` holds a registered, identified profile.
- I5: bases advance only for harvested or applied rows.

### Recover decision tables (with a journal)

Decisions are by the last intent and the identities found in the folders;
whenever the identities contradict the journal (for example `Claude/` holds
neither S nor T), recover stops, reports masked identities and proposes a
move that needs `ConfirmRecover`. Every relaunch below is subject to I4.

**RunSwitch**

| Last intent | Folder facts | Action |
|---|---|---|
| none / quit | — | Relaunch S if paritydesk quit it; close run |
| planned | — | Close run (committed canonical, overlays and S bases stay valid); relaunch S |
| backed_up (not done) | incomplete generation | Delete incomplete generations; close run; relaunch S |
| backed_up, applied (done or not), swapping, swap_back | `Claude/` holds S | Roll T back from the generation (conditional, idempotent restore; LevelDB clone if T's LS was opened); T's bases invalid for conflicted rows; close run; relaunch S |
| swapping, swapped, swap_back | `Claude/` holds T | Continue at P9 |
| fallback_s_parked (done or not) | `Claude/` holds S | As "applied" |
| fallback_s_parked (done or not), fallback_t_placed (not done) | no `Claude/`; S parked; T in its parked folder | Complete the second rename (T into `Claude/`), continue at P10; with `--rollback`: put S back, then as "applied" |
| fallback_t_placed | `Claude/` holds T | Continue at P10 |
| parked | — | Continue at P10 (bases from the re-read only if every post-hash matches; otherwise T's bases invalid) |

**RunSync** (intents quit, planned, backed_up, applied; no swap)

| Last intent | Folder facts | Action |
|---|---|---|
| none / quit | — | Relaunch if paritydesk quit the app; close run |
| planned | — | Close run; relaunch |
| backed_up (not done) | incomplete generation | Delete it; close run; relaunch |
| backed_up (done), applied (done or not) | every post-hash matches | Finish at P10 (re-read bases); relaunch |
| backed_up (done), applied (done or not) | some mismatch | Conditional restore from the generation; bases of mismatched rows invalid; close run; relaunch |

**RunRollback** (intents quit, restoring)

| Last intent | Action |
|---|---|
| none / quit | Relaunch if paritydesk quit the app; close run |
| restoring (done or not) | Re-run the conditional restore (idempotent: entries already at their pre-hash count as restored); record the epoch event; set bases per the recorded `propagate` choice; close run; relaunch |

**RunAdd** (intents quit, add_parked)

| Last intent | Folder facts | Action |
|---|---|---|
| none / quit | — | Relaunch if paritydesk quit the app; close run |
| add_parked (not done) | `Claude/` holds S | Close run; relaunch |
| add_parked (done or not) | no `Claude/`, S parked | Put S back with an exclusive rename; close run; relaunch (the user runs `profile add` again) |
| add_parked (done) | `Claude/` empty or unidentified | Close run with `PendingAdd` set (the I1 exception); launch |
| any, or `PendingAdd` set | `Claude/` holds an account no profile has | Ask (`ConfirmAddBind`) to bind it to the pending alias, then save the config and clear `PendingAdd`; declined: leave as is |

Without a journal (`state` missing or unreadable): list `Claude` and
`Claude-*`, read identities and markers, expect one folder per profile; if
`Claude/` is missing, propose moving the most recently active profile's
folder into place with an exclusive rename and wait for confirmation. Never
create, merge or delete data folders.

## 5. Row snapshot schemas and the edit contract (binding between WI-2, WI-3, WI-6, WI-7)

Every adapter returns a `model.RowSnapshot` with `Schema: 1` and these
shapes. `Value` is JSON. `Raw` (memory only) holds the original bytes the
item came from and is used only for byte copies. **`Hash` is
`model.ItemHash(Value)`**: sha256 of the canonical form of the normalized
value (sorted keys, no insignificant whitespace). Envelope metadata
(`tabId`, `timestamp`, persisted-store `version`) is never part of `Value`,
so the same value read from Local Storage or from the mirror hashes the
same. Items are built with `model.NewItem`. Secret values inside `Value`
are replaced by the string `"sha256:<hex>"` before the item leaves its
adapter; `Secret` is then true.

| Row | Item key | Value | Stamp.AppTime | Order / Tombstones |
|---|---|---|---|---|
| sessions | session id `local_<uuid>` | the record object as stored (raw JSON) | `lastActivityAt` | Tombstones: key = session id, `At` = tombstone time (the file's bare epoch-ms number on app 6.x, else file mtime in ms; see [ADR 0004](../adr/0004-data-shape-survey.md)) |
| tasks | `backlog/<id>`, `scheduled/<id>` | the item object | max of known run-time fields | — |
| waiting-input | file name relative to `waiting-input/` | `{"sha256": "<hex>", "kind": "<subdir or extension>"}` | file mtime ms | — |
| environments | `preset/<id>`; `started/<session id>`; `placeDefaults` | preset object with env-var values hashed; environment id string; object | preset's own update time if present | — |
| group-order | `order` | JSON array (bare value, from the mirror) | envelope `timestamp` when LS and mirror agree, else 0 | `Order` = the array's string elements |
| pinned-order | `order` | JSON array (bare value, from the mirror) | as group-order | `Order` = session ids |
| sidebar-view | `field:<name>` for top-level members of the scope object except `prefsSeeded`; `section:<id>` per section | member value; section object (from the mirror) | as group-order | `Order` = section ids |
| app-prefs | preference name (allow-list only) | value | — | — |
| window-state | `window-state.json` | `{"sha256": "<hex>"}` | file mtime ms | — |
| mcp-servers | server name | server object with `env` values hashed | — | — |
| worktrees | worktree name (the key in `git-worktrees.json` on app 6.x, ADR 0004) | entry object | — | — |
| scratch (v0.2) | relative to the scratch root: `<acct>/<org>/<leaf>/` (root marker), `…/<rel>/` (directory or git unit), `…/<rel>` (file or symlink) | `{"k":"root"\|"dir"\|"git"\|"file"\|"link", …}`: mode, sha256 and size of files, link target, git unit digest; content only | file or link lstat mtime ms; newest member mtime for a git unit; else 0 | Tombstones only in canonical |
| sidebar-collapse (v0.2) | `collapsedGroups`, `collapsedFamilies` | JSON array of the member's string elements, deduplicated and sorted | 0 (the epoch decides) | — |
| sidebar-seed (v0.2) | `code-sessions-sort-ascending`, `code-sessions-show-empty-projects` | the bare JSON boolean (from the mirror) | envelope `timestamp` when LS and mirror agree, else 0 | — |
| pins (v0.2) | session id `local_<uuid>` | `true` | 0 (the epoch decides) | — |

Rules every side relies on:

- An absent file or key yields an empty snapshot, never an error.
- **Mirror authority.** For keys mirrored under `preferences.epitaxyPrefs`,
  `Read` takes the value from the mirror. A differing Local Storage copy is
  listed in `RowSnapshot.Stale`, reported as a warning, and never harvested
  as an edit. `Ops` always edits both copies.
- **Edits, not ops.** `Ops(current, desired)` returns `plan.Edit`s: a
  location (file or LS item name), a literal JSON path, the new value, and
  the hash of the bytes the edit was computed against. `Ops(current,
  current)` returns no edits. JSON files are edited by `set`/`delete` at
  literal paths (parents created by earlier `set` edits of `{}`), new or
  regenerated files by `replace`, removals by `remove`. The orchestrator
  concatenates edits in `model.AllRows` order and `plan.Compose` emits one
  op per location; two rows may not edit overlapping paths of one
  location.
- Items whose `Value` contains hashed secrets are written by copying the
  source's `Raw` bytes for that item (from `OpsContext.Source`). If the
  source no longer has matching bytes, the adapter keeps the target's value
  for that item and reports a `ChangeSkipped` in `OpsResult.Skipped`; the
  run continues.
- Envelope timestamps paritydesk writes are returned in
  `OpsResult.WrittenStamps` and stored in `state.Root.ToolStamps`; the merge
  marks them `ToolWritten` so they never win a tie-break.
- Scope keys of sidebar-view (`<acct>/<org>`) are rewritten to the target
  replica; values are never rewritten (no global id replacement).
- **Contested.** P12 compares the mirror's items (`sources.MirrorReader`,
  same hashes as `Read`) with what was written. A difference stores a
  `model.Contested` per item (server and intended hash, both item hashes)
  under `state.ContestedKey`; a later harvest counts that item as changed
  only if its hash differs from both.
- **Bases.** Bases, overlays, tool stamps and contested entries are keyed
  by `state.BaseKey{alias, replica, row}`. A replica change (for example a
  new active org) is a base miss: union and confirmation, never edits.
  T's bases at P10 are the re-read snapshots, never `Desired`, so a second
  switch without edits produces an empty plan. (v0.2) Set rows
  (sidebar-collapse) and rows that never delete (sidebar-seed): a base
  miss merges as a union, which cannot delete; nothing needs confirming.
- **Overlays.** `rows.Input` carries both overlays and `rows.Output`
  returns both. A profile's edits in a category it does not export stay in
  its overlay and never reach canonical; an overlay never loses an item
  unless its own side removed it against a valid base.
- **Field lists** (`per_field`, `account_bound`, `run_state`, `max_time`,
  `parent`) come only from the manifest, through `rows.Policy.Fields`.
  (v0.2) `rows.Policy` also carries the row's manifest `Limits` and, for
  the sidebar-offline rows, `EnteringStoreMissing`, `EnteringSeeded` and
  `SeedFrom`; adapters may implement `sources.LSVerifier` (verified from a
  Local Storage snapshot) and `sources.TargetStater` (facts about the
  entering folder that decide what a merge may write there).
- **Destructive ops.** Every op that is `Destructive`, `OpQuarantine` or
  `OpLSDelete` maps to a `ChangeDeleted` of the same row and item, or
  carries a reason from `plan.NonUserDataReasons` (app `.json.tmp`
  candidates). `plan.Validate` refuses anything else.

## 6. Exit codes

Implemented in `pderr` (catalog text included). 0 ok, 2 usage, 3 declined
(also: confirmed plan missing, expired or exceeded), 10 app did not quit, 11
run lock busy, 12 sessions busy, 13 update pending, 14 other tool, 15
Automation missing, 20 identity mismatch, 21 base invalid, 22 deletion
breaker, 23 worktree risk, 30 compat refused, 31 swap-only, 32 degraded, 40
apply rolled back, 41 needs recover, 50 verify drift, 51 needs sign-in
(rendered with the validated alias), 70 internal (also
`ErrNotImplemented`).

## 7. Skeleton conventions

- Every package has `doc.go`. Bodies that are pending return
  `pderr.ErrNotImplemented` (or a zero value where the signature has no
  error); a work item replaces them.
- Already implemented and tested (owners keep them as they are): `pderr`,
  `clock`, `ids`, `model` (including `Canonical`, `ItemHash`, `NewItem`),
  `jsonx`, `localstorage`, `store/kv` (interfaces, `ReadOnly`, `MemStore`,
  `MemOpener`), `fsx` (`Ops`, `Contained`, `ReadOnly`, `Confine`,
  `CheckRemovable`, `CheckTestPath`, `fsxtest`), `denylist`,
  `sources/configjson`, `app.SessionURL`, `app.Argv` (with validation),
  `plan.Hash`/`Summarize`/`NewDestructive`/`DestructiveDigest`,
  `state.BaseKey`/`ContestedKey`/`LockFile`, `orchestrator.JobArgs`,
  `config.Default/Load/ResolvePaths`, `cli version`.
- `orchestrator/deps.go` asserts at compile time that every real
  implementation satisfies the orchestrator's interfaces, and that
  `*Orchestrator` satisfies `Service`. Changing a contract means changing
  both sides in one reviewed change, coordinated by the lead; work items
  must not change another item's exported API, nor the implemented
  functions listed above.
- Fixtures use only `testkit.FakeUUID(n)` ids
  (`00000000-0000-4000-8000-<n>`) and never real names, emails, folders or
  ids. Tests never touch `/Applications/Claude.app`,
  `~/Library/Application Support/Claude*` or `~/Library/Logs/Claude`, and
  never run `open`, `osascript` or `launchctl` for real: the command runner
  is faked, `fsxtest.New(t)` fails the test for any path outside the test's
  temp tree, and constructors taking real locations (`app.NewDarwin`,
  `leveldb.New`, `buildDeps`) refuse them under `go test`
  (`fsx.CheckTestPath`). Tests needing the real app are skipped unless
  `PARITYDESK_E2E=1`.
- Fixture files under `internal/**/testdata/` are tracked even with
  `.ldb` or `.log` extensions; prefer generating LevelDB fixtures in
  `t.TempDir()`.

## 8. Work items

Eight items with disjoint paths. Each depends only on the skeleton (and on
the pending `go.mod` step for the two that import goleveldb). Unowned paths
belong to the lead: `go.mod`, `go.sum`, `internal/model/`, `internal/ids/`,
`internal/pderr/`, `internal/clock/`, `docs/dev/`, `docs/adr/`,
`.gitignore`, `LICENSE`.

| ID | Title | Owns |
|---|---|---|
| WI-1 | LevelDB store and format hardening | `internal/store/kv/` (including `leveldb/`), `internal/localstorage/`, `internal/jsonx/`, `internal/sources/configjson/` |
| WI-2 | Merge primitives and row modules | `internal/merge/`, `internal/rows/` |
| WI-3 | On-disk adapters, compat manifest, test fixtures | `internal/sources/` (except `configjson/`), `internal/compat/`, `internal/testkit/` |
| WI-4 | Platform: filesystem and app control | `internal/fsx/`, `internal/app/` |
| WI-5 | State, generations, profiles | `internal/state/`, `internal/gen/`, `internal/profile/` |
| WI-6 | Plan composition, validation and apply | `internal/plan/`, `internal/apply/`, `internal/denylist/` |
| WI-7 | Orchestrator and detached runner | `internal/orchestrator/`, `internal/runner/` |
| WI-8 | CLI, config, output, hook, plugin, policy, CI, docs | `cmd/`, `internal/cli/`, `internal/config/`, `internal/render/`, `internal/i18n/`, `internal/redact/`, `internal/hook/`, `internal/policy/`, `plugin/`, `.claude-plugin/`, `.github/`, `README.md`, `docs/architecture.md`, `docs/if-something-goes-wrong.md`, `docs/release-checklist.md`, `PRIVACY.md`, `SECURITY.md`, `DISCLAIMER.md`, `CONTRIBUTING.md`, `CHANGELOG.md` |

Cross-item seams are interfaces or function fields so every item tests
alone: the orchestrator takes `Compose`, `Validate`, `PostMerge` and
`PermissionAcks` as `Deps` fields and uses `kv.MemOpener`; cli depends on
`orchestrator.Service`; testkit carries its own goleveldb-backed reader for
adapter tests; runner takes an `IsApp` function.

Detailed specs and required tests are in the structured result that
accompanies this plan and are repeated in each item's PR description.

After all items merge, the lead runs one integration pass: wire real
implementations in `cli/wire.go`, add golden end-to-end tests in
`internal/orchestrator/e2e_test.go` over `testkit` data roots with a fake
app, and run the recovery drill (crash after every intent of every run
kind, then `recover`, then assert I1–I5). WI-9 and WI-10 (section 1) follow.

## 9. Test strategy

- **Unit tests** in every package, table-driven; pure packages aim for full
  branch coverage.
- **Property tests** (`pgregory.net/rapid`) for merge and rows: convergence,
  idempotence, order independence, the tombstone rule, "missing without a
  tombstone is restored", no orphan forks, no loss, an invalid base never
  deletes, contested server values are not harvested as edits, bases advance
  only for harvested or applied rows, non-exported edits never reach
  canonical, overlays never lose items. A state-machine test over N
  replicas with random edits, switches and crashes runs in the
  orchestrator's integration pass.
- **Composition**: a plan where two rows edit `claude_desktop_config.json`
  and the `dframe-store` value yields one op per target containing both
  edits.
- **Fuzzing**: the LS codec, `jsonx` (`Set`, `Delete`, `TopLevelString`),
  `plan.Compose`, and the hook payload parser.
- **Byte preservation**: adapters and `jsonx` assert that every byte
  outside an edited span is unchanged.
- **Canary**: `testkit.Canary` in token-bearing fields must never appear in
  logs, state, generations, errors or `--json` output.
- **Platform tests** run on macOS in temp directories: `RENAME_SWAP`,
  clonefile, xattrs, `F_GETLK` with a helper process, symlink refusal, and a
  conformance suite run against both `darwinOps` and `fsxtest`.
- **Cancellation**: orchestrator tests cancel the context in every phase
  and assert I1 and I4.
- **Policy tests**: no `net`, `net/http`, `crypto/tls` in the dependency
  graph; `exec.Command` only in `internal/app/exec.go` (test files exempt);
  no `leveldb.Recover`; no user-facing text advising signing out; PII
  patterns, with personal labels read only from an untracked deny-list.
- **No real app**: every app interaction in tests is a fake; the real-app
  checks live in `docs/release-checklist.md` and run by hand before a
  release.

## 10. Definition of done for v0.1

- `go vet ./...`, `go test ./...`, `gofmt -l .` clean on macOS arm64 and
  amd64; policy tests pass.
- Every command in section 1 works against `testkit` data roots with a fake
  app, including crash-and-recover at every intent of every run kind.
- The manual release checklist passes on a real Mac for the tested app
  version, and ADR 0003 records any new spike before the README claims it.

## 11. Open questions

1. The main.log pattern for a turn start is not yet known. The manifest
   leaves it empty, and the session records have no field that means a
   turn is running (the `busy` role is empty, ADR 0004). Busy detection
   then relies on the app's own quit veto (with the late-quit watch). The
   `active_org` pattern was taken from observed lines (ADR 0004); release
   checklist step 5 confirms it with an account in two organizations.
2. Whether plugin skills expand `${CLAUDE_SESSION_ID}`. The skill quotes it
   and an empty value means no reopen and no turn wait; the job then relies
   on the busy wait and the veto.
3. Where a tombstone's time comes from (file content or mtime). Settled
   by [ADR 0004](../adr/0004-data-shape-survey.md): on app 6.x a tombstone
   file holds a bare epoch-ms number; anything else falls back to the
   file's mtime.
4. RENAME_SWAP on FileVault and firmlinked volumes (S7). Settled: it
   works on APFS with FileVault, through the firmlinked path too
   ([ADR 0003](../adr/0003-online-reconcile.md), second round). The
   journaled two-rename fallback ships for other volumes.
5. What the app's worktree sweep does with directories registered only in
   the other folder (S13). Settled: it can remove a clean worktree it does
   not know by name, so the merged registry and session records are what
   protect them. The loss guard ships either way.
6. Format of the installed bundle's Info.plist and the updater state path
   on current versions; confirmed by a read-only check on a real Mac
   before release.
7. Which copy wins when the server and the config mirror disagree (second
   S1 round). Settled from the code: the server, for the fields it syncs
   per account; Local Storage or the mirror for device-local values
   ([ADR 0003](../adr/0003-online-reconcile.md)). The existing contested
   handling covers it.
8. Whether `go 1.26.0` (rewritten by `go mod tidy` from `go 1.26`) is the
   directive the maintainer wants.
