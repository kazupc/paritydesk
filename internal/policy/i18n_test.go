package policy

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/pderr"
)

// Policy tests of the i18n-ja item (spec-i18n-ja 14.4): user-facing text
// comes from the catalog, catalog text is never re-read as a format, and
// the model-facing hook text stays English.

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

// TestNoUntranslatedLiterals lists every remaining site that would put
// English prose in front of a person outside the catalog (P1-P4, P10-P12).
func TestNoUntranslatedLiterals(t *testing.T) {
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
				if call, fn, ok := isCall(n, name, "T", "TIn", "Tx", "En"); ok {
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

	t.Run("hook", func(t *testing.T) { // P4
		ids := catalogConstants(t)
		for _, g := range files {
			if !strings.HasPrefix(g.rel, "internal/hook/") {
				continue
			}
			name := i18nImport(g.file)
			ast.Inspect(g.file, func(n ast.Node) bool {
				if call, _, ok := isCall(n, name, "T", "TIn"); ok {
					t.Errorf("%s: hook: i18n.T in the hook package (use i18n.En)", at(g, call))
				}
				if sel, ok := n.(*ast.SelectorExpr); ok {
					if x, ok := sel.X.(*ast.Ident); ok && x.Name == name {
						if id, isMsg := ids[sel.Sel.Name]; isMsg && !i18n.EnglishOnly(i18n.Msg(id)) {
							t.Errorf("%s: hook: %s (%s) is not English-only", at(g, sel), sel.Sel.Name, id)
						}
					}
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
			if underAny(g.rel, "internal/i18n/", "internal/pderr/") {
				continue
			}
			name := i18nImport(g.file)
			ast.Inspect(g.file, func(n ast.Node) bool {
				if call, fn, ok := isCall(n, name, "Register", "RegisterIn"); ok {
					t.Errorf("%s: register: i18n.%s outside internal/i18n and internal/pderr", at(g, call), fn)
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

// catalogConstants maps every Msg constant name of catalog.go to its id.
func catalogConstants(t *testing.T) map[string]string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(repoRoot(t), "internal", "i18n", "catalog.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, s := range gd.Specs {
			vs := s.(*ast.ValueSpec)
			for i, v := range vs.Values {
				if id, ok := stringLit(v); ok {
					out[vs.Names[i].Name] = id
				}
			}
		}
	}
	if len(out) < 100 {
		t.Fatalf("found only %d catalog constants", len(out))
	}
	return out
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

// TestJapaneseErrorCatalog (P6): every text of the error catalog has a
// compatible Japanese entry, and the Japanese next keeps every command
// and flag of the English.
func TestJapaneseErrorCatalog(t *testing.T) {
	cmdRE := regexp.MustCompile(`paritydesk [a-z][a-z -]*[a-z]|--[a-z][a-z0-9-]*(?:=[A-Za-z0-9]+)?`)
	for _, e := range pderr.Catalog() {
		for _, f := range []struct {
			id   i18n.Msg
			text string
		}{{pderr.TitleID(e.Code), e.Title}, {pderr.SafeID(e.Code), e.Safe}, {pderr.NextID(e.Code), e.Next}} {
			if f.text == "" {
				continue
			}
			ja, ok := i18n.CatalogIn(i18n.Japanese)[f.id]
			if !ok {
				t.Errorf("%s has no Japanese", f.id)
				continue
			}
			if !i18n.Compatible(f.text, ja) {
				t.Errorf("%s: Japanese verbs differ", f.id)
			}
			if f.id == pderr.NextID(e.Code) {
				for _, tok := range cmdRE.FindAllString(f.text, -1) {
					tok = strings.TrimRight(strings.SplitN(tok, " (", 2)[0], " ,;")
					for _, w := range strings.Split(tok, ", then ") {
						if !strings.Contains(ja, strings.TrimSuffix(w, " then paritydesk switch")) {
							t.Errorf("%s: %q is missing in the Japanese next", f.id, w)
						}
					}
				}
			}
		}
	}
}

// jaSignOut are the Japanese words for ending an account's session (sign
// out, log out, log off, sign off), as escapes.
var jaSignOut = []string{
	"\u30b5\u30a4\u30f3\u30a2\u30a6\u30c8", "\u30ed\u30b0\u30a2\u30a6\u30c8",
	"\u30ed\u30b0\u30aa\u30d5", "\u30b5\u30a4\u30f3\u30aa\u30d5",
}

// TestNoJapaneseSignOutAdvice (P7): neither the Japanese catalog nor
// README.ja.md (when present) advises ending an account's session.
func TestNoJapaneseSignOutAdvice(t *testing.T) {
	check := func(where, s string) {
		for _, w := range jaSignOut {
			if strings.Contains(s, w) {
				t.Errorf("%s advises ending an account's session", where)
			}
		}
	}
	for id, s := range i18n.CatalogIn(i18n.Japanese) {
		check(string(id), s)
	}
	for _, f := range textFiles(t, japaneseAllowed) {
		check(f.rel, string(f.data))
	}
}

// TestSkillsMentionLanguage (P9): every skill that shows `lines` also
// shows `display_lines` and says the lines are not translated or
// reworded.
func TestSkillsMentionLanguage(t *testing.T) {
	const rule = "Show them exactly as printed; do not translate or reword them."
	files := textFiles(t, func(rel string) bool {
		return strings.HasPrefix(rel, "plugin/skills/") && strings.HasSuffix(rel, "/SKILL.md")
	})
	if len(files) < 3 {
		t.Fatalf("found only %d skills", len(files))
	}
	for _, f := range files {
		s := string(f.data)
		if !strings.Contains(s, "lines") {
			continue
		}
		if !strings.Contains(s, "display_lines") {
			t.Errorf("%s shows lines but not display_lines", f.rel)
		}
		if !strings.Contains(strings.Join(strings.Fields(s), " "), rule) {
			t.Errorf("%s lacks the sentence %q", f.rel, rule)
		}
	}
}
