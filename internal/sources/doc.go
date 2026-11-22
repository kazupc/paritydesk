// Package sources is the only layer that knows the app's on-disk formats.
//
// Each row has an Adapter that reads a data folder into a normalized
// model.RowSnapshot and, given a desired snapshot, produces the literal-path
// plan.Edits that make the folder match it. Adapters do not write; edits
// are composed into plan.Ops by plan.Compose and executed by apply. For
// keys mirrored in claude_desktop_config.json the mirror is authoritative.
// Subpackages implement the adapters:
//
//	sessions/     rows sessions, tasks, waiting-input (records, tombstones,
//	              archived-sessions.idx, backlog, scheduled tasks)
//	presets/      row environments (LS presets + ccd-session-presets.json,
//	              session→environment map)
//	sidebar/      rows group-order, sidebar-view, pinned-order (LS and the
//	              config mirror, written together, ADR 0003)
//	prefs/        rows app-prefs and mcp-servers (claude_desktop_config.json)
//	worktrees/    row worktrees (git-worktrees.json)
//	windowstate/  row window-state
//	configjson/   folder identity (span scan of config.json)
//
// The registry that maps rows to adapters lives in sources/all to avoid an
// import cycle.
package sources
