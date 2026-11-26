package policy

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/artifacts"
	"github.com/kazupc/paritydesk/internal/online"
	"github.com/kazupc/paritydesk/internal/pderr"
)

// TestCatalogAnchorsDocumented: every error-catalog entry links to an
// anchor that exists in docs/if-something-goes-wrong.md.
func TestCatalogAnchorsDocumented(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "docs", "if-something-goes-wrong.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	for _, e := range pderr.Catalog() {
		if e.Anchor == "" {
			continue
		}
		if !strings.Contains(doc, `<a id="`+e.Anchor+`"></a>`) {
			t.Errorf("%s: anchor %q missing from docs/if-something-goes-wrong.md", pderr.ID(e.Code), e.Anchor)
		}
		if !strings.Contains(doc, "("+pderr.ID(e.Code)+")") {
			t.Errorf("%s has no section heading", pderr.ID(e.Code))
		}
	}
}

// TestDocsExist: the documents the README and the error messages link to
// are present.
func TestDocsExist(t *testing.T) {
	for _, rel := range []string{
		"README.md", "PRIVACY.md", "SECURITY.md", "DISCLAIMER.md", "CONTRIBUTING.md", "CHANGELOG.md",
		"docs/architecture.md", "docs/if-something-goes-wrong.md", "docs/release-checklist.md",
		"docs/adr/0003-online-reconcile.md",
		"docs/migration-from-legacy.md",
	} {
		if _, err := os.Stat(filepath.Join(repoRoot(t), filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s: %v", rel, err)
		}
	}
}

// TestPrivacyListsHelpersAndHooks: PRIVACY.md names every program in
// internal/app/exec.go, every tool the plugin's PreToolUse and
// PostToolUse hooks receive, and the online and artifacts folders with
// their locks.
func TestPrivacyListsHelpersAndHooks(t *testing.T) {
	root := repoRoot(t)
	doc := mustRead(t, filepath.Join(root, "PRIVACY.md"))
	exec := mustRead(t, filepath.Join(root, "internal", "app", "exec.go"))
	bins := regexp.MustCompile(`(?m)^\s*bin\w+\s*=\s*"(/[^"]+)"`).FindAllStringSubmatch(exec, -1)
	if len(bins) == 0 {
		t.Fatal("no program constants found in internal/app/exec.go")
	}
	for _, b := range bins {
		if name := path.Base(b[1]); !strings.Contains(doc, "`"+name+"`") {
			t.Errorf("PRIVACY.md does not name the helper program %s", name)
		}
	}
	m := regexp.MustCompile(`^\^\(mcp__\(([^)]*)\)__\.\*\|(.*)\)\$$`).FindStringSubmatch(wantMatcher)
	if m == nil {
		t.Fatalf("matcher %q has an unexpected shape", wantMatcher)
	}
	var want []string
	for _, srv := range strings.Split(m[1], "|") {
		want = append(want, "`mcp__"+srv+"__*`")
	}
	for _, tool := range strings.Split(m[2], "|") {
		want = append(want, "`"+tool+"`")
	}
	want = append(want,
		"`~/Library/Application Support/paritydesk/"+online.DirName+"/`",
		"`~/Library/Application Support/paritydesk/"+artifacts.DirName+"/`",
		"`"+online.LockName+"`", "`"+artifacts.LockName+"`",
		"`"+online.FileQueue+"`", "`"+online.FileCapture+"`", "`"+online.FileLease+"`")
	for _, w := range want {
		if !strings.Contains(doc, w) {
			t.Errorf("PRIVACY.md does not mention %s", w)
		}
	}
}
