# How paritydesk works

This page explains what a switch does, what has been confirmed on a real
Mac, and how each kind of data is carried. For the code map and the ids
cited in the code, see [architecture.md](architecture.md).

## A switch, step by step

The Claude desktop app keeps everything for the signed-in account in one
data folder. paritydesk keeps one folder per account: the active one at
the app's default location, the others parked next to it as
`Claude-<alias>`. A switch quits Claude, carries your shared state into
the folder of the account you are switching to, swaps the two folders,
and relaunches Claude.

paritydesk is designed so that no account has to sign in again (release
checklist steps 8 and 10 check this). If Claude still asks for a sign-in,
paritydesk exits 51, described in
[if-something-goes-wrong.md](if-something-goes-wrong.md#needs-sign-in).
paritydesk never touches credentials.

Every write to a data folder is preceded by a numbered backup
generation, every irreversible step is journaled, and an interrupted
switch can be finished or undone with `paritydesk recover`. Changes that
`/paritydesk:reconcile` makes through Claude's own tools (pins, sidebar
sort and group-by) are not part of a rollback.

## What has been confirmed on a real Mac

paritydesk claims a capability only after a check by hand on the real
app backs it. The results are recorded in
[ADR 0003](adr/0003-online-reconcile.md); the ids in parentheses are its
spike and finding numbers:

- **Quitting is clean and polite.** An Apple Event quits Claude cleanly;
  a signal does not, so paritydesk never sends one (S3, finding 2). When
  a Code session is still working, Claude answers the quit with its own
  dialog (finding 1); paritydesk is designed to wait for your answer and
  never force a quit (release checklist step 15 confirms it).
- **A background job started from a Code session survives Claude
  quitting.** A launchd job started from a Code session quit and
  relaunched Claude three times without an Automation prompt (S3);
  paritydesk's own switch from a Code session is release checklist
  step 13.
- **Sidebar order survives a relaunch** when it is written to both of
  the places Claude keeps it, which is how paritydesk writes it. Values
  Claude later replaces are reported, not fought over (S1, S4).
- **The session you switched from can be reopened** on the other account
  with Claude's own session link (S10).
- **Folders keep their identity marker** across restarts, so paritydesk
  can tell which account a folder belongs to (S17).
- **Claude in Chrome keeps working on every account, provided you use one
  Chrome profile per Claude account**, each with Claude in Chrome signed
  in to that account and kept open. The extension follows the Chrome
  profile's sign-in, not the Claude data folder, so paritydesk cannot
  move it between accounts (S18, finding 5).

Everything else is covered by automated tests with synthetic data, plus
read-only checks of real log lines and data copies recorded in ADR 0003.
The [release checklist](release-checklist.md) confirms it on a real Mac
before a release; until then this page describes it as designed.

## Scope of v0.1

v0.1 is designed to carry Code sessions, backlog and scheduled tasks,
environments, the sidebar's group order, pin order and view, selected app
preferences, window size, worktree registrations and, if you opt in, MCP
servers, in both directions with the newest change winning. Each item
becomes a claim only once the release checklist has confirmed it on a
real Mac.

A staged Claude update installs when Claude quits (finding 4 of
ADR 0003), so a switch lets it install first and checks the new version
before it syncs anything. That path has not yet been run against the real
app (release checklist, step 20).

## v0.2 additions (new, not yet confirmed on a real Mac)

The [CHANGELOG](../CHANGELOG.md) lists every v0.2 change and the release
checklist step that confirms it.

### Waiting for working sessions

paritydesk waits for working sessions before quitting Claude. It finds
them in Claude's log, lists them in the plan and waits for them; Claude's
own quit dialog stays the last guard. It refuses to run when a folder was
signed in, inside Claude, to another profile's account
([ADR 0007](adr/0007-log-signals.md); the patterns read from the app's
code are confirmed on a real Mac before a release).

### Checking that no process holds a file

Before anything is written, paritydesk checks that no process still holds
a file in Claude's data folders. It reads macOS's process information
directly (no external command); `lsof` with fixed arguments is the
fallback, and `app.open_file_scan = "lsof"` keeps the v0.1 check. If
paritydesk cannot read other processes' open files (for example inside a
sandbox), the switch stops before writing anything.

### Sidebar: collapsed groups, sort and group-by

Collapsed sidebar groups and fork families carry between accounts
(`sync.collapsed_groups`), and so does the "show empty projects" setting
([ADR 0009](adr/0009-sidebar-collapse-and-seed.md)). Under
`sync.exclude`, collapsed project groups follow the same rule as their
sessions; while any glob is set, collapsed fork families and project
groups that are not plain paths stay on their account.

Some values only Claude's own tools can change after a relaunch (the
sidebar's group-by and sort): Claude keeps them per account on its
servers and puts them back at launch. The switch queues those calls, and
the next Code session's `/paritydesk:reconcile` makes them, with a hook
that lets only the listed calls run while it works and never approves
anything for you ([ADR 0005](adr/0005-online-layer.md); [release checklist](release-checklist.md#online-reconcile-v02)
R-ON). `paritydesk online cancel` drops the waiting changes at any time.

### Pins

Which Code sessions are pinned (Claude 7.x) is carried too. paritydesk
reads the pin list read-only and asks inside Claude before it pins or
unpins anything (`/paritydesk:reconcile`, one approval card per other
session). When unpinning a session would also unpin sessions under it,
those are pinned again, or the unpin waits with a warning. A pin list
that cannot be read only skips pins for that switch
([ADR 0005](adr/0005-online-layer.md); [release checklist](release-checklist.md#pins-v02) R-PIN).
`sync.pins = false` turns it off.

### Scratch workspaces

Scratch workspaces (sessions started without a folder, Claude 7.x) are
carried with their sessions ([ADR 0008](adr/0008-scratch-workspaces.md)).
paritydesk copies the workspace to the other account's folder at the
same path, so the session keeps its history and stays trusted.

- Changed files merge with the newest change winning; the other copy is
  kept next to it as "... (conflict from <profile> <date>)".
- A `.git` folder is never mixed: it is taken whole from one side, and
  while it differs on both accounts it is not carried.
- `node_modules`, `.venv`, `vendor`, build and cache folders
  (`[scratch] cache_dirs`) and other version-control folders are not
  carried, so reinstall or rebuild them where needed.
- A workspace over the limits (`[scratch]`: 20,000 files or 2 GiB per
  workspace, 8 GiB or 50,000 files per switch by default) is reported and
  not carried.
- File previews and Browser pane previews of a carried session work only
  on the account that created it (unverified on a real Mac).
- A file removed in a workspace is moved into the switch's backup
  generation; when old generations are pruned those files move to
  `scratch-kept` and stay until `paritydesk backups prune --scratch`.
  paritydesk never removes a workspace.
- A process still running in a scratch workspace keeps the folder busy,
  like any other open file.

### Artifacts (beta, capture only)

`/paritydesk:artifacts` in a Code session saves the account's claude.ai
artifacts into paritydesk's archive on this Mac, newest change winning
and older versions kept in history. Publishing them to your other
profiles is built but stays off until it has been confirmed on a real Mac
([release checklist](release-checklist.md#artifact-sync-v02) R-AR-4). paritydesk never deletes an artifact;
artifacts with several files, declared capabilities or another author are
skipped ([ADR 0006](adr/0006-artifact-sync.md)). A switch never carries
artifacts.

### Coming from an earlier folder-swap script

If your other accounts are parked as `Claude-<Name>` folders next to
`Claude`, `paritydesk profile adopt --from-legacy` registers them all at
once, keeps their names, and can move other accounts' leftovers into a
backup it never prunes. Start with the dry run in
[migration-from-legacy.md](migration-from-legacy.md).

## What it will never do

- Open, read, copy or edit tokens, browser cookie stores or Keychain
  items; they stay inside their own account's folder when the folder is
  swapped.
- Open a network connection of any kind: it is designed that way, and no
  network package is linked (a test checks this).
- Patch the app, or pool usage limits across accounts.
- Ask any account to give up its sign-in. Every account stays signed in.

[PRIVACY.md](../PRIVACY.md) lists every path paritydesk reads and writes.
