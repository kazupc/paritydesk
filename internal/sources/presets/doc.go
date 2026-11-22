// Package presets adapts machine environments: Local Storage
// persisted.ccd-session-presets.<acct> (the truth) and
// ccd-session-presets.json byAccount[<acct>] (mode 0600), written together,
// plus the session→environment map persisted.ccd-started-environments.
// Env-var values are secret: snapshots carry their hashes only and Ops copy
// the raw bytes from the source folder (D27).
package presets
