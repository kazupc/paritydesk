package i18n

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// Text is a message rendered late, when it is printed or encoded.
type Text struct {
	ID   Msg
	Args []any // normalized by Tx: string, int64, uint64, float64, bool, Text, List
	raw  string
	max  int // > 0: the rendering is cut to max runes (Cut)
}

// Tx builds a Text. Argument normalization: any value whose reflect.Kind
// is String (ids.Alias, model.Category, compat.Mode, ...) becomes string;
// a fmt.Stringer of another kind (time.Duration, for example) becomes its
// String(); signed integer kinds become int64, unsigned uint64, floats
// float64, bool stays bool; Text and List stay; anything else becomes
// fmt.Sprint(v) (a string, so it is sanitized like data).
func Tx(id Msg, args ...any) Text {
	var out []any
	if len(args) > 0 {
		out = make([]any, len(args))
		for i, a := range args {
			out[i] = normalize(a)
		}
	}
	return Text{ID: id, Args: out}
}

// Raw wraps text that is not in the catalog (an OS error text, a Bug
// text).
func Raw(s string) Text { return Text{raw: s} }

// normalize is the argument normalization of Tx.
func normalize(a any) any {
	switch v := a.(type) {
	case nil:
		return fmt.Sprint(v)
	case string, int64, uint64, float64, bool, Text, List, CommaList:
		return v
	case *Text:
		if v == nil {
			return ""
		}
		return *v
	}
	rv := reflect.ValueOf(a)
	if rv.Kind() == reflect.String {
		return rv.String()
	}
	if s, ok := a.(fmt.Stringer); ok {
		return s.String()
	}
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return rv.Uint()
	case reflect.Float32, reflect.Float64:
		return rv.Float()
	case reflect.Bool:
		return rv.Bool()
	}
	return fmt.Sprint(a)
}

// IsZero reports a Text with neither an id nor raw text.
func (t Text) IsZero() bool { return t.ID == "" && t.raw == "" }

// String renders t.
func (t Text) String() string {
	s := t.raw
	if t.ID != "" {
		s = T(t.ID, t.Args...)
	}
	return cutRunes(s, t.max)
}

// Cut returns t whose rendering is cut to max runes,
// the last one an ellipsis (a safety bound that survives the late
// rendering). max <= 0 removes the bound.
func (t Text) Cut(max int) Text {
	t.max = max
	return t
}

func cutRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "\u2026"
}

// MapStrings returns a copy with f applied to every string argument
// (recursively into Text and List arguments) and to raw text; used for
// sanitizing and redaction before rendering. It never touches the format.
func (t Text) MapStrings(f func(string) string) Text {
	out := Text{ID: t.ID, max: t.max}
	if t.ID == "" {
		out.raw = f(t.raw)
		return out
	}
	if t.Args != nil {
		out.Args = make([]any, len(t.Args))
		for i, a := range t.Args {
			out.Args[i] = mapArg(normalize(a), f)
		}
	}
	return out
}

func mapArg(a any, f func(string) string) any {
	switch v := a.(type) {
	case string:
		return f(v)
	case Text:
		return v.MapStrings(f)
	case List:
		return v.MapStrings(f)
	case CommaList:
		return v.MapStrings(f)
	}
	return a
}

// MarshalJSON encodes the rendering as a JSON string. HTML
// characters are not escaped here; the outer encoder decides that.
func (t Text) MarshalJSON() ([]byte, error) { return marshalString(t.String()) }

// UnmarshalJSON makes a JSON string a Raw text.
func (t *Text) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*t = Raw(s)
	return nil
}

func marshalString(s string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// List renders its items joined by word.list_sep ("; ").
type List []Text

// String renders the list.
func (ls List) String() string {
	parts := make([]string, len(ls))
	for i, t := range ls {
		parts[i] = t.String()
	}
	return strings.Join(parts, T(WordListSep))
}

// MapStrings applies Text.MapStrings to every item.
func (ls List) MapStrings(f func(string) string) List {
	out := make(List, len(ls))
	for i, t := range ls {
		out[i] = t.MapStrings(f)
	}
	return out
}

// CommaList renders its items joined by word.comma_sep (", ").
type CommaList []Text

// String renders the list.
func (ls CommaList) String() string {
	parts := make([]string, len(ls))
	for i, t := range ls {
		parts[i] = t.String()
	}
	return strings.Join(parts, T(WordCommaSep))
}

// MapStrings applies Text.MapStrings to every item.
func (ls CommaList) MapStrings(f func(string) string) CommaList {
	return CommaList(List(ls).MapStrings(f))
}

// Join returns a Text that renders items joined by word.list_sep.
func Join(items ...Text) Text { return Tx(WordList, List(items)) }

// Texts wraps each string as Raw text.
func Texts(ss []string) []Text {
	if ss == nil {
		return nil
	}
	out := make([]Text, len(ss))
	for i, s := range ss {
		out[i] = Raw(s)
	}
	return out
}

// Strings renders each text.
func Strings(ts []Text) []string {
	if ts == nil {
		return nil
	}
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.String()
	}
	return out
}
