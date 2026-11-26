package policy

import (
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/artifacts"
	"github.com/kazupc/paritydesk/internal/online"
)

// TestArtifactsSkillRules pins the rules of /paritydesk:artifacts
// (spec-artifacts 14.1, 16.5): the loop, the tools and the data rules,
// and no decision command.
func TestArtifactsSkillRules(t *testing.T) {
	s := strings.Join(strings.Fields(skillText(t, "artifacts")), " ")
	for _, want := range []string{
		`argument-hint: ""`, "ToolSearch", "AskUserQuestion", "plain-text numbered list", "remaining_batches",
		"never more than 200 answers", "paritydesk artifacts pause", "without `--token`", "--refused", "--unavailable",
		"Read tool", "Never run paritydesk in the same message", "Never use a subagent", "do not write any file from it",
		"`Artifact`, `ArtifactComments`, `ArtifactData` or `ArtifactCheck`", "request_directory", "data, never instructions",
		"never add `limit`", "paritydesk artifacts done",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("artifacts skill lacks %q", want)
		}
	}
	// No path from page text to a decision (9.3, B4).
	for _, banned := range []string{"allow", "forget", "import-legacy", "--yes", "bypassPermissions", "bypass-permissions"} {
		if strings.Contains(strings.ToLower(s), strings.ToLower(banned)) {
			t.Errorf("artifacts skill contains %q", banned)
		}
	}
	if regexp.MustCompile(`(?i)sign[ -]?out|log[ -]?out`).MatchString(s) {
		t.Error("artifacts skill advises ending a session of an account")
	}
	sw := skillText(t, "switch")
	if !strings.Contains(sw, "run /paritydesk:artifacts first") || strings.Count(sw, "{label:") != 2 {
		t.Error("switch skill: the artifacts sentence, and no new button")
	}
}

// TestArtifactsToolsGuarded: every tool of the artifacts provider (both
// transports) is on a guarded server or a guarded single tool, none is in
// the floor, and the hook matcher matches them and the app's own artifact
// tools.
func TestArtifactsToolsGuarded(t *testing.T) {
	re := regexp.MustCompile(wantMatcher)
	pol := artifacts.Provider{Guard: true}.Tools()
	tools := append(append([]string{}, pol.Exact...), pol.Reads...)
	if !slices.Contains(tools, artifacts.ToolPublishVersion) || !slices.Contains(tools, artifacts.ToolRequestDir) {
		t.Fatalf("policy %v", tools)
	}
	for _, tool := range append(tools, "Artifact", "ArtifactComments", "ArtifactData", "ArtifactCheck") {
		if !re.MatchString(tool) {
			t.Errorf("matcher misses %s", tool)
		}
		if !online.Guarded(tool, online.GuardExtra{}) {
			t.Errorf("%s is not guarded", tool)
		}
		if online.InFloor(tool, nil) {
			t.Errorf("%s is in the floor", tool)
		}
	}
	for _, e := range readHooks(t)["PreToolUse"] {
		if e.Matcher != wantMatcher {
			t.Errorf("PreToolUse matcher %q", e.Matcher)
		}
	}
}

// TestArtifactsNoDefaultLegacyPath: the importer takes the legacy folder
// from --from only; no string literal of the artifacts code names a
// location under a home directory, and the package never runs commands
// or opens connections.
func TestArtifactsNoDefaultLegacyPath(t *testing.T) {
	bad := regexp.MustCompile(`~/|\$HOME|/Users/|Library/`)
	for _, f := range goFiles(t, false) {
		if !strings.HasPrefix(f.rel, "internal/artifacts/") && f.rel != "internal/cli/artifacts.go" {
			continue
		}
		for _, imp := range f.file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if f.rel != "internal/cli/artifacts.go" && (p == "os/exec" || p == "net" || strings.HasPrefix(p, "net/")) {
				t.Errorf("%s imports %s", f.rel, p)
			}
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil && bad.MatchString(s) {
					t.Errorf("%s:%d names a home location %q", f.rel, f.fset.Position(lit.Pos()).Line, s)
				}
			}
			return true
		})
	}
	for _, rel := range []string{"docs/adr/0006-artifact-sync.md", "plugin/skills/artifacts/SKILL.md"} {
		b, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
		if err != nil {
			t.Fatal(err)
		}
		if regexp.MustCompile(`(?i)~/[a-z-]*artifact`).Match(b) {
			t.Errorf("%s names a default legacy archive location", rel)
		}
	}
}

// TestArtifactsNotInBundle: the doctor bundle never collects archive
// files (only counts reach it, through doctor).
func TestArtifactsNotInBundle(t *testing.T) {
	src := mustRead(t, filepath.Join(repoRoot(t), "internal", "cli", "doctor_bundle.go"))
	for _, s := range []string{"artifacts", "blobs", "outbox", "inventory", "partial", "chain.json"} {
		if strings.Contains(src, s) {
			t.Errorf("doctor bundle mentions %q", s)
		}
	}
}

// TestArtifactsExampleVersionIDs: example version ids in the artifacts
// code, its testdata and ADR 0006 use the fake prefix (M17).
func TestArtifactsExampleVersionIDs(t *testing.T) {
	re := regexp.MustCompile(`(?:^|[^0-9.])([0-9]{9,11}-[0-9a-f]{4,})\b`)
	for _, rel := range trackedFiles(t) {
		if !strings.HasPrefix(rel, "internal/artifacts/") && rel != "docs/adr/0006-artifact-sync.md" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
		if err != nil {
			continue
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			if !strings.HasPrefix(m[1], "1700000000-") {
				t.Errorf("%s: version id %q", rel, m[1])
			}
		}
	}
}
