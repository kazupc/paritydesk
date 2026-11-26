package compat_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/model"
)

func replaceOnce(t *testing.T, s, old, repl string) string {
	t.Helper()
	if strings.Count(s, old) != 1 {
		t.Fatalf("%q occurs %d times", old, strings.Count(s, old))
	}
	return strings.Replace(s, old, repl, 1)
}

func TestPinsManifest7Validates(t *testing.T) {
	m, err := compat.Decode([]byte(text7(t)), "7.x.toml")
	if err != nil {
		t.Fatal(err)
	}
	r, ok := m.Row(model.RowPins)
	if !ok || r.WritePath != compat.PathOn || r.IDB == nil || !m.Online.Pins.CascadeUnpin || len(m.Online.Pins.FamilyFields) != 5 {
		t.Fatalf("pins row %+v online %+v", r, m.Online.Pins)
	}
	s := r.IDB.Reader()
	if s.Dir != r.IDB.Dir || s.BlinkMin != 19 || s.BlinkMax != 21 || s.V8Min != 13 || s.V8Max != 16 || s.MaxRead != 10*time.Second ||
		s.MaxCloneBytes != 2147483648 || s.Key != "store:pin-state:dframe-starred-code" {
		t.Fatalf("reader spec %+v", s)
	}
}

func TestPinsManifestRefusals(t *testing.T) {
	base := text7(t)
	cases := []struct {
		name, old, repl, want string
	}{
		{"missing [row.idb]", "[row.idb]\n# Which Code sessions", "[row.scratch]\nroot = \"x\"\n# Which Code sessions", ""},
		{"unanchored id_pattern", "id_pattern = '^local_", "id_pattern = 'local_", "id_pattern"},
		{"pattern misses the fake id", `id_pattern = '^local_[0-9a-f]{8}`, `id_pattern = '^local_[0-9a-f]{9}`, "id_pattern"},
		{"bad version range", "blink_versions = [19, 21]", "blink_versions = [21, 19]", "blink_versions"},
		{"dir outside IndexedDB", `dir = "IndexedDB/https_claude.ai_0.indexeddb.leveldb"`, `dir = "Local Storage/x"`, "idb.dir"},
		{"refusal shadows an ok text", `refusals_unpin = ["This session's section is hidden"]`, `refusals_unpin = ["Session"]`, "prefix"},
		{"ok text shadows a refusal", `ok_unpinned = ["Session unpinned.", "The session was already unpinned."]`,
			`ok_unpinned = ["Session unpinned.", "The session was already unpinned.", "This"]`, "prefix"},
		{"mirror key not never-touched", `  "*starred-local-code-sessions",` + "\n", "", "ls_never_touched"},
		{"limits out of range", "max_steps = 200", "max_steps = 5000", "max_steps"},
		{"family field not a path", `family_max_depth = 64`, `family_max_depth = 0`, "family_max_depth"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text := replaceOnce(t, base, c.old, c.repl)
			_, err := compat.Decode([]byte(text), "test.toml")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
	// A row whose mirror_paths overlap the pins mirror is refused.
	text := replaceOnce(t, base, "# v0.2 anchor: rows sidebar-offline\n", `# v0.2 anchor: rows sidebar-offline
`)
	text = strings.Replace(text, `mirror_paths = [`, `mirror_paths = [["preferences", "epitaxyPrefs"], `, 1)
	if _, err := compat.Decode([]byte(text), "test.toml"); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("overlapping mirror path: %v", err)
	}
}

func TestPinsOverrideRules(t *testing.T) {
	base := text7(t)
	cases := []struct {
		name, old, repl string
		ok              bool
	}{
		{"tighten a limit", "max_items = 5000", "max_items = 100", true},
		{"add a refusal", `refusals_unpin = ["This session's section is hidden"]`, `refusals_unpin = ["This session's section is hidden", "Not now"]`, true},
		{"cascade off", "cascade_unpin = true", "cascade_unpin = false", false},
		{"remove an ok text", `ok_pinned = ["Session pinned.", "The session was already pinned."]`, `ok_pinned = ["Session pinned."]`, false},
		{"remove unknown_outcome", `unknown_outcome = ["The app window didn't answer."]`, `unknown_outcome = ["The app window"]`, false},
		{"remove a family field", `"forkedFromSessionId", "startChain.firstSessionId"]`, `"forkedFromSessionId"]`, false},
		{"raise a limit", "max_read_ms = 10000", "max_read_ms = 20000", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "7.x.toml"), []byte(replaceOnce(t, base, c.old, c.repl)), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := compat.Load(dir)
			if (err == nil) != c.ok {
				t.Fatalf("err = %v, want ok %v", err, c.ok)
			}
		})
	}
}

func TestPins6xNotCarried(t *testing.T) {
	m, err := compat.Decode([]byte(embeddedText(t)), "6.x.toml")
	if err != nil {
		t.Fatal(err)
	}
	r, ok := m.Row(model.RowPins)
	if !ok || r.WritePath != compat.PathNone || r.IDB != nil {
		t.Fatalf("6.x pins %+v", r)
	}
	found := false
	for _, g := range m.Classification.LSNeverTouched {
		found = found || g == "*starred-local-code-sessions"
	}
	if !found {
		t.Fatal("6.x lacks the mirror glob")
	}
}
