# Moving from an earlier folder-swap script

This page explains `paritydesk profile adopt --from-legacy`, a hidden mode
of `profile adopt` for people who switched accounts with an earlier
folder-swap script. The examples use the folders `Claude`, `Claude-Work`
and `Claude-Home`, the aliases `work` and `home`, and masked fake ids.

## 1. Who this page is for

You switched accounts with a script that renamed the data folder: the
account in use lives in `Claude`, the others are parked as
`Claude-<Name>` next to it. paritydesk uses the same layout, so it can
take your folders over as they are.

## 2. What paritydesk will and will not do

It will:

- read every folder's own account (from the folder, not from the script);
- register one profile per folder, keeping every folder name
  (`folder = "Claude-Home"` in the config; nothing is renamed);
- seed sync bases from the values every folder already agrees on, so the
  first switch can tell later edits apart;
- on request, move directories of other accounts left inside a folder
  into a backup generation that is never pruned.

It never renames, merges or deletes a data folder, never edits the
earlier script or its files, never signs anything out, and never reads
the earlier script's state file unless you name it.

## 3. Step 0: stop the earlier script

Before any `--apply`:

- disable the script's launcher, menu item, scheduled job and shell
  alias, and any Claude Code skill, command or hook that calls it, so it
  cannot swap folders while paritydesk registers them or moves leftovers.
  paritydesk re-checks each folder before moving anything, but two tools
  moving the same folders is unsafe;
- keep the script's file until the first paritydesk switch works;
- if the script's last switch did not finish (no folder is named
  `Claude`, or two folders hold the same account), finish or undo it
  first;
- optional: with Claude quit, make a safety copy of the folders (an APFS
  clone, for example `cp -cR <data folder> <copy>`; it takes no extra
  space until something changes).

Install paritydesk first ([install.md](install.md)).

## 4. Step 1: dry run

```
paritydesk profile adopt --from-legacy
```

The dry run writes nothing. It prints one line per folder:

- `Claude (in use) -> profile work (account 0000…, org 0000…)` and
  `Claude-Home -> profile home (…)`: the folder becomes that profile. The
  alias of a parked folder comes from its name (`Claude-My.Home` gives
  `my-home`). `Claude` itself needs an alias: the command asks in a
  terminal, or pass `--legacy-alias Claude=<alias>`.
- `org unknown`: the account has several organizations in that folder and
  paritydesk cannot tell which one was in use. Rows kept per organization
  are skipped for that profile until the next switch tells.
- `is left out: …` with the reason and the fix:
  - another name: `--legacy-alias <folder>=<alias>`;
  - a folder you no longer use (for example an old copy holding the same
    account): `--legacy-exclude <folder>`;
  - a name paritydesk cannot use: rename the folder yourself while Claude
    is not using it, to `Claude-` followed by letters, digits, `.`, `_` or
    `-`;
  - a folder paritydesk could not read: check its permissions.

### The optional hint

`--legacy-state <path of the earlier script's state file>` lets the dry
run take the alias of the account in `Claude` from that file. It is a
hint only: when it disagrees with the folders, the folders win, and its
labels are never printed.

| Member | Accepted | Used for |
|---|---|---|
| `in_default` | a label of letters, digits, `.`, `_`, `-` (at most 48) | the alias of the account in `Claude`, and the name `Claude` gets when it is parked later |
| `active` | the same | a warning when it differs from `in_default` (the script's last switch did not finish; no sync bases are seeded then) |
| `layout_seeded` | an object of at most 64 members | checked, then ignored |

The file must be a regular file of at most 64 KiB, given as an absolute
path; a symbolic link is refused. Unknown members are ignored.

The command the dry run prints for use without a terminal never names
the state file: it carries the alias from the hint as `--legacy-alias
Claude=<alias>`, and `--legacy-no-seed` when the hint says the last
switch did not finish. It cannot carry the parked name the hint gives
`Claude`, so its plan hash is for the default parked name, and the dry
run says so. To keep the earlier script's name, run `--apply` in
Terminal with the same `--legacy-state`.

## 5. Step 2: leftovers of other accounts

An earlier script that swapped folders could leave behind directories of
one account inside another account's folder: session or skill directories
of another account, or empty directories of this account under another
account's organization. The dry run lists them under "Directories of
other accounts inside a folder" and says for each whether it can be moved
aside.

paritydesk only reports, and never moves:

- scratch workspaces (sessions may point into them);
- directories that hold data of this account under another organization;
- directories it cannot place: named by the folder's install id or by a
  short 8-character id. **Do not delete reported-only directories by
  hand**: some belong to the folder itself;
- directories holding sessions that exist nowhere else ("N of its entries
  are not in Claude-Work"). They are compared with the folder that holds
  that account and will keep it (`Claude` when it does), never with an
  old copy you excluded. A scheduled-tasks file counts unless it is empty
  or the owner folder holds the same file. Bring them home first: switch
  to that account and use Claude's own import from another folder, or,
  with Claude quit, copy the listed session files into the owner folder's
  same directory. Then run the dry run again;
- directories holding a file paritydesk never touches (for example the
  app's notes about scratch workspaces it still owes to remove), and
  anything that is not a directory.

To move the others, run the dry run's command in Terminal with `--apply
--quarantine` added and without `--legacy-plan` (it shows the plan and
always asks; quit Claude first for leftovers inside `Claude`). Right
before anything moves, paritydesk looks at each directory again and
keeps any that changed since the plan. The Terminal rule keeps a tool or
an agent from moving directories on its own by reading the hash from a
dry run; it is a safeguard, not a guarantee (something that drives a
real terminal can still answer), and every move stays restorable. They go into a pinned backup generation, which pruning never
removes. Check it with `paritydesk generations show <n>`, put everything
back with `paritydesk rollback <n>` (a bare `paritydesk rollback` never
picks it), and delete it for good later with `paritydesk backups purge`,
which warns first.

## 6. Step 3: apply

In Terminal:

```
paritydesk profile adopt --from-legacy --apply
```

It shows the plan, asks whether each new profile is managed by an
employer or client, and asks once more before registering. Without a
terminal, run the exact command the dry run printed (it ends with
`--legacy-plan <hash>`) with `--managed`, `--unmanaged` or
`--legacy-managed <alias>[,<alias>...]`; it registers profiles only and
never moves directories.

Afterwards `paritydesk status` and `paritydesk profile list` show the new
profiles. Exit code 32 means some leftovers were kept for now (Claude was
running, or a process used the folder): run the command again later.

## 7. What the first switch does

Values all folders agreed on sync normally. Everything else is merged:
nothing of it is deleted, and where copies differ the one changed last
wins. The first switch asks once. Sidebar sections, group-by settings and
custom groups often differ, because the earlier script never synced them
and each folder kept its own.

## 8. Step 4: retire the earlier script for good

Remove the script after the first paritydesk switch works. Two tools that
move the same folders can leave one account's data in another's folder.
If the script kept copies of your folders, they hold your data
unencrypted: restrict them with `chmod 700`, or delete them once
paritydesk works for you.

## 9. Step 5: plugin and first switch

Install the Claude Code plugin:

```
/plugin marketplace add kazupc/paritydesk
/plugin install paritydesk@paritydesk
```

Preview the first switch with `paritydesk plan`, then `paritydesk switch`.
For Claude in Chrome, use one Chrome profile per account (see the
README).

## 10. Undo

- `paritydesk profile remove <alias>` unregisters a profile; its folder is
  kept.
- `paritydesk rollback <n>` puts moved leftovers back (directories
  recreated since are left alone).
- `paritydesk rebase` forgets the seeded sync bases; the next switch is a
  first sync.

## 11. Troubleshooting

| Exit | Meaning | Next step |
|---|---|---|
| 2 | A flag or folder name was not understood, the state file was refused, or a choice needs a terminal | Read the message; run in Terminal or pass the flag it names |
| 3 | Declined, `--legacy-plan` missing, or the plan changed | "the folders changed since the dry run": run the dry run again; "the flags differ from the dry run's": use the command the dry run printed |
| 11 | Another paritydesk command is running | Wait, then run again |
| 14 | Another account-switching tool was detected | Remove it first |
| 20 | `Claude` holds no signed-in account, or a registered profile's folder holds another account | Open Claude, sign in, quit it, then run again |
| 32 | Registered, but some leftovers were kept for now or a marker could not be written | Run again later; the first switch of a profile without a marker merges without deleting |
| 41 | A run is open, a profile add is waiting, or `Claude` is missing | `paritydesk recover`; or finish the earlier script's switch so one folder is named `Claude` |

## 12. Flag reference

All flags are hidden: they are not listed in help or completion.

| Flag | Meaning |
|---|---|
| `--from-legacy` | Select this mode. No positional arguments. |
| `--apply` | Make the changes (default: dry run). |
| `--quarantine` | With `--apply`: also move the leftovers that can be moved. Needs a terminal. |
| `--legacy-state <file>` | Absolute path of the earlier script's state file (a hint only). |
| `--legacy-alias <folder>=<alias>` | Alias for a folder (`Claude` or a `Claude-*` folder). Repeatable. |
| `--legacy-exclude <folder>` | Leave a folder out. Repeatable. |
| `--managed`, `--unmanaged` | Every new profile is (not) managed by an employer or client. |
| `--legacy-managed <alias>[,<alias>...]` | These new profiles are managed, the others not. |
| `--legacy-plan <hash>` | The plan hash the dry run printed; required for `--apply` without a terminal. |
| `--legacy-no-seed` | Register only; seed no sync bases. |
