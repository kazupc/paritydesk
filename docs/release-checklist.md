# Release checklist

Automated tests never touch the real Claude app. These checks do, so they
run by hand on a real Mac before every release, for the app version the
release claims to support. Record the app version, the Mac (Apple silicon
or Intel) and the result of each step in the release pull request. If a
check confirms a new runtime behaviour, record it in
[ADR 0003](adr/0003-online-reconcile.md) before the README claims it.

Use scratch content wherever a step deletes or edits something.

## Stub-app end-to-end tests (not on this Mac)

Check the "e2e (stub app)" job of the release commit's CI run. If its
summary shows the stub-app tests as passed, nothing more is needed here.
If it shows them as NOT RUN (the Apple Events diagnostic found that the
GitHub-hosted runner refuses Apple Events to the stub, and the
repository variable `PARITYDESK_E2E_ALLOW_SKIP` allows that skip),
nothing was tested: run them by hand before the release, and never on
the Mac used for the steps below:

- Use a self-hosted runner, or a Mac or macOS virtual machine that has
  no real Claude app and no real Claude data, in a disposable user:
  `scripts/ci/e2e.sh` refuses to run when `/Applications/Claude.app`
  exists, so another user account on the Mac with the real app is not
  enough. The tests also refuse the real user's home and the real
  bundle id.
- In that user, from a checkout of the release commit, run
  `CI=true scripts/ci/e2e.sh prepare`, then
  `CI=true scripts/ci/e2e.sh probe local`, then
  `CI=true PARITYDESK_E2E_NONET=1 scripts/ci/e2e.sh run` (or
  `PARITYDESK_E2E_NONET=0` if the probe says Apple Events are refused
  inside the sandbox). Allow Automation for the stub
  (`dev.paritydesk.stubapp`) when macOS asks.
- Expected: `TestStubApp` and its four subtests pass, with no skip.
  Record the macOS version, the Mac (Apple silicon or Intel), the
  sandbox used and the result in the release pull request.

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

4. **Log patterns read from the app's code.** While a Code session works,
   read the new lines of `~/Library/Logs/Claude/main.log`, `main1.log` …
   read-only (for example with `tail`). The 7.x manifest marks some
   patterns "from code"; confirm the text of each against the lines you
   see, without copying any ids:
   - `turn_cleared` items: interrupt a turn, stop a session, cause one
     failed turn (for example a model the account cannot use); in a
     Cowork task, interrupt it once;
   - `work_released`: wait 30 s after all work ends;
   - `work_uncounted`: not practical to provoke (1 h); confirm only that
     the text in the code of the release matches;
   - `account_changed`: during step 8 (profile add), after you signed in
     to the second account in the empty folder and **before**
     `paritydesk recover`: in Claude, switch that folder to the first
     account and back to the second (Claude's account menu, or sign in
     again). Expect two `[account] Login-state transition (… uuid: <x> →
     <y>), clearing oauth cache` lines. `paritydesk doctor` then shows two
     `account-change` warn lines and one `account-change` fail line ("the
     folder of account <masked> changed accounts 2 times since the last
     switch"), and its `version` check reports swap-only: two changes in
     one folder look like Claude's own multi-account mode. Then run
     `paritydesk recover` as step 8 says; the fail line and swap-only are
     gone and the next `paritydesk plan` must show mode full (the binding
     moved the mark). Never do this in the folder of a profile that is
     already bound: Claude disconnects that folder's connectors when its
     account changes;
   - `pairing_disabled`: expected **not** to appear;
   - `pairing_state`: appears as `established` after step 12.

   Drop a pattern that cannot be confirmed (make it empty or remove the
   list item) and record the result in ADR 0007.
5. **active_org.** With an account that belongs to two organizations,
   switch organizations in Claude and confirm which `main.log` line names
   the active one. Add it to `[signals].active_org` only if it is stable;
   otherwise leave it empty (per-organization rows are then skipped when
   more than one organization exists).
6. Re-check the patterns that are already filled in (quit vetoed, turn
   started, turn ended, session focused, Chrome connected, unclean exit)
   against the new app version. Patterns now match at the start of a
   line's message (the text after `<timestamp> [<level>] `), so confirm
   that each still matches there, including `focused_session` (written
   as `[CCD] LocalSessions.setFocusedSession: …`) and `unclean_exit`
   (written as `[updater] Previous run did not exit cleanly…`).
   Also open one session record under `claude-code-sessions` read-only
   and confirm the name of the field that holds the session's project
   path. It must match `project_path` in the manifest's sessions row
   (`cwd` until confirmed), because `sync.exclude` globs match on it.
   In a session that uses a worktree, check whether `cwd` is the
   worktree path (the manifest moves `cwd` with the worktree group).
   Read `preferences` in `claude_desktop_config.json` read-only and note
   which preference names acknowledge a permission mode; add them to
   `[permission_acks]` only when confirmed. Record the result in ADR 0003.

## Setup

7. `paritydesk init` on the first account. Expected: it asks whether the
   account is managed by an employer or client, the config is written
   with mode 0600, Automation is requested once, and `paritydesk doctor`
   reports every check ok or explains a warning.
8. `paritydesk profile add <alias>` for the second account. Expected:
   it asks the managed question, Claude restarts on an empty folder, and
   you sign in to the second account in Claude. Then run `paritydesk
   recover` and confirm the bind. Expected: `paritydesk status` shows
   both profiles, and the first account stays signed in.

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
    B at launch and again after ten minutes. Per the second S1 round
    (ADR 0003), expect sort, group-by and section order to return to B's
    own values (`paritydesk verify` lists them as changed, exits 0 and
    marks them contested) and section collapse to carry. Record what happened in
    ADR 0003 before the README claims it.
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

15. **Busy sessions.** Start a long Code task, then `paritydesk switch`
    in Terminal. Expected: P0 lists the working session with its title
    and asks; after yes, P3 waits, and when the turn ends Claude quits
    without its dialog. Run it again with `/model` sent in another
    session: that session shows as working for at most about 2.5 min (or
    as stale with keep-awake off in Claude's settings). A running Cowork
    task is listed (untitled) and is not dropped while Code is idle.
    Then keep a task working past `app.quit_timeout` and answer yes to
    "These sessions still look busy. Quit Claude now anyway?": Claude's
    own quit dialog appears; if you answer it with "Cancel", paritydesk
    exits 10, nothing changes and Claude keeps running. Record in ADR 0003
    how the quit event ended on "Cancel": no error, or the AppleScript
    error number in the error message (-128 is a refusal and -1712 a
    reply that did not come in time; both are expected to exit 10). Exit
    15 there is a bug: only -1743 and -1744 mean missing Automation, so
    record the number and the output. Also note how long the quit command
    waits while the dialog is open: if it waits past `app.quit_timeout`,
    the timeout only starts after the reply.
16. **Deletion breaker.** Delete several scratch sessions on one account
    and switch. Expected: exit 22 unless `--allow-deletions` is given.
17. **Rollback.** `paritydesk generations list`, then `paritydesk
    rollback <latest>`. Expected: the previous values return and
    anything changed afterwards is reported as a conflict, not
    overwritten.
18. **Recovery drill.** `PARITYDESK_FAULT=after:P7 paritydesk switch
    --yes` (expected: exit 70, the run stopped as if killed), then
    `paritydesk status` (expected: recover needed) and
    `paritydesk recover` (expected: a consistent state, Claude
    relaunches on the right account). Repeat with `after:P8`. Set the
    variable on the switch command only. Only phases with intents can be
    named (P0, P3, P5 to P9); any other value is refused with exit 2
    before anything runs, and so is every other command, `status` and
    `recover` included, while the variable holds it. The profiles must
    differ, so that the plan is not empty: a switch that skips the named
    phase (P6 and P7 with an empty plan, P0 without a staged update, P9
    after the fallback swap) stops just before P10 instead, with a
    warning that says so, and still exits 70. The e2e job also kills
    a switch with SIGKILL inside P7 after its first write
    (`PARITYDESK_FAULT=pause:P7`, refused outside the e2e harness).
19. **Worktree guard.** Leave uncommitted changes in a worktree that
    only the leaving account registers, then switch. Expected: exit 23.
20. **Update gate.** With a Claude update downloaded, `paritydesk
    switch`. Expected: Claude quits, the update installs, and the run
    finishes cleanly on the new version.
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

## Integrations and support archive

25. **Completion.** In bash, zsh and fish, load `paritydesk completion
    <shell>` as [docs/install.md](install.md) shows. Expected: commands,
    flags, `--only` categories and profile aliases complete; hidden
    commands (`hook`, `__aliases`) are not offered.
26. **SwiftBar or xbar.** Install the plugin as
    [docs/integrations.md](integrations.md) shows. Expected: the menu
    bar shows `pd <active alias>`, the menu lists the other profiles, and
    choosing one switches exactly like step 10 (a background job, a
    notification). With a run left open (step 18 before recovering), the
    menu shows only the recover line.
27. **Raycast.** Run "Switch Claude account" with the other alias, then
    with `Bad Alias`. Expected: the first switches like step 10; the
    second is refused and nothing runs.
28. **Shell function.** Source `integrations/shell/paritydesk.sh` in zsh
    and in bash. Expected: `pd` prints the short status; `pd <alias>`
    shows the plan and asks before switching; `pd 'a;b'` is refused;
    `set -o` in the calling shell is unchanged afterwards.
29. **Support archive.** `paritydesk doctor --bundle ~/Desktop/pd.tar.gz`,
    then `tar -xzf` it and read every file. Expected: mode 0600, exactly
    the files [PRIVACY.md](../PRIVACY.md#the-support-archive) lists, and
    no email, real name, full UUID, organization name, session title,
    project path or alias. Running it again with the same file name is
    refused.

<!-- v0.2 anchor: busy -->
## Log signals (v0.2)

- **Doctor.** With Claude running and one Code session working,
  `paritydesk doctor`. Expected: `busy-detection` ok; `busy-now` with the
  right count (it includes the session running doctor when you run it
  from inside Claude); `account-change` as expected (ok with no change;
  after the step 4 account round trip, warn lines until the next committed
  run, and the fail line only until `paritydesk recover` binds the
  profile); `chrome-pairing-org` with the masked organization equal to the
  one the `active_org` line names (`local_…` ids and accounts masked).
- **In-folder sign-in.** Not to be provoked on a bound profile (Claude
  disconnects that folder's connectors). Confirm only that
  `paritydesk doctor` shows no `account-change` fail line on this Mac.

<!-- v0.2 anchor: fdscan -->
## Open-file scan (v0.2)

1. **CI.** The macOS jobs (`macos-latest`, `macos-15-intel`) pass
   `internal/fdscan` and `internal/app` with the native scan
   (`TestScanChild`, `TestSelfCheckUnderSandbox`, `TestNativeMatchesLsof`,
   `TestScanAllFindsChild`); the `pd_nofdscan` step passes; the job log
   shows the full-scan counts (`unreadable`, `partial`) and the
   native-versus-`lsof` timing (not gating). The e2e subtest "leftover
   write holder stops the switch" passed.
2. **Layout.** `clang -arch arm64 -fsyntax-only scripts/dev/libproc-layout.c`
   and the same with `-arch x86_64` pass with the release SDK.
3. **On this Mac.** `paritydesk doctor`: `open-file-scan` is `ok` (native).
   A `warn` with counts means some processes were unreadable and `lsof`
   is used for them; record the counts in the release notes.

<!-- v0.2 anchor: sidebar-offline -->
## Collapsed groups and seed keys (v0.2)

Record every result in ADR 0009.

- SO-1. Collapse a project group, the Pinned group and one fork family on
  A; switch. Expected: on B they are collapsed at launch and again after
  ten minutes; `paritydesk verify` exits 0. If this fails, ship
  `sync.collapsed_groups = false`.
- SO-2. Collapse on A a group that B had expanded although it folds by
  default; switch. Expected: it is folded on B.
- SO-3. Only if a folder whose Sessions section was never set up by
  Claude is at hand: switch into it. Expected: the Sessions sort direction
  and the empty-group setting equal A's; the Sessions and Pinned fold may
  differ (documented in ADR 0009).
- SO-4. Toggle "show empty projects" on A; switch. Expected: B shows the
  same setting.
- SO-5. On two folders Claude has already set up, `paritydesk plan
  --json`. Expected: no change of `code-sessions-sort-ascending`.

<!-- v0.2 anchor: scratch -->
## Scratch workspaces (v0.2)

Use fictional content only.

- **S-1** On A, start a session without a folder, let Claude write two
  files, switch to B, open the session: no trust prompt, full history,
  files present, a new message works.
- **S-2** On B change one file, add one, remove one; switch to A: A
  matches; the removed file is in the newest generation.
- **S-3** Change the same file on both accounts between switches: a
  conflict copy appears. Commit in a scratch git repository on both
  accounts: the plan reports the `.git` as diverged; each repository is
  intact (`git fsck`).
- **S-4** Relaunch and wait 3 minutes: the `main.log` sweep lines report
  no removal of the mirror.
- **S-5** For the mirrored session on B: try a file preview, a Browser
  pane preview of an HTML file in the workspace, and `preview_start`;
  record which are refused; the README text must match.
- **S-6** Under an organization with an allowed-workspace-folders policy
  (if available): run a tool in a mirrored session; record whether it is
  denied.
- **S-7** Run an in-app switch from a scratch session that is building
  (`dist/` rewritten): the switch completes; the plan shows held-back or
  deferred items, not exit 3.


<!-- v0.2 anchor: online -->
## Online reconcile (v0.2)

On a Mac with app 7.x, the plugin 0.2.0 installed and two profiles A and B.

- R-ON-1. With a temporary PostToolUse hook that logs its raw payload
  (outside paritydesk), call `list_sections` once in a Code session.
  Expected: `tool_response` is one of the shapes `online.ExtractText`
  accepts (a string, a content array, or an object with `content`).
- R-ON-2. During `/paritydesk:reconcile`, decline one approval card.
  Expected: the step stays offered for `result_grace`, then shows
  `no_result` in `paritydesk online status`; nothing is retried
  automatically.
- R-ON-3. Run `/paritydesk:switch`; check the run log. Expected:
  `${CLAUDE_SESSION_ID}` expanded to a UUID in `--session`.
- R-ON-4. The switch confirmation and the pins question appear as
  buttons with every plan line, in the reopened session and in a Remote
  Control session; Cancel changes nothing.
- R-ON-6. Change the whole-list group-by on A (for example to state),
  switch to B and run the reconcile. Expected: the value survives a quit
  and relaunch of Claude.
- R-ON-7. (pins work item) `idb` over a copy of a real folder reports
  `db_from_metadata = true`.
- R-ON-8. Check whether the SessionStart payload carries
  `permission_mode`. Without it, the prompt text is shown in auto mode.
- R-ON-9. After a launch and after an account change inside Claude,
  `main.log` shows the account and organization line. Expected: the
  reconcile's replica check is known within two minutes.
- R-ON-10. During the reconcile, ask Claude to call `set_unread` on
  another session. Expected: denied; `paritydesk online status` lists the
  denial. The per-turn plan and wrap-up tools keep working.
- R-ON-11. A manual section's group-by changed on A reaches B after the
  reconcile; the skill loads deferred tools with one ToolSearch call.
- R-ON-12. `paritydesk online cancel` in Terminal while the reconcile
  runs. Expected: the next guarded call is denied with the cancel hint,
  then nothing is armed; nothing in Claude changed.

<!-- v0.2 anchor: pins -->
## Pins (v0.2)

On a Mac with app 7.x, the plugin 0.2.0 installed and two profiles A and
B. These steps are run by the maintainer, never by an agent.

- R-PIN-1. Quit Claude, then run `paritydesk doctor --json` on the real
  active folder. Expected: `pins` is ok and there is no `pins-mirror`
  warning. A debug build logs `DBFromMetadata = OSFromMetadata = true`,
  `Wrapped = ""`, Blink 21, V8 16.
- R-PIN-2. On A, pin a session, its fork and a hand-off child; switch so
  that the parent must be unpinned and the children kept. Expected: the
  keep steps run and both children stay pinned on both profiles after a
  switch back. Repeat with an archived pinned child: the unpin is held
  back with the "were not unpinned" warning.
- R-PIN-3. Hide a section that holds a pinned session that a switch must
  unpin. Expected: the unpin is deferred with the "section is hidden"
  warning (or, when not predicted, the step ends `not_writable`), and
  `paritydesk online status` shows it.
- R-PIN-4. Quit Claude and run `paritydesk doctor` at once. Expected: pins
  readable (no `pending`, no `unclean`).
- R-PIN-5. After an online pin change, quit and run `paritydesk doctor`.
  Expected: no `stale_store`; the Local Storage backup list's `timestamp`
  is not older than the change.

<!-- v0.2 anchor: artifacts -->
## Artifact sync (v0.2)

Use only scratch artifacts with fictional content. Until R-AR-4 passes,
README and CHANGELOG say "capture only (beta)" and the embedded manifest
keeps `publish_via = "off"`; R-AR-3, R-AR-4, R-AR-6 and R-AR-7 run with a
manifest override that sets `publish_via = "files"` (or `"version"`).

- R-AR-1. Shapes: with a temporary logging hook on a scratch artifact,
  record one raw payload of `list_artifacts` (paged), `get_artifact`,
  `list_files`, `read_declaration`, `read_artifact`, `publish_files`
  (PreToolUse and PostToolUse `tool_input`: is `uri` present?) and
  `read_file` with `paths`. Note the reminder's position and first words
  (fill `reminder_prefixes`), whether list titles are wrapped, and which
  tool family the session offers. Confirm `[artifacts.fields]`; remove the
  hook.
- R-AR-2. A scratch page of about 900 KiB, once formatted and once
  minified: the hook payload carries the whole part and the chain saves
  it. Calibrate `read_limit_lines` and `round_bytes`.
- R-AR-3. Round trip: create a scratch artifact on A, sync on A, switch,
  sync on B: `artifacts status` shows `roundtrip exact`, no doubled
  wrapper, the same title.
- R-AR-4 (gate). Transport `files`: the outbox grant card appears and is
  accepted, `publish_files` accepts the outbox path, one approval card per
  publish; note whether the preview pane opens and whether the model reads
  the file first. If the grant or path is refused, repeat with
  `publish_via = "version"` and a page under 256 KiB. Only after one
  transport passed may README and CHANGELOG claim publishing and the
  embedded manifest ship that transport.
- R-AR-5. Refusal texts: a type-made artifact, a stale `base_version`
  (edit the copy on B between inspect and publish), a deleted artifact, a
  public target; fill `[artifacts.refusals]`.
- R-AR-6. Conflict: edit on both accounts; the later edit wins on both and
  the earlier one is in history. A rename on B only reaches A.
- R-AR-7. Delete the copy on B: reported, A's copy untouched, never
  created again until `allow --restore`.
- R-AR-8. A page with a declared capability is skipped until `allow
  --capabilities` in Terminal; the same command inside Claude exits 2.
- R-AR-9. Legacy import of a copy of an earlier archive (dry run, then
  `--apply`); the first sync verifies the imported copies before any
  publish.
- R-AR-10. Change the account inside Claude without a switch, run the
  skill: `blocked`, nothing recorded; note whether a new org line was
  logged. Interrupt a sync, then ask Claude to publish a page of your own:
  not denied (pause and done disarm).

<!-- v0.2 anchor: legacy-adopt -->
## Adopting folders of an earlier script (v0.2)

Use fictional accounts only.

- **L-1** With `Claude` and two parked `Claude-<Name>` folders made by
  swapping folders by hand, run the dry run: every folder is listed with
  its alias and masked account; nothing changed on disk.
- **L-2** Apply in Terminal: the config keeps the folder names; the first
  `paritydesk plan` shows the first-switch line and no deletion; switch
  and switch back.
- **L-3** Put a directory of account A inside B's folder; apply with
  `--quarantine`: it moves into a pinned generation; `backups prune`
  keeps it; `rollback <n>` puts it back; `backups purge` warns first.


<!-- v0.2 anchor: i18n-ja -->

## Uninstall (last on this Mac)

30. `paritydesk uninstall`. Expected: every data folder intact, the
    printed `mv` command (shown only when no profile is active) correct,
    and an exact list of what was removed.

## Release and install (quarantine gate, design risk R8)

paritydesk has no Developer ID and is not notarized. These steps check
that no install path needs Gatekeeper to be weakened. Use a second, clean
macOS user account without Claude data where the step says so.

31. **Set the versions.** Before tagging, set the release version
    (without `-dev`) in `plugin/.claude-plugin/plugin.json`, both entries
    of `.claude-plugin/marketplace.json`, `PLUGIN_VERSION` in
    `plugin/bin/paritydesk-hook` and the default of `Version` in
    `internal/cli/cli.go`, and commit. The policy test keeps the plugin
    and the shim equal; check the other two by hand. The SessionStart
    hook compares the plugin's and the binary's major and minor version
    and stays silent when they differ.
    **Tag and build.** Push a `v<version>` tag. Expected: the release
    workflow passes its tests and govulncheck, and GoReleaser creates a
    draft release with the arm64, amd64 and universal tarballs, the
    source tarball and `SHA256SUMS`. Note the Go version the workflow
    used (`go version` in its log; `runtime.Version` is part of the
    binary). To check that the build is reproducible, rebuild from the
    same tag with that exact Go version and without `--snapshot`, for
    example `goreleaser build --clean --skip=validate`, and compare the
    binaries' sha256 with the released ones. Two downloads of the same
    release file prove nothing, and a `--snapshot` build differs in its
    version string. Until this comparison has passed once, the release
    notes say the build is designed to be reproducible. Paste the
    CHANGELOG section into the draft and publish it.
32. **Update the tap.** The tap repository `kazupc/homebrew-tap` is
    created by hand once (it is not part of this repository). Copy
    `packaging/homebrew/paritydesk.rb` to `Formula/paritydesk.rb` in the
    tap, set the version in `url` and the sha256 of that tag tarball
    (`curl -fsSL <url> | shasum -a 256`), and run `brew audit --strict
    --formula kazupc/tap/paritydesk` and `brew test` before pushing.
33. **Tap install on a clean account.** In the clean account:
    `brew install kazupc/tap/paritydesk`, then
    `xattr -l "$(brew --prefix)/bin/paritydesk"`, `paritydesk version`
    and `paritydesk status`. Expected: Go is installed as a build
    dependency, no `com.apple.quarantine` attribute, no Gatekeeper
    prompt, and both commands run. Record the result for R8 in ADR 0003.
    If a prompt appears, stop the release and record it.
34. **Tarball install on a clean account.** Download the tarball and
    `SHA256SUMS` with Safari, then follow [docs/install.md](install.md):
    the checksum check passes, `xattr -l` shows the quarantine attribute,
    the binary is refused before `xattr -d com.apple.quarantine` on that
    one file and runs after it. Repeat the download with `curl`: no
    quarantine attribute. Never change Gatekeeper's global setting.
35. **go install.** `go install
    github.com/kazupc/paritydesk/cmd/paritydesk@v<version>` with the
    newest Go 1.26 patch release. Expected: `paritydesk version` prints
    the version and the binary runs without a prompt.
