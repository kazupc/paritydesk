// Package apply executes a validated plan against the entering folder
// (phase P7). It is, with fsx, gen and state, the only code that
// writes; it refuses everything in read-only mode.
//
// Order: re-check that nothing holds the folder; a verification pass
// checks every file op's pre-hash, then opens Local Storage writable and
// checks every LS op's pre-hash, before anything is written; then, for
// every file op (re-checking its pre-hash just before the write), write
// through fsx.Ops.WriteFileAtomic (temp files in
// <data_root>/.paritydesk-tmp/<run>/, D30) or quarantine into the
// generation; then one synced Local Storage batch for all LS ops, close,
// and read back every written key through a snapshot; then one full sync
// of the data root. Any error after
// the writable open is returned as *pderr.Error so the orchestrator can
// restore the LevelDB clone and roll files back.
package apply
