package cli

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// golden compares got with testdata/<name>.golden (or rewrites it).
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/cli -run Golden -update)", err)
	}
	if got != string(want) {
		t.Fatalf("%s differs from the golden file:\n--- got\n%s\n--- want\n%s", path, got, want)
	}
}

// commandPaths lists every node of the command tree.
func commandPaths(c *command, prefix []string) [][]string {
	out := [][]string{prefix}
	for _, s := range c.sub {
		out = append(out, commandPaths(s, append(append([]string(nil), prefix...), s.name))...)
	}
	return out
}

func TestEveryLeafRuns(t *testing.T) {
	var walk func(c *command, path string)
	walk = func(c *command, path string) {
		if len(c.sub) == 0 && c.run == nil {
			t.Errorf("%s has no run function", path)
		}
		for _, s := range c.sub {
			walk(s, path+" "+s.name)
		}
	}
	walk(root(), "paritydesk")
}

func TestUsageGolden(t *testing.T) {
	for _, path := range commandPaths(root(), nil) {
		name := "usage/" + strings.Join(append([]string{"paritydesk"}, path...), "_")
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			code, out, errOut := h.run("", append(append([]string(nil), path...), "--help")...)
			if code != 0 || errOut != "" {
				t.Fatalf("code %d stderr %q", code, errOut)
			}
			golden(t, name, out)
			// help <path> prints the same text.
			code, out2, _ := h.run("", append([]string{"help"}, path...)...)
			if code != 0 || out2 != out {
				t.Fatalf("help %v differs from --help (code %d):\n%s", path, code, out2)
			}
			if strings.Contains(out, "plugin-version") || strings.Contains(out, "from-runner") {
				t.Fatalf("hidden flag listed:\n%s", out)
			}
		})
	}
}

// jsonCases is one --json invocation per command.
var jsonCases = []struct {
	name  string
	args  []string
	setup func(h *harness)
	code  int
}{
	{name: "init", args: []string{"init", "--alias", "work", "--label", "Work", "--unmanaged"}},
	{name: "profile_list", args: []string{"profile", "list"}},
	{name: "profile_adopt", args: []string{"profile", "adopt", "home", "Claude-home", "--unmanaged"}},
	{name: "profile_add", args: []string{"profile", "add", "client", "--managed"}},
	{name: "profile_rename", args: []string{"profile", "rename", "home", "personal"}},
	{name: "profile_remove", args: []string{"profile", "remove", "home"}},
	{name: "status", args: []string{"status"}},
	{name: "status_short", args: []string{"status", "--short"}},
	{name: "plan", args: []string{"plan", "home"}},
	{name: "plan_read_only", args: []string{"plan", "--read-only", "--", "-"}},
	{name: "switch", args: []string{"switch", "home", "--yes"}},
	{name: "sync", args: []string{"sync", "--yes"}},
	{name: "verify", args: []string{"verify"}},
	{name: "verify_drift", args: []string{"verify"}, code: 50, setup: func(h *harness) {
		h.svc.verify.Drift = []model.RowID{model.RowGroupOrder}
	}},
	{name: "recover", args: []string{"recover", "--yes"}},
	{name: "rollback", args: []string{"rollback", "3", "--propagate", "--yes"}},
	{name: "rebase", args: []string{"rebase"}},
	{name: "generations_list", args: []string{"generations", "list"}},
	{name: "generations_show", args: []string{"generations", "show", "1"}},
	{name: "backups_prune", args: []string{"backups", "prune"}},
	{name: "backups_purge", args: []string{"backups", "purge", "--yes"}},
	{name: "doctor", args: []string{"doctor"}},
	{name: "log", args: []string{"log"}, setup: writeLogs},
	{name: "history", args: []string{"history"}, setup: writeLogs},
	{name: "config_path", args: []string{"config", "path"}},
	{name: "config_show", args: []string{"config", "show"}},
	{name: "config_validate", args: []string{"config", "validate"}, setup: writeConfig},
	{name: "uninstall", args: []string{"uninstall", "--yes"}, setup: func(h *harness) {
		h.svc.status.Active = ""
		mkdirs(h, "Library/Application Support/paritydesk/state", "Library/Application Support/paritydesk/run",
			"Library/Application Support/paritydesk/generations/1")
		writeFile(h, "Library/Application Support/paritydesk/state/root.json", "{}")
		writeFile(h, "Library/Application Support/paritydesk/hook-binary", "/x\n")
	}},
	{name: "version", args: []string{"version"}},
	{name: "help", args: []string{"help", "profile", "add"}},
	{name: "error_usage", args: []string{"switch", "Not-An-Alias"}, code: 2},
	{name: "error_breaker", args: []string{"switch", "home"}, code: 22, setup: func(h *harness) {
		h.svc.errs["switch"] = breakerErr()
	}},
}

func TestJSONGolden(t *testing.T) {
	for _, tc := range jsonCases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			if tc.setup != nil {
				tc.setup(h)
			}
			code, out, _ := h.run("", append([]string{"--json"}, tc.args...)...)
			if code != tc.code {
				t.Fatalf("code %d, want %d; stdout %s", code, tc.code, out)
			}
			if !strings.HasPrefix(out, `{"v":1`) || strings.Count(out, "\n") != 1 {
				t.Fatalf("not one JSON document with v first: %q", out)
			}
			var doc map[string]any
			if err := json.Unmarshal([]byte(out), &doc); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
			golden(t, "json/"+tc.name, h.normalize(out))
		})
	}
}

func TestPlanJSONFields(t *testing.T) {
	h := newHarness(t)
	code, out, _ := h.run("", "plan", "--json", "--", "home")
	if code != 0 {
		t.Fatalf("code %d", code)
	}
	var doc struct {
		PlanHash          string     `json:"plan_hash"`
		DestructiveDigest string     `json:"destructive_digest"`
		NeedsConfirmation *bool      `json:"needs_confirmation"`
		ExpiresAt         *time.Time `json:"expires_at"`
		Lines             []string   `json:"lines"`
		Saved             *bool      `json:"saved"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	p := fakePlan()
	if doc.PlanHash != p.Hash() || len(doc.PlanHash) != 64 {
		t.Errorf("plan_hash %q", doc.PlanHash)
	}
	if doc.DestructiveDigest != p.DestructiveDigest() || doc.DestructiveDigest == "" {
		t.Errorf("destructive_digest %q", doc.DestructiveDigest)
	}
	if doc.NeedsConfirmation == nil || doc.ExpiresAt == nil || !doc.ExpiresAt.Equal(p.ExpiresAt) {
		t.Errorf("needs_confirmation or expires_at missing: %s", out)
	}
	if doc.Saved == nil || !*doc.Saved || !h.svc.previewOpts.Save {
		t.Errorf("plan was not saved: %s", out)
	}
	if n := len(doc.Lines); n < 2 || !strings.HasPrefix(doc.Lines[n-1], "DESTRUCTIVE: 1 deletions") {
		t.Errorf("lines %q", doc.Lines)
	}
	// The text output prints the same lines, DESTRUCTIVE last before the
	// save note.
	_, text, _ := h.run("", "plan", "home")
	for _, l := range doc.Lines {
		if !strings.Contains(text, l+"\n") {
			t.Errorf("text output misses %q:\n%s", l, text)
		}
	}
}

func writeLogs(h *harness) {
	dir := filepath.Join(h.home, "Library", "Logs", "paritydesk")
	mkdirs(h, "Library/Logs/paritydesk")
	logs := map[string]string{
		"run-0001": `{"v":1,"phase":"P0","status":"start","from":"work","to":"home"}` + "\n" + `{"v":1,"code":0}` + "\nnot json\n",
		"run-0002": `{"v":1,"phase":"P3","status":"fail","from":"home","to":"work\u001b"}` + "\n" + `{"v":1,"code":10}` + "\n",
	}
	for i, id := range []string{"run-0001", "run-0002"} {
		p := filepath.Join(dir, id+".jsonl")
		if err := os.WriteFile(p, []byte(logs[id]), 0o600); err != nil {
			h.t.Fatal(err)
		}
		mt := fixedTime.Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(p, mt, mt); err != nil {
			h.t.Fatal(err)
		}
	}
	writeFile(h, "Library/Logs/paritydesk/notes.txt", "ignored")
}

func writeConfig(h *harness) {
	writeFile(h, ".config/paritydesk/config.toml", "version = 1\n[[profile]]\nalias = \"work\"\n[[profile]]\nalias = \"home\"\nfolder = \"Claude-Home\"\n")
}

func mkdirs(h *harness, rels ...string) {
	for _, r := range rels {
		if err := os.MkdirAll(filepath.Join(h.home, filepath.FromSlash(r)), 0o700); err != nil {
			h.t.Fatal(err)
		}
	}
}

func writeFile(h *harness, rel, body string) {
	p := filepath.Join(h.home, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		h.t.Fatal(err)
	}
}

func breakerErr() error {
	return pderr.New(pderr.DeletionBreaker, "switch/P5", "sessions: 5 deletions exceed the limit of 3")
}
