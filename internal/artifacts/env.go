package artifacts

// InClaude reports whether the process runs inside Claude (9.3): the
// app's hook and Bash environment carries CLAUDECODE, Claude Code
// sessions carry CLAUDE_CODE_SESSION_ID, and the CLI sets
// CLAUDE_CODE_ENTRYPOINT. Decision leaves refuse to run inside Claude;
// the leaves the skill uses refuse to run outside it.
func InClaude(getenv func(string) string) bool {
	if getenv == nil {
		return false
	}
	for _, k := range []string{"CLAUDECODE", "CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_ENTRYPOINT"} {
		if getenv(k) != "" {
			return true
		}
	}
	return false
}
