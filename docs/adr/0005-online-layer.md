# ADR 0005: The online layer (changes made through Claude's own tools)

- Status: accepted (release checklist steps R-ON-1 to R-ON-9 pending)
- Date: 2026-11-26
- App version: 7.37095.0 (read from the app's code; 6.x has no online
  steps)

## Context

Some values cannot be written while Claude is stopped: the server puts its
own copy back at the first reconcile after a launch (ADR 0003, S1), or the
value lives in a store paritydesk does not write offline (the pin list in
IndexedDB, S14). The app offers Code sessions a set of tools for exactly
these values (the `ccd_sidebar` server: `set_view`, `set_pinned`,
`create_section`, `list_sections`, and `get_session` of
`ccd_session_mgmt`). The private design names the two phases that use
them: P1 (a read-only capture before the quit) and P13 (the online
reconcile after the relaunch).

This ADR records what paritydesk v0.2 does with them. Every fact was read
from a read-only copy of the app's code; no value, id or path from a real
data folder is in the repository. The pins section is added by the pins
work item.

## Facts (app 7.37095.0)

- The sidebar tools are an MCP server named `ccd_sidebar`; tool names are
  `mcp__ccd_sidebar__<name>`. They exist only in Code sessions and can be
  turned off by a flag, after which every call answers "Sidebar tools are
  turned off right now." as an error result.
- `set_view` takes `group_by` (`date`, `project`, `state`, `custom`,
  `none`), `status`, `sort_by` (`recency`, `alpha`, `created`),
  `last_activity` and `section_id`. Without `section_id` (or with
  `sessions`) it changes the whole list. Manual sections and `routines`
  take `date`, `state` or `none`; `custom` is refused in the sections
  layout; `environment` is not in the tool's enum. It answers
  `{changed, view:{group_by, status, sort_by, last_activity}}`, and its
  refusals are fixed texts (hand-ordered rows, `state` not enabled for the
  account, no view of its own). Grouping the whole list by `state` also
  sets the status filter to `active`.
- A stored section uses other words than the tools: stored `folder`,
  `savedEnvironment`, `status` are reported as `project`, `environment`,
  `state`. A section carries `groupBy` only once one was set; `sortBy` is
  always filled. paritydesk compares only in reported spelling and never
  writes a stored-only value (the manifest carries both maps).
- The whole-list group-by and sort are the members of the built-in
  `sessions` section object: the UI and `set_view` always write it, and
  write the older per-mode members only while a feature flag is off. The
  sidebar-view row (v0.1) already carries that object, so it is the
  intent; the per-mode members are not read.
- `list_sections` returns the sections in order with their reported
  group-by and sort (`user_order` for hand-ordered rows); manual section
  ids are per account, so manual sections are matched by name.
- `create_section` takes a name of 1 to 200 UTF-16 code units.
- `list_sessions` reports other sessions' titles, folders and branches and
  a pin flag that can be stale-true after the renderer reinitialises; it
  is never read. `get_session` of another session returns its title; it
  runs only as the exact `self` probe.
- The app's per-turn tools (`ccd_turn`, `ccd_session`) and its window,
  view, connector and host tools are on other servers.
- Hooks fire for app-injected tools; a PreToolUse `deny` stops the call
  and its reason shows in the tool error (ADR 0003, S5). A call on
  another session asks the user to approve it in default permission mode
  (S6).
- Results are `{content:[{type:"text",text}]}`, errors add `isError`, and
  a `_meta` member carries the session title (never read).

## Decision

1. **Queue, derived.** At P5 the orchestrator builds a queue of exact tool
   calls from the merge outputs (`internal/online`, providers: probe,
   sidebar; the pins and artifacts work items add theirs). It is stored
   with the planned intent (state object `online-draft`, kept by GC while
   the run is open) and published at P10, between the profiles' epoch
   update and the root commit, as `<SupportDir>/online/queue.json`. The
   queue is derived, never authoritative: losing it loses nothing, the
   next merge recomputes it. Online rows produce no offline ops; their base
   at P10 is the entering side's re-read.
2. **Probe first.** Every queue starts with `get_session {"session_id":
   "self"}`; every other call needs its recorded result. If neither hook
   runs in the session, no write is ever offered (`no_hooks`).
3. **Guard.** A PreToolUse hook (plugin `hooks.json` matcher: the guarded
   servers `ccd_sidebar`, `ccd_session_mgmt`, `ccd_settings`,
   `claude-artifacts`, `ccd_directory`, and the app's artifact tools)
   denies, while a queue is armed for the session, every call that is not
   an offered queue step with exactly the queued input, plus a fixed floor
   (deletes, shares, artifact database tools, `set_remote_control`) and
   the untrusted-text reads. It never answers "allow", so the app's own
   approval cards stay. An in-flight marker created exclusively is the
   gate against parallel duplicates. The shim denies on its own while the
   lease's deadline lies ahead and the binary is missing or fails.
   The manifest's `guarded_servers_extra` and `guarded_tools_extra` take
   effect only for names the plugin matcher already covers; a new server
   needs a plugin release (the policy test keeps the matcher equal to the
   code's guarded tables).
4. **Recorder.** A PostToolUse hook records only validated fields of the
   result of a matching step (first result wins).
5. **Lease and token.** `online next` arms a lease (20 minutes by default,
   unbound for 3 minutes until the probe binds it) and prints a token once;
   another session without the token gets `busy_elsewhere` and changes
   nothing. `online cancel` (Terminal) stops everything without a lock.
6. **Replica check.** Before each batch the newest account and org line
   of `main.log` since the launch must match the queue's; a mismatch drops
   the queue, an unknown replica holds server-synced steps for two minutes
   and then skips them.
7. **Live check.** Sidebar writes wait 45 s after the launch, then read
   `list_sections` and write a field only when the live value equals the
   value the entering account had before the switch (or that value is
   unknown); a third value is skipped (`changed_elsewhere`, newest wins).
8. **Bases follow confirmed changes.** `online done` (or, when the run lock
   is busy, the next `verify` or switch) moves the entering profile's
   sidebar-view base members to the observed values the steps set, and
   clears contested entries that now hold the intended value.
9. **Skills.** `/paritydesk:switch` asks its one confirmation with
   buttons (plain-text fallback) and runs the optional capture after it;
   `/paritydesk:reconcile` runs the loop with a bound of
   `remaining_batches` + 4 answers (at most 40).

10. **Details settled during review.**
    - A sidebar field whose prior value on the entering account is known
      and already equals the intended value gets no step: the live check
      could only find it equal or changed elsewhere. A switch without
      sidebar changes therefore publishes no queue and shows no
      reconcile line.
    - With sections creatable, a manual section whose name fails the
      name rules has no create step; missing live it is skipped
      `not_writable`, not `not_found`.
    - `online retry` records how many in-flight markers the step already
      had; only later markers count as the new attempt, the guard lets
      exactly one more call through, and a result that made the step
      fail is removed (results are first-wins), so the new attempt's
      result decides. A late result of the first attempt of a step that
      had none (`no_result`) can still resolve it.
    - An unbound lease keeps the deadline it got when it was created; only
      binding refreshes it.
    - The PreToolUse binary does all of its file reads under its 2 s
      deadline. When the lease cannot be read or judged (unreadable,
      oversized, symlinked, no monotonic clock), or the deadline passes,
      it reads the shim's `armed` marker and denies guarded mutations
      while it holds a future deadline (or cannot be read). When it cannot
      decide at all (its dependencies, a panic outside the guard, a bad
      command line) it exits non-zero without output, so the shim applies
      its own armed check.
    - The capture's reads (the same `get_session` call as the probe) get no
      decision while `capture.json` is live, even under a reconcile lease,
      and are recorded for both queues.
    - The not-found, archived and scheduled-run texts are recognised only
      in error answers and in answers that are not JSON: a JSON answer can
      carry a session title or section name the user wrote.
    - `online cancel` creates `stop` and removes `armed` from the resolved
      folder alone, without loading the config, the state or the app.

## Accepted risks

- A user MCP server registered under exactly one of the guarded server
  names would be guarded as if it were the app's, and could forge results
  of the app's tools; paritydesk cannot tell them apart from the hook
  payload.
- If Claude Code's own 5 s hook timeout kills the shim (a hung disk), the
  call proceeds. The binary's 2 s deadline keeps this far away.
- A model that runs arbitrary shell commands as the user can edit the
  online folder or remove the plugin; the guard stops unplanned tool
  calls, and the queue's tool allowlist and steps hash only narrow what a
  damaged file can do.
- Rollback does not undo changes made through Claude's tools; the next
  merge treats them as the entering account's state.
- With `online.guard = "always"`, the manifest's additions to the floor
  (`always_deny`) reach the hooks only through the newest `queue.json`
  (hooks never load a manifest). With no queue file, an unarmed session
  gets the floor fixed in code only.

## Not covered

Reading the per-mode group-by members, `environment` group-by, status and
last-activity filters, section order, `move_sessions`, renames and deletes,
unread dots, and any online step on 6.x.

## Pins

Row `pins` (which Code sessions are pinned) is written only through this
layer. Facts were read in the read-only copy of app 7.37095.0 and in a
copy of one data folder (shapes and counts only; no value of it is in
the repository); minified names are evidence only.

### Facts

- P4. The pin list is the renderer's zustand store `dframe-starred-code`
  in idb-keyval (key `store:pin-state:dframe-starred-code`). It is
  server-synced, but local session ids never leave the device: the upload
  drops them, the download keeps the device's own.
- P7. An unpin is refused when the session's own sidebar section is
  hidden (its custom-group assignment names a group whose section has
  `shown == false`).
- P8. Unpinning a pinned session first unpins every pinned local session
  whose family root it is (lineage, hand-off, display parent, side
  session, fork), archived children included, unless a flag turns that
  off; the family map is empty outside the nested sidebar layout.
- P13. The renderer mirrors the local ids into
  `preferences.epitaxyPrefs["starred-local-code-sessions"]` and into Local
  Storage (`LSS-persisted.starred-local-code-sessions`); an empty store is
  seeded from it at launch, and every change rewrites it.
- P14. The family fields: `displayParentSessionId`, `lineage.sessionId`
  (kinds side_session, hand_off, suggested_task, fork),
  `spawnedFrom.sessionId`, `forkedFromSessionId`.
- P15. `set_pinned` waits 8 s for the window; on timeout it answers "The
  app window didn't answer." after the command was dispatched, so the
  renderer may still apply it, cascade included.
- D3-D7. The IndexedDB is LevelDB with comparator `idb_cmp1`; data
  blocks are snappy-compressed; `DatabaseName` and `ObjectStoreNames`
  metadata keys give the database (`keyval-store`) and object store
  (`keyval`) ids; the value is a record version, the Blink envelope (21,
  with a zero 12-byte trailer), V8 16 and a one-byte string holding
  `{"state":{"starredIds":[...]},"version":0,"updatedAt":<ms>}`; larger
  values are wrapped (`ff 11 02` raw snappy, decoded; `ff 11 01` a blob
  reference, never read); LevelDB-scopes bookkeeping lives under
  `00 00 00 00 32`.

### Decision

1. **Read, never write.** `internal/store/idb` clones the directory file
   by file (`clonefile`, byte copy only within a budget) without `LOCK`
   and `LOG*`, checks the listing before and after, replays the manifest,
   walks every live table in full (block types checked first) and scans
   the logs with its own record scanner (a torn last record of the newest
   log is dropped as the C++ library does). The newest entry of the key
   wins; an unfinished scopes transaction that names the key blocks the
   read. Neither IndexedDB nor the backup list is ever written: a wrong
   IndexedDB write reads as corruption, and the backup list seeds an
   empty store outside the per-change approval.
2. **Unreadable means "no information".** Every unknown format, damage
   or concurrent write maps to a stable reason. Absence reads as "no
   pins" only when the side has no valid non-empty base and no
   `isStarred` evidence; an unknown format falls back to the backup list.
   An unreadable side removes only the pins row from the plan: canonical,
   overlays and every base stay as they were.
3. **Merge.** A set with per-profile bases; between conflicting changes
   the side that was live later wins (no per-pin time exists; `updatedAt`
   is used only by the freshness probe). Sessions that `sync.exclude`
   keeps private leave every input.
4. **Steps.** Unpins first; pins and keep steps stay dormant until every
   unpin settled. For each unpin, pinned family members that must stay
   pinned (a superset of the app's family: every family field, plus the
   chain start) get a keep step; when one of them cannot be pinned again
   (archived, a scheduled run) or the section is predicted hidden, the
   unpin waits with a warning. An unpin whose answer is unknown (no
   result, timeout) still activates its keeps; `online retry` never
   retries an unpin once a pin or keep left dormant.
5. **Bases.** T's pins base moves to what is known to be true: applied
   pins and unpins, minus the desired ids an unpin may have cascaded over
   (always for an unknown answer, unless pinned again for a definite
   one). A confirmation with changes starts a freshness probe: the next
   read of that profile must show a store written after the queue was
   created, or a fresh backup list; otherwise pins stop with doctor
   `fail` until paritydesk is updated or `rebase` clears it.
6. **Not destructive offline.** Unpins are not in the DESTRUCTIVE line,
   its digest or the deletion breaker; the pins question and the approval
   cards are the confirmation, and the pins breaker offers "Pin only".

### Residual risks

- If calls ran but their results were never recorded and the user undid
  one of them before the next switch, that undo is not carried once.
- The hidden-section prediction assumes the row key `code:<id>`; a wrong
  prediction only defers an unpin or meets the app's own refusal.
- Only the fields of the app's family are followed; a later app build
  with another link would cascade over pins paritydesk did not keep (the
  next switch offers them again).
