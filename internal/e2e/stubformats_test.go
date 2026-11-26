package e2e

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/testkit"
)

// stubSource is the stub app's source, relative to this package.
var stubSource = filepath.Join("..", "..", "testdata", "stubapp", "StubApp.swift")

var (
	mainLogCallRE = regexp.MustCompile(`mainLog\("((?:[^"\\]|\\.)*)"\)`)
	interpRE      = regexp.MustCompile(`\\\(([^)]*)\)`)
)

// stubLogLines returns the messages of the main.log lines the stub can
// write (the text after "[…] [info] "), read from its source, with fake
// values for the interpolated parts.
func stubLogLines(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile(stubSource)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		"account":        testkit.FakeUUID(1),
		"org":            testkit.FakeUUID(2),
		"session":        testkit.FakeSession(1),
		"config.session": testkit.FakeSession(1),
	}
	var lines []string
	for _, m := range mainLogCallRE.FindAllSubmatch(src, -1) {
		line := interpRE.ReplaceAllStringFunc(string(m[1]), func(s string) string {
			if v, ok := values[interpRE.FindStringSubmatch(s)[1]]; ok {
				return v
			}
			return "x"
		})
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		t.Fatal("no mainLog calls found in the stub source")
	}
	return lines
}

// TestStubLogLinesMatchManifest keeps the stub's main.log lines in step
// with the signals of the 6.x manifest: every signal the stub is meant to
// produce matches the message of one of its lines, anchored at the start
// of the message as the app package matches it.
func TestStubLogLinesMatchManifest(t *testing.T) {
	lines := stubLogLines(t)
	sig := testkit.Manifest(t).Signals
	want := map[string]string{
		"quit_vetoed":     sig.QuitVetoed,
		"turn_ended":      sig.TurnEnded,
		"app_started":     sig.AppStarted,
		"focused_session": sig.FocusedSession,
		"unclean_exit":    sig.UncleanExit,
		"active_org":      sig.ActiveOrg,
	}
	for name, pat := range want {
		if pat == "" {
			t.Logf("%s is empty in the manifest; nothing to match", name)
			continue
		}
		re, err := regexp.Compile("^(?:" + strings.ReplaceAll(pat, "{session}", `local_`+ids.UUIDPattern) + ")")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		found := false
		for _, l := range lines {
			if re.MatchString(l) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no stub main.log line matches signal %s", name)
		}
	}
	if !strings.Contains(strings.Join(lines, "\n"), vetoLine) {
		t.Errorf("the stub does not write %q", vetoLine)
	}
}
