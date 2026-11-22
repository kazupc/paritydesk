// Package denylist is the fixed, non-overridable floor of things no plan
// may ever touch: credential and identity stores in a data folder, and
// Local Storage keys that look token-bearing. plan.Validate applies it on
// top of the compat manifest's never_touched list, so neither a
// user-supplied manifest override nor an adapter bug can remove it.
//
// Entries may be added; removing one needs a reviewed change by the lead.
// The names here are generic Chromium and Electron file names, not
// anything taken from the app bundle.
package denylist
