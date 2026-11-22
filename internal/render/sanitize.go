package render

import (
	"strings"
	"unicode/utf8"
)

const (
	esc = 0x1b
	bel = 0x07
)

// Sanitize strips C0 and C1 controls (except tab), DEL, bidi overrides,
// embeddings, isolates and marks, and escape sequences (CSI, OSC, DCS,
// SOS, PM, APC and two-character escapes, in their 7-bit and 8-bit
// forms); replaces invalid UTF-8 with U+FFFD; and truncates to max runes
// with an ellipsis. max <= 0 means no truncation (H7).
func Sanitize(s string, max int) string {
	var out []rune
	for i := 0; i < len(s); {
		if s[i] == esc {
			i = skipEscape(s, i+1)
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if r == utf8.RuneError && size <= 1 {
			out = append(out, utf8.RuneError)
			continue
		}
		switch r {
		case 0x9b: // 8-bit CSI
			i = skipCSI(s, i)
			continue
		case 0x90, 0x98, 0x9d, 0x9e, 0x9f: // 8-bit DCS, SOS, OSC, PM, APC
			i = skipString(s, i)
			continue
		}
		if dropRune(r) {
			continue
		}
		out = append(out, r)
	}
	if max > 0 && len(out) > max {
		out = append(out[:max-1], '\u2026')
	}
	return string(out)
}

// dropRune reports whether r is a control or bidi formatting character.
func dropRune(r rune) bool {
	switch {
	case r < 0x20:
		return r != '\t'
	case r == 0x7f, r >= 0x80 && r <= 0x9f:
		return true
	case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
		return true
	case r == 0x200e, r == 0x200f, r == 0x061c:
		return true
	}
	return false
}

// skipEscape skips an escape sequence whose ESC byte precedes i and
// returns the index after it.
func skipEscape(s string, i int) int {
	if i >= len(s) {
		return i
	}
	switch s[i] {
	case '[':
		return skipCSI(s, i+1)
	case ']', 'P', 'X', '^', '_':
		return skipString(s, i+1)
	}
	// Two-character escapes, with optional intermediate bytes (ESC ( B).
	for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
		i++
	}
	if i < len(s) {
		i++
	}
	return i
}

// skipCSI skips parameter and intermediate bytes up to and including the
// final byte (0x40-0x7e).
func skipCSI(s string, i int) int {
	for i < len(s) {
		c := s[i]
		i++
		if c >= 0x40 && c <= 0x7e {
			return i
		}
		if c == esc || c < 0x20 && c != bel {
			// A malformed sequence: stop here and let the caller drop
			// the control byte.
			return i - 1
		}
	}
	return i
}

// skipString skips a control string up to and including its terminator
// (BEL, ESC \ or the 8-bit ST). An unterminated string runs to the end.
func skipString(s string, i int) int {
	for i < len(s) {
		switch {
		case s[i] == bel:
			return i + 1
		case s[i] == esc && i+1 < len(s) && s[i+1] == '\\':
			return i + 2
		case strings.HasPrefix(s[i:], "\u009c"):
			return i + len("\u009c")
		}
		i++
	}
	return i
}
