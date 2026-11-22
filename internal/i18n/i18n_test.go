package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strings"
	"testing"
)

// msgConstants returns every Msg constant declared in catalog.go.
func msgConstants(t *testing.T) []Msg {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "catalog.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []Msg
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, s := range gd.Specs {
			vs := s.(*ast.ValueSpec)
			for _, v := range vs.Values {
				if lit, ok := v.(*ast.BasicLit); ok {
					out = append(out, Msg(strings.Trim(lit.Value, `"`)))
				}
			}
		}
	}
	return out
}

func TestEveryMessageHasEnglish(t *testing.T) {
	ids := msgConstants(t)
	if len(ids) < 50 {
		t.Fatalf("found only %d message ids", len(ids))
	}
	seen := map[Msg]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Errorf("duplicate message id %q", id)
		}
		seen[id] = true
		if !Has(id) || T(id) == string(id) {
			t.Errorf("message %q has no English text", id)
		}
	}
	for id := range catalog {
		if !seen[id] {
			t.Errorf("catalog entry %q has no constant", id)
		}
	}
}

func TestCatalogIsASCIIAndSafe(t *testing.T) {
	bad := regexp.MustCompile(`(?i)sign[ -]?out|log[ -]?out`)
	for id, s := range catalog {
		for _, r := range s {
			if r > 0x7e || (r < 0x20 && r != '\t') {
				t.Errorf("%q: non-printable or non-ASCII text", id)
				break
			}
		}
		if bad.MatchString(s) {
			t.Errorf("%q advises ending a session of an account", id)
		}
	}
}

func TestTFormatsAndFallsBack(t *testing.T) {
	tests := []struct {
		id   Msg
		args []any
		want string
	}{
		{StatusActive, []any{"work"}, "Active profile: work"},
		{"no.such.id", nil, "no.such.id"},
		{DestructiveNone, nil, "DESTRUCTIVE: none"},
	}
	for _, tc := range tests {
		if got := T(tc.id, tc.args...); got != tc.want {
			t.Errorf("T(%q) = %q, want %q", tc.id, got, tc.want)
		}
	}
	Register(map[Msg]string{"test.extra": "extra %d"})
	if got := T("test.extra", 2); got != "extra 2" {
		t.Errorf("registered message: %q", got)
	}
}
