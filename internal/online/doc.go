// Package online is the online layer (ADR 0005): changes paritydesk can
// only make through the desktop app's own tools after a launch (P13
// reconcile), the read-only capture before a quit (P1), the PreToolUse
// guard and the PostToolUse recorder.
//
// The reconcile queue is derived, never authoritative: the orchestrator
// builds it from the merge outputs at P5 (Build), publishes it at P10
// (Publish) and the next Code session works through it with
// `paritydesk online next` and the /paritydesk:reconcile skill. Losing or
// dropping a queue loses nothing: the next merge recomputes it.
//
// Files live under <SupportDir>/online (0700 directories, 0600 files).
// Hooks only create files (results, captures, in-flight markers, denial
// records, SessionStart counters); only CLI commands under the online
// lock and the orchestrator replace queue.json, capture.json, lease.json
// and armed. Every model-visible string is a fixed catalog text or a
// validated value.
//
// Everything here is pure except store.go, fsguard.go, service.go and
// mono_*.go. Tests inject fsxtest and FakeMono.
package online
