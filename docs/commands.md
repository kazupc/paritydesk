# Commands

`paritydesk help <command>` prints the usage of every command (the flags
of the legacy migration are described in
[migration-from-legacy.md](migration-from-legacy.md)). This page groups
them and explains what is easy to miss. Exit codes and what
to do next are in [if-something-goes-wrong.md](if-something-goes-wrong.md).

## Profiles

```
paritydesk init                              set up for the account signed in to Claude now
paritydesk profile add <alias>               add another account (finish with recover)
paritydesk profile adopt <alias> <folder>    register a parked folder by its name, e.g. Claude-personal
paritydesk profile list                      list profiles
paritydesk profile rename <alias> <new>      rename a profile
paritydesk profile remove <alias>            unregister a profile; its folder is kept
```

- `init`, `profile add` and `profile adopt` ask whether the account is
  managed by an employer or client (or take `--managed` / `--unmanaged`;
  without a terminal one of them is required). Secret values cross the
  boundary of a managed account only for the entries listed in
  `sync.secrets_allow`. `init` also takes `--alias` (asked
  when missing) and `--label`.
- `profile add` quits Claude and relaunches it on an empty folder so you
  can sign in to the new account. After signing in, run `paritydesk
  recover` to bind the new account to the profile; until then other
  commands stop with exit 41.
- If you already park folders as `Claude-<name>` with your own script,
  register each with `paritydesk profile adopt` instead of `profile add`;
  pass the folder's name, not its path. To register all of them at once,
  see [migration-from-legacy.md](migration-from-legacy.md).

## Switching

```
paritydesk status                            active profile and anything pending
paritydesk plan [<alias>|-]                  preview a switch (approximate)
paritydesk switch [<alias>|-]                switch ("-" is the previous profile)
paritydesk sync                              re-apply the shared state to the active profile
paritydesk verify                            check that Claude kept what the last run wrote
```

- Without an alias, `plan` and `switch` target the previous profile;
  before the first switch, the only other profile.
- `plan` and `switch` take `--only` and `--skip` with a comma-separated
  list of categories: `sessions`, `environments`, `order`, `sidebar`,
  `pins`, `prefs`, `worktrees`, `scratch`, `window`, `mcp`. Scratch
  workspaces follow `sessions`.
- `switch` asks before it changes anything. `--yes` skips the question
  (destructive differences found after the preview still ask);
  `--allow-deletions <n>` lets a run delete up to n items per category
  past the deletion limit; `--no-launch` leaves Claude quit afterwards;
  `--detach` runs the switch as a background launchd job. The plugin uses
  `--detach` with `--confirmed-plan`; the SwiftBar and Raycast
  integrations use `--detach --yes`; the `pd` shell function uses
  `--detach` alone and asks in the terminal.
- `status --short` prints one line and never opens Local Storage (used by
  the menu bar integration).
- `sync` quits and relaunches Claude like a switch, without changing
  accounts. It runs only in Terminal.

## Recovery and backups

```
paritydesk recover                           finish or undo (--rollback) an interrupted run
paritydesk rollback [<n>]                    restore a backup generation without overwriting newer work
paritydesk generations list                  list backup generations
paritydesk generations show <n>              show one generation
paritydesk backups prune                     apply the retention policy
paritydesk backups prune --scratch           remove kept scratch files of pruned generations (asks first)
paritydesk backups purge                     remove every generation
paritydesk rebase                            forget every sync base; the next run is a first sync
```

`rollback --propagate` also carries the restored values to the other
profiles on the next switch. A bare `rollback` never picks a pinned
generation (the one `profile adopt --from-legacy --quarantine` creates).

## Diagnostics

```
paritydesk doctor                            check the installation, folders and app compatibility
paritydesk doctor --bundle <file>            also write a redacted support archive
paritydesk doctor --grant-automation         ask macOS for Automation permission now
paritydesk history                           list past runs
paritydesk log [--run <id>]                  show the log of a run (default: the last one)
paritydesk version                           print the version
```

The support archive is described in
[if-something-goes-wrong.md](if-something-goes-wrong.md#reporting-a-problem-the-support-archive);
[PRIVACY.md](../PRIVACY.md#the-support-archive) lists every file in it.
Read it before you attach it to an issue.

## Changes applied inside Claude, and artifacts

```
paritydesk online status                     changes waiting to be applied in Claude
paritydesk online retry                      offer the changes that were not applied once more
paritydesk online cancel                     drop the waiting changes; nothing in Claude changes
paritydesk artifacts status                  show the artifact archive (beta)
paritydesk artifacts plan                    preview an artifact sync on a profile
paritydesk artifacts ingest                  record a finished sync round now
paritydesk artifacts skip <id>               stop syncing an artifact (it stays in the archive)
paritydesk artifacts allow [<id>]            decide about one artifact (Terminal only)
paritydesk artifacts forget <id>             stop tracking an artifact (Terminal only)
paritydesk artifacts import-legacy --from <dir>   import an earlier script's archive (dry run)
```

`online next`, `decide`, `done` and `capture`, `artifacts next`,
`decide`, `done` and `pause`, and `hook` are used by the plugin's skills
and hooks; you do not need to run them.

## Setup and removal

```
paritydesk config path                       print where the config file is
paritydesk config show                       print it with every default filled in
paritydesk config validate                   check it
paritydesk completion bash|zsh|fish          print a shell completion script
paritydesk uninstall                         remove paritydesk's state; every data folder is kept
```

`uninstall` keeps backups and the artifact archive unless you pass
`--keep-backups=false` or `--keep-artifacts=false`. The keys of the
config file are described in [configuration.md](configuration.md);
shell completion in [install.md](install.md#shell-completion).

## Global flags

Every command accepts these flags, before or after its arguments:

| Flag | Meaning |
|---|---|
| `--json` | print one JSON document (`"v":1`); the schemas are part of the public API |
| `--plain` | plain lines, no tables or spinners |
| `--no-color` | no colour (also `NO_COLOR`, `TERM=dumb`) |
| `--ascii` | ASCII only (automatic when the locale is not UTF-8) |
| `--read-only` | plan only; never write (also `PARITYDESK_READONLY=1`) |
| `--lang auto\|en\|ja` | message language (also `PARITYDESK_LANG`); `--json` stays English |
| `--config <file>` | config file (also `PARITYDESK_CONFIG`) |
| `--state-dir <dir>` | state directory (also `PARITYDESK_STATE_DIR`) |
| `--app-support-dir <dir>` | paritydesk support directory |

## Inside Claude Code

The [Claude Code plugin](install.md#claude-code-plugin) adds three
commands:

- `/paritydesk:switch [<alias>]` shows the plan, asks once with Switch
  and Cancel buttons (a plain yes/no question where buttons are not
  available), and hands the switch to a background job that waits until
  the turn has ended. Claude reopens the session on the other account.
- `/paritydesk:reconcile` applies the changes only Claude's own tools can
  make (pins, sidebar sort and group-by). The next session start offers
  it when something is waiting.
- `/paritydesk:artifacts` saves this account's claude.ai artifacts into
  paritydesk's archive (beta, capture only).

For one-click switching from the menu bar, Raycast or a shell function,
see [integrations.md](integrations.md).
