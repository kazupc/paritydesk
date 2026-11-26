// Package hook implements `paritydesk hook <event>`, the entry point of the
// Claude Code plugin's hooks (design D19, M11).
//
// SessionStart finishes in under 50 ms, prints nothing when nothing is
// pending, prints one fixed line (no untrusted strings) when a recover is
// needed, a sign-in or drift was found, or changes wait to be applied in
// Claude, stays silent outside the desktop app, and never fails the
// session (exit 0 on every error). PreToolUse is the guard of the online
// layer (ADR 0005): it denies, while a queue is armed for the session,
// every call on the guarded servers that is not an offered queue step,
// and never answers allow. PostToolUse records validated fields of the
// results of queued calls. Spike S5 showed both fire for the tools the
// app injects.
package hook
