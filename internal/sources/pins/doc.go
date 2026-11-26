// Package pins is the adapter of row pins: which Code sessions are pinned
// in a data folder. It reads the app's pin list from IndexedDB through an
// idb.Reader (a private clone in production) and, where the spec allows,
// the app's own mirror of that list (claude_desktop_config.json and Local
// Storage). It never writes anything: pins are applied only through the
// app's set_pinned tool by the online layer, so Ops always returns no
// edits.
//
// An unreadable list is reported as *Unavailable (errors.Is
// sources.ErrSourceUnavailable) with a stable reason; the orchestrator
// then skips only this row. The adapter never logs, returns or stores the
// list's JSON text, only ids that match the manifest's id pattern.
package pins
