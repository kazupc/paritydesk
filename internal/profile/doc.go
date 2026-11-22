// Package profile identifies data folders and maps them to profiles.
//
// Identity comes from folder contents only (D4, I2): the account recorded
// in config.json (span scan, see sources/configjson) and the
// claude-code-sessions/<acct>/<org> directories. A folder's xattr marker and
// its (device, inode, birth time) tie it to the bases recorded in state; a
// mismatch makes those bases invalid, never a source of deletions (C1).
// profile also detects other account-switching tools (D28) and signs of
// the app's native multi-account mode.
package profile
