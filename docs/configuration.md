# Configuration

paritydesk works without editing anything: `paritydesk init` and the
profile commands write the config file for you. This page lists the keys
you may want to change.

## Where the file is

`~/.config/paritydesk/config.toml`, or `$XDG_CONFIG_HOME/paritydesk/config.toml`
when `XDG_CONFIG_HOME` is set. `PARITYDESK_CONFIG` or `--config <file>`
names another file. It is TOML, holds your choices only (aliases, labels,
folder names, what to sync; never account or organization ids) and is
written atomically with mode 0600.

- `paritydesk config path` prints where the config file is.
- `paritydesk config show` prints it with every default filled in.
- `paritydesk config validate` checks it.

Do not downgrade paritydesk after running v0.2: an older binary rejects
the tables that v0.2 writes, such as `[online]` and `[scratch]`.

## What to carry: `[sync]`

| Key | Default | Meaning |
|---|---|---|
| `sessions` | `true` | Code sessions, backlog and scheduled tasks, sessions waiting for input (and scratch workspaces with them) |
| `tombstones` | `"newer-wins"` | a deletion is carried only when it is newer than the other copy's last activity; `"delete-wins"` always carries it |
| `exclude` | none | sessions that stay on their account (see below) |
| `environments` | `true` | environments, including environment variables |
| `group_order` | `true` | the sidebar's group order |
| `sidebar_view` | `true` | the sidebar view |
| `collapsed_groups` | `true` | collapsed sidebar groups and fork families |
| `pinned_order` | `true` | pin order |
| `pins` | `true` | which sessions are pinned (needs the plugin, applied by `/paritydesk:reconcile`; off while `online.reconcile = "off"`) |
| `worktrees` | `true` | worktree registrations |
| `window_state` | `true` | window size and position |
| `mcp_servers` | `false` | MCP servers (opt-in) |
| `app_prefs` | `["sidebarMode", "keepAwakeEnabled"]` | app preferences to carry; only names the compatibility manifest marks as safe are accepted |
| `artifacts` | `true` | artifact sync through `/paritydesk:artifacts` (beta) |
| `secrets` | `"same-boundary"` | secret values (environment variable values, MCP server environments) are copied only between profiles that are both unmanaged; to or from a managed profile only the entries in `secrets_allow` |
| `secrets_allow` | none | `"<preset>/<VAR>"` entries (an environment and one of its variables) that may cross to or from a managed profile |
| `export`, `import` | none | per profile alias, the categories it shares with or takes from the others; an alias that is not listed shares and takes everything |

Categories for `export`, `import`, `--only` and `--skip` are `sessions`,
`environments`, `order`, `sidebar`, `pins`, `prefs`, `worktrees`,
`scratch`, `window` and `mcp`; `export` and `import` also accept
`artifacts`. `scratch` follows `sessions`.

### Keeping sessions on one account: `sync.exclude`

`sync.exclude` keeps Code sessions on the account where they are, so they
are never copied to another account. Each entry is either a session id
(`local_<uuid>`) or a glob over the session's project path: `*` and `?`
match within one path segment, an entry ending in `/**` matches that
directory and everything below it, and a leading `~/` is your home
directory. While any glob is set, a session whose project path cannot be
read is treated as excluded. The rule applies to backlog and scheduled
tasks and to collapsed sidebar groups too.

```toml
[sync]
exclude = ["~/clients/**", "local_00000000-0000-4000-8000-000000000000"]
```

## The app: `[app]`

| Key | Default | Meaning |
|---|---|---|
| `quit_timeout` | `"1m0s"` | how long a switch waits for Claude to quit (5 s to 10 min) |
| `late_quit_watch` | `"10m0s"` | after a quit that timed out, how long paritydesk keeps watching and relaunches the profile you were on if Claude quits late (0 to 1 h) |
| `busy_stale_after` | `"6h0m0s"` | a session whose turn started longer ago with no end in the log is shown as stuck and not waited for (10 min to 48 h) |
| `open_file_scan` | `"auto"` | how open files are found before writing: `auto` (native scan, `lsof` fallback), `native` or `lsof` |
| `require_tested` | `false` | on a Claude version paritydesk has not been tested with, only preview instead of switching with a warning |
| `data_root` | `"~/Library/Application Support"` | where Claude's data folders are |

## Safety limits and backups

| Key | Default | Meaning |
|---|---|---|
| `limits.max_delete_fraction`, `limits.max_delete_min` | `0.1`, `3` | the deletion limit for runs with `--yes` (exit 22) |
| `backups.keep` | `10` | backup generations kept |
| `backups.leveldb_clones_keep` | `2` | newest generations that also hold a full Local Storage copy |
| `backups.max_size` | `"2GiB"` | size limit of the backups |
| `log.redact` | `true` | redact identifiers in the run logs |

## Changes applied inside Claude: `[online]`

| Key | Default | Meaning |
|---|---|---|
| `reconcile` | `"prompt"` | the next Code session offers `/paritydesk:reconcile`; `"auto"` asks Claude to run it before other work (Claude's approval cards still appear); `"off"` turns it off (no pins, no sidebar sort or group-by, no artifact sync) |
| `guard` | `"armed"` | the plugin's guard blocks its fixed list of tools only while changes are being applied; `"always"` blocks it in every session |
| `create_sections` | `false` | create missing manual sidebar sections by name |
| `capture_max_age`, `queue_max_age`, `lease_ttl` | `"10m0s"`, `"336h0m0s"`, `"20m0s"` | how long a capture, a queue and a session's lease stay valid |

## Artifacts: `[artifacts]`

| Key | Default | Meaning |
|---|---|---|
| `verify` | `"first"` | read back the first publish of every sync, or `"all"` |
| `history_keep` | `10` | replaced versions kept per artifact |
| `cross_managed` | `false` | carry artifacts between a managed and an unmanaged profile |

## Scratch workspaces: `[scratch]`

| Key | Default | Meaning |
|---|---|---|
| `enabled` | `true` | carry scratch workspaces with their sessions |
| `max_files_per_workspace`, `max_workspace_bytes` | `20000`, `"2GiB"` | a larger workspace is reported and not carried |
| `max_files_per_run`, `max_run_bytes` | `50000`, `"8GiB"` | limits per switch |
| `cache_dirs` | `target`, `.next`, `dist`, `build`, `__pycache__`, ... | folder names held back wherever they occur (`config show` lists them all) |
| `exclude` | none | relative globs inside a workspace that are held back as well |
| `propagate_deletions` | `true` | carry file removals (the removed file goes into the backup generation) |
| `cross_managed` | `false` | carry workspaces between a managed and an unmanaged profile |
| `max_delete_fraction`, `max_delete_min` | `0.5`, `50` | the deletion limit for scratch files |

## Profiles: `[[profile]]`

`init` and the profile commands write one `[[profile]]` table per
account: `alias`, an optional `label`, `folder` (the parked folder name,
`Claude-<alias>` by default) and `managed_by_org`. Change them with
`paritydesk profile rename` and `paritydesk profile remove` rather than
by hand.
