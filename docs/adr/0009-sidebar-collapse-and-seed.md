# ADR 0009: Collapsed sidebar groups and the legacy sidebar seed keys

- Status: accepted (release checklist steps SO-1 to SO-5 pending)
- Date: 2026-11-26
- App version: 7.37095.0 (read from the app's code; the 6.x facts come
  from ADR 0003)

## Context

v0.1 carried the sidebar's group order, pin order and section view, but
left out two things the design listed: which sidebar groups and fork
families are collapsed (design row 11), and the per-account sidebar keys
the app reads when it sets up a sidebar scope for the first time (design
row 13). This ADR records what the app does with them on 7.37095.0 and
what paritydesk v0.2 carries. Every fact below was read from a read-only
copy of the app's code and, for shapes only, from a copy of one parked
data folder; no value, id or path from those copies is in the repository.

## Facts (app 7.37095.0)

### Collapsed groups and fork families

- The sidebar store in Local Storage (`dframe-store`, persisted version 1)
  holds three lists next to the v0.1 members: the collapsed groups, the
  collapsed fork families and the groups the person opened although they
  fold by default. All three start empty.
- On load the app keeps only the string elements of each list, in their
  order. paritydesk normalizes the same way and also sorts and
  deduplicates, so order and duplicates never count as a change.
- None of the three is sent to the server, and the store is not mirrored
  in `claude_desktop_config.json`. The store document also has a top-level
  `shared` member next to `state` and `version`; paritydesk never edits
  it.
- When an account's session ends in the app, including an account switch
  inside one folder, the app clears Local Storage and puts back a fixed list of device-level members
  of the store. The collapsed groups and fork families are on that list;
  the opened groups are not, and the app also empties them when it strips
  one account's slice of the store. So the first two are device-level and
  the opened groups are account-level.
- Group ids: built-in groups (pinned, recents, day buckets, routines and a
  few mode and section keys), custom groups (`custom-<id>`), project
  groups (`project-<key>`, `project-folder::<x>::<key>`,
  `project-preset::<environment id>`, `project-kind::<kind>`, and the
  fixed `project-default::`, `project-__scratch__`,
  `project-__no_project__`) and groups named by server ids. A project key
  is the lower-cased `owner/name` of a repository with an owner, else for
  a local session its working folder (an absolute path in practice), for
  a bridge session `<environment id>:<name>`, else the repository name.
  Fork families are `code:<session id>` and `open:code:<session id>`.
- Folding or expanding a group row writes the opened and the collapsed
  list together; expanding a group that folds itself puts it in the opened
  list and takes it out of the collapsed list. Deleting a custom group
  removes its member from the collapsed list.
- In Code mode with sections on, a custom group's fold comes from its
  section, not from the collapsed list; the collapsed list's custom
  members then matter only for seeding and for other modes.

### Sidebar sections

- Each Code section object holds `id`, `kind`, `name`, `order`, `shown`,
  `collapsed`, `groupBy`, `sortBy`, `ascending`, `v` and `show` (and
  `members` for some kinds). The app edits them one member at a time, and
  `show` member by member.
- A sort change that crosses the alphabetical sort without an explicit
  direction also resets `ascending`, so `sortBy` and `ascending` move
  together.
- A section's `collapsed` is reset to its kind's default before upload and
  taken from the local copy on pull: it is device-local.

### Legacy seed keys

- When a sidebar scope (`<account>/<organization>`) is not marked as set
  up, the app sets it up once from per-account inputs: the manual section
  order from the project order key, the Sessions sort direction from
  `code-sessions-sort-ascending.<account>`, the Sessions empty-group
  setting from `code-sessions-show-empty-projects.<account>`, the
  Sessions, Pinned and manual sections' fold from the collapsed list, and
  the Routines fold from IndexedDB. Then it marks the scope as set up. A
  scope already set up in the config mirror's copy is adopted as is.
- Each per-account key is read from the config mirror first, then from
  its Local Storage envelope.
- The sort key is read only by that set-up step. The empty-group key is
  also the live setting while the app's session-presets feature gate is
  off; the app's setter writes the Sessions section in both gate states
  and the key only while the gate is off. The gate can differ between
  accounts and cannot be read offline.
- Set-up runs after the config mirror has loaded and, inside the first
  server reconcile, before the server values are applied; for the active
  account and organization a server row with sections then replaces sort,
  group-by, direction and order. The empty-group setting is not in the
  server's field list.
- When an account's session ends in the app, it deletes both keys.

## Decisions

- **Collapsed groups and fork families are carried** by a new row
  `sidebar-collapse` (`sync.collapsed_groups`, on by default if the
  release checklist confirms it, see below). Each list is one item merged
  member by member against each folder's own base. While either base is
  invalid the result is the union of both sides: no member is ever
  removed. The row never deletes an item and never needs a first-sync
  confirmation. It is written to Local Storage only, inside the existing
  store, by editing just the changed list; the store is never created.
  Lists over 4096 members or with a member over 2048 bytes keep the
  target's own list. Members `project-preset::<id>` follow the
  environments remap both ways.
- **The opened groups are not carried**: the app treats them as
  account-level, so each folder keeps its own. Expanding a group that
  folds by default therefore does not carry.
- **Section members merge on their own.** The v0.1 sidebar-view row now
  merges `collapsed`, `name`, `shown`, `show` (member by member),
  `groupBy`, `order` and `members` per member, and the sort pair as one
  group taken whole from the side that changed it.
- **Seed keys** are a new row `sidebar-seed` (it follows
  `sync.sidebar_view`). The sort key is seed-only: it is written only to a
  folder whose sidebar scope for the entering account is provably not set
  up; an unreadable or unknown state counts as set up. The empty-group key
  is written to every folder. When the sidebar-view row of the same switch
  carries a Sessions section, both keys take that section's values
  (`ascending`, `show.emptyGroups`), so the seed and the live setting
  match the carried view in both gate states. A key absent on one side is
  never deleted on the other (the app deletes them itself when an
  account's session ends).
  The row is skipped when sidebar-view is not part of the run.
- **Never written:** the set-up and migration markers of a scope, the
  routines fold in IndexedDB, the store's backup copy.
- **P12 reads Local Storage rows from a snapshot copy.** The collapse row
  has no config mirror, so verification after launch reads it from a
  snapshot copy of the running app's Local Storage (no lock needed). Both
  new rows are contested: a difference after launch (the app rebuilt the
  store, the person folded a group) is reported as replaced, exit 0, and
  the base moves to what was observed. A snapshot that cannot be read
  lists the row as unverified, exit 0; the next switch compares it with
  the base.

### What the seed matches, and what it does not

On a folder whose scope is not set up, the Sessions sort direction and the
empty-group setting match the carried view after the switch. The Sessions
and Pinned fold are seeded from `recents` and `pinned` in the collapsed
list, which carries chat-mode folding rather than the Code sections' flags;
steering the seed through those members was rejected because it would
change chat-mode folding. Manual sections' fold comes from per-account
custom group ids, the manual order from the carried project order, and the
Routines fold from IndexedDB. Such differences are recorded as replaced on
sidebar-view (exit 0) and later switches do not fight them. The server
then replaces sort and order for the active account when its row has
sections, so the seeded direction matters only when it has none and for
scopes of other organizations.

### `sync.exclude` and the collapsed lists

Collapsed members can name a session (fork families) or a project (a
client's repository name or project path). Members of excluded sessions
and projects never leave their profile:

- a fork family member is private when its session id is excluded, and
  always while any glob is set (the member alone does not tell the
  session's project);
- a project member (other than the fixed and the preset and kind forms)
  is private, while any glob is set, when its project key after one
  `folder::<x>::` part is a path that a glob matches, or is not a path at
  all (an owner and name, a repository name, an environment and name):
  unknown counts as excluded;
- every other member is shared.

Private members never reach canonical; each folder keeps its own. Members
carried before an exclude was added are dropped from canonical but stay
in every folder that holds them.

## Unknowns and fallbacks

| Id | Unknown | Fallback |
|---|---|---|
| U1 | The 6.x code was not examined for the seed keys. | `6.x.toml` declares `sidebar-seed` as not carried. `sidebar-collapse` is declared on 6.x from ADR 0003 with the same probes, so a different shape skips the row. |
| U2 | No run yet shows that a written collapsed list survives launch on 7.x. | The row is contested and verified from a Local Storage snapshot; a difference is replaced, never drift. `sync.collapsed_groups` ships on only if release checklist step SO-1 passes, otherwise off. |
| U3 | The exact form of a local session's project key, and which built-in or server-id members hold account-specific parts. | Only a project key starting with `/` is treated as a path for `sync.exclude`; every other key is kept private while any glob is set. Server-id members are carried as inert strings: an id that matches nothing in the other account does nothing. |
| U4 | How often a target scope is not set up in practice. | The sort key is written only when the scope is provably not set up. |
| U5 | Whether a seed key ever holds something other than a boolean. | The probes skip the row; the adapter refuses to read it. |
| U6 | Whether the presets gate is on for an account. | Not needed: the empty-group key gets the carried section's value on every folder, right in both gate states. |

## Release checklist results

Steps SO-1 to SO-5 of the release checklist (section "Collapsed groups
and seed keys (v0.2)") have not been run yet. Record the results here.
If SO-1 fails, the release ships `sync.collapsed_groups = false`.

## Not covered

The opened groups, the whole-sidebar fold, the sidebar width and the other
device-level store members; the Routines fold in IndexedDB; mapping custom
groups or manual sections between accounts; the per-account environment
filter seeded from its older key (it holds environment ids and needs the
preset remap); the live per-account status filter, activity days, PR
status and chat-mode empty-group settings. Follow-ups recorded: the v0.1
group-order row does not yet remap `preset::<id>` entries or apply
`sync.exclude` to its project keys.
