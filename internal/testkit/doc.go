// Package testkit builds synthetic data roots for tests: Claude and
// Claude-* folders with config.json (fake ids, optional canary token
// strings), session directories (plus other accounts' decoy directories),
// presets files, claude_desktop_config.json with an epitaxyPrefs mirror,
// git-worktrees.json, window-state.json, files the app keeps that
// paritydesk never touches, and real goleveldb Local Storage databases
// with Chromium-encoded keys and values (dframe-store with its backup
// copy, the per-org presets item). Member names and nesting follow the
// read-only survey of app 6.28899.2 (ADR 0004); values are synthetic. No
// binaries are committed; every fixture is generated at test time.
//
// Only allow-listed fake UUIDs (FakeUUID) and no email-like strings may
// appear in fixtures (PII guard).
package testkit
