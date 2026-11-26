package policy

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// shimPath is the plugin's hook shim, relative to the repository root.
const shimPath = "plugin/bin/paritydesk-hook"

// TestShimVersionMatchesPluginJSON: the version the shim passes with
// --plugin-version is the plugin's own version.
func TestShimVersionMatchesPluginJSON(t *testing.T) {
	root := repoRoot(t)
	shim, err := os.ReadFile(filepath.Join(root, shimPath))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^PLUGIN_VERSION="([^"]+)"`).FindSubmatch(shim)
	if m == nil {
		t.Fatal("PLUGIN_VERSION not found in the shim")
	}
	raw, err := os.ReadFile(filepath.Join(root, "plugin", ".claude-plugin", "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if string(m[1]) != manifest.Version || manifest.Version == "" {
		t.Fatalf("shim PLUGIN_VERSION %q, plugin.json version %q", m[1], manifest.Version)
	}
}

// homebrewBinaries are the shim's fixed fallback locations.
var homebrewBinaries = []string{"/opt/homebrew/bin/paritydesk", "/usr/local/bin/paritydesk"}

// runShim runs the shim with HOME set to home and returns its exit code
// and stdout.
func runShim(t *testing.T, home, cwd, stdin string) (int, string) {
	t.Helper()
	cmd := exec.Command("/bin/sh", filepath.Join(repoRoot(t), shimPath), "session-start")
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	cmd.Dir = cwd
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, out.String()
	case errors.As(err, &ee):
		return ee.ExitCode(), out.String()
	}
	t.Fatalf("run shim: %v", err)
	return -1, ""
}

// writeFake writes a fake paritydesk that prints its arguments and its
// stdin, then fails.
func writeFake(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho \"args: $*\"\ncat\nexit 3\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

func writeRecord(t *testing.T, home, content string) {
	t.Helper()
	p := filepath.Join(home, "Library", "Application Support", "paritydesk", "hook-binary")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestShimUsesRecordedBinary(t *testing.T) {
	home := t.TempDir()
	fake := filepath.Join(home, "bin", "paritydesk")
	writeFake(t, fake)
	writeRecord(t, home, fake+"\n")
	code, out := runShim(t, home, home, `{"hook_event_name":"SessionStart"}`)
	if code != 0 {
		t.Fatalf("exit %d (the fake binary failed; the shim must still exit 0)", code)
	}
	version := regexp.MustCompile(`(?m)^PLUGIN_VERSION="([^"]+)"`).FindStringSubmatch(mustRead(t, filepath.Join(repoRoot(t), shimPath)))[1]
	want := "args: hook session-start --plugin-version " + version + "\n" + `{"hook_event_name":"SessionStart"}`
	if strings.TrimSpace(out) != want {
		t.Fatalf("stdout %q, want %q", out, want)
	}
}

func TestShimIgnoresUnsafeRecords(t *testing.T) {
	for _, p := range homebrewBinaries {
		if _, err := os.Stat(p); err == nil {
			t.Skipf("%s exists; the fallback would run it", p)
		}
	}
	tests := []struct {
		name   string
		record func(home string) string // returns the record content; "" writes none
	}{
		{"relative path", func(home string) string {
			writeFake(t, filepath.Join(home, "bin", "paritydesk"))
			return "bin/paritydesk\n"
		}},
		{"not executable", func(home string) string {
			p := filepath.Join(home, "bin", "paritydesk")
			writeFake(t, p)
			if err := os.Chmod(p, 0o600); err != nil {
				t.Fatal(err)
			}
			return p + "\n"
		}},
		{"a directory", func(home string) string { return home + "\n" }},
		{"missing binary", func(home string) string { return filepath.Join(home, "nope") + "\n" }},
		{"no record", func(string) string { return "" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if rec := tc.record(home); rec != "" {
				writeRecord(t, home, rec)
			}
			code, out := runShim(t, home, home, "{}")
			if code != 0 || out != "" {
				t.Fatalf("exit %d stdout %q", code, out)
			}
		})
	}
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// runShimEvent runs the shim for event with HOME set to home.
func runShimEvent(t *testing.T, home, event, stdin string) (int, string) {
	t.Helper()
	cmd := exec.Command("/bin/sh", filepath.Join(repoRoot(t), shimPath), event)
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	cmd.Dir = home
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, out.String()
	case errors.As(err, &ee):
		return ee.ExitCode(), out.String()
	}
	t.Fatalf("run shim: %v", err)
	return -1, ""
}

// writeArmed writes the online armed marker with content.
func writeArmed(t *testing.T, home, content string) {
	t.Helper()
	p := filepath.Join(home, "Library", "Application Support", "paritydesk", "online", "armed")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeScript writes an executable fake binary.
func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
}

// shimDeny is the shim's fixed deny document.
func shimDeny(t *testing.T) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^DENY='([^']*)'$`).FindStringSubmatch(mustRead(t, filepath.Join(repoRoot(t), shimPath)))
	if m == nil {
		t.Fatal("DENY not found")
	}
	return m[1] + "\n"
}

func noHomebrew(t *testing.T) {
	t.Helper()
	for _, p := range homebrewBinaries {
		if _, err := os.Stat(p); err == nil {
			t.Skipf("%s exists; the fallback would run it", p)
		}
	}
}

// TestShimArmedWithoutBinary: while armed (deadline ahead, also 45 min
// ahead), a pre-tool-use without a binary prints the deny document; every
// other case is silent.
func TestShimArmedWithoutBinary(t *testing.T) {
	noHomebrew(t)
	now := time.Now().Unix()
	for _, tc := range []struct {
		name, armed, event string
		deny               bool
	}{
		{"armed", strconv.FormatInt(now+600, 10) + "\n", "pre-tool-use", true},
		{"armed 45 min", strconv.FormatInt(now+45*60, 10) + "\n", "pre-tool-use", true},
		{"deadline passed", strconv.FormatInt(now-1, 10) + "\n", "pre-tool-use", false},
		{"empty", "", "pre-tool-use", false},
		{"not numeric", "12ab\n", "pre-tool-use", false},
		{"too long", "9999999999999\n", "pre-tool-use", false},
		{"post-tool-use", strconv.FormatInt(now+600, 10) + "\n", "post-tool-use", false},
		{"session-start", strconv.FormatInt(now+600, 10) + "\n", "session-start", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			writeArmed(t, home, tc.armed)
			code, out := runShimEvent(t, home, tc.event, "{}")
			want := ""
			if tc.deny {
				want = shimDeny(t)
			}
			if code != 0 || out != want {
				t.Fatalf("exit %d stdout %q, want %q", code, out, want)
			}
		})
	}
	home := t.TempDir()
	if code, out := runShimEvent(t, home, "pre-tool-use", "{}"); code != 0 || out != "" {
		t.Fatalf("no armed file: exit %d stdout %q", code, out)
	}
}

// TestShimArmedBinaryOutcomes: a failing binary (at once or after its own
// deadline) gets the deny document while armed; a slow binary that
// answers is passed through exactly once.
func TestShimArmedBinaryOutcomes(t *testing.T) {
	answer := `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"x"}}`
	for _, tc := range []struct {
		name, body, want string
		deny             bool
	}{
		{"exits 1", "exit 1\n", "", true},
		{"slow answer", "sleep 1\necho '" + answer + "'\nexit 0\n", answer + "\n", false},
		{"silent pass", "cat >/dev/null\nexit 0\n", "", false},
		{"past its deadline", "sleep 3\nexit 1\n", "", true},
		{"partial then crash", "printf '{\"hook'\nexit 2\n", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			fake := filepath.Join(home, "bin", "paritydesk")
			writeScript(t, fake, tc.body)
			writeRecord(t, home, fake+"\n")
			writeArmed(t, home, strconv.FormatInt(time.Now().Unix()+600, 10)+"\n")
			code, out := runShimEvent(t, home, "pre-tool-use", "{}")
			want := tc.want
			if tc.deny {
				want = shimDeny(t)
			}
			if code != 0 || out != want {
				t.Fatalf("exit %d stdout %q, want %q", code, out, want)
			}
		})
	}
	// Not armed: a failing binary stays silent.
	home := t.TempDir()
	fake := filepath.Join(home, "bin", "paritydesk")
	writeScript(t, fake, "exit 1\n")
	writeRecord(t, home, fake+"\n")
	if code, out := runShimEvent(t, home, "pre-tool-use", "{}"); code != 0 || out != "" {
		t.Fatalf("unarmed failing binary: exit %d stdout %q", code, out)
	}
}
