// Package legacy is the importer behind profile adopt --from-legacy: it
// reads the folders an earlier folder-swap script left next to Claude (the
// account in use in Claude, other accounts parked as Claude-<Name>),
// derives aliases from their names, finds directories of other accounts
// left inside a folder, compares row values for the seeded sync bases, and
// builds the plan and its hash.
//
// Every input is untrusted: folder names, the optional state file of the
// earlier script (a hint only), and every directory name below a data
// folder. Nothing in this package writes; the orchestrator does every
// write under the run lock. No path is ever defaulted: the state file is
// read only when its absolute path is given.
package legacy
