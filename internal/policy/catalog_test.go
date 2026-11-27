package policy

import (
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// Policy tests of the message catalog: user-facing text comes from the
// catalog, catalog text is never re-read as a format, the repository is
// English only, and the skills translate faithfully.

// bugWrappers are functions whose own body may pass a non-literal
// message to pderr.Bug/Bugf; their call sites pass a literal first
// argument instead.
var bugWrappers = map[string]string{"internal/plan/validate.go": "invalid"}

// i18nImport returns the local name of the i18n import of f ("" when f
// does not import it).
func i18nImport(f *ast.File) string {
	return importName(f, "github.com/kazupc/paritydesk/internal/i18n")
}

// isCall reports whether n is a call of pkg.name and returns it.
func isCall(n ast.Node, pkg string, names ...string) (*ast.CallExpr, string, bool) {
	call, ok := n.(*ast.CallExpr)
	if !ok || pkg == "" {
		return nil, "", false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil, "", false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok || id.Name != pkg {
		return nil, "", false
	}
	for _, name := range names {
		if sel.Sel.Name == name {
			return call, name, true
		}
	}
	return nil, "", false
}

// stringLit returns the value of a string literal.
func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}

// enclosingFunc returns the name of the function declaration holding pos.
func enclosingFunc(f *ast.File, pos token.Pos) string {
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Pos() <= pos && pos <= fd.End() {
			return fd.Name.Name
		}
	}
	return ""
}

// TestUserTextFromCatalog lists every remaining site that would put prose
// in front of a person outside the catalog (P1-P3, P10-P12).
func TestUserTextFromCatalog(t *testing.T) {
	files := goFiles(t, false)
	at := func(g goFile, n ast.Node) string {
		return g.rel + ":" + strconv.Itoa(g.fset.Position(n.Pos()).Line)
	}

	t.Run("pderr", func(t *testing.T) { // P1
		for _, g := range files {
			pd := importName(g.file, "github.com/kazupc/paritydesk/internal/pderr")
			wrapper := bugWrappers[g.rel]
			ast.Inspect(g.file, func(n ast.Node) bool {
				if call, _, ok := isCall(n, pd, "New"); ok && len(call.Args) == 3 {
					if s, lit := stringLit(call.Args[2]); !lit || s != "" {
						t.Errorf("%s: pderr: pderr.New with a message (use pderr.NewT or pderr.Bug)", at(g, call))
					}
				}
				if call, _, ok := isCall(n, pd, "Bug", "Bugf"); ok && len(call.Args) >= 2 {
					if _, lit := stringLit(call.Args[1]); !lit && (wrapper == "" || enclosingFunc(g.file, call.Pos()) != wrapper) {
						t.Errorf("%s: pderr: pderr.Bug/Bugf with a non-literal message", at(g, call))
					}
				}
				if wrapper != "" {
					if call, ok := n.(*ast.CallExpr); ok {
						if id, ok := call.Fun.(*ast.Ident); ok && id.Name == wrapper && len(call.Args) > 0 {
							if _, lit := stringLit(call.Args[0]); !lit {
								t.Errorf("%s: pderr: %s with a non-literal format", at(g, call), wrapper)
							}
						}
					}
				}
				return true
			})
		}
	})

	t.Run("reformat", func(t *testing.T) { // P2
		for _, g := range files {
			name := i18nImport(g.file)
			ast.Inspect(g.file, func(n ast.Node) bool {
				if call, fn, ok := isCall(n, name, "T", "Tx"); ok {
					for _, a := range call.Args {
						if s, lit := stringLit(a); lit && strings.HasPrefix(s, "%") {
							t.Errorf("%s: reformat: i18n.%s(..., %q) re-reads catalog text as a format", at(g, call), fn, s)
						}
					}
				}
				return true
			})
		}
	})

	t.Run("line", func(t *testing.T) { // P3
		for _, g := range files {
			if !underAny(g.rel, "internal/cli/", "internal/render/") {
				continue
			}
			ast.Inspect(g.file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch sel.Sel.Name {
				case "Warn":
					t.Errorf("%s: line: .Warn( (use WarnText)", at(g, call))
				case "Line":
					if len(call.Args) == 0 {
						return true
					}
					if _, lit := stringLit(call.Args[0]); lit {
						return true
					}
					if id, ok := call.Args[0].(*ast.Ident); ok && id.Obj != nil && id.Obj.Kind == ast.Con {
						return true
					}
					t.Errorf("%s: line: the format of .Line( is not a constant", at(g, call))
				}
				return true
			})
		}
	})

	t.Run("sentinels", func(t *testing.T) { // P10
		re := actionableRE(t)
		for _, g := range files {
			if strings.HasPrefix(g.rel, "internal/i18n/") {
				continue
			}
			errs := importName(g.file, "errors")
			fm := importName(g.file, "fmt")
			ast.Inspect(g.file, func(n ast.Node) bool {
				call, fn, ok := isCall(n, errs, "New")
				if !ok {
					call, fn, ok = isCall(n, fm, "Errorf")
				}
				if !ok || len(call.Args) == 0 {
					return true
				}
				if s, lit := stringLit(call.Args[0]); lit && re.MatchString(s) {
					t.Errorf("%s: sentinels: %s(%q) is an actionable text (use i18n.NewErr or pderr.NewT)", at(g, call), fn, s)
				}
				return true
			})
		}
	})

	t.Run("register", func(t *testing.T) { // P11
		for _, g := range files {
			if strings.HasPrefix(g.rel, "internal/i18n/") {
				continue
			}
			name := i18nImport(g.file)
			ast.Inspect(g.file, func(n ast.Node) bool {
				if call, fn, ok := isCall(n, name, "Register"); ok {
					t.Errorf("%s: register: i18n.%s outside internal/i18n", at(g, call), fn)
				}
				return true
			})
		}
	})

	t.Run("fields", func(t *testing.T) { // P12
		fields := map[string]bool{"Problem": true, "Detail": true, "Message": true, "Warnings": true, "Lines": true, "Actions": true}
		hasLit := func(e ast.Expr) bool {
			if s, ok := stringLit(e); ok {
				return s != ""
			}
			if cl, ok := e.(*ast.CompositeLit); ok {
				for _, el := range cl.Elts {
					if s, ok := stringLit(el); ok && s != "" {
						return true
					}
				}
			}
			return false
		}
		for _, g := range files {
			if strings.HasPrefix(g.rel, "internal/i18n/") {
				continue
			}
			ast.Inspect(g.file, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.KeyValueExpr:
					if k, ok := x.Key.(*ast.Ident); ok && fields[k.Name] && hasLit(x.Value) {
						t.Errorf("%s: fields: a string literal is assigned to %s", at(g, x), k.Name)
					}
				case *ast.AssignStmt:
					for i, l := range x.Lhs {
						if sel, ok := l.(*ast.SelectorExpr); ok && fields[sel.Sel.Name] && i < len(x.Rhs) && hasLit(x.Rhs[i]) {
							t.Errorf("%s: fields: a string literal is assigned to %s", at(g, x), sel.Sel.Name)
						}
					}
				}
				return true
			})
		}
	})
}

// commandNameRE finds command names in cli.go and the group files.
var commandNameRE = regexp.MustCompile(`(?:leaf|group)\("([a-z][a-z-]*)"|name: "([a-z][a-z-]*)"`)

// actionableRE matches error texts that tell a person what to run:
// "run paritydesk", "upgrade paritydesk", or "paritydesk <command>" for a
// command of the root command tree.
func actionableRE(t *testing.T) *regexp.Regexp {
	t.Helper()
	root := repoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "internal", "cli", "cli.go"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range commandNameRE.FindAllStringSubmatch(string(b), -1) {
		n := m[1] + m[2]
		if n != "" && !strings.Contains(n, "paritydesk") {
			names = append(names, regexp.QuoteMeta(n))
		}
	}
	if len(names) < 10 {
		t.Fatalf("found only %d command names in cli.go", len(names))
	}
	return regexp.MustCompile(`run paritydesk|upgrade paritydesk|paritydesk (?:` + strings.Join(names, "|") + `)\b`)
}

// japaneseRanges are the Unicode ranges no file of the repository may
// hold: CJK punctuation, Hiragana, Katakana, the CJK ideographs
// (extension A, the unified block and the compatibility block) and the
// half- and full-width forms. Tests that need such text write it as \u
// escapes.
var japaneseRanges = [][2]rune{
	{0x3000, 0x303f}, // CJK symbols and punctuation
	{0x3040, 0x309f}, // Hiragana
	{0x30a0, 0x30ff}, // Katakana
	{0x31f0, 0x31ff}, // Katakana phonetic extensions
	{0x3400, 0x4dbf}, // CJK unified ideographs extension A
	{0x4e00, 0x9fff}, // CJK unified ideographs
	{0xf900, 0xfaff}, // CJK compatibility ideographs
	{0xff00, 0xffef}, // half-width and full-width forms
}

// japaneseRune reports a rune in japaneseRanges.
func japaneseRune(r rune) bool {
	for _, rg := range japaneseRanges {
		if r >= rg[0] && r <= rg[1] {
			return true
		}
	}
	return false
}

// TestRepoIsEnglishOnly: paritydesk prints English only and the
// plugin's skills translate, so no file of the repository holds Japanese
// characters (Han, Hiragana, Katakana, CJK punctuation or full-width
// forms). Every hit is reported as file:line.
func TestRepoIsEnglishOnly(t *testing.T) {
	files := textFiles(t, nil)
	if len(files) < 100 {
		t.Fatalf("found only %d files", len(files))
	}
	for _, f := range files {
		for i, line := range strings.Split(string(f.data), "\n") {
			if j := strings.IndexFunc(line, japaneseRune); j >= 0 {
				r, _ := utf8.DecodeRuneInString(line[j:])
				t.Errorf("%s:%d holds U+%04X", f.rel, i+1, r)
			}
		}
	}
}

// TestSkillsTranslateFaithfully (P9): every skill talks to the user in
// the user's language and, when it shows printed lines, says to show
// every line and translate each one faithfully.
func TestSkillsTranslateFaithfully(t *testing.T) {
	const talk = "Talk to the user in the language they use."
	const lines = "Show every line. If the user writes in another language, translate each line faithfully into it"
	files := textFiles(t, func(rel string) bool {
		return strings.HasPrefix(rel, "plugin/skills/") && strings.HasSuffix(rel, "/SKILL.md")
	})
	if len(files) < 3 {
		t.Fatalf("found only %d skills", len(files))
	}
	for _, f := range files {
		s := strings.Join(strings.Fields(string(f.data)), " ")
		if !strings.Contains(s, talk) {
			t.Errorf("%s lacks the sentence %q", f.rel, talk)
		}
		if strings.Contains(s, "display_") {
			t.Errorf("%s still names a display_ field", f.rel)
		}
		if strings.Contains(s, "`lines`") && !strings.Contains(s, lines) {
			t.Errorf("%s shows lines but lacks %q", f.rel, lines)
		}
	}
}
