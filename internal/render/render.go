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

// JSONWithError writes v as one JSON document with an "error" member
// added last: the same object a failed command prints on its own (see
// Error), so a result that ends with a non-zero code also says why. The
// members of v keep their order.
func (p *Printer) JSONWithError(v any, err error) error {
	b, derr := Document(v)
	if derr != nil {
		return derr
	}
	ev := NewErrorView(err)
	var pe *pderr.Error
	if errors.As(err, &pe) && pe.Msg == "" && pe.Err == nil && ev.Message == pe.Error() {
		// A bare result code carries no detail beyond the catalog entry.
		ev.Message = ""
	}
	if p.Redact != nil {
		ev.Message = p.Redact(ev.Message)
	}
	eb, derr := Document(ev)
	if derr != nil {
		return derr
	}
	// Both documents start with {"v":N; drop the error's schema member.
	eb = bytes.TrimSpace(eb)
	head := fmt.Sprintf(`{"v":%d`, SchemaVersion)
	body := bytes.TrimPrefix(eb, []byte(head))
	body = bytes.TrimPrefix(body, []byte(","))
	body = bytes.TrimSuffix(body, []byte("}"))
	b = bytes.TrimSpace(b)
	out := make([]byte, 0, len(b)+len(body)+16)
	out = append(out, b[:len(b)-1]...)
	out = append(out, `,"error":{`...)
	out = append(out, body...)
	out = append(out, "}}\n"...)
	_, werr := p.Out.Write(out)
	return werr
}

// Line writes one human line. format is a constant format (never catalog
// text: that goes through Msg or Text); every string argument is
// sanitized. In JSON mode human lines go to Err so that Out carries only
// the JSON document.
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
	p.write(fmt.Sprintf(format, clean...))
}

// write writes one finished line: to Out, or to Err in JSON mode.
func (p *Printer) write(s string) {
	w := p.Out
	if p.Mode.JSON {
		w = p.Err
	}
	fmt.Fprintln(w, p.fold(s))
}

// Lines writes each line verbatim (after sanitizing).
func (p *Printer) Lines(lines []string) {
	for _, l := range lines {
		p.Line("%s", l)
	}
}

// Msg prints one line: id in the process language with sanitized args.
func (p *Printer) Msg(id i18n.Msg, args ...any) { p.Text(i18n.Tx(id, args...)) }

// Text prints one line.
func (p *Printer) Text(t i18n.Text) { p.write(p.Render(t, 0)) }

// Texts prints one line per text.
func (p *Printer) Texts(ts []i18n.Text) {
	for _, t := range ts {
		p.Text(t)
	}
}

// Render returns t with every string argument passed through
// Sanitize(s, 0), rendered in the process language, then cut to max
// runes (0: none).
func (p *Printer) Render(t i18n.Text, max int) string {
	return RenderIn(i18n.Current(), t, max)
}

// RenderIn is Render in language l (the JSON values use English).
func RenderIn(l i18n.Lang, t i18n.Text, max int) string {
	s := t.MapStrings(func(s string) string { return Sanitize(s, 0) }).In(l)
	return CutRunes(s, max)
}

// CutRunes cuts s to max runes with an ellipsis; max <= 0 means none.
func CutRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-1]) + "\u2026"
}

// WarnText writes a warning line to Err.
func (p *Printer) WarnText(t i18n.Text) {
	fmt.Fprintln(p.Err, p.fold(i18n.T(i18n.Warning, p.Render(t, 0))))
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

// TableOptions change how Table lays out a table.
type TableOptions struct {
	// FreeLast excludes the last column from fitWidths and never truncates
	// it; the terminal wraps a long cell.
	FreeLast bool
}

// Table writes rows readable at TableWidth columns; in Plain mode, one
// tab-separated line per row (header first). Cells are sanitized and
// tabs inside cells become spaces. Widths are terminal columns (East
// Asian wide runes count 2).
func (p *Printer) Table(header []string, rows [][]string) {
	p.TableWith(header, rows, TableOptions{})
}

// TableWith is Table with options.
func (p *Printer) TableWith(header []string, rows [][]string, o TableOptions) {
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
	widths := columnWidths(clean)
	if o.FreeLast && len(widths) > 1 {
		last := len(widths) - 1
		fitted := fitWidths(widths[:last], TableWidth-2-min(widths[last], TableWidth/2))
		widths = append(fitted, widths[last])
	} else {
		widths = fitWidths(widths, TableWidth)
	}
	for _, r := range clean {
		if o.FreeLast {
			last := len(r) - 1
			cells := layoutRow(r[:last], widths[:last], p.Mode.ASCII)
			line := strings.Join(append(padAll(cells, widths), r[last]), "  ")
			fmt.Fprintln(w, strings.TrimRight(line, " "))
			continue
		}
		fmt.Fprintln(w, formatRow(r, widths, p.Mode.ASCII))
	}
}

// padAll pads every cell to its column (layoutRow leaves the last one).
func padAll(cells []string, widths []int) []string {
	for j := range cells {
		cells[j] = PadWidth(cells[j], widths[j])
	}
	return cells
}

func columnWidths(rows [][]string) []int {
	var widths []int
	for _, r := range rows {
		for j, c := range r {
			if j >= len(widths) {
				widths = append(widths, 0)
			}
			widths[j] = max(widths[j], Width(c))
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
	return strings.TrimRight(strings.Join(layoutRow(cells, widths, ascii), "  "), " ")
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
	code := pderr.Code(v.Code)
	head := i18n.T(i18n.ErrHeadline, catalogText(pderr.TitleID(code), v.Title), v.ID)
	if !p.Mode.NoColor {
		head = "\x1b[1;31m" + head + "\x1b[0m"
	}
	fmt.Fprintln(p.Err, p.fold(head))
	if msg := p.errorDetail(err, v); msg != "" {
		fmt.Fprintln(p.Err, p.fold(i18n.T(i18n.ErrDetail, msg)))
	}
	if v.Safe != "" {
		fmt.Fprintln(p.Err, p.fold(i18n.T(i18n.ErrSafe, catalogText(pderr.SafeID(code), v.Safe))))
	}
	if v.Next != "" {
		fmt.Fprintln(p.Err, p.fold(i18n.T(i18n.ErrNext, catalogText(pderr.NextID(code), v.Next))))
	}
	if v.Anchor != "" {
		fmt.Fprintln(p.Err, p.fold(i18n.T(i18n.ErrMore, v.Anchor)))
	}
}

// catalogText renders an error-catalog id in the process language, or
// the English text when the id is not registered (an unknown code).
func catalogText(id i18n.Msg, english string) string {
	if !i18n.Has(id) {
		return english
	}
	return i18n.T(id)
}

// errorDetail is the detail line of a human error: the English message
// exactly as before in English; otherwise the first catalog text in the
// chain, of a NewT/WrapT error or of an i18n.Error (without the "op:"
// prefix), so a pderr.Wrap around either still shows it; or the English
// message when the text is not ours.
func (p *Printer) errorDetail(err error, v ErrorView) string {
	if i18n.Current() == i18n.English || v.Message == "" {
		return v.Message
	}
	if t, ok := chainText(err); ok {
		return p.Render(t, MaxErrorText)
	}
	return v.Message
}

// chainText returns the first catalog text in err's chain, depth first
// like errors.As: the Text of a *pderr.Error made by NewT/WrapT, or the
// text of an i18n.Texter. A *pderr.Error with a zero Text (Wrap, New,
// Bug) is passed through to its cause.
func chainText(err error) (i18n.Text, bool) {
	for err != nil {
		if pe, ok := err.(*pderr.Error); ok {
			if !pe.Text.IsZero() {
				return pe.Text, true
			}
		} else if tx, ok := err.(i18n.Texter); ok {
			return tx.I18nText(), true
		}
		switch u := err.(type) {
		case interface{ Unwrap() error }:
			err = u.Unwrap()
		case interface{ Unwrap() []error }:
			for _, e := range u.Unwrap() {
				if t, ok := chainText(e); ok {
					return t, true
				}
			}
			return i18n.Text{}, false
		default:
			return i18n.Text{}, false
		}
	}
	return i18n.Text{}, false
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
