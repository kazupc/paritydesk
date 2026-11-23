// Package hook implements `paritydesk hook <event>`, the entry point of the
// Claude Code plugin's hooks (design D19, M11).
//
// v0.1 ships SessionStart only: it finishes in under 50 ms, prints nothing
// when nothing is pending, prints one fixed line (no untrusted strings)
// when a recover is needed or the last verify found drift or a sign-in,
// stays silent outside the desktop app, and never fails the session (exit
// 0 on every error). PreToolUse (the guard) and PostToolUse (captures) are
// v0.2; spike S5 showed both fire for the tools the app injects.
package hook
