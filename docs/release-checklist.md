# Release checklist

Automated tests never touch the real Claude app. These checks do, so they
run by hand on a real Mac before every release, for the app version the
release claims to support. Record the app version, the Mac (Apple silicon
or Intel) and the result of each step in the release pull request. If a
check confirms a new runtime behaviour, record it in
[ADR 0003](adr/0003-online-reconcile.md) before the README claims it.

Use scratch content wherever a step deletes or edits something.

## Before you start

1. Quit Claude and make a safety copy of every `Claude` and `Claude-*`
   data folder and of `~/Library/Application Support/paritydesk` (for
   example with Time Machine). Restore from it only with Claude quit.
2. Install the release candidate and check `paritydesk version`.
3. Set up Claude in Chrome with one Chrome profile per Claude account,
   each signed in to its account and kept open.

## Confirm the log patterns (compatibility manifest)

The manifest leaves a `main.log` pattern empty until it has been seen on
a real Mac; callers then fall back to slower or weaker signals. Before a
release:

4. **turn_started.** Start a Code session, send a message, and while
   Claude is working read the new lines of `~/Library/Logs/Claude/main.log`
   (read-only, for example with `tail`). Confirm the line that marks the
   start of a turn and note its exact text without any ids. If it is
   stable across two turns and two sessions, add the pattern to
   `[signals].turn_started` in the manifest, with `{session}` in place of
   the session id, and record it in ADR 0003. Otherwise leave it empty.
5. **active_org.** With an account that belongs to two organizations,
   switch organizations in Claude and confirm which `main.log` line names
   the active one. Add it to `[signals].active_org` only if it is stable;
   otherwise leave it empty (per-organization rows are then skipped when
   more than one organization exists).
6. Re-check the patterns that are already filled in (quit vetoed, turn
   ended, session focused, Chrome connected) against the new app version.
   Also open one session record under `claude-code-sessions` read-only
   and confirm the name of the field that holds the session's project
   path. It must match `project_path` in the manifest's sessions row
   (`cwd` until confirmed), because `sync.exclude` globs match on it.
   Record the result in ADR 0003.

## Setup

7. `paritydesk init` on the first account. Expected: the config is
   written with mode 0600, Automation is requested once, and
   `paritydesk doctor` reports every check ok or explains a warning.
8. `paritydesk profile add <alias>` for the second account. Expected:
   Claude restarts on an empty folder, you sign in to the second account
   in Claude, and `paritydesk status` shows both profiles. The first
   account stays signed in.

## Switching

9. **Preview.** `paritydesk plan`. Expected: one line per category with
   changes, the DESTRUCTIVE line last, plausible counts. A first sync
   shows a merge with no deletions.
10. **Switch from Terminal.** `paritydesk switch`, answer `y`. Expected:
    Claude quits by itself, the folders swap, Claude relaunches on the
    other account without any sign-in screen, and the session list
    matches. Note the total time.
11. **Check parity**, item by item against the other account: sessions
    and archived sessions, a renamed session title, backlog and scheduled
    tasks, environments including environment variables, sidebar group
    order, pin order, sidebar view, window size and position, and a
    session that uses a worktree (it must open without a missing-folder
    error). For the sidebar view, change it on A, switch, and check it on
    B at launch and again after ten minutes: ADR 0003 tested only pin and
    group order written to both copies. Record whether B keeps the view
    paritydesk wrote or Claude replaces it (then `paritydesk verify`
    exits 50), and record it in ADR 0003 before the README claims it.
12. **Claude in Chrome.** In a Code session, use Claude in Chrome on a
    harmless page. Expected: it works. Repeat after switching back.
13. **Switch from inside a Code session** with `/paritydesk:switch`.
    Expected: the plan is shown verbatim and confirmed, the switch runs
    as a background job after the turn ends, Claude relaunches on the
    other account, a notification appears, and the invoking session
    reopens.
14. **Newest wins both ways.** Rename one session and pin another on
    account A, switch, rename a third and reorder a group on B, switch
    back. Expected on A: all four changes.

## Safety

15. **Busy sessions.** Start a long Code task, then `paritydesk switch` in
    Terminal. Expected: the busy session is listed; if you answer
    Claude's quit dialog with "Cancel", paritydesk exits 10, nothing
    changes and Claude keeps running.
16. **Deletion breaker.** Delete several scratch sessions on one account
    and switch. Expected: exit 22 unless `--allow-deletions` is given.
17. **Rollback.** `paritydesk generations list`, then
    `paritydesk rollback <latest>`. Expected: the previous values return
    and anything changed afterwards is reported as a conflict, not
    overwritten.
18. **Recovery drill.** `PARITYDESK_FAULT=after:P7 paritydesk switch
    --yes`, then `paritydesk status` (expected: recover needed) and
    `paritydesk recover` (expected: a consistent state, Claude relaunches
    on the right account). Repeat with `after:P8`.
19. **Worktree guard.** Leave uncommitted changes in a worktree that only
    the leaving account registers, then switch. Expected: exit 23.
20. **Update gate.** With a Claude update downloaded, `paritydesk
    switch`. Expected: exit 13; with `--allow-update`, a clean run on the
    new version.
21. **SessionStart hook.** With a run left open (step 18 before
    recovering), start a new Code session. Expected: exactly one line
    asking you to run `paritydesk recover`. With nothing pending: no
    output.

## Privacy and permissions

22. While a switch runs, `nettop -p $(pgrep paritydesk)` or LuLu.
    Expected: no connections at all.
23. `ls -ld ~/Library/Application\ Support/paritydesk
    ~/Library/Logs/paritydesk`. Expected: `drwx------`.
24. `paritydesk status --json` and `paritydesk doctor --json`. Expected:
    ids masked to four characters, home paths hashed.

## Uninstall (last)

25. `paritydesk uninstall`. Expected: every data folder intact, the
    printed `mv` command (shown only when no profile is active) correct,
    and an exact list of what was removed.
