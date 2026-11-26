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
)

// futureIDRE is the shape of an i18n id (area.what, snake case).
var futureIDRE = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)+$`)

// TestBusyTextIDs keeps internal/orchestrator/busytext.go ready for the
// i18n-ja migration: every text constant starts with "txt" (the name
// without it is the future catalog constant) and carries a comment that
// names its future catalog id, unique and not already a catalog id.
func TestBusyTextIDs(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "internal", "orchestrator", "busytext.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := os.ReadFile(filepath.Join(root, "internal", "i18n", "catalog.go"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	n := 0
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			for _, name := range vs.Names {
				n++
				if !strings.HasPrefix(name.Name, "txt") {
					t.Errorf("%s: constant without the txt prefix", name.Name)
					continue
				}
				if vs.Doc == nil || len(vs.Doc.List) == 0 {
					t.Errorf("%s: no comment naming its future id", name.Name)
					continue
				}
				id := strings.Fields(strings.TrimPrefix(vs.Doc.List[0].Text, "//"))[0]
				switch {
				case !futureIDRE.MatchString(id):
					t.Errorf("%s: %q is not an id", name.Name, id)
				case seen[id]:
					t.Errorf("%s: id %s used twice", name.Name, id)
				case strings.Contains(string(catalog), strconv.Quote(id)):
					t.Errorf("%s: id %s is already in the catalog", name.Name, id)
				case regexp.MustCompile(`(?m)^\t` + strings.TrimPrefix(name.Name, "txt") + `\s+Msg = `).Match(catalog):
					t.Errorf("%s: the future constant is already in the catalog", name.Name)
				}
				seen[id] = true
			}
		}
	}
	if n == 0 {
		t.Fatal("no constants found in busytext.go")
	}
}

// TestSignalPatternsInManifests: every main.log pattern of the embedded
// manifests starts at the message (no "^" or ".*" in front); the loader
// anchors it. Their full validation runs when the manifests load.
func TestSignalPatternsInManifests(t *testing.T) {
	root := repoRoot(t)
	for _, name := range []string{"6.x.toml", "7.x.toml"} {
		data, err := os.ReadFile(filepath.Join(root, "internal", "compat", "manifests", name))
		if err != nil {
			t.Fatal(err)
		}
		in := false
		for i, line := range strings.Split(string(data), "\n") {
			switch {
			case strings.HasPrefix(line, "[signals]"):
				in = true
				continue
			case strings.HasPrefix(line, "["), strings.HasPrefix(line, "# ----"):
				in = false
			}
			if !in || strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			for _, bad := range []string{"'^", "'.*", "'.+"} {
				if strings.Contains(line, bad) {
					t.Errorf("%s:%d: a signal pattern must start at the message", name, i+1)
				}
			}
		}
	}
}
