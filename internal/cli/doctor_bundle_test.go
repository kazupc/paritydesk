package cli

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/orchestrator"
)

// readBundle returns the members of the archive at p.
func readBundle(t *testing.T, p string) map[string]string {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		out[hdr.Name] = string(b)
	}
}

// plantBundleInputs writes a config and a run log that hold planted
// personal values under the harness home, and makes doctor report a
// detail with an id and a home path.
func plantBundleInputs(t *testing.T, h *harness) []string {
	t.Helper()
	uuid := fakeUUID(7)
	cfgDir := filepath.Join(h.home, ".config", "paritydesk")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := `version = 1
[[profile]]
alias = "plantedwork"
label = "Planted Person Label"
[[profile]]
alias = "plantedhome"
folder = "Claude-plantedfolder"
[sync]
exclude = ["~/clients/plantedclient/**", "local_` + uuid + `"]
`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	logDir := filepath.Join(h.home, "Library", "Logs", "paritydesk")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	lines := []map[string]any{
		{"v": 1, "t": "2026-01-02T03:04:05Z", "phase": "P2", "status": "warn",
			"message": "could not read " + h.home + "/projects/plantedrepo/x: denied; session \"Planted Title\" " + uuid},
		{"v": 1, "t": "2026-01-02T03:04:06Z", "run": "20260102-030405-abcd", "code": 0, "from": "plantedwork", "to": "plantedhome", "title": "Planted Title"},
	}
	var b strings.Builder
	for _, l := range lines {
		j, _ := json.Marshal(l)
		b.Write(j)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(logDir, "20260102-030405-abcd.jsonl"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	h.svc.doctorChecks = []orchestrator.Check{
		{ID: "identity", Status: "ok", Detail: "Claude-plantedfolder: account " + uuid + " (plantedhome)"},
		{ID: "probes", Status: "warn", Detail: h.home + "/Library/Application Support/Claude: 1 of 9 probes failed"},
	}
	return []string{"plantedwork", "plantedhome", "Planted Person", "plantedfolder", "plantedclient", "plantedrepo",
		"Planted Title", uuid, h.home, "/Users/"}
}

func TestDoctorBundle(t *testing.T) {
	h := newHarness(t)
	planted := plantBundleInputs(t, h)
	out := filepath.Join(t.TempDir(), "support.tar.gz")
	code, stdout, stderr := h.run("", "--json", "doctor", "--bundle", out)
	if code != 0 {
		t.Fatalf("code %d stdout %s stderr %s", code, stdout, stderr)
	}
	var v struct {
		Bundle string `json:"bundle"`
	}
	if err := json.Unmarshal([]byte(stdout), &v); err != nil || v.Bundle != "support.tar.gz" {
		t.Fatalf("bundle field %q (%v) in %s", v.Bundle, err, stdout)
	}
	fi, err := os.Lstat(out)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	files := readBundle(t, out)
	for _, name := range []string{"README.txt", "version.json", "doctor.json", "config.json", "manifests.json",
		"logs/01-20260102-030405-abcd.jsonl"} {
		if _, ok := files["paritydesk-support/"+name]; !ok {
			t.Errorf("missing %s", name)
		}
	}
	for name, content := range files {
		for _, p := range planted {
			if strings.Contains(content, p) {
				t.Errorf("%s holds the planted value %q:\n%s", name, p, content)
			}
		}
	}
	if !strings.Contains(files["paritydesk-support/manifests.json"], `"6.x"`) {
		t.Errorf("manifests.json: %s", files["paritydesk-support/manifests.json"])
	}
	if !strings.Contains(files["paritydesk-support/doctor.json"], "Claude-profile-2") {
		t.Errorf("doctor.json: %s", files["paritydesk-support/doctor.json"])
	}

	// Never over an existing file, and the file stays as it was.
	before, _ := os.ReadFile(out)
	if code, _, _ := h.run("", "doctor", "--bundle", out); code != 2 {
		t.Fatalf("overwrite: code %d, want 2", code)
	}
	after, _ := os.ReadFile(out)
	if string(before) != string(after) {
		t.Fatal("the existing archive changed")
	}
}

func TestDoctorBundleRefusals(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	if code, _, _ := h.run("", "--read-only", "doctor", "--bundle", filepath.Join(dir, "a.tar.gz")); code != 2 {
		t.Errorf("read-only: code %d, want 2", code)
	}
	if code, _, _ := h.run("", "doctor", "--bundle", filepath.Join(dir, "b.tar.gz"), "--bundle-logs", "51"); code != 2 {
		t.Errorf("too many logs: code %d, want 2", code)
	}
	// Inside a Claude data folder under the configured data root.
	root := filepath.Join(h.home, "Library", "Application Support")
	for _, folder := range []string{"Claude", "Claude-other", "claude"} {
		if err := os.MkdirAll(filepath.Join(root, folder), 0o700); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(root, folder, "x.tar.gz")
		if code, _, _ := h.run("", "doctor", "--bundle", p); code != 2 {
			t.Errorf("%s: code %d, want 2", folder, code)
		}
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s: an archive was written", folder)
		}
	}
	// A case variant of the data root, and a directory that links into a
	// data folder, are refused too.
	upper := filepath.Join(h.home, "LIBRARY", "application support", "Claude", "case.tar.gz")
	if code, _, _ := h.run("", "doctor", "--bundle", upper); code != 2 {
		t.Errorf("case variant: code %d, want 2", code)
	}
	via := filepath.Join(dir, "via")
	if err := os.Symlink(filepath.Join(root, "Claude"), via); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := h.run("", "doctor", "--bundle", filepath.Join(via, "s.tar.gz")); code != 2 {
		t.Errorf("through a symlinked directory: code %d, want 2", code)
	}
	if _, err := os.Lstat(filepath.Join(root, "Claude", "s.tar.gz")); err == nil {
		t.Error("an archive was written through the symlinked directory")
	}
	// A symbolic link at the target is never followed.
	link := filepath.Join(dir, "link.tar.gz")
	if err := os.Symlink(filepath.Join(dir, "target"), link); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := h.run("", "doctor", "--bundle", link); code != 2 {
		t.Errorf("symlink: code %d, want 2", code)
	}
	if _, err := os.Lstat(filepath.Join(dir, "target")); err == nil {
		t.Error("the link target was created")
	}
	for _, c := range h.svc.calls {
		if c == "doctor" {
			t.Fatalf("doctor ran although the target was refused: %v", h.svc.calls)
		}
	}
}

func TestInsideDataFolder(t *testing.T) {
	tests := []struct {
		p    string
		want bool
	}{
		{"/r/Claude/x", true},
		{"/R/claude/x", true},
		{"/r/Claude-a/b/c", true},
		{"/r/CLAUDE-A/x", true},
		{"/r/Claude", true},
		{"/r/paritydesk/x", false},
		{"/r", false},
		{"/other/Claude/x", false},
		{"/r/ClaudeX/x", false},
	}
	for _, tc := range tests {
		if got := insideDataFolder("/r", tc.p); got != tc.want {
			t.Errorf("insideDataFolder(%q) = %v, want %v", tc.p, got, tc.want)
		}
	}
}
