// Package redact removes identifying data from logs, --json diagnostics and
// support output (M12): UUIDs are masked to 4 characters,
// $HOME paths become ~/<hash>, key names that embed ids have the id
// replaced by <id>. Emails are never read in the first place.
package redact
