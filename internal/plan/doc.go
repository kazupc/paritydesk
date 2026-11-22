// Package plan defines the typed operations a switch applies to the
// entering folder, the plan that groups them, its hash and summary, and
// the validator that every plan passes before anything is written (D18, phase P5).
//
// The same Plan type serves the approximate preview (from snapshot copies
// while the app runs) and the final plan (after the quit). Op contents may
// include secret values in memory; they are never persisted, logged or
// rendered (Content has no JSON encoding).
package plan
