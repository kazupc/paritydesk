package jsonx

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"
)

// Path is a sequence of literal object keys.
type Path []string

// Errors.
var (
	ErrNotFound     = errors.New("jsonx: path not found")
	ErrNotObject    = errors.New("jsonx: parent is not an object")
	ErrDuplicateKey = errors.New("jsonx: duplicate key")
	ErrNotString    = errors.New("jsonx: value is not a string")
	ErrEmptyPath    = errors.New("jsonx: empty path")
)

// SyntaxError reports malformed JSON at a byte offset.
type SyntaxError struct {
	Offset int
	Msg    string
}

func (e *SyntaxError) Error() string { return fmt.Sprintf("jsonx: %s at offset %d", e.Msg, e.Offset) }

const maxDepth = 512

type member struct {
	key                        string
	keyStart, valStart, valEnd int
}

type parser struct {
	b        []byte
	i        int
	checkDup bool
}

func (p *parser) fail(msg string) error { return &SyntaxError{Offset: p.i, Msg: msg} }

func (p *parser) ws() {
	for p.i < len(p.b) {
		switch p.b[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

func (p *parser) value(depth int) error {
	if depth > maxDepth {
		return p.fail("nesting too deep")
	}
	p.ws()
	if p.i >= len(p.b) {
		return p.fail("unexpected end")
	}
	switch c := p.b[p.i]; {
	case c == '{':
		_, err := p.object(depth, nil)
		return err
	case c == '[':
		return p.array(depth)
	case c == '"':
		_, err := p.str()
		return err
	case c == 't':
		return p.lit("true")
	case c == 'f':
		return p.lit("false")
	case c == 'n':
		return p.lit("null")
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number()
	default:
		return p.fail("unexpected character")
	}
}

func (p *parser) lit(s string) error {
	if !bytes.HasPrefix(p.b[p.i:], []byte(s)) {
		return p.fail("invalid literal")
	}
	p.i += len(s)
	return nil
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// str scans a string starting at '"' and returns its raw span with quotes.
func (p *parser) str() ([]byte, error) {
	start := p.i
	p.i++ // opening quote
	for p.i < len(p.b) {
		c := p.b[p.i]
		switch {
		case c == '"':
			p.i++
			return p.b[start:p.i], nil
		case c == '\\':
			if p.i+1 >= len(p.b) {
				return nil, p.fail("unterminated escape")
			}
			switch p.b[p.i+1] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				p.i += 2
			case 'u':
				if p.i+6 > len(p.b) || !isHex(p.b[p.i+2]) || !isHex(p.b[p.i+3]) || !isHex(p.b[p.i+4]) || !isHex(p.b[p.i+5]) {
					return nil, p.fail("invalid unicode escape")
				}
				p.i += 6
			default:
				return nil, p.fail("invalid escape")
			}
		case c < 0x20:
			return nil, p.fail("control character in string")
		default:
			p.i++
		}
	}
	return nil, p.fail("unterminated string")
}

func (p *parser) digits() int {
	n := 0
	for p.i < len(p.b) && p.b[p.i] >= '0' && p.b[p.i] <= '9' {
		p.i++
		n++
	}
	return n
}

func (p *parser) number() error {
	if p.b[p.i] == '-' {
		p.i++
	}
	if p.i < len(p.b) && p.b[p.i] == '0' {
		p.i++
	} else if p.digits() == 0 {
		return p.fail("invalid number")
	}
	if p.i < len(p.b) && p.b[p.i] == '.' {
		p.i++
		if p.digits() == 0 {
			return p.fail("invalid number")
		}
	}
	if p.i < len(p.b) && (p.b[p.i] == 'e' || p.b[p.i] == 'E') {
		p.i++
		if p.i < len(p.b) && (p.b[p.i] == '+' || p.b[p.i] == '-') {
			p.i++
		}
		if p.digits() == 0 {
			return p.fail("invalid number")
		}
	}
	return nil
}

func (p *parser) array(depth int) error {
	p.i++ // '['
	p.ws()
	if p.i < len(p.b) && p.b[p.i] == ']' {
		p.i++
		return nil
	}
	for {
		if err := p.value(depth + 1); err != nil {
			return err
		}
		p.ws()
		if p.i >= len(p.b) {
			return p.fail("unterminated array")
		}
		switch p.b[p.i] {
		case ',':
			p.i++
		case ']':
			p.i++
			return nil
		default:
			return p.fail("expected , or ]")
		}
	}
}

// object scans an object starting at '{'. If collect is non-nil, members are
// appended to it.
func (p *parser) object(depth int, collect *[]member) (int, error) {
	start := p.i
	p.i++ // '{'
	var seen map[string]struct{}
	if p.checkDup {
		seen = map[string]struct{}{}
	}
	p.ws()
	if p.i < len(p.b) && p.b[p.i] == '}' {
		p.i++
		return start, nil
	}
	for {
		p.ws()
		if p.i >= len(p.b) || p.b[p.i] != '"' {
			return 0, p.fail("expected object key")
		}
		ks := p.i
		raw, err := p.str()
		if err != nil {
			return 0, err
		}
		var key string
		if seen != nil || collect != nil {
			if err := json.Unmarshal(raw, &key); err != nil {
				return 0, &SyntaxError{Offset: ks, Msg: "invalid key"}
			}
		}
		if seen != nil {
			if _, dup := seen[key]; dup {
				return 0, fmt.Errorf("%w at offset %d", ErrDuplicateKey, ks)
			}
			seen[key] = struct{}{}
		}
		p.ws()
		if p.i >= len(p.b) || p.b[p.i] != ':' {
			return 0, p.fail("expected :")
		}
		p.i++
		p.ws()
		vs := p.i
		if err := p.value(depth + 1); err != nil {
			return 0, err
		}
		if collect != nil {
			*collect = append(*collect, member{key: key, keyStart: ks, valStart: vs, valEnd: p.i})
		}
		p.ws()
		if p.i >= len(p.b) {
			return 0, p.fail("unterminated object")
		}
		switch p.b[p.i] {
		case ',':
			p.i++
		case '}':
			p.i++
			return start, nil
		default:
			return 0, p.fail("expected , or }")
		}
	}
}

// Validate checks that doc is exactly one JSON value (surrounded by optional
// whitespace) and that no object in it has duplicate keys.
func Validate(doc []byte) error {
	p := &parser{b: doc, checkDup: true}
	if err := p.value(0); err != nil {
		return err
	}
	p.ws()
	if p.i != len(doc) {
		return p.fail("trailing data")
	}
	return nil
}

// members parses the object whose '{' is at off and returns its members.
func members(doc []byte, off int) ([]member, error) {
	if off >= len(doc) || doc[off] != '{' {
		return nil, ErrNotObject
	}
	p := &parser{b: doc, i: off}
	var ms []member
	if _, err := p.object(0, &ms); err != nil {
		return nil, err
	}
	return ms, nil
}

type located struct {
	parentStart int      // offset of the parent object's '{'
	siblings    []member // members of the parent object
	index       int      // index of the member in siblings, -1 if absent
}

// locate validates doc and finds the last path segment's member.
func locate(doc []byte, path Path) (located, error) {
	if len(path) == 0 {
		return located{}, ErrEmptyPath
	}
	if err := Validate(doc); err != nil {
		return located{}, err
	}
	p := &parser{b: doc}
	p.ws()
	cur := p.i
	for d, seg := range path {
		ms, err := members(doc, cur)
		if err != nil {
			return located{}, err
		}
		idx := -1
		for j, m := range ms {
			if m.key == seg {
				idx = j
				break
			}
		}
		if d == len(path)-1 {
			return located{parentStart: cur, siblings: ms, index: idx}, nil
		}
		if idx < 0 {
			return located{}, ErrNotFound
		}
		cur = ms[idx].valStart
	}
	return located{}, ErrNotFound // unreachable
}

// Get returns the raw bytes of the value at path.
func Get(doc []byte, path Path) (json.RawMessage, error) {
	loc, err := locate(doc, path)
	if err != nil {
		return nil, err
	}
	if loc.index < 0 {
		return nil, ErrNotFound
	}
	m := loc.siblings[loc.index]
	return json.RawMessage(doc[m.valStart:m.valEnd]), nil
}

// errInvalidPathKey refuses a path key that is not valid UTF-8: it could
// never match a decoded key, and inserting it would write U+FFFD instead.
var errInvalidPathKey = errors.New("jsonx: path key is not valid UTF-8")

// Set replaces the value at path with v, or inserts the last key into its
// parent object when absent. Every parent must already exist. v must be
// valid JSON without duplicate keys, and path keys valid UTF-8. The
// returned slice is new.
func Set(doc []byte, path Path, v json.RawMessage) ([]byte, error) {
	for _, k := range path {
		if !utf8.ValidString(k) {
			return nil, errInvalidPathKey
		}
	}
	if err := Validate(v); err != nil {
		return nil, err
	}
	v = bytes.TrimSpace(v)
	loc, err := locate(doc, path)
	if err != nil {
		return nil, err
	}
	if loc.index >= 0 {
		m := loc.siblings[loc.index]
		return splice(doc, m.valStart, m.valEnd, v), nil
	}
	key, err := Marshal(path[len(path)-1])
	if err != nil {
		return nil, err
	}
	ins := make([]byte, 0, len(key)+len(v)+2)
	if n := len(loc.siblings); n > 0 {
		ins = append(ins, ',')
		ins = append(ins, key...)
		ins = append(ins, ':')
		ins = append(ins, v...)
		end := loc.siblings[n-1].valEnd
		return splice(doc, end, end, ins), nil
	}
	ins = append(ins, key...)
	ins = append(ins, ':')
	ins = append(ins, v...)
	at := loc.parentStart + 1
	return splice(doc, at, at, ins), nil
}

// Delete removes the member at path. It returns ErrNotFound if absent.
func Delete(doc []byte, path Path) ([]byte, error) {
	loc, err := locate(doc, path)
	if err != nil {
		return nil, err
	}
	if loc.index < 0 {
		return nil, ErrNotFound
	}
	ms, j := loc.siblings, loc.index
	switch {
	case j < len(ms)-1:
		return splice(doc, ms[j].keyStart, ms[j+1].keyStart, nil), nil
	case j > 0:
		return splice(doc, ms[j-1].valEnd, ms[j].valEnd, nil), nil
	default:
		return splice(doc, ms[j].keyStart, ms[j].valEnd, nil), nil
	}
}

func splice(doc []byte, from, to int, with []byte) []byte {
	out := make([]byte, 0, len(doc)-(to-from)+len(with))
	out = append(out, doc[:from]...)
	out = append(out, with...)
	out = append(out, doc[to:]...)
	return out
}

// TopLevelString finds key among the top-level members of doc and returns
// its string value. Other values are skipped without being decoded, which
// is how credential-bearing files are read. A document with any duplicate
// top-level key is refused with ErrDuplicateKey, since its meaning is
// ambiguous.
func TopLevelString(doc []byte, key string) (string, bool, error) {
	raw, found, err := topLevel(doc, key)
	if err != nil || !found {
		return "", found, err
	}
	if len(raw) == 0 || raw[0] != '"' {
		return "", true, ErrNotString
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", true, ErrNotString
	}
	return s, true, nil
}

// HasTopLevelKey reports whether doc's top-level object has key. The value
// is never decoded. Duplicate top-level keys are refused as in
// TopLevelString.
func HasTopLevelKey(doc []byte, key string) (bool, error) {
	_, found, err := topLevel(doc, key)
	return found, err
}

// TopLevelMembers validates doc, which must be an object, and returns its
// top-level members' raw values by key. One pass replaces a Get per key
// when many top-level keys of a large document are looked at.
func TopLevelMembers(doc []byte) (map[string]json.RawMessage, error) {
	if err := Validate(doc); err != nil {
		return nil, err
	}
	p := &parser{b: doc}
	p.ws()
	ms, err := members(doc, p.i)
	if err != nil {
		return nil, err
	}
	out := make(map[string]json.RawMessage, len(ms))
	for _, m := range ms {
		out[m.key] = json.RawMessage(doc[m.valStart:m.valEnd])
	}
	return out, nil
}

func topLevel(doc []byte, key string) ([]byte, bool, error) {
	p := &parser{b: doc}
	p.ws()
	if p.i >= len(doc) || doc[p.i] != '{' {
		return nil, false, ErrNotObject
	}
	p.i++
	var out []byte
	found := false
	seen := map[string]struct{}{}
	p.ws()
	if p.i < len(doc) && doc[p.i] == '}' {
		p.i++
		return nil, false, p.trailing()
	}
	for {
		p.ws()
		if p.i >= len(doc) || doc[p.i] != '"' {
			return nil, false, p.fail("expected object key")
		}
		ks := p.i
		raw, err := p.str()
		if err != nil {
			return nil, false, err
		}
		var k string
		if err := json.Unmarshal(raw, &k); err != nil {
			return nil, false, &SyntaxError{Offset: ks, Msg: "invalid key"}
		}
		p.ws()
		if p.i >= len(doc) || doc[p.i] != ':' {
			return nil, false, p.fail("expected :")
		}
		p.i++
		p.ws()
		vs := p.i
		if err := p.value(1); err != nil {
			return nil, false, err
		}
		if _, dup := seen[k]; dup {
			return nil, false, fmt.Errorf("%w at offset %d", ErrDuplicateKey, ks)
		}
		seen[k] = struct{}{}
		if k == key {
			found, out = true, doc[vs:p.i]
		}
		p.ws()
		if p.i >= len(doc) {
			return nil, false, p.fail("unterminated object")
		}
		switch doc[p.i] {
		case ',':
			p.i++
		case '}':
			p.i++
			return out, found, p.trailing()
		default:
			return nil, false, p.fail("expected , or }")
		}
	}
}

func (p *parser) trailing() error {
	p.ws()
	if p.i != len(p.b) {
		return p.fail("trailing data")
	}
	return nil
}

// Marshal encodes v compactly without HTML escaping and without a trailing
// newline.
func Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
