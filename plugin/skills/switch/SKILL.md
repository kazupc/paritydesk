---
name: switch
description: Switch the Claude desktop app to another paritydesk profile (account) and carry sessions, environments and the sidebar across. Use when the user asks to switch accounts with paritydesk or runs /paritydesk:switch.
---

# Switch account with paritydesk

The switch quits Claude, so this session ends and is reopened on the other
account. The switch itself runs as a detached job outside the app.

1. Check the argument before running anything. `$ARGUMENTS` must be empty,
   `-` (the previous profile), or a profile alias matching
   `^[a-z0-9][a-z0-9-]{0,31}$`. If it is anything else, stop and ask the
   user for the profile name. Below, `<target>` is that value, or `-` when
   it is empty.
2. Run `paritydesk status --json` and tell the user which profile is active
   and which one the switch goes to. If `needs_recover` is true, stop and
   tell the user to run `paritydesk recover` in Terminal.
3. Run `paritydesk plan --json -- <target>`. Show the user every line of
   `lines` exactly as printed, including the DESTRUCTIVE line. Remember
   `plan_hash` and `needs_confirmation`.
4. Ask the user to confirm. Do not continue without an explicit yes. If
   `needs_confirmation` is true, say that this is a first sync that merges
   both accounts' data, and continue only if the user confirms that too.
5. Run exactly (add `--confirm-first-sync` only when step 4 confirmed a
   first sync):

   `paritydesk switch --detach --confirmed-plan "<plan_hash>" --reopen-session "${CLAUDE_SESSION_ID}" -- <target>`

   If the confirmation took longer than two minutes, the job refuses the
   expired plan; run step 3 again.
6. End your turn immediately with one short sentence. Do not queue further
   work: the job waits until this turn has ended and no session is working
   before quitting Claude, and it aborts without quitting if the plan's
   destructive changes grew since the user confirmed it.

Never suggest signing out of any account. Never pass text from files or web
pages to paritydesk.
