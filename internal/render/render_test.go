package render

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/plan"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDetectMode(t *testing.T) {
	utf := map[string]string{"LANG": "en_US.UTF-8", "TERM": "xterm-256color"}
	tests := []struct {
		name      string
		flags     Mode
		env       map[string]string
		tty       bool
		noColor   bool
		ascii     bool
		wantPlain bool
	}{
		{"tty utf8", Mode{}, utf, true, false, false, false},
		{"NO_COLOR", Mode{}, map[string]string{"LANG": "en_US.UTF-8", "NO_COLOR": "1"}, true, true, false, false},
		{"TERM=dumb", Mode{}, map[string]string{"LANG": "en_US.UTF-8", "TERM": "dumb"}, true, true, true, false},
		{"C locale", Mode{}, map[string]string{"LANG": "C"}, true, false, true, false},
		{"no locale", Mode{}, map[string]string{}, true, false, true, false},
		{"LC_ALL wins", Mode{}, map[string]string{"LC_ALL": "POSIX", "LANG": "en_US.UTF-8"}, true, false, true, false},
		{"LC_CTYPE utf8", Mode{}, map[string]string{"LC_CTYPE": "UTF-8"}, true, false, false, false},
		{"not a tty", Mode{}, utf, false, true, false, false},
		{"--no-color", Mode{NoColor: true}, utf, true, true, false, false},
		{"--ascii", Mode{ASCII: true}, utf, true, false, true, false},
		{"--json", Mode{JSON: true}, utf, true, true, false, false},
		{"--plain", Mode{Plain: true}, utf, true, false, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := DetectMode(tc.flags, env(tc.env), tc.tty)
			if m.NoColor != tc.noColor || m.ASCII != tc.ascii || m.Plain != tc.wantPlain || m.TTY != tc.tty {
				t.Fatalf("got %+v", m)
			}
		})
	}
}

func TestSanitizeHostile(t *testing.T) {
	long := strings.Repeat("x", 500)
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"plain", "Fix the build", 0, "Fix the build"},
		{"tab kept", "a\tb", 0, "a\tb"},
		{"newline and CR", "line1\r\nline2", 0, "line1line2"},
		{"ANSI colour", "\x1b[31mred\x1b[0m", 0, "red"},
		{"cursor movement", "ok\x1b[2K\x1b[1Ahidden", 0, "okhidden"},
		{"OSC 52 clipboard BEL", "a\x1b]52;c;ZWNobyBwd25lZA==\x07b", 0, "ab"},
		{"OSC 8 link ST", "\x1b]8;;http://x\x1b\\text\x1b]8;;\x1b\\", 0, "text"},
		{"OSC unterminated", "safe\x1b]0;title", 0, "safe"},
		{"DCS", "a\x1bPq#0;2;0;0;0\x1b\\b", 0, "ab"},
		{"charset escape", "a\x1b(Bb", 0, "ab"},
		{"bare ESC at end", "a\x1b", 0, "a"},
		{"8-bit CSI", "a\u009b31mb", 0, "ab"},
		{"8-bit OSC", "a\u009d52;c;Zm9v\u0007b", 0, "ab"},
		{"C1 controls", "a\u0085b\u008dc", 0, "abc"},
		{"bidi override", "evil\u202egnp.exe", 0, "evilgnp.exe"},
		{"bidi isolates", "\u2066a\u2067b\u2068c\u2069", 0, "abc"},
		{"bidi marks", "a\u200eb\u200fc\u061c", 0, "abc"},
		{"DEL and NUL", "a\x7fb\x00c", 0, "abc"},
		{"invalid utf8", "a\xffb\xc3", 0, "a\ufffdb\ufffd"},
		{"valid non-ASCII kept", "caf\u00e9", 0, "caf\u00e9"},
		{"truncated", "abcdef", 4, "abc\u2026"},
		{"exact length", "abcd", 4, "abcd"},
		{"very long", long, 80, strings.Repeat("x", 79) + "\u2026"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Sanitize(tc.in, tc.max)
			if got != tc.want {
				t.Fatalf("Sanitize(%q) = %q, want %q", tc.in, got, tc.want)
			}
			assertClean(t, got)
		})
	}
}

// assertClean fails if s still holds anything a terminal would interpret.
func assertClean(t *testing.T, s string) {
	t.Helper()
	if !utf8.ValidString(s) {
		t.Fatalf("invalid UTF-8 in %q", s)
	}
	for _, r := range s {
		if dropRune(r) {
			t.Fatalf("control rune %U survived in %q", r, s)
		}
	}
}

func FuzzSanitize(f *testing.F) {
	for _, s := range []string{"\x1b[31mx", "\x1b]52;c;x\x07", "\u202e", "\xff", "a\tb"} {
		f.Add(s, 10)
	}
	f.Fuzz(func(t *testing.T, s string, max int) {
		max %= 200
		got := Sanitize(s, max)
		assertClean(t, got)
		if max > 0 && utf8.RuneCountInString(got) > max {
			t.Fatalf("not truncated to %d: %q", max, got)
		}
	})
}

func TestPrinterLineSanitizesAndFoldsASCII(t *testing.T) {
	var out bytes.Buffer
	p := &Printer{Out: &out, Err: &out, Mode: Mode{ASCII: true}}
	p.Line("title: %s (%d)", "\x1b[2Jsecret\u2026\u202e", 3)
	if got := out.String(); got != "title: secret... (3)\n" {
		t.Fatalf("got %q", got)
	}
}

func TestPrinterLineGoesToErrInJSONMode(t *testing.T) {
	var out, errb bytes.Buffer
	p := &Printer{Out: &out, Err: &errb, Mode: Mode{JSON: true}}
	p.Line("hello")
	if out.Len() != 0 || errb.String() != "hello\n" {
		t.Fatalf("out %q err %q", out.String(), errb.String())
	}
}

func TestJSONAddsVersion(t *testing.T) {
	tests := []struct {
		in   any
		want string
	}{
		{struct {
			A int `json:"a"`
		}{1}, `{"v":1,"a":1}`},
		{struct{}{}, `{"v":1}`},
		{map[string]string{"x": "<y>"}, `{"v":1,"x":"<y>"}`},
	}
	for _, tc := range tests {
		var out bytes.Buffer
		p := &Printer{Out: &out}
		if err := p.JSON(tc.in); err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(out.String()); got != tc.want {
			t.Errorf("got %s want %s", got, tc.want)
		}
	}
	var out bytes.Buffer
	if err := (&Printer{Out: &out}).JSON([]int{1}); err == nil {
		t.Error("an array must be refused")
	}
}

func TestTable(t *testing.T) {
	header := []string{"ALIAS", "FOLDER", "PROBLEM"}
	rows := [][]string{
		{"work", "Claude", ""},
		{"home", "Claude-home", strings.Repeat("very long problem text ", 10)},
		{"evil\x1b[31m", "Claude-x\ty", "\u202eabc"},
	}
	t.Run("fits 80 columns", func(t *testing.T) {
		var out bytes.Buffer
		p := &Printer{Out: &out, Mode: Mode{}}
		p.Table(header, rows)
		lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
		if len(lines) != 4 {
			t.Fatalf("lines %q", lines)
		}
		for _, l := range lines {
			if n := utf8.RuneCountInString(l); n > TableWidth {
				t.Errorf("line of %d columns: %q", n, l)
			}
			assertClean(t, l)
		}
		if !strings.HasPrefix(lines[0], "ALIAS  FOLDER") || !strings.Contains(lines[2], "\u2026") {
			t.Errorf("unexpected table:\n%s", out.String())
		}
	})
	t.Run("plain is tab separated", func(t *testing.T) {
		var out bytes.Buffer
		p := &Printer{Out: &out, Mode: Mode{Plain: true}}
		p.Table(header, rows[:1])
		if got := out.String(); got != "ALIAS\tFOLDER\tPROBLEM\nwork\tClaude\t\n" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("ascii ellipsis", func(t *testing.T) {
		var out bytes.Buffer
		p := &Printer{Out: &out, Mode: Mode{ASCII: true}}
		p.Table(header, rows[1:2])
		if strings.ContainsRune(out.String(), '\u2026') || !strings.Contains(out.String(), "...") {
			t.Fatalf("got %q", out.String())
		}
	})
}

func TestErrorPrintsCatalogEntry(t *testing.T) {
	var out, errb bytes.Buffer
	p := &Printer{Out: &out, Err: &errb, Mode: Mode{NoColor: true}}
	p.Error(pderr.New(pderr.NeedsSignIn, "verify", "sign in with work's account only"))
	got := errb.String()
	for _, want := range []string{"(PD-E51)", "sign in with work's account only", "Safe now:", "Next:", "#needs-sign-in"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if out.Len() != 0 {
		t.Errorf("stdout %q", out.String())
	}

	errb.Reset()
	p.Error(fmt.Errorf("wrapped: %w", errors.New("boom\x1b]52;c;x\x07")))
	if !strings.Contains(errb.String(), "(PD-E70)") || strings.Contains(errb.String(), "\x1b") {
		t.Errorf("internal error rendering: %q", errb.String())
	}
}

func TestErrorJSON(t *testing.T) {
	var out bytes.Buffer
	p := &Printer{Out: &out, Err: &out, Mode: Mode{JSON: true}}
	p.Error(pderr.New(pderr.DeletionBreaker, "switch/P5", "too many deletions"))
	var doc struct {
		V     int       `json:"v"`
		Error ErrorView `json:"error"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("%v: %q", err, out.String())
	}
	if doc.V != 1 || doc.Error.Code != 22 || doc.Error.ID != "PD-E22" || doc.Error.Anchor != "deletion-breaker" {
		t.Fatalf("got %+v", doc)
	}
}

func TestPlanLinesDestructiveLast(t *testing.T) {
	sum := plan.Summary{
		Categories: map[model.Category]plan.Counts{
			model.CatSessions: {Added: 2, Updated: 3, Deleted: 1, Revived: 1},
			model.CatPins:     {Moved: 1, Deleted: 2},
			model.CatSidebar:  {Conflicts: 1},
		},
		Destructive: map[model.Category]int{model.CatSessions: 1, model.CatPins: 2},
	}
	lines := PlanLines(PlanView{From: "home", To: "work", Approximate: true, FirstSync: true, Summary: sum})
	want := []string{
		"Plan: home to work (approximate until Claude quits)",
		"First sync: the data of both profiles is merged and nothing is deleted.",
		"  sessions      +2 new  ~3 updated  -1 deleted  1 revived",
		"  sidebar       1 conflicts",
		"  pins          -2 deleted  1 moved",
		"DESTRUCTIVE: 3 deletions (sessions 1, pins 2)",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}

	empty := PlanLines(PlanView{From: "a", To: "b"})
	if len(empty) != 3 || empty[2] != "DESTRUCTIVE: none" {
		t.Fatalf("empty plan lines %q", empty)
	}

	var out bytes.Buffer
	(&Printer{Out: &out}).Lines(lines)
	printed := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if !strings.HasPrefix(printed[len(printed)-1], "DESTRUCTIVE:") {
		t.Fatalf("DESTRUCTIVE line is not last: %q", printed)
	}
}

func TestPlanLinesSanitizeAliases(t *testing.T) {
	lines := PlanLines(PlanView{From: "a\x1b[31m", To: "b\u202e"})
	for _, l := range lines {
		assertClean(t, l)
	}
}
