package compat

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestFieldRolesCoverRoleConstants checks that every Role* string
// constant of packages rows and sources is a role legacy.agree_ignoring_roles
// accepts, so a valid manifest override naming one is never refused.
func TestFieldRolesCoverRoleConstants(t *testing.T) {
	found := 0
	for _, dir := range []string{"../rows", "../sources"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range file.Decls {
				g, ok := d.(*ast.GenDecl)
				if !ok || g.Tok != token.CONST {
					continue
				}
				for _, sp := range g.Specs {
					vs := sp.(*ast.ValueSpec)
					for i, n := range vs.Names {
						if !strings.HasPrefix(n.Name, "Role") || i >= len(vs.Values) {
							continue
						}
						lit, ok := vs.Values[i].(*ast.BasicLit)
						if !ok || lit.Kind != token.STRING {
							continue
						}
						v, _ := strconv.Unquote(lit.Value)
						found++
						if !fieldRoles[v] {
							t.Errorf("%s: %s = %q is not in compat's fieldRoles", f, n.Name, v)
						}
					}
				}
			}
		}
	}
	if found < 20 {
		t.Fatalf("only %d role constants found; the scan is broken", found)
	}
}

// TestLegacyProtectedBelowRoots checks the per-path floor of leftovers:
// a never_touched path deep inside a quarantinable root is protected in
// both manifests, an ordinary session file is not.
func TestLegacyProtectedBelowRoots(t *testing.T) {
	ms, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	const a, o = "00000000-0000-4000-8000-000000000201", "00000000-0000-4000-8000-000000000202"
	for _, m := range ms {
		for rel, want := range map[string]bool{
			"claude-code-sessions/" + a + "/" + o + "/scratchowed_x": m.Name == "7.x",
			"claude-code-sessions/" + a + "/" + o + "/local_x.json":  false,
			"claude-code-sessions/" + a:                              false,
			"IndexedDB/x":                                            true,
			"config.json":                                            true,
		} {
			if got := m.LegacyProtected(rel); got != want {
				t.Errorf("%s: LegacyProtected(%s) = %v, want %v", m.Name, rel, got, want)
			}
		}
	}
}
