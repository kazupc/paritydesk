package render

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"pgregory.net/rapid"

	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/redact"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// W1
func TestRuneWidth(t *testing.T) {
	tests := []struct {
		r    rune
		want int
	}{
		{'a', 1}, {'\u30a2', 2}, {'\u6f22', 2}, {'\uff71', 1}, {'\uff21', 2}, {'\u2026', 1},
		{'\u0301', 0}, {'\u200d', 0}, {'\ufe0f', 0}, {'\u3000', 2},
	}
	for _, tc := range tests {
		if got := RuneWidth(tc.r); got != tc.want {
			t.Errorf("RuneWidth(U+%04X) = %d, want %d", tc.r, got, tc.want)
		}
	}
}

// W2
func TestTruncateWidth(t *testing.T) {
	alias := "\u30a8\u30a4\u30ea\u30a2\u30b9"
	tests := []struct {
		s     string
		n     int
		ascii bool
		want  string
	}{
		{alias, 6, false, "\u30a8\u30a4\u2026"},
		{alias, 5, false, "\u30a8\u30a4\u2026"},
		{"ab\u6f22", 3, false, "ab\u2026"},
		{"abcdef", 5, true, "ab..."},
		{alias, 0, false, alias},
		{alias, -1, false, alias},
	}
	for _, tc := range tests {
		if got := TruncateWidth(tc.s, tc.n, tc.ascii); got != tc.want {
			t.Errorf("TruncateWidth(%q, %d, %v) = %q, want %q", tc.s, tc.n, tc.ascii, got, tc.want)
		}
	}
}

// mixedRunes are the runes of the width properties.
var mixedRunes = []rune("aZ09 -\u3042\u3093\u6f22\u5b57\uff21\uff22\u0301\u30a2")

// W3 (property)
func TestTruncateWidthProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		s := rapid.StringOf(rapid.SampledFrom(mixedRunes)).Draw(rt, "s")
		n := rapid.IntRange(1, 40).Draw(rt, "n")
		ascii := rapid.Bool().Draw(rt, "ascii")
		got := TruncateWidth(s, n, ascii)
		if Width(got) > n {
			rt.Fatalf("Width(%q) = %d > %d", got, Width(got), n)
		}
		if Width(s) <= n {
			if got != s {
				rt.Fatalf("%q fits but became %q", s, got)
			}
			return
		}
		if ascii && n <= 3 {
			if got != strings.Repeat(".", n) {
				rt.Fatalf("n=%d gives %q", n, got)
			}
			return
		}
		base := strings.TrimSuffix(got, "...")
		if !ascii {
			base = strings.TrimSuffix(got, "\u2026")
		}
		if !strings.HasPrefix(s, base) {
			rt.Fatalf("%q is not a prefix of %q", base, s)
		}
	})
}

// W4 (property)
func TestLayoutRowProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		cols := rapid.IntRange(2, 5).Draw(rt, "cols")
		cells := make([]string, cols)
		widths := make([]int, cols)
		for j := range cells {
			cells[j] = rapid.StringOf(rapid.SampledFrom(mixedRunes)).Draw(rt, "cell")
			widths[j] = rapid.IntRange(4, 20).Draw(rt, "width")
		}
		out := layoutRow(cells, widths, false)
		for j := 0; j < cols-1; j++ {
			// A wide rune may leave the truncated cell one column short;
			// padding fills it, so the next column starts in step.
			if Width(out[j]) != widths[j] {
				rt.Fatalf("column %d: width %d, want %d (%q)", j, Width(out[j]), widths[j], out[j])
			}
		}
	})
}

// golden compares got with testdata/<name>, rewriting it with -update.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", filepath.FromSlash(name))
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs:\n--- got\n%s\n--- want\n%s", path, got, want)
	}
}

// W5: the Japanese headers of profile list align (fake rows).
func TestTableJapaneseHeaders(t *testing.T) {
	var out bytes.Buffer
	p := &Printer{Out: &out, Err: &out, Mode: Mode{NoColor: true}}
	h := []string{}
	for _, id := range []i18n.Msg{i18n.ColAlias, i18n.ColFolder, i18n.ColActive, i18n.ColAccount, i18n.ColManaged, i18n.ColBase, i18n.ColProblem} {
		h = append(h, i18n.TIn(i18n.Japanese, id))
	}
	yes, no, valid := i18n.TIn(i18n.Japanese, i18n.Yes), i18n.TIn(i18n.Japanese, i18n.No), i18n.TIn(i18n.Japanese, i18n.Valid)
	p.Table(h, [][]string{
		{"work", "Claude", yes, "0000\u2026", no, valid, ""},
		{"home", "Claude-home", no, "0000\u2026", no, valid, i18n.TIn(i18n.Japanese, i18n.ProfileProblemFolderMissing)},
	})
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	col := func(line string, cell int) int {
		// The display column where the cell-th cell starts.
		w, n, prevSpace := 0, 0, false
		for _, r := range line {
			if r != ' ' && prevSpace && n < cell {
				n++
				if n == cell {
					return w
				}
			}
			prevSpace = r == ' '
			w += RuneWidth(r)
		}
		return -1
	}
	if a, b := col(lines[0], 1), col(lines[1], 1); a != b {
		t.Errorf("the folder column starts at %d and %d", a, b)
	}
	golden(t, "ja/table.golden", out.String())
}

// W6: a Japanese error frame; the anchor stays; JSON stays English.
func TestErrorJapanese(t *testing.T) {
	defer i18n.SetLang(i18n.SetLang(i18n.Japanese))
	var out bytes.Buffer
	p := &Printer{Out: &out, Err: &out, Mode: Mode{NoColor: true}}
	breaker := pderr.NewT(pderr.DeletionBreaker, "plan/validate", i18n.ErrValidateBreaker, 5, "sessions", 3, 8)
	p.Error(breaker)
	got := out.String()
	for _, want := range []string{
		"paritydesk: " + i18n.TIn(i18n.Japanese, pderr.TitleID(pderr.DeletionBreaker)) + " (PD-E22)",
		"  " + i18n.TIn(i18n.Japanese, i18n.ErrValidateBreaker, 5, "sessions", 3, 8),
		i18n.TIn(i18n.Japanese, i18n.ErrSafe, i18n.TIn(i18n.Japanese, pderr.SafeID(pderr.DeletionBreaker))),
		"docs/if-something-goes-wrong.md#deletion-breaker",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	golden(t, "ja/error_breaker.golden", got)
	out.Reset()
	p.Error(pderr.Wrap(pderr.Usage, "profile add", i18n.NewErr(i18n.ErrIDsAlias)))
	if got := out.String(); !strings.Contains(got, "  "+i18n.TIn(i18n.Japanese, i18n.ErrIDsAlias)+"\n") || strings.Contains(got, "profile add:") {
		t.Errorf("sentinel detail:\n%s", got)
	}
	// A pderr.Wrap around a NewT (the apply path: exit 40 wraps the
	// applier's "folder in use") shows the inner Japanese text, once,
	// without the op prefixes.
	inUse := pderr.NewT(pderr.Internal, "apply", i18n.ErrApplyInUse, "Local Storage")
	for _, wrapped := range []error{
		pderr.Wrap(pderr.ApplyRolledBack, "apply", inUse),
		pderr.Wrap(pderr.ApplyRolledBack, "apply", fmt.Errorf("step: %w", inUse)),
		pderr.Wrap(pderr.ApplyRolledBack, "apply", errors.Join(errors.New("other"), inUse)),
	} {
		out.Reset()
		p.Error(wrapped)
		got := out.String()
		if !strings.Contains(got, "  "+i18n.TIn(i18n.Japanese, i18n.ErrApplyInUse, "Local Storage")+"\n") ||
			strings.Contains(got, "apply:") || strings.Contains(got, "in use") {
			t.Errorf("wrapped NewT detail:\n%s", got)
		}
	}
	out.Reset()
	p.Error(pderr.Wrap(pderr.ApplyRolledBack, "apply", errors.New("disk full")))
	if got := out.String(); !strings.Contains(got, "  apply: disk full\n") {
		t.Errorf("a wrapped foreign error keeps the English message:\n%s", got)
	}
	out.Reset()
	jp := &Printer{Out: &out, Err: &out, Mode: Mode{JSON: true}}
	jp.Error(breaker)
	if got := out.String(); !strings.Contains(got, `"title":"The plan would delete more than the configured limit."`) ||
		!strings.Contains(got, `"message":"5 deletions in sessions exceed the limit (3 of 8 items)"`) {
		t.Errorf("JSON error is not English: %s", got)
	}
	i18n.SetLang(i18n.English)
	out.Reset()
	p.Error(breaker)
	if !strings.Contains(out.String(), "  5 deletions in sessions exceed the limit (3 of 8 items)\n") {
		t.Errorf("English detail changed:\n%s", out.String())
	}
	out.Reset()
	p.Error(errors.New("plain"))
	if !strings.Contains(out.String(), "  plain\n") {
		t.Errorf("plain error detail:\n%s", out.String())
	}
}

// W7: Japanese plan lines keep DESTRUCTIVE: first on the last line.
func TestPlanLinesIn(t *testing.T) {
	sum := plan.Summary{Categories: map[model.Category]plan.Counts{model.CatSessions: {Added: 2, Deleted: 1}},
		Destructive: map[model.Category]int{model.CatSessions: 1}}
	en := PlanLinesIn(i18n.English, PlanView{From: "a", To: "b\x1b[31m", Summary: sum})
	ja := PlanLinesIn(i18n.Japanese, PlanView{From: "a", To: "b\x1b[31m", Summary: sum})
	if len(en) != len(ja) || ja[0] == en[0] {
		t.Fatalf("en %q ja %q", en, ja)
	}
	if !strings.HasPrefix(ja[len(ja)-1], "DESTRUCTIVE: ") {
		t.Errorf("last line %q", ja[len(ja)-1])
	}
	if strings.Contains(ja[0], "\x1b") {
		t.Errorf("alias not sanitized: %q", ja[0])
	}
	if got := strings.Join(PlanLines(PlanView{From: "a", To: "b", Summary: sum}), "\n"); got != strings.Join(en, "\n") {
		t.Errorf("PlanLines in the English process differs from PlanLinesIn(English)")
	}
}

// W8: FreeLast prints a long last cell whole; the others still fit.
func TestTableFreeLast(t *testing.T) {
	long := strings.Repeat("x", 120)
	var out bytes.Buffer
	p := &Printer{Out: &out, Mode: Mode{NoColor: true}}
	rows := [][]string{{"check-id", "ok", long}}
	p.TableWith([]string{"CHECK", "STATUS", "DETAIL"}, rows, TableOptions{FreeLast: true})
	if !strings.Contains(out.String(), long) {
		t.Errorf("the last cell was cut:\n%s", out.String())
	}
	if !strings.HasPrefix(strings.Split(out.String(), "\n")[1], "check-id  ok      ") {
		t.Errorf("the other columns do not line up:\n%s", out.String())
	}
	out.Reset()
	p.Table([]string{"CHECK", "STATUS", "DETAIL"}, rows)
	if strings.Contains(out.String(), long) {
		t.Errorf("without FreeLast the cell is not cut:\n%s", out.String())
	}
}

// W9: redaction and the 300-rune bound survive late rendering.
func TestRenderRedactsArgs(t *testing.T) {
	home := "/Users/someone"
	red := redact.Redactor{Home: home}
	tx := i18n.Tx(i18n.DoctorScratchKept, 2, strings.Repeat("z", 400), home+"/Library/x")
	cut := tx.MapStrings(red.String).MapStrings(func(s string) string { return Sanitize(s, 0) }).Cut(300)
	for _, l := range i18n.Langs {
		got := cut.In(l)
		if strings.Contains(got, home) {
			t.Errorf("%s: the home path is not hashed: %q", l, got)
		}
		if n := utf8.RuneCountInString(got); n != 300 {
			t.Errorf("%s: %d runes, want 300", l, n)
		}
	}
}

// RenderIn sanitizes arguments but never the format.
func TestRenderSanitizesArgs(t *testing.T) {
	got := RenderIn(i18n.English, i18n.Tx(i18n.StatusActive, "a\x1b[2Jb"), 0)
	if got != "Active profile: ab" {
		t.Errorf("RenderIn = %q", got)
	}
}
