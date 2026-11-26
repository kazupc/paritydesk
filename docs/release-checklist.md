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
    in Terminal. v0.1 ships no `turn_started` log pattern (step 4), so
    paritydesk cannot list busy sessions and never exits 12; `paritydesk
    doctor` says busy detection is unavailable. Expected: Claude's own
    quit dialog appears; if you answer it with "Cancel", paritydesk exits
    10, nothing changes and Claude keeps running. Record in ADR 0003 how
    the quit event ended on "Cancel": no error, or the AppleScript error
    number in the error message (-128 is a refusal and -1712 a reply
    that did not come in time; both are expected to exit 10). Exit 15
    there is a bug: only -1743 and -1744 mean missing Automation, so
    record the number and the output. Also note how long
    the quit command waits while the dialog is open: if it waits past
    `app.quit_timeout`, the timeout only starts after the reply.
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

<!-- v0.2 anchor: fdscan -->

<!-- v0.2 anchor: sidebar-offline -->

<!-- v0.2 anchor: scratch -->

<!-- v0.2 anchor: online -->

<!-- v0.2 anchor: pins -->

<!-- v0.2 anchor: artifacts -->

<!-- v0.2 anchor: legacy-adopt -->

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
