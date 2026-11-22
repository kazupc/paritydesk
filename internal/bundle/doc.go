// Package bundle writes the support archive of `paritydesk doctor
// --bundle <file>`: a gzip-compressed tar file, mode 0600, that holds only
//
//   - README.txt: a fixed text that lists the files and the redaction;
//   - version.json: paritydesk's version, commit and build date, the Go
//     version, the OS and the CPU architecture;
//   - doctor.json: the doctor checks, redacted again;
//   - config.json: paritydesk's own config with aliases, labels, folder
//     names, exclude entries, secret names and custom paths replaced;
//   - manifests.json: the embedded compatibility manifests' names and
//     version ranges, and the names and sha256 of override files;
//   - logs/<n>-<run>.jsonl: the newest run logs, field by field, with
//     every message redacted again.
//
// The caller gathers the inputs; this package never opens a file other
// than the archive it writes, and in particular nothing in a Claude data
// folder and no line of Claude's main.log.
//
// Redaction is applied here, on top of the redaction the inputs already
// carry, so a bug upstream cannot leak through the bundle: paths under
// the home directory become ~/<hash>, any other path becomes
// <path:hash> unless it is a system location, UUIDs are masked to four
// characters, long hex runs become <id>, quoted text becomes "<text>",
// email-like strings become <email>, profile aliases become profile-N and
// parked folder names become Claude-profile-N. PRIVACY.md lists the same
// rules for users.
package bundle
