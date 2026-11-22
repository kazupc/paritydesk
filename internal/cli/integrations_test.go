package cli

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The integration scripts, relative to the repository root.
var integrationScripts = []string{
	"integrations/swiftbar/paritydesk.5m.sh",
	"integrations/raycast/paritydesk-switch.sh",
	"integrations/shell/paritydesk.sh",
}

// repoFile returns the absolute path of a file relative to the module
// root (two levels above this package).
func repoFile(t *testing.T, rel string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestIntegrationScriptsShellcheck runs shellcheck on the integration
// scripts and on the generated bash completion when shellcheck is
// installed (CI and the release checklist install it).
func TestIntegrationScriptsShellcheck(t *testing.T) {
	sc, err := exec.LookPath("shellcheck")
	if err != nil {
		t.Skip("shellcheck not found")
	}
	for _, rel := range integrationScripts {
		out, err := exec.Command(sc, "--shell=sh", "--severity=style", repoFile(t, rel)).CombinedOutput()
		if err != nil {
			t.Errorf("shellcheck %s: %v\n%s", rel, err, out)
		}
	}
	s, err := completionScript("bash")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "paritydesk.bash")
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(sc, "--shell=bash", "--severity=style", p).CombinedOutput(); err != nil {
		t.Errorf("shellcheck bash completion: %v\n%s", err, out)
	}
}

// TestIntegrationScriptsSyntax parses every script with sh, bash and zsh
// when present, and checks the rules every script follows.
func TestIntegrationScriptsSyntax(t *testing.T) {
	for _, rel := range integrationScripts {
		p := repoFile(t, rel)
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, sh := range []string{"sh", "bash", "zsh"} {
			bin, err := exec.LookPath(sh)
			if err != nil {
				continue
			}
			if out, err := exec.Command(bin, "-n", p).CombinedOutput(); err != nil {
				t.Errorf("%s -n %s: %v\n%s", sh, rel, err, out)
			}
		}
		for _, want := range []string{"set -eu", "hook-binary", "/opt/homebrew/bin/paritydesk", "*[!a-z0-9-]*", "switch --detach"} {
			if !strings.Contains(src, want) {
				t.Errorf("%s lacks %q", rel, want)
			}
		}
		for _, banned := range []string{"eval ", "command -v paritydesk", "which paritydesk", "osascript", "launchctl", "kill "} {
			if strings.Contains(src, banned) {
				t.Errorf("%s contains %q", rel, banned)
			}
		}
	}
}

// fakeParitydesk installs a fake binary under home, records it the way
// paritydesk init does, and returns the file its arguments are logged to.
func fakeParitydesk(t *testing.T, home, status string) string {
	t.Helper()
	bin := filepath.Join(home, "bin", "paritydesk")
	logFile := filepath.Join(home, "args.log")
	if err := os.MkdirAll(filepath.Dir(bin), 0o700); err != nil {
		t.Fatal(err)
	}
	statusFile := filepath.Join(home, "status.json")
	if err := os.WriteFile(statusFile, []byte(status), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + logFile + "'\n" +
		"if [ \"$1\" = status ]; then cat '" + statusFile + "'; fi\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	rec := filepath.Join(home, "Library", "Application Support", "paritydesk", HookBinaryFile)
	if err := os.MkdirAll(filepath.Dir(rec), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rec, []byte(bin+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return logFile
}

// runScript runs name with args with HOME set to home and returns the exit code
// and the output.
func runScript(t *testing.T, name, home string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	cmd.Dir = home
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, out.String()
	case errors.As(err, &ee):
		return ee.ExitCode(), out.String()
	}
	t.Fatalf("run %s: %v", name, err)
	return -1, ""
}

func loggedArgs(t *testing.T, logFile string) string {
	t.Helper()
	b, err := os.ReadFile(logFile)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// badAliases must never reach paritydesk.
var badAliases = []string{"", "-x", "Work", "a b", "a;b", "$(id)", "a\nb", "../x", strings.Repeat("a", 33), "--yes"}

func TestSwiftBarPlugin(t *testing.T) {
	if _, err := os.Stat("/usr/bin/plutil"); err != nil {
		t.Skip("plutil not found (not macOS)")
	}
	script := repoFile(t, "integrations/swiftbar/paritydesk.5m.sh")
	home := t.TempDir()
	logFile := fakeParitydesk(t, home, `{"v":1,"active":"work","needs_recover":false,"profiles":[{"alias":"home"},{"alias":"work"},{"alias":"Bad|x"}]}`)

	code, out := runScript(t, "/bin/sh", home, script)
	if code != 0 {
		t.Fatalf("menu: exit %d\n%s", code, out)
	}
	for _, want := range []string{"pd work\n", "work (active)\n", "Switch to home | bash=\"" + script + "\" param1=switch param2=home terminal=false refresh=true\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("menu lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Bad") {
		t.Errorf("an invalid alias reached the menu:\n%s", out)
	}
	if got := loggedArgs(t, logFile); got != "status --short --json\n" {
		t.Fatalf("menu ran %q", got)
	}

	if code, out := runScript(t, "/bin/sh", home, script, "switch", "home"); code != 0 {
		t.Fatalf("switch: exit %d\n%s", code, out)
	}
	if got := loggedArgs(t, logFile); !strings.HasSuffix(got, "switch --detach --yes -- home\n") {
		t.Fatalf("switch ran %q", got)
	}
	before := loggedArgs(t, logFile)
	for _, bad := range badAliases {
		if code, _ := runScript(t, "/bin/sh", home, script, "switch", bad); code == 0 {
			t.Errorf("alias %q: exit 0", bad)
		}
	}
	if got := loggedArgs(t, logFile); got != before {
		t.Fatalf("an invalid alias reached paritydesk: %q", strings.TrimPrefix(got, before))
	}
}

func TestSwiftBarPluginNeedsRecover(t *testing.T) {
	if _, err := os.Stat("/usr/bin/plutil"); err != nil {
		t.Skip("plutil not found (not macOS)")
	}
	script := repoFile(t, "integrations/swiftbar/paritydesk.5m.sh")
	home := t.TempDir()
	fakeParitydesk(t, home, `{"v":1,"active":"work","needs_recover":true,"profiles":[{"alias":"home"}]}`)
	code, out := runScript(t, "/bin/sh", home, script)
	if code != 0 || !strings.Contains(out, "paritydesk recover") || strings.Contains(out, "Switch to") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

func TestRaycastScript(t *testing.T) {
	script := repoFile(t, "integrations/raycast/paritydesk-switch.sh")
	home := t.TempDir()
	logFile := fakeParitydesk(t, home, "{}")
	code, out := runScript(t, "/bin/sh", home, script, "home")
	if code != 0 || !strings.Contains(out, "Switching to home") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if got := loggedArgs(t, logFile); got != "switch --detach --yes -- home\n" {
		t.Fatalf("ran %q", got)
	}
	for _, bad := range badAliases {
		if code, _ := runScript(t, "/bin/sh", home, script, bad); code == 0 {
			t.Errorf("alias %q: exit 0", bad)
		}
	}
	if got := loggedArgs(t, logFile); got != "switch --detach --yes -- home\n" {
		t.Fatalf("an invalid alias reached paritydesk: %q", got)
	}
}

func TestShellFunction(t *testing.T) {
	snippet := repoFile(t, "integrations/shell/paritydesk.sh")
	for _, sh := range []string{"sh", "bash", "zsh"} {
		bin, err := exec.LookPath(sh)
		if err != nil {
			continue
		}
		t.Run(sh, func(t *testing.T) {
			home := t.TempDir()
			logFile := fakeParitydesk(t, home, "{}")
			// The function must not change the caller's shell options.
			driver := `. "$1"; shift; pd "$@"; rc=$?; case $- in *e*|*u*) echo LEAKED;; esac; exit $rc`
			code, out := runScript(t, bin, home, "-c", driver, "driver", snippet, "home")
			if code != 0 || strings.Contains(out, "LEAKED") {
				t.Fatalf("exit %d\n%s", code, out)
			}
			code, out = runScript(t, bin, home, "-c", driver, "driver", snippet)
			if code != 0 {
				t.Fatalf("status: exit %d\n%s", code, out)
			}
			want := "switch --detach -- home\nstatus --short\n"
			if got := loggedArgs(t, logFile); got != want {
				t.Fatalf("ran %q, want %q", got, want)
			}
			for _, bad := range badAliases {
				if code, _ := runScript(t, bin, home, "-c", driver, "driver", snippet, bad); code == 0 {
					t.Errorf("alias %q: exit 0", bad)
				}
			}
			if got := loggedArgs(t, logFile); got != want {
				t.Fatalf("an invalid alias reached paritydesk: %q", got)
			}
		})
	}
}
