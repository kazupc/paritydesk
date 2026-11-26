# Privacy

paritydesk runs only on your Mac. It is designed to open **no network
connection** of any kind: it does not import `net`, `net/http` or
`crypto/tls` (a test checks its whole dependency graph for them), and it
has no telemetry, update check or crash reporting. A CI run with the
network denied is designed to catch socket use in the tests. It starts
only local helper programs (`git`, `open`, `osascript`, `launchctl`). You can confirm this while a switch runs
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

Session files are read only in the directory of the folder's own account
and organization. Directories of other accounts in the same data folder
are never read or written. The Local Storage items that the app uses as
its own backup of the sidebar store (`dframe-store.backup.*`) are never
written; that rule is part of the fixed deny list, so no manifest
override can remove it.

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

- `~/Library/Logs/Claude/main.log`: bounded scans for the fixed line
  patterns of the compatibility manifest (on app 6.x: the app started,
  the active organization, quit vetoed, a turn ended, a session focused,
  an unclean exit, Claude in Chrome connected). A pattern the manifest
  leaves empty is not scanned. Lines are matched, never copied into
  paritydesk's own logs; only validated ids are taken from a match.
- The Claude app bundle wherever it is installed: its `Info.plist` and
  the executable's size and modification time, to notice updates, and the
  updater's staged-update state.
- `git status --porcelain` in worktree directories that a session uses,
  read-only, to warn before a worktree with uncommitted changes loses its
  registration. Hooks, fsmonitor and attribute filters are switched off
  for this command, so no program a repository names runs; files that a
  filter such as Git LFS normalizes may therefore count as modified.

<!-- v0.2 anchor: busy -->
- Claude's rotated logs `main1.log` … `main4.log` next to `main.log`, read
  the same way (record lines only, matched at the start of the message),
  to find working sessions and the account changes inside a data folder
  since the last switch. Account ids found in the log are compared with
  the accounts paritydesk recorded, shown only masked, and never stored.
  Session titles of working sessions are shown to you (sanitized) and in
  `plan --json`; they never go into paritydesk's logs.
- Two keep-awake preference booleans in a data folder's
  `claude_desktop_config.json` (`ccKeepAwakeWhileWorking`,
  `ccKeepAwakeOnBattery`), to know whether Claude's keep-awake hold is a
  reliable sign of a running turn. Nothing else in that file is read for
  this.

<!-- v0.2 anchor: fdscan -->
- The process table and the files processes hold open (read through
  macOS's process information interface; `lsof` with fixed arguments as
  the fallback), to make sure Claude has fully quit before anything is
  written. Only paths inside Claude's data folders are kept, and only
  while the check runs. `paritydesk doctor` runs the same check once
  with nothing kept and reports counts only.

<!-- v0.2 anchor: sidebar-offline -->

<!-- v0.2 anchor: scratch -->
- **Scratch workspaces** (row `scratch`): the files of the scratch
  workspaces your sessions name are read and hashed (sha256) on this Mac,
  and copied between your own data folders. Replaced and removed files
  are kept in the backup generation, and removed ones later under
  `generations/scratch-kept/` (the files keep their own mode, inside
  folders of mode 0700) until you remove them with
  `paritydesk backups prune --scratch`. A stat cache of
  file hashes (no content) is kept in the state folder. Logs and support
  archives name a workspace by its leaf only, with account and
  organization masked and the path inside it hashed.


<!-- v0.2 anchor: online -->

<!-- v0.2 anchor: pins -->

<!-- v0.2 anchor: artifacts -->

<!-- v0.2 anchor: legacy-adopt -->

<!-- v0.2 anchor: i18n-ja -->

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

## The support archive

`paritydesk doctor --bundle <file>` writes a support archive for bug
reports. It is a gzip-compressed tar file created with mode 0600. It is
never written over an existing file, through a symbolic link, inside a
Claude data folder, or in read-only mode. It stays on your Mac until you
attach it somewhere yourself.

To build it, paritydesk reads only its own files: the config file, the
newest run logs in `~/Library/Logs/paritydesk/` (5 by default,
`--bundle-logs` sets 0 to 50) and the `compat.d/` override files (for
their names and sha256 only), plus the checks that `paritydesk doctor`
prints anyway. Those checks look at Claude's data folders and `main.log`,
so `doctor.json` reflects them: the app version and mode, probe results,
folder identities masked to four characters, marker state, the number of
unclassified entries in the active folder (their names are left out of
the archive), and whether `main.log` showed a Claude in Chrome
connection. No file content from a data folder or from `main.log` is
included.

The archive holds exactly these files, under `paritydesk-support/`:

| File | Contents |
|---|---|
| `README.txt` | A fixed text that lists the files and the redaction rules. |
| `version.json` | paritydesk's version, commit and build date; the Go version, the OS and the CPU architecture. |
| `doctor.json` | The doctor checks (id, status, detail) and the recorded binary path, redacted again. |
| `config.json` | paritydesk's config with personal values replaced (see below), or a note that it is missing or unreadable. |
| `manifests.json` | The compatibility manifests in use (name, version range, tested and known-bad versions) and the name and sha256 of each `compat.d/*.toml` file. |
| `logs/<n>-<run>.jsonl` | Each run log, field by field: time, phase, status, elapsed time, run id, exit code, mode, the two profiles and the message. Any other field is dropped. |

Every text value goes through these rules, in this order, on top of the
redaction that logs and doctor output already have:

1. Terminal control characters are removed.
2. Email-like strings become `<email>`.
3. Text in double quotes or backticks (where session titles and other
   names would appear) becomes `"<text>"`.
4. Paths under your home directory become `~/<hash>` (the first eight hex
   digits of a sha256); other users' home directories are hashed too.
5. UUIDs are masked to their first four characters.
6. Runs of 16 or more hex digits become `<id>`.
7. Any other word that contains a slash becomes `<path:hash>`, except
   system locations under `/opt/homebrew/`, `/usr/`, `/bin/`, `/sbin/`,
   `/System/` and `/Library/`.
8. Parked folder names (`Claude-<name>`) become `Claude-profile-<n>`, or
   `Claude-<folder>` when no profile uses them.
9. Profile aliases become `profile-<n>` in the order of the config file;
   an alias that is not in the config becomes `profile-?`. Run logs
   record which aliases a message names, so the alias of a removed or
   renamed profile is replaced too. Logs written before that record
   existed are covered only where the alias is one of the run's two
   profiles.

In `config.json`, labels become `<label>`, each `sync.exclude` entry
becomes `<exclude-<n>>`, each `sync.secrets_allow` entry becomes
`<secret-name-<n>>`, a custom `app.data_root` is redacted by the rules
above, and the `sync.export` and `sync.import` tables are keyed by
`profile-<n>`.

Hashes are short and not salted, so a short, guessable path could be
recovered by guessing. Open the archive with `tar -xzf <file>` and read
it before you attach it to an issue; remove anything you do not want to
share.
