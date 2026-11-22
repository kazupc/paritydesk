# Privacy

paritydesk runs only on your Mac. It opens **no network connection** of
any kind: no package in its dependency graph can (a test enforces that
`net`, `net/http` and `crypto/tls` are absent), and it has no telemetry,
update check or crash reporting. You can confirm this while a switch runs
with `nettop -p $(pgrep paritydesk)` or a firewall such as LuLu.

paritydesk never reads, copies or moves credentials: no OAuth tokens, no
browser cookie stores, no saved-password databases, no Keychain items. A
fixed deny floor in the code refuses any plan that would touch them,
whatever a compatibility manifest says.

This page lists every path paritydesk reads or writes. `~` is your home
directory; the data root is `~/Library/Application Support` unless
`app.data_root` in the config says otherwise.

## paritydesk's own files

| Path | Access | Contents |
|---|---|---|
| `~/.config/paritydesk/config.toml` (or `$XDG_CONFIG_HOME/paritydesk/config.toml`, `PARITYDESK_CONFIG`, `--config`) | read, written by `init` and the profile commands (mode 0600) | Your choices only: aliases, labels, folder names, what to sync. Never account or organization ids. |
| `~/Library/Application Support/paritydesk/` (mode 0700) | read and written | `state/` (the journal `root.json` and content-addressed sync bases), `generations/` (backups), `run/` (background-job property lists, result files, confirmed plans), `locks/` (the run lock), `hook-binary` (the absolute path of the paritydesk binary, for the plugin), `compat.d/` (optional compatibility overrides you add; read only). |
| `~/Library/Logs/paritydesk/` (mode 0700) | written | One JSON-lines debug log per run (`<run>.jsonl`, identifiers redacted by default) and the output of background jobs. |
| `<data root>/.paritydesk-tmp/` | written, then removed | Temporary copies of Local Storage for reading, and temporary files that are renamed into place. |

Backups and sync bases can contain your session titles, project paths and
other app data. Values classed as secret (environment variable values and
MCP server environments) are stored only as hashes. `paritydesk backups
purge` removes every backup; `paritydesk uninstall` removes the state and,
with `--keep-backups=false`, the backups.

## Claude's data folders

paritydesk lists the folders named `Claude` and `Claude-*` in the data
root. It renames them to switch accounts (an atomic swap, then an
exclusive rename) and sets one extended attribute, `dev.paritydesk.marker`,
on each data folder to tie sync bases to it. It never creates, merges or
deletes a data folder.

Inside a data folder it reads, and writes only while Claude is quit and
only after taking a backup:

| Data | Files |
|---|---|
| Code sessions and archive state | `claude-code-sessions/<account>/<organization>/local_*.json`, `deleted_*`, `archived-sessions.idx` |
| Backlog and scheduled tasks | `backlog/tasks.json`, `scheduled-tasks.json` in the same directory |
| Sessions waiting for input | `waiting-input/` (hashes and file kinds; contents are copied, not interpreted) |
| Environments | `ccd-session-presets.json` and the matching Local Storage items |
| Sidebar group order, pin order and view; selected app preferences; MCP servers (opt-in) | `claude_desktop_config.json` (only the members listed in the compatibility manifest) and the matching Local Storage items |
| Window size and position | `window-state.json` |
| Worktree registrations | `git-worktrees.json` |
| Local Storage | `Local Storage/leveldb/`: read through a temporary copy; written only for the item names listed in the compatibility manifest, while Claude is quit |

### The one read of `config.json`

Each data folder's `config.json` holds Claude's credentials next to the
account id. paritydesk reads it only to learn which account a folder
belongs to:

- The file is opened only if it is a regular file (never through a
  symbolic link) of at most 8 MiB, and read into memory.
- A scan of the top-level members locates two of them and nothing else:
  the last signed-in account id, which is parsed and checked to be a
  UUID, and the token-cache member, whose **presence** is recorded while
  its value is never decoded, copied, hashed or logged.
- No other member is parsed. The file is never written, and errors never
  include its content.

### Never read

Browser cookie and saved-password stores, `Local State`, `Session
Storage`, `IndexedDB`, `Network/`, device and usage-history files, and
every other entry the compatibility manifest marks as never touched.

## Other things paritydesk looks at

- `~/Library/Logs/Claude/main.log`: bounded scans for a few fixed line
  patterns (quit vetoed, a turn ended, a session focused, Claude in
  Chrome connected, a sign-in needed). Lines are matched, never copied
  into paritydesk's own logs.
- The Claude app bundle wherever it is installed: its `Info.plist` and
  the executable's size and modification time, to notice updates, and the
  updater's staged-update state.
- The process table and open files (`lsof` with fixed arguments), to make
  sure Claude has fully quit before anything is written.
- `git status --porcelain` in worktree directories that a session uses,
  read-only, to warn before a worktree with uncommitted changes loses its
  registration. Hooks, fsmonitor and attribute filters are switched off
  for this command, so no program a repository names runs; files that a
  filter such as Git LFS normalizes may therefore count as modified.

## Things paritydesk asks macOS to do

- Send Claude Apple Events: `quit`, and `get name` once while you are
  present so that macOS asks for Automation permission.
- Launch Claude (`open -b`) and reopen a session with Claude's own
  session link.
- Post a notification with fixed text when a background switch finishes.
- Start and remove one-shot launchd jobs in your user session for
  switches started inside Claude.

## Diagnostics

`--json` output and logs mask account, organization and session ids to
four characters. Logs, `doctor` output and the error documents of
`--json` output also replace paths under your home directory with
`~/<hash>`. Commands whose purpose is to show a path (`config path`,
`config show`, `uninstall`) print it in full. Nothing leaves your Mac
unless you attach it to an issue yourself.
