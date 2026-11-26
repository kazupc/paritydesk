# If something goes wrong

Every paritydesk error has an exit code and a catalog id `PD-E<code>`.
The error message says what happened, what is safe right now, the next
command to run, and links to the matching section below. With `--json`
the same fields come as one JSON document:
`{"v":1,"error":{"code":..,"id":..,"title":..,"safe":..,"next":..,"anchor":..}}`.

Three rules hold for every error:

- **Do not move data folders by hand** unless a section below tells you
  the exact command. `paritydesk recover` knows which folder is which.
- **Every account stays signed in.** If Claude asks for a sign-in, sign in
  with that profile's own account only.
- `paritydesk status` is always safe to run and tells you what is pending.

<a id="usage"></a>
## Exit 2: the command line was not understood (PD-E2)

Nothing was changed. Check the command with `paritydesk help <command>`.
A switch inside a non-interactive context (a script, or the plugin) needs
`--yes` or a confirmed plan, because paritydesk cannot ask you there.
An unknown value in `PARITYDESK_FAULT` (the release checklist's
recovery drill) also exits 2, for every command, `status` and `recover`
included: unset the variable.

Some commands make decisions or move directories and run only in
Terminal: `paritydesk artifacts allow`, `paritydesk artifacts forget`,
`paritydesk artifacts import-legacy --apply` and `paritydesk sync` exit 2
when run inside Claude (a Code session or the plugin), and `paritydesk
profile adopt --from-legacy --apply --quarantine` exits 2 without a
terminal. They change nothing. Run the same command in Terminal.

<a id="declined"></a>
## Exit 3: the plan was not confirmed (PD-E3)

Nothing was changed. You answered no, or a confirmed plan
(`--confirmed-plan`) was missing or smaller than what the switch would do
now: a switch never deletes more than the plan you confirmed. Run `paritydesk plan` again and confirm
the new plan.

<a id="app-did-not-quit"></a>
## Exit 10: Claude did not quit (PD-E10)

Nothing was changed, and Claude was not forced to quit. Usually a Code
session was still working and Claude asked whether to quit; your answer
in Claude decides. If you chose "Wait for Claude" and Claude quits later,
paritydesk relaunches the profile you were on. Finish or stop the running
work, then run the command again.

The same exit covers a quit event that Claude refused (AppleScript
error -128) or did not answer in time (-1712), and one that failed for
another reason. Whether Claude's own dialog makes the quit event fail
with -128 or end without an error is not yet recorded. After a refusal
or a missing answer paritydesk waits and watches for a late quit as
above. The message names the AppleScript error number:

- -1712 can also mean that macOS showed its Automation prompt and
  nobody answered it. Allow it, or run
  `paritydesk doctor --grant-automation` while you are at the Mac.
- -10004 means macOS refused to send the event, as it does when
  paritydesk runs inside a sandbox (for example under `sandbox-exec`).
  Run paritydesk from Terminal instead.

Missing Automation permission (-1743 or -1744) is exit 15, not 10.

<a id="run-lock-busy"></a>
## Exit 11: another paritydesk run is in progress (PD-E11)

Nothing was changed by this run. Wait for the other run (for example a
switch started from the plugin) to finish, then check
`paritydesk status`.

<a id="sessions-busy"></a>
## Exit 12: Code sessions are still working (PD-E12)

Nothing was changed. A switch quits Claude, which would interrupt the
listed sessions. Wait until they finish, then run the command again.

paritydesk finds working sessions in Claude's log (`main.log` and the
rotated `main1.log` … `main4.log`): a session works from the line that
starts its turn until a line ends, interrupts or stops it, within the
current run of Claude. `paritydesk plan` lists them (masked ids and how
long they have been working; the titles appear only in the human output
and in the `busy` member of `--json`). In a terminal, the switch asks,
then waits for them up to `app.quit_timeout`; if they are still working
then, it asks whether to quit anyway. A switch from inside Claude (the
detached job) waits the same way and exits 12 without quitting Claude
when they are still working. Other runs without a terminal exit 12 at
once.

A session whose turn started more than `app.busy_stale_after` ago (6
hours by default, between 10 minutes and 48 hours) with no end in the log
is shown as stuck and not waited for. Claude's own quit dialog stays the
last guard: it also counts background tasks, loops, shells and
permission cards, which no log line names. Answer "Cancel" there and
paritydesk exits 10 without changing anything.

On Claude 6.x the compatibility manifest has no confirmed turn start
line, so `paritydesk doctor` reports busy detection as unavailable and
only the session records and Claude's quit dialog protect a working
session.

<a id="update-pending"></a>
## Exit 13: a Claude update did not finish installing (PD-E13)

Nothing was synced, and Claude was relaunched. A switch lets a staged
update install when Claude quits and re-checks the new version before it
syncs anything; this time the update was still installing when the wait
ended. Wait a minute, then run the command again.

<a id="foreign-tool"></a>
## Exit 14: another account-switching tool was detected (PD-E14)

Nothing was changed. Two tools that swap data folders corrupt each
other's view. `paritydesk doctor` names what it found. Stop using one of
them; paritydesk can adopt the other tool's parked folders by identity
with `paritydesk profile adopt`.

The compatibility manifests for Claude 6.x and 7.x list no other tool's
footprints yet, so this version does not detect other tools and does not
exit 14. `paritydesk init` still lists parked `Claude-*` folders that hold an
account no profile registers, and suggests `paritydesk profile adopt`
for them.

<a id="automation"></a>
## Exit 15: Automation permission is missing (PD-E15)

Nothing was changed. paritydesk quits Claude with an Apple Event, which
macOS allows only with Automation permission. paritydesk exits 15 only
when macOS itself refuses the event for that reason (AppleScript error
-1743 or -1744). An Automation prompt that nobody answered shows as a
timeout instead (-1712, exit 10), and a quit that Claude refuses or that
fails otherwise is exit 10 too. Run
`paritydesk doctor --grant-automation` while you are at the Mac and allow
the request, or enable it in System Settings, Privacy & Security,
Automation.

<a id="identity-mismatch"></a>
## Exit 20: a folder holds a different account than expected (PD-E20)

Nothing was changed, and every account stays signed in. A data folder
holds an account that its profile does not expect, for example after a
folder was moved by hand. `paritydesk doctor` shows the masked
identities. Fix the folder names, or register the folder with
`paritydesk profile adopt`.

If the message says the folder was signed in, inside Claude, to the
account of another profile, do not rename folders or run
`profile adopt`. Switch the account back inside Claude instead; see
[a folder was signed in to another profile's account](#signed-in-to-another-account)
below.

<a id="signed-in-to-another-account"></a>
## Exit 20: a folder was signed in to another profile's account

Nothing was changed, and every account stays signed in. Claude holds
the folder of one profile, but inside Claude that folder was signed in to
the account of another profile, so paritydesk cannot tell whose sessions
and settings the folder now holds. The message names both profiles. In
Claude, switch that folder back to the account of the profile it belongs
to (Claude's account menu, or sign in again with that account), then run
the command again. `paritydesk doctor` shows the change in its
`account-change` check (accounts masked). paritydesk sees this from the
folder's marker, or from Claude's log line that records the account
change since the last switch.

If the folder changed accounts twice since the last switch (for example
with Claude's own multi-account mode), the next switch only swaps folders
and carries nothing, and `paritydesk sync` exits 31; the switch after
that is normal again.

<a id="base-invalid"></a>
## Exit 21: a first-sync merge needs your confirmation (PD-E21)

Nothing was changed. A sync base no longer matches its folder (a folder
was restored, replaced or seen for the first time), so paritydesk merges
both sides without deleting anything. This needs a person to see the
plan: run `paritydesk switch` in Terminal and confirm, or use the plugin.
Only a run with `--yes` stops here; add `--confirm-first-sync` to it if
the merge is intended.

<a id="deletion-breaker"></a>
## Exit 22: the plan deletes more than the limit (PD-E22)

Nothing was changed. Only a run with `--yes`, where nobody saw the plan,
checks this: the plan would delete more items in one category than
`limits.max_delete_fraction` and `limits.max_delete_min` allow. A plan you
confirm at the terminal or through the plugin has no such limit, because
you saw its DESTRUCTIVE line. Look at `paritydesk plan`; if the deletions
are intended, confirm the switch in Terminal or add
`--allow-deletions=N`.

<a id="worktree-risk"></a>
## Exit 23: a worktree with uncommitted changes would lose its registration (PD-E23)

Nothing was changed. Claude may clean up worktree directories that its
folder does not know about. Commit or stash the changes in the listed
worktree, or run the switch again with `--accept-worktree-risk`.

<a id="compat-refused"></a>
## Exit 30: this Claude version cannot be handled safely (PD-E30)

Nothing was changed. The installed Claude version is known to be
incompatible, the files paritydesk relies on have a shape it does not
recognize, or the installed Claude is older than the version the last
switch ran against (a downgrade: the newer app may have migrated the
data folders). `paritydesk doctor` lists the reasons. Wait for a
paritydesk release that supports the version, or reinstall the newer
Claude.

<a id="swap-only"></a>
## Exit 31: switched without syncing (PD-E31)

The switch completed, but nothing was synced, because syncing could not
be done safely (for example an unknown Local Storage format, Chromium's
SQLite Local Storage backend, or an account that uses the app's own
multi-account support). Your data in each folder is intact. `paritydesk
doctor` decides the mode from the same inputs and lists the reasons.

<a id="degraded"></a>
## Exit 32: some rows were skipped (PD-E32)

Everything else was synced. Some data was left alone, for example per
organization data when the active organization could not be determined.
`paritydesk doctor` lists the failed probes, the rows a run would skip,
the reasons, and the active organization (masked) or that it is
unknown.

`paritydesk profile adopt --from-legacy` also exits 32, although it syncs
nothing: the registration is complete, but some leftovers were kept for
now, or a folder marker could not be written. Quit Claude and run the
same command again; see [Adopting folders from an earlier
script](#legacy-adopt).

<a id="apply-rolled-back"></a>
## Exit 40: applying the plan failed and was rolled back (PD-E40)

The previous account is active again, and the entering folder was
restored from the backup generation. `paritydesk log` shows the step that
failed. Please report it with that log.

If the step failed before anything was written, nothing needed
restoring. One such case is a process that still held the entering
folder's Local Storage lock: the log says the folder is in use. Quit
that process (for example a copy of Claude started with
`--user-data-dir`), then run the command again.

<a id="needs-recover"></a>
## Exit 41: a run was interrupted (PD-E41)

A switch, sync, rollback or profile add stopped part way, for example
because the Mac shut down. Do not move data folders by hand. Run
`paritydesk recover` in Terminal: it finishes the run, or with
`--rollback` undoes it, and it never creates, merges or deletes a data
folder. A profile add that waits for its first sign-in also shows here
until you sign in to the new account in Claude.

A rollback or a restore also stops here, with nothing restored, when
another process holds the folder's Local Storage lock. Quit Claude (and
any copy started with `--user-data-dir`), then run `paritydesk recover`.

<a id="verify-drift"></a>
## Exit 50: values differ from what was written (PD-E50)

Your data is intact. After the relaunch, Claude replaced values that
paritydesk had written in a row the compatibility manifest does not
expect Claude to replace. The difference is recorded, so it is not
mistaken for your own edit on the next switch. `paritydesk verify` shows
which rows drifted.

Rows Claude also keeps per account or per device (the sidebar's group
order, pin order and view) are expected to change after a launch. They
are recorded the same way, `verify` lists them as changed, and the exit
code stays 0.

Only the check that a run makes after its own launch, or the first
`paritydesk verify` within 15 minutes of that run, records drift. A later
`paritydesk verify` only reports differences and records nothing, because
by then a difference may be your own edit. Your edit is then carried as
usual on the next switch.

<a id="needs-sign-in"></a>
## Exit 51: the active account needs a sign-in (PD-E51)

All other accounts stay signed in. Claude asks for a sign-in on the
account that is now active. Sign in with that profile's own account only;
the message names the profile.

<a id="internal"></a>
## Exit 70: internal error (PD-E70)

Something unexpected happened. Run `paritydesk status` before trying
again; if a run was interrupted it says so. Please report the problem
with a support archive (see below), or with `paritydesk log` and
`paritydesk doctor` output (both are redacted).

The recovery drill of the release checklist (`PARITYDESK_FAULT=after:<phase>`)
also exits 70 on purpose: the run stops as if the process had been
killed, and `paritydesk status` then asks for `paritydesk recover`.

If the message says paritydesk cannot read other processes' open files
("is it running inside a sandbox?"), this is not a bug: paritydesk runs
inside a sandbox (for example a Code session's sandbox, or
`sandbox-exec`) that hides other processes' open files, so a switch
stops before writing anything. Run paritydesk from Terminal.
`paritydesk doctor` shows the `open-file-scan` check.

## Reporting a problem: the support archive

`paritydesk doctor --bundle <file>` runs doctor and also writes a
support archive (a `.tar.gz` file, mode 0600) for a bug report. It never
overwrites a file, never writes through a symbolic link, never writes
inside a Claude data folder, and is refused in read-only mode.
`--bundle-logs <n>` sets how many of the newest run logs it holds (5 by
default, at most 50; 0 for none).

The archive is designed to hold only redacted text that paritydesk
produced itself: no file from a Claude data folder, no line of Claude's
`main.log`, no session titles, no full ids and no paths under your home
directory.
[PRIVACY.md](../PRIVACY.md#the-support-archive) lists every file in it
and every redaction rule. Open it (`tar -xzf <file>`) and read it before
you attach it to an issue.

## A carried scratch session says "folder not found" or shows no previews

A session started without a folder lives in a scratch workspace.
paritydesk copies that workspace to the other account's folder at the
same path when it switches. If the session cannot find its folder:

- run `paritydesk plan --json -- <profile>` and read the `scratch` member:
  a workspace listed under `not_carried` (over the limits, unreadable, a
  symbolic link in its path, a folder without write permission for you)
  was not copied. Raise the limits in `[scratch]`, tidy the workspace or
  give yourself write permission on its folders, then switch again;
- `paritydesk doctor` lists carried workspaces no session uses any more
  (`scratch-orphans`); paritydesk never removes them.

File previews and Browser pane previews of a carried session work only on
the account that created the workspace. Removed scratch files are in the
newest backup generation (`paritydesk rollback`), and after pruning in the
`scratch-kept` folder doctor names; copy a file back by hand from there.

## Changes waiting to be applied in Claude

`paritydesk status` shows "… wait to be applied in Claude" after a switch
when some values (the sidebar's group-by and sort) can only be set
through Claude's own tools. Open a Code session and run
`/paritydesk:reconcile`; Claude may ask you to approve some calls.

- **A call was blocked by paritydesk.** While the reconcile works in a
  session, only the listed calls run there. Finish the reconcile, or run
  `paritydesk online cancel` in Terminal; nothing in Claude changes and
  the next switch plans the values again.
- **"paritydesk's hooks did not run in this session".** The plugin is not
  installed, is older than the binary, or hooks are turned off. Fix that,
  then run `/paritydesk:reconcile` again; nothing was changed.
- **"Another Claude session is applying these changes".** Let that session
  finish, or run `paritydesk online cancel`.
- **Some changes were not applied.** `paritydesk online status` lists the
  steps; `paritydesk online retry` followed by `/paritydesk:reconcile`
  offers the failed ones once more.
- **Claude is on another account.** The waiting changes are dropped; the
  next switch plans them again.
- **`--app-support-dir` is set.** The hooks only know the default folder,
  so the changes are not applied; `paritydesk doctor` says so.

## Pins are not carried

The plan says "pins not carried this time" and a warning names the profile
and the reason. paritydesk could not read that folder's pin list, so it
left the pins of both profiles exactly as they were; every other row was
carried as usual.

- **"Claude had not finished saving it" / "it changed while it was being
  read"** (in a preview: "not shown now"): quit Claude and run the switch
  or `paritydesk doctor` again.
- **"Claude did not close it cleanly last time"**: open and quit Claude
  once, then try again.
- **A format paritydesk does not know** (a newer Claude): update
  paritydesk. Until then pins are read from Claude's backup list when it
  has one (`doctor` says "backup list").
- **Some pins stayed although the other profile unpinned them**: the
  warning says why. Unpinning a session also unpins the pinned sessions
  under it; when paritydesk cannot pin those again (archived sessions,
  scheduled runs) or the session's section is hidden, it leaves the pin
  and the next switch offers it again. Unpinning it yourself in Claude
  also unpins the sessions under it.

## The pins check fails ("no longer saves pins where paritydesk reads them")

After paritydesk changed pins inside Claude, the pin list it reads did
not change, so this Claude version keeps pins somewhere else. Pins are
not carried while this lasts, and nothing else is affected. Update
paritydesk; `paritydesk rebase` clears the check (every row then starts
as on a first sync).

## Artifact sync is blocked or stopped

- *"cannot tell from Claude's log which account Claude is using"*:
  Claude's log does not show the account and organization, or shows an
  account change inside Claude after them. Quit and reopen Claude, then
  run `/paritydesk:artifacts` again. Nothing was recorded.
- *"The artifact list did not come from this profile's account"*: the
  list named an artifact recorded for another profile. Check which
  account Claude is on; nothing was recorded.
- *Publishing paused for this Claude version* (only when publishing is
  on; v0.2 ships capture only, so this cannot happen yet): a published
  page read back differently from the archive. The archive and every copy are unchanged
  since then; `paritydesk artifacts status` shows the gate. After an
  update of Claude or paritydesk, `paritydesk artifacts allow --roundtrip`
  (in Terminal) allows one verified publish again.
- *A sync stopped half-way*: `/paritydesk:artifacts` continues where it
  stopped. While a sync runs in a session, Claude's own artifact tools are
  paused there; `paritydesk online cancel` in Terminal ends it at once.
- *An artifact was overwritten*: the archive keeps every replaced page in
  its history (`paritydesk artifacts status`), and the older version stays
  in `<SupportDir>/artifacts/blobs`.
- *An artifact was deleted on one account*: paritydesk records the
  deletion and never deletes the other copies. `paritydesk artifacts
  allow <id> --restore --profile <name>` records that the copy should be
  created again there. v0.2 is capture only, so the copy is made by the
  first sync after publishing is turned on.

<a id="legacy-adopt"></a>
## Adopting folders from an earlier script stops or keeps leftovers

`paritydesk profile adopt --from-legacy` explains each folder it leaves
out and how to fix it ([migration-from-legacy.md](migration-from-legacy.md)).

- "the folders changed since the dry run": a folder was renamed, added or
  changed between the dry run and `--apply`; run the dry run again and use
  its new command. "the flags differ from the dry run's": use exactly the
  command the dry run printed.
- Exit 32 with "kept for now": Claude was running (for leftovers inside
  `Claude`) or a process used the folder; quit Claude and run again.
- A directory marked "do not delete it by hand" may belong to the folder
  itself; leave it.
- Moved leftovers are in a pinned backup generation:
  `paritydesk rollback <n>` puts them back; a bare `paritydesk rollback`
  never picks it.
