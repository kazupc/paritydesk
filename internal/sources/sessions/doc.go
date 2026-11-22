// Package sessions adapts the replica's session directory
// (claude-code-sessions/<acct>/<org>/): local_<id>.json records, deleted_<id>
// tombstones, archived-sessions.idx (derived from isArchived, always
// {"v":1,"archived":[sorted]}, never a hardlink), backlog/tasks.json,
// scheduled-tasks.json and waiting-input/*. App-written local_<id>.json.tmp
// files are read as candidate versions and quarantined after apply; no file
// paritydesk writes ever matches the app's tmp recovery globs (D30).
package sessions
