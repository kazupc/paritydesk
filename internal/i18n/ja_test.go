package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// pderrKeyRE matches the ids of the error catalog (registered by pderr).
var pderrKeyRE = regexp.MustCompile(`^pderr\.E[0-9]+\.(title|safe|next)$`)

// T1: every id of catalog.go that is not English-only has Japanese. The
// failure lists the missing ids with their English, ready to translate.
func TestJapaneseComplete(t *testing.T) {
	var missing []string
	for _, id := range msgConstants(t) {
		if !EnglishOnly(id) && !HasIn(Japanese, id) {
			missing = append(missing, string(id)+"\t"+catalog[id])
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%d ids have no Japanese:\n%s", len(missing), strings.Join(missing, "\n"))
	}
}

// T2: every Japanese key is a catalog.go constant or a pderr id, and none
// is English-only.
func TestJapaneseHasNoStrays(t *testing.T) {
	known := map[Msg]bool{}
	for _, id := range msgConstants(t) {
		known[id] = true
	}
	for id := range japanese {
		switch {
		case EnglishOnly(id):
			t.Errorf("%s is English-only but has Japanese", id)
		case !known[id] && !pderrKeyRE.MatchString(string(id)):
			t.Errorf("%s is not a catalog id", id)
		}
	}
}

// T3: every Japanese format may replace its English one.
func TestFormatVerbsMatch(t *testing.T) {
	en := Catalog()
	for id, ja := range japanese {
		e, ok := en[id]
		if !ok {
			continue
		}
		if !Compatible(e, ja) {
			ev, _, eerr := Verbs(e)
			jv, _, jerr := Verbs(ja)
			t.Errorf("%s: verbs differ:\n en %q %v %v\n ja %q %v %v", id, e, ev, eerr, ja, jv, jerr)
		}
	}
}

// T4: same leading spaces, [y/N] and prompt endings, and line breaks.
func TestJapaneseKeepsLayout(t *testing.T) {
	en := Catalog()
	lead := func(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }
	for id, ja := range japanese {
		e, ok := en[id]
		if !ok {
			continue
		}
		if lead(e) != lead(ja) && id != WordListSep { // " / " separates list items

			t.Errorf("%s: %d leading spaces in English, %d in Japanese", id, lead(e), lead(ja))
		}
		if strings.HasSuffix(e, "[y/N] ") != strings.HasSuffix(ja, "[y/N] ") {
			t.Errorf("%s: the [y/N] prompt ending differs", id)
		}
		if strings.HasSuffix(e, ": ") && !strings.HasSuffix(ja, ": ") {
			t.Errorf("%s: the prompt ending \": \" is missing", id)
		}
		if strings.Count(e, "\n") != strings.Count(ja, "\n") {
			t.Errorf("%s: line breaks differ", id)
		}
	}
}

// protectedRE lists the tokens a translation keeps character for
// character (spec-i18n-ja 12.1 item 8).
var protectedRE = []*regexp.Regexp{
	regexp.MustCompile("`[^`]*`"),
	regexp.MustCompile(`--[a-z][a-z0-9-]*(?:=[^\s,;)\x{ff09}]*)?`),
	regexp.MustCompile(`\[y/N\]`),
	regexp.MustCompile(`DESTRUCTIVE`),
	regexp.MustCompile(`PD-E[0-9]+`),
	regexp.MustCompile(`docs/[A-Za-z0-9._/#-]+`),
	regexp.MustCompile(`PARITYDESK_[A-Z_]+`),
	regexp.MustCompile(`NO_COLOR`),
	regexp.MustCompile(`TERM=dumb`),
	regexp.MustCompile(`LANG=[A-Za-z_.0-9-]+`),
	regexp.MustCompile(`[A-Za-z0-9_.-]+\.(?:json|md|toml|gz|log)`),
	regexp.MustCompile(`mode 0600`),
	regexp.MustCompile(`\[ui\]`),
	regexp.MustCompile(`fallback_lang`),
	regexp.MustCompile(`lang = "ja"`),
	regexp.MustCompile(`\[[^\]]*\]\{[0-9,]+\}`),
}

// T5: protected tokens appear as often in Japanese as in English.
func TestJapaneseKeepsTokens(t *testing.T) {
	en := Catalog()
	for id, ja := range japanese {
		e, ok := en[id]
		if !ok {
			continue
		}
		for _, re := range protectedRE {
			want := map[string]int{}
			for _, m := range re.FindAllString(e, -1) {
				want[m]++
			}
			got := map[string]int{}
			for _, m := range re.FindAllString(ja, -1) {
				got[m]++
			}
			for tok, n := range want {
				if got[tok] != n {
					t.Errorf("%s: %q appears %d times in English, %d in Japanese", id, tok, n, got[tok])
				}
			}
		}
	}
}

// japaneseLetter reports Han, Hiragana, Katakana and the prolonged sound
// mark.
func japaneseLetter(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana) || r == '\u30fc'
}

// adjacencyExceptions are ids whose argument is an optional parenthetical
// that carries its own leading space (err.quit.osa_status), so the verb
// follows a Japanese letter directly.
var adjacencyExceptions = map[Msg]bool{
	"err.quit.refused": true, "err.quit.timed_out": true, "err.quit.sandboxed": true,
	"err.quit.not_delivered": true, "err.quit.not_quit": true,
}

// signOutWords are Japanese words for ending an account's session
// (sign out, log out, log off, sign off), as escapes.
var signOutWords = []string{
	"\u30b5\u30a4\u30f3\u30a2\u30a6\u30c8", "\u30ed\u30b0\u30a2\u30a6\u30c8",
	"\u30ed\u30b0\u30aa\u30d5", "\u30b5\u30a4\u30f3\u30aa\u30d5",
}

// T6: printable runes only; in the full-width forms block only the
// parentheses and the question mark; no half-width katakana; no
// exclamation mark; no sign-out words; the spacing rule; no closing \u3002 in
// table cells and values.
func TestJapaneseStyle(t *testing.T) {
	asciiTok := func(r rune) bool {
		return r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '%' || r == '`')
	}
	for id, ja := range japanese {
		rs := []rune(ja)
		for i, r := range rs {
			switch {
			case !unicode.IsPrint(r) && r != '\n':
				t.Errorf("%s: control rune U+%04X", id, r)
			case r >= 0xff01 && r <= 0xff5e && r != 0xff08 && r != 0xff09 && r != 0xff1f:
				t.Errorf("%s: full-width form U+%04X", id, r)
			case r >= 0xff61 && r <= 0xff9f:
				t.Errorf("%s: half-width katakana U+%04X", id, r)
			case r == '!' || r == 0xff01:
				t.Errorf("%s: exclamation mark", id)
			}
			if i > 0 && !adjacencyExceptions[id] && japaneseLetter(r) && asciiTok(rs[i-1]) {
				t.Errorf("%s: no space between %q and a Japanese letter", id, string(rs[i-1:i+1]))
			}
			if i > 0 && !adjacencyExceptions[id] && asciiTok(r) && japaneseLetter(rs[i-1]) {
				t.Errorf("%s: no space between a Japanese letter and %q", id, string(rs[i-1:i+1]))
			}
		}
		for _, w := range signOutWords {
			if strings.Contains(ja, w) {
				t.Errorf("%s advises ending an account's session", id)
			}
		}
		s := string(id)
		for _, p := range []string{"col.", "word.", "profile.why.", "profile.problem."} {
			if strings.HasPrefix(s, p) && strings.HasSuffix(ja, "\u3002") {
				t.Errorf("%s: a table cell ends with a full stop", id)
			}
		}
	}
}

// japaneseKeyOrder parses the key order of the japanese map literal.
func japaneseKeyOrder(t *testing.T) []Msg {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "catalog_ja.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	consts := map[string]Msg{}
	cf, err := parser.ParseFile(token.NewFileSet(), "catalog.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range cf.Decls {
		if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.CONST {
			for _, s := range gd.Specs {
				vs := s.(*ast.ValueSpec)
				for i, v := range vs.Values {
					if lit, ok := v.(*ast.BasicLit); ok {
						consts[vs.Names[i].Name] = Msg(strings.Trim(lit.Value, `"`))
					}
				}
			}
		}
	}
	var out []Msg
	ast.Inspect(f, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "japanese" {
			return true
		}
		for _, e := range vs.Values[0].(*ast.CompositeLit).Elts {
			switch k := e.(*ast.KeyValueExpr).Key.(type) {
			case *ast.Ident:
				out = append(out, consts[k.Name])
			case *ast.BasicLit:
				s, _ := strconv.Unquote(k.Value)
				out = append(out, Msg(s))
			}
		}
		return false
	})
	return out
}

// T7: the Japanese map follows catalog.go's order (English-only ids
// left out), then the pderr ids in code order.
func TestJapaneseOrder(t *testing.T) {
	var want []Msg
	for _, id := range msgConstants(t) {
		if !EnglishOnly(id) {
			want = append(want, id)
		}
	}
	got := japaneseKeyOrder(t)
	i := 0
	for ; i < len(got) && !pderrKeyRE.MatchString(string(got[i])); i++ {
		if i >= len(want) || got[i] != want[i] {
			t.Fatalf("key %d of catalog_ja.go is %q, want %q (catalog.go order)", i, got[i], want[min(i, len(want)-1)])
		}
	}
	if i != len(want) {
		t.Fatalf("catalog_ja.go has %d catalog keys, want %d", i, len(want))
	}
	last := -1
	for ; i < len(got); i++ {
		m := regexp.MustCompile(`^pderr\.E([0-9]+)\.`).FindStringSubmatch(string(got[i]))
		if m == nil {
			t.Fatalf("%q follows the pderr ids", got[i])
		}
		n, _ := strconv.Atoi(m[1])
		if n < last {
			t.Fatalf("pderr ids out of code order at %q", got[i])
		}
		last = n
	}
}

// T9: verbs with a width only in ids whose arguments are ASCII ids.
func TestWidthVerbsOnlyInAllowedIds(t *testing.T) {
	allowed := map[Msg]string{
		PlanCategory: "category ids",
		Progress:     "phase ids and status words",
	}
	width := regexp.MustCompile(`%[-+# 0]*[0-9]+`)
	for _, l := range Langs {
		for id, s := range CatalogIn(l) {
			if width.MatchString(s) && allowed[id] == "" {
				t.Errorf("%s (%s): a verb with a width", id, l)
			}
		}
	}
}

// T13 (TestCatalogIsASCIIAndSafe) and TestEveryMessageHasEnglish stay in
// i18n_test.go for the English catalog.
