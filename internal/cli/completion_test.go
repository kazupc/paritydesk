package cli

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompletionGolden(t *testing.T) {
	for _, shell := range completionShells {
		t.Run(shell, func(t *testing.T) {
			h := newHarness(t)
			code, out, errOut := h.run("", "completion", shell)
			if code != 0 || errOut != "" {
				t.Fatalf("code %d stderr %q", code, errOut)
			}
			golden(t, "completion/"+shell, out)
		})
	}
}

func TestCompletionUnknownShell(t *testing.T) {
	h := newHarness(t)
	if code, _, _ := h.run("", "completion", "tcsh"); code != 2 {
		t.Fatalf("code %d, want 2", code)
	}
	if code, _, _ := h.run("", "completion"); code != 2 {
		t.Fatalf("no shell: code %d, want 2", code)
	}
}

// TestCompletionCoversEveryCommandAndFlag walks the command tree on its
// own and checks that every visible command and flag is in every script,
// and that hidden ones are not offered.
func TestCompletionCoversEveryCommandAndFlag(t *testing.T) {
	scripts := map[string]string{}
	for _, shell := range completionShells {
		s, err := completionScript(shell)
		if err != nil {
			t.Fatal(err)
		}
		scripts[shell] = s
	}
	flagSpelling := func(shell, name string) string {
		if shell == "fish" {
			return " -l " + name + " "
		}
		return "--" + name
	}
	check := func(what, want string) {
		for shell, s := range scripts {
			w := want
			if strings.HasPrefix(what, "flag ") {
				w = flagSpelling(shell, want)
			}
			if !strings.Contains(s, w) {
				t.Errorf("%s: %s %q is missing", shell, what, w)
			}
		}
	}
	for _, f := range visibleFlags(func(fs *flag.FlagSet) { (&globalFlags{}).register(fs) }) {
		check("flag (global)", f.name)
	}
	var walk func(c *command, path string)
	walk = func(c *command, path string) {
		if c.hidden {
			return
		}
		if path != "" {
			check("command", c.name)
		}
		if c.flags != nil {
			for _, f := range visibleFlags(c.flags) {
				check("flag of "+path, f.name)
			}
		}
		for _, s := range c.sub {
			walk(s, strings.TrimSpace(path+" "+s.name))
		}
	}
	walk(root(), "")
	for shell, s := range scripts {
		for _, hidden := range []string{"session-start", "plugin-version", "from-runner", "log-offset", "log-inode"} {
			if strings.Contains(s, hidden) {
				t.Errorf("%s offers the hidden %q", shell, hidden)
			}
		}
	}
}

// TestCompletionBashBehaviour sources the bash script into the system
// bash (3.2 on macOS) and asks it for completions.
func TestCompletionBashBehaviour(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not found")
	}
	s, err := completionScript("bash")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "paritydesk.bash")
	if err := os.WriteFile(script, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	driver := `source "$1"
shift
COMP_WORDS=("$@")
COMP_CWORD=$(( ${#COMP_WORDS[@]} - 1 ))
_paritydesk
printf '%s\n' "${COMPREPLY[@]}"
`
	tests := []struct {
		words []string
		want  string
	}{
		{[]string{"paritydesk", "pro"}, "profile"},
		{[]string{"paritydesk", "profile", "re"}, "remove rename"},
		{[]string{"paritydesk", "--json", "--config", "x", "profile", "l"}, "list"},
		{[]string{"paritydesk", "switch", "--only", "sessions,env"}, "sessions,environments"},
		{[]string{"paritydesk", "switch", "--only", "sessions,order,pi"}, "sessions,order,pins"},
		// The last value flag of a command (here --skip) is matched too,
		// although the flag lists come on two lines.
		{[]string{"paritydesk", "switch", "--skip", "sessions,order,pi"}, "sessions,order,pins"},
		{[]string{"paritydesk", "doctor", "--bundle-logs", "5", "--gr"}, "--grant-automation"},
		{[]string{"paritydesk", "doctor", "--bun"}, "--bundle --bundle-logs"},
		{[]string{"paritydesk", "completion", ""}, "bash fish zsh"},
		{[]string{"paritydesk", "help", "sw"}, "switch"},
	}
	for _, tc := range tests {
		args := append([]string{"-c", driver, "driver", script}, tc.words...)
		cmd := exec.Command(bash, args...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", tc.words, err, out)
		}
		if got := strings.Join(strings.Fields(string(out)), " "); got != tc.want {
			t.Errorf("%v: got %q, want %q", tc.words, got, tc.want)
		}
	}
}

// TestCompletionZshHelpers checks the zsh script's syntax and its tables
// with a stub compdef.
func TestCompletionZshHelpers(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh not found")
	}
	s, err := completionScript("zsh")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "_paritydesk")
	if err := os.WriteFile(script, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(zsh, "-n", script).CombinedOutput(); err != nil {
		t.Fatalf("zsh -n: %v\n%s", err, out)
	}
	cmd := exec.Command(zsh, "-f", "-c", `compdef() { :; }; source "$1"; _paritydesk_subs profile; print -l $reply; _paritydesk_args switch; print $REPLY`, "driver", script)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"add:Add an account", "remove:", "alias"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("zsh helpers lack %q:\n%s", want, out)
		}
	}
}

func TestCompletionFishSyntax(t *testing.T) {
	fish, err := exec.LookPath("fish")
	if err != nil {
		t.Skip("fish not found")
	}
	s, err := completionScript("fish")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "paritydesk.fish")
	if err := os.WriteFile(script, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(fish, "--no-execute", script).CombinedOutput(); err != nil {
		t.Fatalf("fish --no-execute: %v\n%s", err, out)
	}
}

func TestAliasesCommand(t *testing.T) {
	h := newHarness(t)
	if code, out, _ := h.run("", aliasesCommand); code != 0 || out != "" {
		t.Fatalf("no config: code %d out %q", code, out)
	}
	cfgDir := filepath.Join(h.home, ".config", "paritydesk")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "version = 1\n[[profile]]\nalias = \"work\"\n[[profile]]\nalias = \"home\"\n[[profile]]\nalias = \"Bad Alias\"\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, _ := h.run("", aliasesCommand)
	if code != 0 || out != "work\nhome\n" {
		t.Fatalf("code %d out %q", code, out)
	}
	if code, _, _ := h.run("", aliasesCommand, "extra", "--bogus"); code != 0 {
		t.Fatalf("a hidden lenient command failed: %d", code)
	}
}
