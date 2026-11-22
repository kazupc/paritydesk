// Package ids validates and masks the identifiers paritydesk handles:
// account and organization UUIDs, local session ids, profile aliases and
// parked-folder names. Every id read from disk or from a hook payload is
// untrusted and must pass these checks before it is used in a path, a URL
// or a log line.
package ids
