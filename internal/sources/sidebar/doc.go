// Package sidebar adapts the sidebar rows that the app keeps in Local
// Storage and mirrors under preferences.epitaxyPrefs in
// claude_desktop_config.json: group order (code-projects-order.<acct>),
// sections and group-by (dframe-code-sections, opt-in), and pin order
// (dframe-local-slice pinnedOrder and dframe-store pinnedOrder). ADR 0003:
// a value written to Local Storage only was replaced from the mirror, so
// every key is written to both copies, edited by declared sub-paths only,
// and verified after launch.
//
// It also adapts collapsed groups and fork families (dframe-store only,
// not mirrored; written to Local Storage alone and verified from a Local
// Storage snapshot) and two legacy per-account sidebar keys (mirrored; the
// sort key written only to a folder whose sidebar scope is not seeded).
// ADR 0009.
package sidebar
