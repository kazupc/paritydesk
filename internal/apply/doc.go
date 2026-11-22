// Package apply executes a validated plan against the entering folder
// (phase P7). It is, with fsx, gen and state, the only code that
// writes; it refuses everything in read-only mode.
//
// Order: re-check that nothing holds the folder; for every file op, verify
// the pre-hash, then write through fsx.Ops.WriteFileAtomic (temp files in
// <data_root>/.paritydesk-tmp/<run>/, D30) or quarantine into the
// generation; then one synced Local Storage batch for all LS ops (after
// checking each value's pre-hash), close, and read back every written key
// through a snapshot; then one full sync of the data root. Any error after
// the writable open is returned as *pderr.Error so the orchestrator can
// restore the LevelDB clone and roll files back.
package apply
