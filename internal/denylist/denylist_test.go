package denylist

import "testing"

func TestMatchPath(t *testing.T) {
	tests := []struct {
		rel  string
		deny bool
	}{
		{"config.json", true},
		{"Cookies", true},
		{"cookies", true},
		{"Login Data", true},
		{"Local State", true},
		{"Network", true},
		{"Network/Cookies", true},
		{"IndexedDB/https_claude.ai_0.indexeddb.leveldb/000003.log", true},
		{"Local Storage/leveldb/000005.ldb", true},
		{"Session Storage", true},
		{"claude_desktop_config.json", false},
		{"claude-code-sessions/a/b/local_x.json", false},
		{"window-state.json", false},
		{"git-worktrees.json", false},
	}
	for _, tc := range tests {
		if got := MatchPath(tc.rel); got != tc.deny {
			t.Errorf("MatchPath(%q) = %v, want %v", tc.rel, got, tc.deny)
		}
	}
}

func TestMatchLSKey(t *testing.T) {
	for name, deny := range map[string]bool{
		"dframe-store":            false,
		"dframe-store.backup.v1":  true,
		"DFRAME-STORE.BACKUP.v2":  true,
		"code-projects-order.x":   false,
		"LSS-persisted.something": false,
		"oauthState":              true,
		"accessToken":             true,
		"SomeCredentialCache":     true,
	} {
		if got := MatchLSKey(name); got != deny {
			t.Errorf("MatchLSKey(%q) = %v, want %v", name, got, deny)
		}
	}
}

func TestMatchGlob(t *testing.T) {
	tests := []struct {
		pat, rel string
		want     bool
	}{
		{"a/**", "a", true},
		{"a/**", "a/b/c", true},
		{"**/x.json", "x.json", true},
		{"**/x.json", "d/e/x.json", true},
		{"local_*.json.tmp", "local_1.json.tmp", true},
		{"*/local_*.json.tmp", "local_1.json.tmp", false},
		{"[", "anything", true},
	}
	for _, tc := range tests {
		if got := MatchGlob(tc.pat, tc.rel); got != tc.want {
			t.Errorf("MatchGlob(%q, %q) = %v, want %v", tc.pat, tc.rel, got, tc.want)
		}
	}
}
