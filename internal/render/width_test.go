package render

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"

	"pgregory.net/rapid"

	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/redact"
)

// Display width of user data (session titles and labels can be in any
// script). Non-Latin samples are written as escapes.

// W1
func TestRuneWidth(t *testing.T) {
	tests := []struct {
		r    rune
		want int
	}{
		{'a', 1}, {'\uac09', 2}, {'\uac0a', 2}, {'\uffa1', 1}, {'\uff21', 2}, {'\u2026', 1},
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
	alias := "\uac00\uac01\uac02\uac03\uac04"
	tests := []struct {
		s     string
		n     int
		ascii bool
		want  string
	}{
		{alias, 6, false, "\uac00\uac01\u2026"},
		{alias, 5, false, "\uac00\uac01\u2026"},
		{"ab\uac0a", 3, false, "ab\u2026"},
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
var mixedRunes = []rune("aZ09 -\uac05\uac06\uac07\uac08\uff21\uff22\u0301\uac09")

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
	got := cut.String()
	if strings.Contains(got, home) {
		t.Errorf("the home path is not hashed: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != 300 {
		t.Errorf("%d runes, want 300", n)
	}
}

// Render sanitizes arguments but never the format.
func TestRenderSanitizesArgs(t *testing.T) {
	got := (&Printer{}).Render(i18n.Tx(i18n.StatusActive, "a\x1b[2Jb"), 0)
	if got != "Active profile: ab" {
		t.Errorf("Render = %q", got)
	}
}
