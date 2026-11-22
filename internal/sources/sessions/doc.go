// Package sessions adapts the replica's own session directory
// (claude-code-sessions/<acct>/<org>/): local_<id>.json records,
// deleted_<id> tombstones (a bare JSON number of epoch ms), and
// archived-sessions.idx (derived from isArchived, always
// {"v":1,"archived":[sorted]}, never a hardlink); backlog/tasks.json
// ({"version":1,"items":[...]}), scheduled-tasks.json and the
// waiting-input/ directory. Other accounts' directories under
// claude-code-sessions are never read or written. App-written
// local_<id>.json.tmp files are read as candidate versions and quarantined
// after apply; no file paritydesk writes ever matches the app's tmp
// recovery globs (D30).
package sessions
