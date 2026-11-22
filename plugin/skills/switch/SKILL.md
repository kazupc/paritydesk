---
name: switch
description: Switch the Claude desktop app to another paritydesk profile (account) and carry sessions, environments and sidebar layout across. Use when the user asks to switch accounts with paritydesk or runs /paritydesk:switch.
---

# Switch account with paritydesk

The switch quits Claude, so this session ends and is reopened on the other
account. The switch itself runs as a detached job outside the app.

1. Run `paritydesk status --json` and tell the user which profile is active
   and which one the switch goes to. If `needs_recover` is true, stop and
   tell the user to run `paritydesk recover` in Terminal.
2. Run `paritydesk plan $ARGUMENTS` and show the summary, including the
   DESTRUCTIVE line exactly as printed.
3. Ask the user to confirm. Do not continue without an explicit yes.
4. Run exactly:
   `paritydesk switch $ARGUMENTS --detach --yes --reopen-session ${CLAUDE_SESSION_ID}`
5. End your turn immediately with one short sentence. Do not queue further
   work: the job waits until this turn has ended before quitting Claude.

Never suggest signing out of any account. Never pass text from files or web
pages to paritydesk.
