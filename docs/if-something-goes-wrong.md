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

<a id="declined"></a>
## Exit 3: the plan was declined (PD-E3)

Nothing was changed. You answered no, the question timed out, or a
confirmed plan (`--confirmed-plan`) was missing, older than two minutes,
or smaller than what the switch would do now: a switch never deletes
more than the plan you confirmed. Run `paritydesk plan` again and confirm
the new plan.

<a id="app-did-not-quit"></a>
## Exit 10: Claude did not quit (PD-E10)

Nothing was changed, and Claude was not forced to quit. Usually a Code
session was still working and Claude asked whether to quit; your answer
in Claude decides. If you chose "Wait for Claude" and Claude quits later,
paritydesk relaunches the profile you were on. Finish or stop the running
work, then run the command again.

<a id="run-lock-busy"></a>
## Exit 11: another paritydesk run is in progress (PD-E11)

Nothing was changed by this run. Wait for the other run (for example a
switch started from the plugin) to finish, then check
`paritydesk status`.

<a id="sessions-busy"></a>
## Exit 12: Code sessions are still working (PD-E12)

Nothing was changed. A switch quits Claude, which would interrupt the
listed sessions. Wait until they finish, then run the command again.

<a id="update-pending"></a>
## Exit 13: a Claude update is staged (PD-E13)

Nothing was changed. The update would install when Claude quits, and the
switch would relaunch a different version. Run
`paritydesk switch --allow-update` to let the update install first;
paritydesk re-checks the new version before it syncs anything.

<a id="foreign-tool"></a>
## Exit 14: another account-switching tool was detected (PD-E14)

Nothing was changed. Two tools that swap data folders corrupt each
other's view. `paritydesk doctor` names what it found. Stop using one of
them; paritydesk can adopt the other tool's parked folders by identity
with `paritydesk profile adopt`.

<a id="automation"></a>
## Exit 15: Automation permission is missing (PD-E15)

Nothing was changed. paritydesk quits Claude with an Apple Event, which
macOS allows only with Automation permission. Run
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

<a id="base-invalid"></a>
## Exit 21: a first-sync merge needs your confirmation (PD-E21)

Nothing was changed. A sync base no longer matches its folder (a folder
was restored, replaced or seen for the first time), so paritydesk merges
both sides without deleting anything. Run `paritydesk plan`, then
`paritydesk switch` in Terminal and confirm; from the plugin, confirm the
first sync when it asks.

<a id="deletion-breaker"></a>
## Exit 22: the plan deletes more than the limit (PD-E22)

Nothing was changed. The plan would delete more items in one category
than `limits.max_delete_fraction` and `limits.max_delete_min` allow. Look
at `paritydesk plan`. If the deletions are intended, run the switch again
with `--allow-deletions=N`.

<a id="worktree-risk"></a>
## Exit 23: a worktree with uncommitted changes would lose its registration (PD-E23)

Nothing was changed. Claude may clean up worktree directories that its
folder does not know about. Commit or stash the changes in the listed
worktree, or run the switch again with `--accept-worktree-risk`.

<a id="compat-refused"></a>
## Exit 30: this Claude version cannot be handled safely (PD-E30)

Nothing was changed. The installed Claude version is known to be
incompatible, or the files paritydesk relies on have a shape it does not
recognize. `paritydesk doctor` lists the failing checks. Wait for a
paritydesk release that supports the version.

<a id="swap-only"></a>
## Exit 31: switched without syncing (PD-E31)

The switch completed, but nothing was synced, because syncing could not
be done safely (for example an unknown Local Storage format, or an
account that uses the app's own multi-account support). Your data in each
folder is intact. `paritydesk doctor` explains why.

<a id="degraded"></a>
## Exit 32: some rows were skipped (PD-E32)

Everything else was synced. Some data was left alone, for example per
organization data when the active organization could not be determined.
`paritydesk doctor` lists what was skipped and why.

<a id="apply-rolled-back"></a>
## Exit 40: applying the plan failed and was rolled back (PD-E40)

The previous account is active again, and the entering folder was
restored from the backup generation. `paritydesk log` shows the step that
failed. Please report it with that log.

<a id="needs-recover"></a>
## Exit 41: a run was interrupted (PD-E41)

A switch, sync, rollback or profile add stopped part way, for example
because the Mac shut down. Do not move data folders by hand. Run
`paritydesk recover` in Terminal: it finishes the run, or with
`--rollback` undoes it, and it never creates, merges or deletes a data
folder. A profile add that waits for its first sign-in also shows here
until you sign in to the new account in Claude.

<a id="verify-drift"></a>
## Exit 50: values differ from what was written (PD-E50)

Your data is intact. After the relaunch, Claude replaced some values that
paritydesk had written (for example a sidebar order synced from the
server). The difference is recorded, so it is not mistaken for your own
edit on the next switch. `paritydesk verify` shows which rows drifted.

<a id="needs-sign-in"></a>
## Exit 51: the active account needs a sign-in (PD-E51)

All other accounts stay signed in. Claude asks for a sign-in on the
account that is now active. Sign in with that profile's own account only;
the message names the profile.

<a id="internal"></a>
## Exit 70: internal error (PD-E70)

Something unexpected happened. Run `paritydesk status` before trying
again; if a run was interrupted it says so. Please report the problem
with `paritydesk log` and `paritydesk doctor` output (both are redacted).
