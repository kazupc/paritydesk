// Package plan defines the typed operations a switch applies to the
// entering folder, the plan that groups them, its hash and summary, and
// the validator that every plan passes before anything is written (D18, phase P5).
//
// Adapters return literal-path Edits per location; Compose folds the edits
// of every row, in model.AllRows order, into exactly one Op per file or
// Local Storage key, so rows that share a file or a value never overwrite
// each other. Validate applies the fixed floor of package denylist on top
// of the manifest's lists.
//
// The same Plan type serves the approximate preview (from snapshot copies
// while the app runs) and the final plan (after the quit). Op contents may
// include secret values in memory; they are never persisted, logged or
// rendered (Content has no JSON encoding).
package plan
