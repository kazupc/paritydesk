# Architecture

This page gives a short map of the code and an index of the ids that code
comments cite: decisions (D), invariants (I), goals (G), review findings
(C critical, H high, M medium, L low, K competitive) and runtime spikes
(S). The design notes that first defined these ids are not part of the
repository; each entry below is a short, self-contained summary of what
the id means for the code. Spike results live in
[ADR 0003](adr/0003-online-reconcile.md); the on-disk shapes observed on
app 6.x and the decisions taken from them are in
[ADR 0004](adr/0004-data-shape-survey.md); the binding contracts of v0.1
are in the [implementation plan](dev/implementation-plan.md).

## Map

```
cmd/paritydesk -> cli -> orchestrator (Service) -> interfaces only
                   |
                   +-- wire.go builds the real dependencies:
       config state gen profile app runner compat sources/* rows plan apply
                   shared foundations:
       model ids pderr clock jsonx localstorage store/kv fsx denylist
       render i18n redact hook (front end and plugin entry point)
```

A switch runs in phases P0 to P12: preflight and busy list (P0), an
approximate preview while Claude runs (P2), waiting for idle sessions and
quitting by Apple Event (P3), quiescence (P4), the final plan from quiet
folders (P5), a backup generation (P6), apply to the parked target (P7),
the folder swap (P8), parking the leaving folder (P9), one state commit
(P10), relaunch and session reopen (P11), and verification after launch
(P12). P1 (capture) and P13 (online reconcile) are v0.2.

## Decisions

| Id | Meaning in the code |
|---|---|
| D2 | No message ever suggests ending an account's session; every account stays signed in. A policy test checks strings, docs and the plugin. |
| D4 | A folder's account comes from its own contents; sync bases are tied to the physical folder by an extended-attribute marker plus the folder's file identity. |
| D5 | One static Go binary without cgo; Local Storage through a pinned goleveldb, never with its recovery functions. |
| D6 | Sync works on replicas: one (folder, account, organization). Each switch harvests the leaving and the entering replica against bases kept per replica and row, into a canonical state plus per-profile overlays. |
| D7 | Conflict tie-break: the switch epoch first, then an app timestamp from this Mac that paritydesk did not write, then the side being left. |
| D8 | Session records merge per field for a fixed field list and otherwise by last activity; permission modes are capped at what the target allows. |
| D9 | Deletions propagate only from tombstones, and only when the tombstone is newer than the other copy's last activity; otherwise the item is revived with a warning. |
| D10 | Account-bound fields of a session record keep the target's own values for sessions the target already knows. |
| D13 | Writes are gated by shape probes of the files, not by the app version; a failed probe drops its row, a failed critical probe means swap-only or refusal. |
| D14 | Claude is quit only by Apple Event; a timeout never escalates to a signal. |
| D15 | Quiescence needs every signal clear: no app process, free LevelDB locks, no open files in the data folders, no updater activity. |
| D16 | Backups are numbered generations with per-item previous values and a full LevelDB clone only in the most recent ones. |
| D17 | The Local Storage size bookkeeping key is left untouched. |
| D18 | The preview plan and the final plan are compared; new destructive changes need a new confirmation or abort a detached run. |
| D19 | One Claude Code plugin with a `switch` skill and one hook entry point, `paritydesk hook <event>`. |
| D20 | A switch started inside the app runs as a detached launchd one-shot job. |
| D21 | Config is TOML with user choices only; state is one atomically replaced JSON root plus content-addressed objects. |
| D22 | Zero network and zero credential access; external commands only from `internal/app/exec.go` with fixed argument templates. |
| D27 | Each profile has a private overlay and export and import lists; secret values are stored only as hashes and copied byte for byte, never across a managed-account boundary without a per-key allowance. |
| D28 | Another folder-swapping tool on the same Mac stops the run; its parked folders can be adopted by identity. |
| D30 | paritydesk never creates files whose names match the app's own recovery patterns; temporary files live in `<data root>/.paritydesk-tmp/`. |

## Invariants

| Id | Meaning |
|---|---|
| I1 | After any crash and `recover`, the active folder holds exactly one identified, registered account (a profile add waiting for its sign-in is the one recorded exception). |
| I2 | paritydesk never creates, merges or deletes a data folder. |
| I3 | Every write is preceded by a complete backup generation. |
| I4 | The app is launched only if paritydesk quit it in this run and the active folder holds a registered, identified profile. |
| I5 | Sync bases advance only for rows that were harvested or applied. |

## Goals

| Id | Meaning |
|---|---|
| G3 | Newest change wins in both directions: after a switch the entering replica equals the canonical state plus its overlay. |
| G5 | Performance targets, for example `status --short` reads only the journal and folder names. |
| G8 | Any number of accounts, every id discovered, no personal defaults. |

## Review findings

| Id | Risk and how the code answers it |
|---|---|
| C1 | A base that no longer matches its folder could look like mass deletion: invalid bases mean a union without deletions plus confirmation, and the deletion breaker. |
| C2 | A stale server value could be harvested as a user edit: a contested value on a side whose base is invalid is read as absent, never as an edit. |
| C5 | A link from untrusted text could be opened: session links are rebuilt from validated ids only. |
| H2 | The app's worktree cleanup could delete unsynced worktrees: worktree registrations are carried and a loss guard checks `git status`. |
| H7 | Terminal control characters could hide plan lines: every rendered string is sanitized and the destructive counts are always the last line. |
| H8 | Editing JSON by dotted paths or re-encoding it could corrupt it: a literal-path byte-splice editor keeps every other byte. |
| H9 | The Local Storage codec could corrupt text: raw bytes are kept and edits happen only when the value round-trips exactly. |
| H10 | Rewriting a shared Local Storage blob could clobber other scopes: only declared sub-paths are edited. |
| H11 | Permission modes and run state must not cross accounts: modes are capped and run-state fields are not carried. |
| H12 | An app update during a switch: staged updates are gated at P0 and the bundle identity is re-checked later. |
| M1 | Two sources of truth: the journal in `root.json` is the only one; run logs are a debug mirror. |
| M4 | Token bytes in memory: `config.json` is read by a span scan, and errors carry no content. |
| M6 | A rollback could overwrite newer work: restores are conditional on post-hashes and recorded as an epoch event. |
| M7 | Partial runs could advance bases: bases are per replica and row and advance only for harvested or applied rows. |
| M8 | App schema changes could look like user changes: bases are migrated by the manifest, and an unmigratable base is invalid. |
| M11 | Hook hygiene: absolute binary path, version-skew check, no untrusted strings in hook output. |
| M12 | Diagnostics could leak identifiers: ids are masked, home paths hashed and id-bearing key names redacted. |
| M14 | Two Macs or a migrated Mac: state is bound to one data root and `rebase` starts over. |
| M15 | goleveldb edge cases: no recovery functions, restore from the backup clone on any error after a writable open. |
| L1 | The preview is torn while the app runs: it is labelled approximate and compared with the final plan. |
| K21 | Switches made outside paritydesk are detected at P0 and offered a sync. |
| K24 | Error messages come from one catalog with a next step, and never advise ending a session. |

## Spikes

| Id | Question | Status |
|---|---|---|
| S1 | Do values written while the app is stopped survive the next launch? | Run: device-local values do (pin order of local items, group order, collapse state); values synced per account (section sort, group-by, order) come back from the server at the first reconcile and are recorded as contested. |
| S2 | Does a sync-pending marker make the app push instead of pull? | Run: yes, without a conflict check; v0.1 keeps writing after launch and verifying. |
| S3 | Does a launchd job started from a Code session survive the quit and quit the app? | Run: yes. |
| S4 | When does the app write a change to disk? | Run: the config mirror at once, Local Storage later. |
| S5 | Do hooks fire for the app-injected tools? | Run: yes, and a PreToolUse deny stops them; the guard, captures and artifact sync can be built (v0.2). |
| S6 | Does `set_pinned` need one approval per call? | Run: yes, for another session in default mode. |
| S7 | Does `RENAME_SWAP` work on the user's volume? | Run: yes, on APFS with FileVault. |
| S10 | Which link reopens a given session? | Run: the session link with the local session id. |
| S13 | Can one account's worktree sweep delete the other's worktrees? | Run: only worktrees it does not know by name; the merged registry and session records cover them. |
| S14 | Can pinned sessions be read offline? | Run: yes, from a copy of IndexedDB. |
| S15 | Which mark continues a turn cut off by a quit? | Run: `interruptedByQuitAt` and related fields; never carried, and dropped from the target once the other account moved the session on. |
| S16 | Are the token cache's entry names plaintext? | Run: no, the cache is one encrypted string. |
| S17 | Does the app keep an extended attribute on its data folder? | Run: yes. |
| S18 | Does Claude in Chrome work on every account with one Chrome profile per account? | Run: yes. |
