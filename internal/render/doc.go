// Package render prints everything users see: human tables, --plain lines,
// --json documents and NDJSON progress. Every untrusted string (titles,
// group and preset names, paths) passes through Sanitize first (H7); the
// destructive counts are printed last, on a fixed line.
package render
