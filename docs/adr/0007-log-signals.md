# ADR 0007: Log signals (busy detection, account changes inside a folder, the Chrome pairing organization)

- Status: accepted (design); the patterns marked "from code" are confirmed by release checklist step 4 before a release lists a version as tested
- Date: 2026-11-26
- App version read: 7.37095.0 (a read-only copy of its JavaScript; no real `main.log` was read for this record)

## Context

v0.1 could not see whether a Code session was working (no turn start line was known), so only Claude's own quit dialog protected a working session, and `doctor` said "busy detection unavailable". It also could not tell that a data folder had been signed in to another account inside Claude, and it had no record of which organization Claude in Chrome pairs with (risk R10). This ADR records what the app code shows, the rules paritydesk derives from it, and what stays unverified.

## Facts

From observation on a tested Mac (ADR 0003, ADR 0004, and the brief of this work):

| Id | Fact |
|---|---|
| O1 | A turn starts with `Sending message to session local_<uuid>`, once per user message. It is often followed by `[keep-awake] code session hold taken (turn, battery=...)`, which names no session. |
| O2 | A Code turn ends with `[Stop hook] Query completed for session local_<uuid>`. |
| O3 | An interrupt logs `Interrupting session …` and `LocalSessions.interrupt: sessionId=…`. |
| O4 | A stopped session logs `Stopping session …`. |
| O5 | An account change inside one folder logs `[connector-owner] account changed — disconnecting N stored built-in connector(s)`; `[growthbook] refreshing for account change` also appears at normal starts and is not used. |
| O6 | The active organization: `[(CCD)ScheduledTasks] Initialized { accountId: '…', orgId: '…' }`. |
| O7 | No `requiredOrg` line is in the logs. |
| O8 | Starts with no end come from turns that ended in an API error, interrupted turns, slash commands and turns still running (ADR 0003). |

From the app code (7.37095.0):

| Id | Fact |
|---|---|
| A1 | A record line is `<timestamp> [<level>] <message>`; the timestamp is local time `YYYY-MM-DD HH:MM:SS`, or `invalid-clock (epoch-ms N)` when the clock is invalid. |
| A2 | The log rotates at 10 MiB into `main1.log` … `main4.log` by rename (five files, newest in `main.log`). |
| A3 | A Code session logs the turn start in its send path; a held send does not mark the session running. |
| A4 | Cowork sessions log the same start text and also use `local_<uuid>` ids. |
| A5 | The Code turn end line comes from the app's Stop hook. |
| A6 | More Code ends: `Session … query interrupted (intentional stop)`, `Session … query error: …`, `Session … query iterator completed`, `[CCD] …: the turn failed; … queued message(s) now held unsent`. |
| A7 | Interrupt and stop lines of Code, Cowork (`[interruptTurn] …`) and the IPC layer (`LocalSessions.interrupt`, `.stop`, `.stopFamily`). |
| A8 | Cowork ends: `[Result] Turn succeeded for session …`, `[Result] Turn failed for session …`, `[Result] Turn ended by user interrupt for session …`, `Session … stream ended without a result message, transitioning to idle`, `Session … query interrupted by user (intentional stop)`. |
| A9 | The quit veto counts more than turns: held input, background tasks, loops, shells and permission-card waits, over Code and Cowork. |
| A10 | The keep-awake hold is global: `[keep-awake] code session hold taken (<reason>, battery=…)` when its count becomes non-zero, and `[keep-awake] code session hold released (<reason>, held Nms, …)` 30 s after it becomes zero. `idle`, `armed_grace` and `agent_grace` mean no turn was counted; `battery`, `stalled` and `max` do not. |
| A11 | The hold exists only when the preferences `ccKeepAwakeWhileWorking` and `ccKeepAwakeOnBattery` (default true) allow it. |
| A12 | On a login-state or account change the app logs `[account] Login-state transition (loggedOut: X → Y, uuid: A → B), clearing oauth cache` (or `preserving oauth cache (same account uuid)`). Claude's own multi-account switch goes through the same line. |
| A13 | The same change disconnects the folder's stored built-in connectors (O5). |
| A14 | Claude's own multi-account mode is gated by a flag and a policy; its account group lives in memory only. |
| A15 | Claude in Chrome's local pairing (claude.ai deployment) is pinned to the organization active in Claude: an organization override set by an administrator when there is one, else the `lastActiveOrg` cookie. It is reset and pinned again when the account or organization changes. |
| A16 | A `requiredOrg` from a bootstrap URL exists only in the custom third-party deployment. |
| A17 | Local pairing is off for a launch whose data folder is not at the usual path, and the app logs `[Chrome Extension MCP] local pairing disabled for this launch: …`. |
| A18 | `[startup-perf] main window …` lines are logged only in the first minute of a launch. |
| A19 | Messages are written unescaped and may span lines; continuation lines can carry text from other programs (for example the Claude Code CLI's stderr tail). |
| A20 | The keep-awake hold counts Code sessions only; Cowork has its own counter that only the quit veto sums. |
| A21 | The release timer can fire around a new start: a held send logs the start without being counted. |
| A22 | After 1 h without progress (up to 24 h) the hold stops counting a session and logs `[keep-awake] session … no longer counted (stalled|max)`. |
| A23 | The existing signals' messages start with their known tags: `[CCD] LocalSessions.setFocusedSession: …`, `[updater] Previous run did not exit cleanly…`, `[claude-in-chrome] …`. |
| A24 | `[Chrome Extension MCP] local pairing beside the relay: established|closed`. |

## Decisions

### Record lines and anchoring

Every signal (old and new) is matched only on a record line (a line whose header parses as A1, bracketed or not, with an optional `Z` and fraction), only against the message after the header, and anchored at the message start: the loader adds the `^`. Continuation lines are never matched and never inherit a time. Each pattern must carry a fixed text of at least 8 characters, which every match contains; the reader skips a line at once when that text is absent (no allocation for a non-matching line). Patterns that named known tags keep those tags optional so the stub app's untagged lines still match. Captured ids are validated (`local_<uuid>`, account and organization UUIDs) and only validated ids, times and counts leave the reader: lines are never returned, logged or put into errors.

### Rotation-safe reading

A position in the log is an inode and an offset (`LogCursor`). The reader opens `main.log` and `main1.log` … `main4.log` once, compares each name's listed inode with the open descriptor's, and opens the set again once when a rotation happened in between; a second mismatch is a gap. A cursor whose file was rotated is followed through the rotated files into the new `main.log` within a 24 MiB budget; a file rotated out, a truncated file or an exceeded budget is a gap, and the busy tracker then reads its window again. A window read goes back to the file whose first record time is at or before the app's start, within 24 MiB. The detached job receives both the offset and the inode (`--log-offset`, `--log-inode`), so the in-app turn wait survives a rotation.

### Busy rules

A session is working from its `turn_started` line until a `turn_ended` or `turn_cleared` line names it, within the current run of the app (lines older than the start of the app's earliest process are ignored; without a start time the last `app_started` line marks the run). Set semantics: the last event wins. The rules, in order: R0 drop lines of an earlier run; R1 an app start clears (only without a start time); R2 an end or clear removes the session; R3 a start adds it; R4 hold taken; R5 hold released (idle reasons make the hold idle and remember the time); R8 a session the hold stopped counting is marked. The report then applies: R5′ a Code turn that started more than 31 s before an idle release had ended; R6 a start older than `app.busy_stale_after` (default 6 h) is stale, shown but not waited for; R7 a Code turn older than 2 min while the hold is idle and trusted (A11) was never counted, so it is not running. Cowork sessions are never counted by the hold (A20), so R5′ and R7 never apply to them. Claude's own quit veto stays the last guard (A9).

Rejected: a counter of starts minus ends. A queued message logs a second start that the app may merge into the running turn, which then logs one end; a counter keeps the session busy and gives repeated exit 12 for detached switches. Set semantics can call a running second turn idle; the quit veto then shows the app's dialog, which is the safer failure.

At P0 an interactive run asks; a detached job warns and waits at P3; any other non-interactive run exits 12 as in v0.1. P3 waits up to `quit_timeout`; an interactive run asks again when a new session starts working and, at the deadline, asks whether to quit anyway (Claude's dialog then decides). `plan` lists the working sessions (masked ids and ages in `lines`, titles only in the `busy` member and the human output), leaving out the invoking session (`--invoking-session`).

### Account changes inside a folder

`account_changed` (A12, with a new account) gives (from, to) pairs with times; lines that go back in time by more than 65 minutes within one file are ignored (a forged header with a stale time is dropped; 65 minutes lets a daylight-saving fall-back hour through). Changes are attributed to profiles by the accounts paritydesk recorded. They are never stored.

- **In-folder sign-in (exit 20).** When the active folder is one profile's folder signed in, inside Claude, to another profile's account, every run refuses before anything is written. Two rules: the folder's marker names another configured profile whose account is not the folder's (no log needed); or the logged changes since the last committed run, followed in time order from the recorded account of the profile state calls active, end at the folder's account, and the folder carries no marker of the profile that holds that account (such a marker moved with its folder: a real outside switch). A folder signed in to another account and back again ends at its own account, so the log rule does not fire for it. A single forged line cannot trigger the log rule alone: it needs both accounts to be the real ones.
- **Claude's own multi-account mode (swap-only).** Two chained changes since the last committed run (the `to` of one is the `from` of the next) mean one folder changed accounts twice; the run carries nothing and says why, `sync` exits 31 with the cause. The mark (`last_commit_at`) moves on every committed switch or sync, on the binding of a pending profile add and on `profile adopt`, so the next switch is normal again unless the folder changes accounts again. A change before paritydesk was installed (no mark) is reported only.
- The `account_changed_hint` lines (O5 and the mid-flow configuration line) name no account. They never count as a change; they only make the active organization from the log unknown until the next `active_org` line (`LastReplica`, used by online reconcile and artifact sync).

### Chrome pairing organization

The pairing is pinned to the organization active in Claude (or an override) and pinned again when it changes (A15); no other organization stays pinned, and nothing paritydesk may read stores it. `doctor` reports exactly that next to the active organization from the log (masked), the last local pairing state logged since the app started, and the disabled-for-this-launch line (A17), now filled in the manifest. The `lastActiveOrg` cookie and the override are never read.

### Deprecated key

`signals.native_multi_account` stays decodable (an override copied from v0.1 still loads), is ignored, and `doctor` warns about a non-empty value. It is removed in v0.3.

## Unverified, and the fallback

| Id | Unverified | Fallback |
|---|---|---|
| U1 | The on-disk text of the hold `released` line. | No match: the hold stays unknown; sessions without an end line stay working until `busy_stale_after`. |
| U2 | That the hold is taken for every counted Code turn. | R7 applies only to Code sessions with a trusted, idle hold and a start older than 2 min; a wrong drop is caught by the veto. |
| U3 | The on-disk text of the login-state line and its arrow. | No match: no log-based report; the marker rule and the recorded-history rule still work. |
| U4 | That the ids of the process-level and `no longer counted` lines are local session ids. | Every capture is validated; anything else never matches. |
| U5 | That a slash command logs a start with no end. | R7 (Code, trusted hold) or R6 (stale, shown, not waited for). |
| U6 | Whether the `ScheduledTasks Initialized` organization equals the pairing organization (an override wins over the cookie). | `doctor` labels the organization "from the Claude log" and never claims a match or a mismatch. |
| U7 | Second precision of real timestamps (fixtures use the bracketed form with milliseconds). | The header parser accepts both forms. |
| U8 | That every observed signal's message starts where A23 says on disk. | Known tags are optional; release checklist step 6 re-confirms each; a pattern that stops matching falls back (`quit_vetoed`: the quit wait ends on its timeout, exit 10). |
| U9 | A continuation line that forges a full header cannot be told from a record line. | Residual. Every log-driven outcome is in the safe direction: a busy error only waits or is caught by the veto, a forged chain gives swap-only (carries nothing), and a forged single line cannot cause exit 20. |

## Consequences

- `doctor` gains `busy-now`, `account-change` and `chrome-pairing-org` after `busy-detection`; `busy-detection` is ok on 7.x and keeps the v0.1 warning on 6.x, whose manifest has no turn start.
- `status` (full) reports account changes (masked) and whether Claude's own multi-account mode looks active; the short status reads no log.
- Manifest overrides must follow the anchoring rule; a stale v0.1 override only makes paritydesk fall back, and `doctor` shows the missing signals.
