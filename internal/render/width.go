package render

import (
	"strings"
	"unicode"

	"golang.org/x/text/width"
)

// RuneWidth returns the terminal columns of r: 0 for combining marks
// (unicode.Mn, unicode.Me), U+200B, U+200D, U+FE00-U+FE0F and
// U+E0100-U+E01EF; 2 for East Asian Wide and Fullwidth runes; 1 for
// everything else, including East Asian Ambiguous (most macOS terminals
// default to narrow).
func RuneWidth(r rune) int {
	switch {
	case r == 0x200b, r == 0x200d, r >= 0xfe00 && r <= 0xfe0f, r >= 0xe0100 && r <= 0xe01ef:
		return 0
	case unicode.In(r, unicode.Mn, unicode.Me):
		return 0
	}
	switch width.LookupRune(r).Kind() {
	case width.EastAsianWide, width.EastAsianFullwidth:
		return 2
	}
	return 1
}

// Width returns the terminal columns of s.
func Width(s string) int {
	n := 0
	for _, r := range s {
		n += RuneWidth(r)
	}
	return n
}

// TruncateWidth cuts s to at most n columns, with an ellipsis ("\u2026", or
// "..." when ascii) inside the n columns. It never splits a wide rune;
// the result may be one column narrower than n. n <= 0 returns s.
func TruncateWidth(s string, n int, ascii bool) string {
	if n <= 0 || Width(s) <= n {
		return s
	}
	ell, ew := "\u2026", 1
	if ascii {
		ell, ew = "...", 3
	}
	if n <= ew {
		if ascii {
			return strings.Repeat(".", n)
		}
		return ell
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		w := RuneWidth(r)
		if used+w > n-ew {
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String() + ell
}

// PadWidth right-pads s with spaces to n columns (no truncation).
func PadWidth(s string, n int) string {
	if w := Width(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s
}

// layoutRow fits each cell to its column: TruncateWidth, then PadWidth
// for every column but the last.
func layoutRow(cells []string, widths []int, ascii bool) []string {
	out := make([]string, len(cells))
	for j, c := range cells {
		c = TruncateWidth(c, widths[j], ascii)
		if j < len(cells)-1 {
			c = PadWidth(c, widths[j])
		}
		out[j] = c
	}
	return out
}
