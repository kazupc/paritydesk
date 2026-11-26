# ADR 0006: Artifact sync (beta)

Status: accepted for v0.2 as **capture only (beta)**: the embedded manifest
ships `[artifacts] publish_via = "off"` until release step R-AR-4 has
passed for one publish transport on a real Mac.

## Context

claude.ai artifacts belong to an account. A person with several Claude
accounts wants the same pages on each of them. paritydesk switches the
desktop app between accounts by swapping data folders; artifacts are not
in those folders, they live on the server and are reached only through
Claude's own tools inside a Code session. The online layer (ADR 0005)
already lets paritydesk queue exact tool calls, guard every other call of
the guarded servers while a queue is armed, and record results from the
PostToolUse hook payload.

## Facts (app 7.37095.0, read in a read-only copy of the app's code)

Evidence is cited by fact, not by code: the app's JavaScript is minified
and its chunk names change with every build.

- **B1** The artifacts server is `claude-artifacts`; its tools are
  `mcp__claude-artifacts__<name>`. The app sorts them into publish
  (`publish_version`, `edit_version`, `publish_files`, `copy_file`,
  `create_from_type`, `declare_capabilities`, `authorize_upload`), read
  (`get_artifact`, `read_artifact`, `read_shared_artifact`,
  `read_declaration`), list, files, delete, database and guide classes;
  an unknown name is treated like a delete.
- **B2** A publish result is a string or text blocks; when the first text
  starts with `<artifact-content-authored-by-others/>` the JSON is in the
  second text; the JSON carries `url` and optionally `slug`.
- **B3** The app's own PostToolUse hook for this server reads the same
  `tool_response` shapes; subagent payloads carry `agent_id`.
- **B4** After each successful publish the app opens the artifact in its
  preview pane unless `auto_open` says otherwise; `publish_files` has no
  `auto_open`, so each paritydesk publish may open a pane.
- **B5** Artifact page URLs have the form
  `https://<host>/{code/artifact|code/frame|artifact}/[<vanity>-]<uuid or 22-character base58 id>`.
- **B6** Every `publish_files` call is expected to show an approval card;
  the app's special per-call handling covers other publish tools only.
- **B7** `claude-artifacts` is a remote HTTP MCP server: tool schemas and
  result texts come from the server, not from the app, and can change
  without an app update.
- **B8** The app's local artifact service wraps document text in a
  per-call `<untrusted-document-…>` tag and serves large documents in
  parts by line offset; this supports, but does not prove, the shape of
  the server's `read_artifact` results.
- **B9** The app also offers non-MCP tools `Artifact`, `ArtifactComments`,
  `ArtifactData` and `ArtifactCheck` that map onto the same operations
  (deletes, database writes, publishes). Hooks fire for them. Which family
  a Code session offers depends on flags.
- **B10** File publishing is limited to the session's folders; a folder
  outside them must be granted with `mcp__ccd_directory__request_directory`
  (one approval card). The app tells the model not to request system or
  application-internal folders.
- **B11** For `publish_files` the client fills `files[].uri` as
  `artifact-file://<host>/<sha256>` before approval and re-hashes the file
  at approval time; a changed file is refused.
- **B12** Hook and Bash processes of Claude carry `CLAUDECODE=1` and
  `CLAUDE_CODE_SESSION_ID`.
- **B13** The app's text for `publish_files` publishes only to the user's
  own, non-public artifacts and tells the model to read a file before
  publishing it.

Tool schemas of a Code session of the same version (schema text only, no
artifact tool was called): **T1** `read_artifact` (slug, line offset and
limit; a JSON summary with slug, version, title, total_lines, a notice
naming the wrapper tag, then the wrapped HTML, then a reminder; large
documents in parts with `next_offset`), **T2** `list_artifacts` (paged,
cursor, entries with slug, title, version, own or shared, change time,
`soft_deleted`), **T3** `get_artifact` (version, title, icon, read mode
owner/users/org/public, URL), **T4** `publish_files` (`files[]` of
`{as, path, uri}`, `slug`, `base_version`, `title`, `icon`, …;
`additionalProperties: false`), **T5** `publish_version`/`edit_version`
(content up to 16 MiB starting with `<!DOCTYPE html>`), **T6**
`list_files`, **T7** `read_declaration` (declared capabilities; another
author's declarations come wrapped), **T8** deletes, capability
declarations, database tools and sharing are in the online layer's fixed
floor.

## Decision

- **Rounds of the online layer.** A sync is a chain of rounds; each round
  is an ordinary reconcile-phase queue built from the archive by a pure
  planner (inventory, inspect, read, publish, verify), driven by
  `paritydesk artifacts next` and ingested into the archive when it is
  complete. Only list pages are templated (cursor). The skill
  `/paritydesk:artifacts` makes the calls; a SessionStart line offers it.
- **Capture only from hook payloads** of exactly queued calls, parsed by
  the artifacts provider (`ResultParser`); raw text is never stored, only
  validated fields and, for reads of the user's own artifacts, the page
  body. paritydesk never takes page content from the model.
- **Account proof.** Rounds start and are ingested only while Claude's log
  shows the active account and organization equal to the chain's; an
  account-change line after the newest org line makes the account unknown
  (ADR 0007). A list that names another profile's artifact aborts the
  chain.
- **Archive** under `<SupportDir>/artifacts` (0700/0600): manifest v3,
  content-addressed blobs (`.html.txt`, never rendered by Finder), per
  profile inventories (title keys only), the chain, partial parts between
  read rounds, a publish outbox and `due.json`. The previous three
  manifests are kept.
- **Newest change wins** per pair, for content and title; the losing
  version goes to history with its title. Version ids and title keys
  decide change, so a copy read back differently cannot ping-pong.
- **Publish** from a staged outbox copy (`publish_files` with a path in a
  granted folder), so content never passes through paritydesk's calls;
  `publish_version` with a hashed input is the fallback transport. The
  guard compares inputs exactly after removing only a client `uri` whose
  hash is the staged file's.
- **Round-trip gate.** The first publish of every chain is read back and
  compared byte for byte; a page that comes back wrapped (the suspected
  double wrapping of an earlier tool) or otherwise different closes
  publishing for that app version; only the title element differing is a
  per-artifact result.
- **Never deletes.** A deletion on one account is recorded and reported,
  never propagated; a copy is created again only after `paritydesk
  artifacts allow <id> --restore` in Terminal.
- **Skipped and remembered**: multi-file, type-made and live-document
  artifacts, artifacts with declared capabilities (until allowed for that
  declaration), and artifacts another person wrote to.
- **Decisions in Terminal only.** `allow`, `forget` and `import-legacy
  --apply` refuse to run inside Claude, even with `--yes`.
- **Data boundary.** `sync.export`/`sync.import` accept the category
  `artifacts`; content does not cross a managed profile's boundary unless
  `artifacts.cross_managed` is set. The boundary is checked for every
  publish, including a conflict the archive side won. Content whose source
  profile is no longer configured (or was removed) counts as managed and
  not exported, so removing a work profile never releases its pages.
- **Online layer.** Artifact rounds are queues of the online layer, so
  `online.reconcile = "off"` (which turns that layer off) also turns
  artifact sync off, with its own line naming that key. Profile rename and
  remove still update the archive while artifact sync is off.
- **Legacy import.** `paritydesk artifacts import-legacy --from <dir>`
  reads the artifact archive of an earlier folder-swap script (formats 1
  and 2) from an explicit folder only, treats every byte as untrusted,
  never reads its labels, maps accounts only to a profile's current
  account (or `--map`), imports wrapped pages without content, and marks
  every imported copy unverified until the first sync of its profile
  confirms it.

## Not verifiable here (safe fallbacks)

| Unknown | Fallback | Release step |
|---|---|---|
| Result field names and the reminder's position | Manifest `[artifacts.fields]` lists, `reminder_prefixes`; anything else is `unrecognized` at once | R-AR-1 |
| Whether the hook payload carries a whole large part | A cut part fails the exact line count; nothing partial is saved | R-AR-2 |
| Newlines around the wrapper, the final-newline count | `wrapper_forms`; the round-trip gate | R-AR-3 |
| Whether the outbox grant and path are accepted | `publish_via = "off"` until one transport passes | R-AR-4 |
| Refusal texts | `[artifacts.refusals]`, empty until observed; unmatched errors are `app_refused` | R-AR-5 |
| A new org line after an in-app account change | Account unknown, sync blocked | R-AR-10 |

## Consequences

- Artifact sync needs the plugin and runs only when the person uses
  `/paritydesk:artifacts`; a switch never carries artifacts (`--only
  artifacts` is refused). The plan tells the person when the leaving
  profile was not synced since it became active, and how many artifacts
  wait for the entering one.
- Each publish needs approval cards and may open a preview pane.
- Accepted risk: a model that runs arbitrary shell commands could still
  run decision commands with a changed environment; no paritydesk text
  suggests it.
