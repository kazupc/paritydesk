// Package presets adapts machine environments: Local Storage
// persisted.ccd-session-presets.<acct> (persisted version 4) and its file
// mirror ccd-session-presets.json byAccount[<acct>] (mode 0600), read with
// the mirror first when state.fileMirrored is true (the Local Storage copy
// otherwise) and written together. While the two copies disagree, presets
// are not written at all and each change is reported skipped, so neither
// copy overwrites the other. Also the session to environment
// map persisted.ccd-started-environments (version 1). The per-org item
// persisted.ccd-session-presets.<acct>.<org> is never read or written.
// Env-var values are secret: snapshots carry their hashes only and Ops
// copy the raw bytes from the source folder (D27).
package presets
