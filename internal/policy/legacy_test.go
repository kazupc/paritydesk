package policy

import (
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// legacyHiddenFlags are the hidden flags of profile adopt --from-legacy.
var legacyHiddenFlags = []string{"from-legacy", "legacy-state", "legacy-alias", "legacy-exclude", "legacy-managed",
	"legacy-plan", "legacy-no-seed", "quarantine"}

// TestLegacyFlagsInNoGolden: the hidden flags appear in no usage and no
// completion golden (spec-legacy-adopt 4.9 a).
func TestLegacyFlagsInNoGolden(t *testing.T) {
	root := repoRoot(t)
	for _, dir := range []string{"internal/cli/testdata/usage", "internal/cli/testdata/completion"} {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			b, err := os.ReadFile(filepath.Join(root, dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range legacyHiddenFlags {
				if strings.Contains(string(b), "--"+f) || strings.Contains(string(b), "-l "+f+" ") {
					t.Errorf("%s/%s lists the hidden --%s", dir, e.Name(), f)
				}
			}
		}
	}
}

// TestLegacyNoHomePaths: no string literal of the legacy importer names a
// home-relative or app location, so no earlier script's path can be
// defaulted (spec-legacy-adopt 4.9 b).
func TestLegacyNoHomePaths(t *testing.T) {
	bad := []string{".claude/", "~/", "Library/"}
	seen := 0
	for _, f := range goFiles(t, false) {
		if !strings.HasPrefix(f.rel, "internal/legacy/") && f.rel != "internal/orchestrator/legacy.go" &&
			f.rel != "internal/orchestrator/legacy_recover.go" {
			continue
		}
		seen++
		ast.Inspect(f.file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				s = lit.Value
			}
			for _, b := range bad {
				if strings.Contains(s, b) {
					t.Errorf("%s: %s holds %q", f.rel, f.fset.Position(lit.Pos()), b)
				}
			}
			return true
		})
	}
	if seen < 5 {
		t.Fatalf("only %d legacy files checked", seen)
	}
}

// TestLegacyDocsCovered: the migration page is one of the files the PII
// deny-list test reads (spec-legacy-adopt 4.9 c), and it names no fixed
// location of an earlier script.
func TestLegacyDocsCovered(t *testing.T) {
	found := false
	for _, tf := range textFiles(t, nil) {
		if tf.rel != "docs/migration-from-legacy.md" {
			continue
		}
		found = true
		for _, b := range []string{"Library/Application Support/Claude-", ".claude/"} {
			if strings.Contains(string(tf.data), b) {
				t.Errorf("the migration page names %q", b)
			}
		}
	}
	if !found {
		t.Fatal("docs/migration-from-legacy.md is not among the checked text files")
	}
}
