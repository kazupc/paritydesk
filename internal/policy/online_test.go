package policy

import (
	"encoding/json"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/hook"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/online"
)

// hooksFile is the plugin's hook registration.
const hooksFile = "plugin/hooks/hooks.json"

// wantMatcher is the PreToolUse and PostToolUse matcher: exactly the
// guarded servers and the guarded single tools (spec-online 10.2, A12).
const wantMatcher = `^(mcp__(ccd_sidebar|ccd_session_mgmt|ccd_settings|claude-artifacts|ccd_directory)__.*|Artifact|ArtifactComments|ArtifactData|ArtifactCheck)$`

type hookEntry struct {
	Matcher string `json:"matcher"`
	Hooks   []struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	} `json:"hooks"`
}

func readHooks(t *testing.T) map[string][]hookEntry {
	t.Helper()
	var doc struct {
		Hooks map[string][]hookEntry `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(mustRead(t, filepath.Join(repoRoot(t), hooksFile))), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Hooks
}

// TestHooksJSON: exactly three events, the guarded matcher on both tool
// events, the shim as command and a 5 s timeout everywhere.
func TestHooksJSON(t *testing.T) {
	hooks := readHooks(t)
	var events []string
	for e := range hooks {
		events = append(events, e)
	}
	slices.Sort(events)
	if !slices.Equal(events, []string{"PostToolUse", "PreToolUse", "SessionStart"}) {
		t.Fatalf("events %v", events)
	}
	leaves := map[string]string{"SessionStart": "session-start", "PreToolUse": "pre-tool-use", "PostToolUse": "post-tool-use"}
	for event, entries := range hooks {
		if len(entries) != 1 || len(entries[0].Hooks) != 1 {
			t.Fatalf("%s: %d entries", event, len(entries))
		}
		e := entries[0]
		if event != "SessionStart" && e.Matcher != wantMatcher {
			t.Errorf("%s matcher %q", event, e.Matcher)
		}
		h := e.Hooks[0]
		if h.Type != "command" || h.Timeout != 5 || h.Command != `"${CLAUDE_PLUGIN_ROOT}/bin/paritydesk-hook" `+leaves[event] {
			t.Errorf("%s hook %+v", event, h)
		}
	}
}

// TestMatcherEqualsGuardedTables: the matcher's servers are the code's
// guarded servers plus the servers of the guarded MCP tools, and its
// single names the guarded non-MCP tools.
func TestMatcherEqualsGuardedTables(t *testing.T) {
	m := regexp.MustCompile(`^\^\(mcp__\(([^)]*)\)__\.\*\|(.*)\)\$$`).FindStringSubmatch(wantMatcher)
	if m == nil {
		t.Fatal("matcher shape")
	}
	servers := strings.Split(m[1], "|")
	singles := strings.Split(m[2], "|")
	want := slices.Clone(online.GuardedServers)
	var wantSingles []string
	for _, tool := range online.GuardedTools {
		if rest, ok := strings.CutPrefix(tool, "mcp__"); ok {
			if srv := rest[:strings.Index(rest, "__")]; !slices.Contains(want, srv) {
				want = append(want, srv)
			}
			continue
		}
		wantSingles = append(wantSingles, tool)
	}
	slices.Sort(servers)
	slices.Sort(want)
	slices.Sort(singles)
	slices.Sort(wantSingles)
	if !slices.Equal(servers, want) || !slices.Equal(singles, wantSingles) {
		t.Fatalf("matcher servers %v / %v, singles %v / %v", servers, want, singles, wantSingles)
	}
	re := regexp.MustCompile(wantMatcher)
	for _, tool := range []string{"mcp__ccd_turn__update_plan", "mcp__ccd_turn__wrap_up", "mcp__ccd_session__mark_chapter",
		"mcp__ccd_view__get_layout", "Bash", "mcp__ccd_sidebarx__set_view"} {
		if re.MatchString(tool) {
			t.Errorf("matcher routes %s to the guard", tool)
		}
	}
	for _, tool := range []string{"mcp__ccd_sidebar__set_pinned", "mcp__claude-artifacts__delete_artifact", "ArtifactData"} {
		if !re.MatchString(tool) {
			t.Errorf("matcher misses %s", tool)
		}
	}
}

// TestPluginVersionsAgree: plugin.json and both marketplace version
// fields carry the same version (the shim's is checked separately).
func TestPluginVersionsAgree(t *testing.T) {
	root := repoRoot(t)
	var plugin struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(mustRead(t, filepath.Join(root, "plugin", ".claude-plugin", "plugin.json"))), &plugin); err != nil {
		t.Fatal(err)
	}
	var market struct {
		Metadata struct {
			Version string `json:"version"`
		} `json:"metadata"`
		Plugins []struct {
			Version string `json:"version"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal([]byte(mustRead(t, filepath.Join(root, ".claude-plugin", "marketplace.json"))), &market); err != nil {
		t.Fatal(err)
	}
	if plugin.Version != "0.2.0" || market.Metadata.Version != plugin.Version || len(market.Plugins) != 1 || market.Plugins[0].Version != plugin.Version {
		t.Fatalf("plugin %q, marketplace %+v", plugin.Version, market)
	}
	// The binary's default version agrees in major and minor, so a plain
	// build from a clone is not skewed against the in-tree plugin.
	m := regexp.MustCompile(`(?m)^\s*Version\s*=\s*"([^"]+)"`).FindStringSubmatch(mustRead(t, filepath.Join(root, "internal", "cli", "cli.go")))
	if m == nil {
		t.Fatal("default Version not found in internal/cli/cli.go")
	}
	if got, want := majorMinorOf(m[1]), majorMinorOf(plugin.Version); got == "" || got != want {
		t.Fatalf("cli.Version %q, plugin %q: major.minor differ", m[1], plugin.Version)
	}
}

// majorMinorOf returns the part of a version before its second dot.
func majorMinorOf(v string) string {
	parts := strings.SplitN(strings.TrimPrefix(v, "v"), ".", 3)
	if len(parts) < 3 {
		return ""
	}
	return parts[0] + "." + parts[1]
}

// TestShimDenyEqualsCatalog: the shim's fixed deny document is the
// binary's document for guard.deny_unreadable, byte for byte.
func TestShimDenyEqualsCatalog(t *testing.T) {
	shim := mustRead(t, filepath.Join(repoRoot(t), shimPath))
	m := regexp.MustCompile(`(?m)^DENY='([^']*)'$`).FindStringSubmatch(shim)
	if m == nil {
		t.Fatal("DENY not found in the shim")
	}
	if want := strings.TrimSpace(string(hook.DenyDocument(i18n.GuardDenyUnreadable))); m[1] != want {
		t.Fatalf("shim deny %q, binary %q", m[1], want)
	}
	if !strings.Contains(m[1], i18n.T(i18n.GuardDenyUnreadable)) {
		t.Fatal("shim deny text differs from guard.deny_unreadable")
	}
}

// TestNoAllowDecision: the guard never answers "allow" (that would skip
// the app's own approval cards): no string literal of internal/hook and
// no line of the shim outside comments says allow.
func TestNoAllowDecision(t *testing.T) {
	allowRE := regexp.MustCompile(`(?i)\ballow\b`)
	for _, g := range goFiles(t, false) {
		if !strings.HasPrefix(g.rel, "internal/hook/") {
			continue
		}
		ast.Inspect(g.file, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil && allowRE.MatchString(s) {
					t.Errorf("%s:%d: string literal says allow", g.rel, g.fset.Position(lit.Pos()).Line)
				}
			}
			return true
		})
	}
	for i, line := range strings.Split(mustRead(t, filepath.Join(repoRoot(t), shimPath)), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if allowRE.MatchString(line) {
			t.Errorf("%s:%d says allow", shimPath, i+1)
		}
	}
}

// skillText reads a plugin skill.
func skillText(t *testing.T, name string) string {
	t.Helper()
	return mustRead(t, filepath.Join(repoRoot(t), "plugin", "skills", name, "SKILL.md"))
}

// TestSkillsOnlineRules pins the rules of the switch and reconcile skills
// (spec-online 17, 21.5).
func TestSkillsOnlineRules(t *testing.T) {
	sw, rc := skillText(t, "switch"), skillText(t, "reconcile")
	for name, s := range map[string]string{"switch": sw, "reconcile": rc} {
		for _, want := range []string{"argument-hint:", "AskUserQuestion", "plain text", "ToolSearch"} {
			if !strings.Contains(s, want) {
				t.Errorf("%s skill does not mention %q", name, want)
			}
		}
		if strings.Contains(s, "bypassPermissions") {
			t.Errorf("%s skill names bypassPermissions", name)
		}
	}
	confirm := strings.Index(sw, "AskUserQuestion")
	capture := strings.Index(sw, "paritydesk online capture")
	swtch := strings.Index(sw, "paritydesk switch --detach")
	if confirm < 0 || capture < confirm || swtch < capture {
		t.Errorf("switch skill order: confirm %d, capture %d, switch %d", confirm, capture, swtch)
	}
	if s, c := strings.Index(sw, `label: "Switch"`), strings.Index(sw, `label: "Cancel"`); s < 0 || c < s {
		t.Error(`switch skill: "Switch" must come before "Cancel"`)
	}
	for _, want := range []string{"Never run\n     paritydesk in the same message as the calls", "remaining_batches", "+ 4", "40 answers",
		"without running `paritydesk online done`", "--unavailable", "--wait", "online decide", "online done",
		"with `error` instead of `state`: stop the loop", "`error.next`"} {
		if !strings.Contains(rc, want) {
			t.Errorf("reconcile skill misses %q", want)
		}
	}
	if !strings.Contains(sw, "(never in the same message as the calls)") {
		t.Error("switch skill: the capture finish must not share a message with the calls")
	}
}

// TestCatalogNoBypassAdvice: no catalog text names a bypass mode.
func TestCatalogNoBypassAdvice(t *testing.T) {
	for id, text := range i18n.Catalog() {
		if strings.Contains(strings.ToLower(text), "bypass") {
			t.Errorf("%s mentions a bypass mode", id)
		}
	}
}

// TestOnlineFilesOnlyInOnlineDir: the online store keeps its files under
// the support directory's online folder (uninstall removes it).
func TestOnlineFilesOnlyInOnlineDir(t *testing.T) {
	src := mustRead(t, filepath.Join(repoRoot(t), "internal", "cli", "uninstall.go"))
	if !strings.Contains(src, "online.DirName") {
		t.Fatal("uninstall does not remove the online folder")
	}
	if _, err := os.Stat(filepath.Join(repoRoot(t), "plugin", "skills", "reconcile", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
}
