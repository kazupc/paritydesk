package render

import (
	"io"

	"github.com/kazupc/paritydesk/internal/pderr"
)

// Mode selects the output style.
type Mode struct {
	JSON         bool // one JSON document on stdout
	JSONProgress bool // NDJSON progress events on stderr
	Plain        bool // no tables, no spinners
	NoColor      bool // --no-color, NO_COLOR, TERM=dumb
	ASCII        bool // --ascii, or automatic when the locale is not UTF-8
	TTY          bool // stdout is a terminal
}

// DetectMode derives the mode from flags and the environment.
func DetectMode(flags Mode, getenv func(string) string, stdoutIsTTY bool) Mode {
	return flags
}

// Sanitize strips C0 and C1 controls, bidi overrides and isolates, and
// escape sequences (including OSC), replaces invalid UTF-8, and truncates
// to max runes with an ellipsis. max <= 0 means no truncation.
func Sanitize(s string, max int) string {
	return ""
}

// Printer writes output in one mode.
type Printer struct {
	Out, Err io.Writer
	Mode     Mode
}

// JSON writes v as one JSON document with a schema version field.
func (p *Printer) JSON(v any) error { return pderr.ErrNotImplemented }

// Line writes one human line (already localized; args are sanitized).
func (p *Printer) Line(format string, args ...any) {}

// Table writes rows readable at 80 columns; in Plain mode, tab-separated.
func (p *Printer) Table(header []string, rows [][]string) {}

// Error writes an error with its catalog entry (PD-E<code>, what is safe,
// next command, docs anchor).
func (p *Printer) Error(err error) {}
