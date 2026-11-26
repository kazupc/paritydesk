package compat_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/compat"
)

func TestLegacySectionOfBothManifests(t *testing.T) {
	ms, err := compat.Load("")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		l := m.Legacy
		if !reflect.DeepEqual(l.IgnoreNames, []string{".DS_Store"}) ||
			!reflect.DeepEqual(l.AgreeIgnoringRoles, []string{"account_bound", "grant", "run_state", "resume_mark"}) {
			t.Errorf("%s: legacy %+v", m.Name, l)
		}
		if len(l.Stubs) != 1 || l.Stubs[0].File != "scheduled-tasks.json" || len(l.Stubs[0].EmptyMembers) != 4 || len(l.Stubs[0].FlagMembers) != 2 {
			t.Errorf("%s: stubs %+v", m.Name, l.Stubs)
		}
		want := map[string]struct {
			root, layout string
			quarantine   bool
		}{
			"code-sessions":      {"claude-code-sessions", compat.LayoutAcctOrg, true},
			"agent-sessions":     {"local-agent-mode-sessions", compat.LayoutAcctOrg, true},
			"agent-skills":       {"local-agent-mode-sessions/skills-plugin", compat.LayoutOrgAcct, true},
			"scratch-workspaces": {"scratch-workspaces", compat.LayoutAcctOrg, false},
		}
		for _, d := range l.ScopedDirs {
			w, ok := want[d.ID]
			if !ok || d.Root != w.root || d.Layout != w.layout || d.Quarantine != w.quarantine {
				t.Errorf("%s: scoped dir %+v", m.Name, d)
			}
			delete(want, d.ID)
		}
		if len(want) != 0 {
			t.Errorf("%s: missing scoped dirs %v", m.Name, want)
		}
	}
	// The manifest text names no home-relative path.
	for _, n := range []string{"6.x", "7.x"} {
		b, err := os.ReadFile(filepath.Join("manifests", n+".toml"))
		if err != nil {
			t.Fatal(err)
		}
		i := strings.Index(string(b), "legacy migration")
		if i < 0 || strings.Contains(string(b[i:]), "Library/") || strings.Contains(string(b[i:]), "~/") {
			t.Errorf("%s: legacy section missing or names a home path", n)
		}
	}
}

// legacyText returns the 6.x manifest with sec as its [legacy] section.
func legacyText(t *testing.T, sec string) []byte {
	t.Helper()
	return []byte(withoutLegacy(t, embeddedText(t)) + sec)
}

func TestLegacyValidationRefuses(t *testing.T) {
	dir := func(root, layout string, quarantine bool) string {
		q := "false"
		if quarantine {
			q = "true"
		}
		return "\n[[legacy.scoped_dir]]\nid = \"x\"\nroot = \"" + root + "\"\nlayout = \"" + layout + "\"\nquarantine = " + q + "\n"
	}
	cases := map[string]string{
		"scratch quarantinable":       dir("scratch-workspaces", "{acct}/{org}", true),
		"IndexedDB":                   dir("IndexedDB", "{acct}/{org}", true),
		"Local Storage":               dir("Local Storage", "{acct}/{org}", true),
		"Network below the deny":      dir("Network", "{acct}/{org}", true),
		"placeholder root":            dir("claude-code-sessions/{acct}/{org}", "{acct}/{org}", true),
		"replica root":                dir("claude-code-sessions/00000000-0000-4000-8000-000000000021/00000000-0000-4000-8000-000000000022", "{acct}/{org}", true),
		"absolute root":               dir("/tmp/x", "{acct}/{org}", false),
		"dotdot root":                 dir("a/../b", "{acct}/{org}", false),
		"unknown layout":              dir("claude-code-sessions", "{acct}", false),
		"duplicate id":                dir("a", "{acct}/{org}", false) + dir("b", "{acct}/{org}", false),
		"unknown role":                "\n[legacy]\nagree_ignoring_roles = [\"nonsense\"]\n",
		"stub with a path":            "\n[[legacy.stub]]\nfile = \"a/b.json\"\n",
		"ignore name with a path":     "\n[legacy]\nignore_names = [\"a/b\"]\n",
		"skip name with a path":       "\n[[legacy.scoped_dir]]\nid = \"x\"\nroot = \"a\"\nlayout = \"{acct}/{org}\"\nskip_names = [\"../x\"]\n",
		"never until classified root": dir("git-shadow", "{acct}/{org}", true),
	}
	for name, sec := range cases {
		if _, err := compat.Decode(legacyText(t, sec), "test.toml"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	ok := map[string]string{
		"absent section":       "\n",
		"report-only scratch":  dir("scratch-workspaces", "{acct}/{org}", false),
		"quarantinable skills": dir("local-agent-mode-sessions/skills-plugin", "{org}/{acct}", true),
		"quarantinable roots":  dir("claude-code-sessions", "{acct}/{org}", true),
	}
	for name, sec := range ok {
		if _, err := compat.Decode(legacyText(t, sec), "test.toml"); err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
}

// A hostile override that makes IndexedDB quarantinable is refused at load.
func TestLegacyHostileOverrideRefused(t *testing.T) {
	d := t.TempDir()
	writeOverride(t, d, "6.x.toml", withoutLegacy(t, embeddedText(t))+
		"\n[[legacy.scoped_dir]]\nid = \"idb\"\nroot = \"IndexedDB\"\nlayout = \"{acct}/{org}\"\nquarantine = true\n")
	if _, err := compat.Load(d); err == nil {
		t.Fatal("override accepted")
	}
}
