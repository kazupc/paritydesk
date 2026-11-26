package cli

import (
	"bytes"
	"encoding/json"
	"reflect"

	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/online"
	"github.com/kazupc/paritydesk/internal/render"
)

// displayDoc is a JSON document with the additive display members: the
// document's own members first (English, unchanged), then "lang" and
// "display_lines" (spec-i18n-ja D5, 7.4 item 5).
type displayDoc struct {
	doc   json.RawMessage
	lang  i18n.Lang
	lines []string
}

// MarshalJSON appends lang and display_lines to the document object.
func (d displayDoc) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(struct {
		Lang  i18n.Lang `json:"lang"`
		Lines []string  `json:"display_lines"`
	}{d.lang, d.lines}); err != nil {
		return nil, err
	}
	tail := bytes.TrimSpace(buf.Bytes())
	doc := bytes.TrimSpace(d.doc)
	out := append([]byte(nil), doc[:len(doc)-1]...)
	if len(doc) > 2 {
		out = append(out, ',')
	}
	out = append(out, tail[1:]...)
	return out, nil
}

// withDisplay returns the document to encode for v: v itself when the
// display language is English or v carries no catalog lines; otherwise
// v with its question localized (display_header, display_text and the
// options' display_label and display_description) and with "lang" and
// "display_lines" (the same lines in the display language) appended.
// The English members are byte-identical to v's own encoding.
func withDisplay(v any, l i18n.Lang) any {
	if l == i18n.English {
		return v
	}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return v
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return v
	}
	lf := rv.FieldByName("Lines")
	if !lf.IsValid() {
		return v
	}
	lines, ok := lf.Interface().([]i18n.Text)
	if !ok {
		return v
	}
	cp := reflect.New(rv.Type()).Elem()
	cp.Set(rv)
	if qf := cp.FieldByName("Question"); qf.IsValid() {
		if q, ok := qf.Interface().(*online.Question); ok && q != nil {
			lq := q.Localized(l)
			qf.Set(reflect.ValueOf(&lq))
		}
	}
	b, err := render.Document(cp.Interface())
	if err != nil {
		return v
	}
	// Document adds the schema version; the printer adds it again, so
	// strip it here (the first member).
	b = bytes.TrimSpace(b)
	if i := bytes.IndexByte(b, ','); bytes.HasPrefix(b, []byte(`{"v":`)) && i > 0 {
		b = append([]byte{'{'}, b[i+1:]...)
	}
	out := make([]string, len(lines))
	for i, t := range lines {
		out[i] = render.RenderIn(l, t, 0)
	}
	return displayDoc{doc: b, lang: l, lines: out}
}
