// Package artifacts keeps claude.ai artifacts the same on every
// paritydesk profile (beta, ADR 0006).
//
// A sync is a chain of rounds. Each round is an ordinary reconcile-phase
// queue of the online layer (internal/online), built from the archive by
// the pure PlanRound, driven by `online next` through the provider of
// this package, and ingested into the archive by the pure Ingest when the
// round queue is complete. Capture happens only from PostToolUse payloads
// of exactly queued calls (ParseResult); paritydesk never takes page
// content from the model.
//
// The archive lives under <SupportDir>/artifacts (0700 directories, 0600
// files): manifest v3, content-addressed blobs, per-profile inventories,
// the chain record, partial chunks between read rounds, a short-lived
// publish outbox and due.json for the SessionStart line.
//
// Everything here is pure except archive.go, outbox.go, legacy_read.go
// and service.go, which do I/O over injected dependencies.
package artifacts
