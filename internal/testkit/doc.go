// Package testkit builds synthetic data roots for tests: Claude and
// Claude-* folders with config.json (fake ids, optional canary token
// strings), session directories, presets files, claude_desktop_config.json
// with an epitaxyPrefs mirror, git-worktrees.json, window-state.json, and
// real goleveldb Local Storage databases with Chromium-encoded keys and
// values. Shapes follow the 6.x manifest. No binaries are committed; every
// fixture is generated at test time.
//
// Only allow-listed fake UUIDs (FakeUUID) and no email-like strings may
// appear in fixtures (PII guard).
package testkit
