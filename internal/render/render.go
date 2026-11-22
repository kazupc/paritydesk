package render

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/pderr"
)

// SchemaVersion is the "v" field of every JSON document and NDJSON line.
const SchemaVersion = 1

// TableWidth is the width tables are fitted to.
const TableWidth = 80

// Mode selects the output style.
type Mode struct {
	JSON         bool // one JSON document on stdout
	JSONProgress bool // NDJSON progress events on stderr
	Plain        bool // no tables, no spinners
	NoColor      bool // --no-color, NO_COLOR, TERM=dumb
	ASCII        bool // --ascii, or automatic when the locale is not UTF-8
	TTY          bool // stdout is a terminal
}

// DetectMode derives the mode from flags and the environment: NO_COLOR,
// TERM=dumb, a non-terminal stdout and JSON output turn colour off;
// TERM=dumb and a locale that is not UTF-8 turn on ASCII output.
func DetectMode(flags Mode, getenv func(string) string, stdoutIsTTY bool) Mode {
	m := flags
	m.TTY = stdoutIsTTY
	dumb := getenv("TERM") == "dumb"
	if getenv("NO_COLOR") != "" || dumb || !stdoutIsTTY || m.JSON {
		m.NoColor = true
	}
	if dumb || !utf8Locale(getenv) {
		m.ASCII = true
	}
	return m
}

// utf8Locale reports whether the effective locale (LC_ALL, then LC_CTYPE,
// then LANG) names UTF-8. An unset locale is the C locale, which is not.
func utf8Locale(getenv func(string) string) bool {
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := getenv(k); v != "" {
			v = strings.ToLower(v)
			return strings.Contains(v, "utf-8") || strings.Contains(v, "utf8")
		}
	}
	return false
}

// Printer writes output in one mode.
type Printer struct {
	Out, Err io.Writer
	Mode     Mode
	// Redact, if set, is applied to error messages in JSON mode (home
	// paths hashed, ids masked), so a --json document can be attached to
	// an issue as it is.
	Redact func(string) string
}

// JSON writes v as one JSON document with a schema version field ("v")
// first. v must encode as a JSON object.
func (p *Printer) JSON(v any) error {
	b, err := Document(v)
	if err != nil {
		return err
	}
	_, err = p.Out.Write(b)
	return err
}

// Document encodes v as one JSON line with "v":SchemaVersion as its first
// field. HTML characters are not escaped.
func Document(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("render: encode json: %w", err)
	}
	b := bytes.TrimSpace(buf.Bytes())
	if len(b) < 2 || b[0] != '{' {
		return nil, errors.New("render: json document must be an object")
	}
	head := fmt.Sprintf(`{"v":%d`, SchemaVersion)
	if string(b) == "{}" {
		return []byte(head + "}\n"), nil
	}
	out := make([]byte, 0, len(b)+len(head)+2)
	out = append(out, head...)
	out = append(out, ',')
	out = append(out, b[1:]...)
	return append(out, '\n'), nil
}

// Line writes one human line. format is a trusted, localized string;
// every string argument is sanitized. In JSON mode human lines go to Err
// so that Out carries only the JSON document.
func (p *Printer) Line(format string, args ...any) {
	clean := make([]any, len(args))
	for i, a := range args {
		switch v := a.(type) {
		case string:
			clean[i] = Sanitize(v, 0)
		case fmt.Stringer:
			clean[i] = Sanitize(v.String(), 0)
		default:
			clean[i] = a
		}
	}
	w := p.Out
	if p.Mode.JSON {
		w = p.Err
	}
	fmt.Fprintln(w, p.fold(fmt.Sprintf(format, clean...)))
}

// Lines writes each line verbatim (after sanitizing).
func (p *Printer) Lines(lines []string) {
	for _, l := range lines {
		p.Line("%s", l)
	}
}

// Warn writes a warning line to Err.
func (p *Printer) Warn(msg string) {
	fmt.Fprintln(p.Err, p.fold(i18n.T(i18n.Warning, Sanitize(msg, 0))))
}

// fold replaces non-ASCII runes in ASCII mode.
func (p *Printer) fold(s string) string {
	if !p.Mode.ASCII {
		return s
	}
	return ASCII(s)
}

// ASCII folds s to printable ASCII: the ellipsis becomes "...", arrows
// become "->", and any other non-ASCII rune becomes "?".
func ASCII(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < utf8.RuneSelf:
			b.WriteRune(r)
		case r == '\u2026':
			b.WriteString("...")
		case r == '\u2192':
			b.WriteString("->")
		case r == '\u2190':
			b.WriteString("<-")
		default:
			b.WriteByte('?')
		}
	}
	return b.String()
}

// Table writes rows readable at TableWidth columns; in Plain mode, one
// tab-separated line per row (header first). Cells are sanitized and
// tabs inside cells become spaces.
func (p *Printer) Table(header []string, rows [][]string) {
	all := make([][]string, 0, len(rows)+1)
	all = append(all, header)
	all = append(all, rows...)
	clean := make([][]string, len(all))
	for i, r := range all {
		clean[i] = make([]string, len(header))
		for j := range header {
			if j < len(r) {
				clean[i][j] = p.fold(strings.ReplaceAll(Sanitize(r[j], 0), "\t", " "))
			}
		}
	}
	w := p.Out
	if p.Mode.JSON {
		w = p.Err
	}
	if p.Mode.Plain {
		for _, r := range clean {
			fmt.Fprintln(w, strings.Join(r, "\t"))
		}
		return
	}
	widths := fitWidths(columnWidths(clean), TableWidth)
	for _, r := range clean {
		fmt.Fprintln(w, formatRow(r, widths, p.Mode.ASCII))
	}
}

func columnWidths(rows [][]string) []int {
	var widths []int
	for _, r := range rows {
		for j, c := range r {
			if j >= len(widths) {
				widths = append(widths, 0)
			}
			widths[j] = max(widths[j], utf8.RuneCountInString(c))
		}
	}
	return widths
}

// fitWidths shrinks the widest columns until the row fits total columns
// (two spaces between columns), never below 4.
func fitWidths(widths []int, total int) []int {
	out := append([]int(nil), widths...)
	sum := 2 * max(len(out)-1, 0)
	for _, w := range out {
		sum += w
	}
	for sum > total {
		widest := 0
		for i, w := range out {
			if w > out[widest] {
				widest = i
			}
		}
		if out[widest] <= 4 {
			break
		}
		out[widest]--
		sum--
	}
	return out
}

func formatRow(cells []string, widths []int, ascii bool) string {
	var b strings.Builder
	for j, c := range cells {
		c = truncate(c, widths[j], ascii)
		b.WriteString(c)
		if j < len(cells)-1 {
			b.WriteString(strings.Repeat(" ", widths[j]-utf8.RuneCountInString(c)+2))
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// truncate cuts s to n runes with an ellipsis.
func truncate(s string, n int, ascii bool) string {
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	if ascii {
		if n <= 3 {
			return strings.Repeat(".", n)
		}
		return string(r[:n-3]) + "..."
	}
	return string(r[:n-1]) + "\u2026"
}

// Error writes an error with its catalog entry (PD-E<code>, what is safe,
// the next command, the docs anchor). For a *pderr.Error with a message,
// the message (already validated, for example carrying an alias for exit
// 51) is printed; otherwise the sanitized error text. In JSON mode the
// error is one JSON document on Out, its message passed through Redact.
func (p *Printer) Error(err error) {
	if err == nil {
		return
	}
	v := NewErrorView(err)
	if p.Mode.JSON {
		if p.Redact != nil {
			v.Message = p.Redact(v.Message)
		}
		_ = p.JSON(struct {
			Error ErrorView `json:"error"`
		}{v})
		return
	}
	head := i18n.T(i18n.ErrHeadline, v.Title, v.ID)
	if !p.Mode.NoColor {
		head = "\x1b[1;31m" + head + "\x1b[0m"
	}
	fmt.Fprintln(p.Err, p.fold(head))
	if v.Message != "" {
		fmt.Fprintln(p.Err, p.fold(i18n.T(i18n.ErrDetail, v.Message)))
	}
	if v.Safe != "" {
		fmt.Fprintln(p.Err, p.fold(i18n.T(i18n.ErrSafe, v.Safe)))
	}
	if v.Next != "" {
		fmt.Fprintln(p.Err, p.fold(i18n.T(i18n.ErrNext, v.Next)))
	}
	if v.Anchor != "" {
		fmt.Fprintln(p.Err, p.fold(i18n.T(i18n.ErrMore, v.Anchor)))
	}
}

// ErrorView is the JSON form of an error.
type ErrorView struct {
	Code    int    `json:"code"`
	ID      string `json:"id"`
	Title   string `json:"title"`
	Message string `json:"message,omitempty"`
	Safe    string `json:"safe,omitempty"`
	Next    string `json:"next,omitempty"`
	Anchor  string `json:"anchor,omitempty"`
}

// MaxErrorText bounds the error text printed with a catalog entry.
const MaxErrorText = 400

// NewErrorView builds the view of err from its catalog entry.
func NewErrorView(err error) ErrorView {
	code := pderr.CodeOf(err)
	e, ok := pderr.Lookup(code)
	if !ok {
		e, _ = pderr.Lookup(pderr.Internal)
	}
	v := ErrorView{Code: int(code), ID: pderr.ID(code), Title: e.Title, Safe: e.Safe, Next: e.Next, Anchor: e.Anchor}
	var pe *pderr.Error
	if errors.As(err, &pe) && pe.Msg != "" {
		v.Message = Sanitize(pe.Msg, MaxErrorText)
	} else {
		v.Message = Sanitize(err.Error(), MaxErrorText)
	}
	return v
}
