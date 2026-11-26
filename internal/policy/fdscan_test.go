package policy

import (
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The native open-file scan calls libSystem through linknamed helpers and
// assembly trampolines. These rules fence that code (internal/fdscan).

const (
	fdscanDir     = "internal/fdscan/"
	fdscantestDir = "internal/fdscan/fdscantest/"
	fdscantestPkg = "github.com/kazupc/paritydesk/internal/fdscan/fdscantest"
)

// libSystemFiles are the only files that may import libSystem symbols.
var libSystemFiles = map[string]bool{
	"internal/fdscan/libproc_darwin.go":             true,
	"internal/fdscan/fdscantest/fileport_darwin.go": true,
}

// Directive prefixes, built so this file's own lines never start with
// them.
var (
	dirLinkname = "//" + "go:linkname"
	dirCgoDyn   = "//" + "go:cgo_import_dynamic"
)

// readRel reads a tracked file.
func readRel(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// hasDirective reports whether a line of text starts with prefix.
func hasDirective(text, prefix string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			return true
		}
	}
	return false
}

// TestLibSystemImportsFenced: linkname and cgo_import_dynamic directives
// appear only in the two libSystem files, and assembly files only under
// internal/fdscan.
func TestLibSystemImportsFenced(t *testing.T) {
	for _, rel := range trackedFiles(t) {
		if strings.HasSuffix(rel, ".s") {
			if !strings.HasPrefix(rel, fdscanDir) {
				t.Errorf("%s: assembly outside %s", rel, fdscanDir)
			}
			continue
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasPrefix(rel, policyDir) || libSystemFiles[rel] {
			continue
		}
		text := readRel(t, rel)
		if hasDirective(text, dirLinkname) || hasDirective(text, dirCgoDyn) {
			t.Errorf("%s: linkname or cgo_import_dynamic outside %v", rel, libSystemFiles)
		}
	}
}

// TestFdscantestOnlyInTests: the test helper package (it links
// fileport_makeport) is imported only by test files.
func TestFdscantestOnlyInTests(t *testing.T) {
	for _, g := range goFiles(t, false) {
		if strings.HasPrefix(g.rel, fdscantestDir) {
			continue
		}
		if importName(g.file, fdscantestPkg) != "" {
			t.Errorf("%s imports %s; only test files may", g.rel, fdscantestPkg)
		}
	}
}

// nativeOnly reports whether a //go:build expression selects darwin
// without pd_nofdscan only.
func nativeOnly(expr constraint.Expr) bool {
	eval := func(tags ...string) bool {
		return expr.Eval(func(tag string) bool {
			for _, t := range tags {
				if t == tag {
					return true
				}
			}
			return false
		})
	}
	return eval("darwin") && !eval("darwin", "pd_nofdscan") && !eval("linux")
}

// buildExpr returns the file's //go:build expression, or nil.
func buildExpr(text string) constraint.Expr {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if constraint.IsGoBuild(line) {
			if e, err := constraint.Parse(line); err == nil {
				return e
			}
		}
		if line != "" && !strings.HasPrefix(line, "//") {
			break // build lines come before the package clause
		}
	}
	return nil
}

// TestNofdscanTags: every file that holds native-only code (linkname,
// cgo_import_dynamic, a trampoline, a TestMain, or an import of
// fdscantest) under internal/fdscan, and every internal/app test file
// that imports fdscantest or declares TestMain, builds only for darwin
// without pd_nofdscan; and no package declares TestMain in more than one
// file.
func TestNofdscanTags(t *testing.T) {
	testMains := map[string][]string{}
	for _, rel := range trackedFiles(t) {
		if strings.HasPrefix(rel, policyDir) || strings.Contains(rel, "/testdata/") {
			continue
		}
		isGo, isAsm := strings.HasSuffix(rel, ".go"), strings.HasSuffix(rel, ".s")
		if !isGo && !isAsm {
			continue
		}
		text := readRel(t, rel)
		declaresMain := isGo && strings.Contains(text, "func TestMain(")
		if declaresMain {
			testMains[path.Dir(rel)] = append(testMains[path.Dir(rel)], rel)
		}
		inFdscan := strings.HasPrefix(rel, fdscanDir)
		inAppTest := strings.HasPrefix(rel, "internal/app/") && strings.HasSuffix(rel, "_test.go")
		if !inFdscan && !inAppTest {
			continue
		}
		native := declaresMain || strings.Contains(text, strconv.Quote(fdscantestPkg))
		if inFdscan {
			native = native || hasDirective(text, dirLinkname) || hasDirective(text, dirCgoDyn) ||
				(isAsm && strings.Contains(text, "TEXT "))
		}
		if !native {
			continue
		}
		if e := buildExpr(text); e == nil || !nativeOnly(e) {
			t.Errorf("%s: needs //go:build darwin && !pd_nofdscan", rel)
		}
	}
	for dir, files := range testMains {
		if len(files) > 1 {
			t.Errorf("%s: TestMain is declared in %d files %v; at most one per package", dir, len(files), files)
		}
	}
}

// TestTrampolineCallsKeepAlive: in the libSystem files, every buffer
// address is converted in the call expression itself (uintptr(unsafe.
// Pointer(unsafe.SliceData(buf))) or uintptr(unsafe.Pointer(&x))), and no
// function other than syscall6 returns a uintptr (a helper returning an
// address would lose the keep-alive guarantee). Comment lines are not
// code and are skipped.
func TestTrampolineCallsKeepAlive(t *testing.T) {
	const ptr = "unsafe.Pointer("
	for rel := range libSystemFiles {
		text := readRel(t, rel)
		for i, line := range strings.Split(text, "\n") {
			code := strings.TrimSpace(line)
			if strings.HasPrefix(code, "//") {
				continue
			}
			if j := strings.Index(code, " //"); j >= 0 {
				code = code[:j]
			}
			for off := 0; ; {
				k := strings.Index(code[off:], ptr)
				if k < 0 {
					break
				}
				at := off + k
				rest := code[at+len(ptr):]
				if !strings.HasSuffix(code[:at], "uintptr(") || !(strings.HasPrefix(rest, "unsafe.SliceData(") || strings.HasPrefix(rest, "&")) {
					t.Errorf("%s:%d: %s must be written as uintptr(unsafe.Pointer(unsafe.SliceData(buf))) or uintptr(unsafe.Pointer(&x)) in the call", rel, i+1, ptr)
				}
				off = at + len(ptr)
			}
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filepath.Join(repoRoot(t), filepath.FromSlash(rel)), text, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Name.Name == "syscall6" || fn.Type.Results == nil {
				continue
			}
			for _, r := range fn.Type.Results.List {
				if id, ok := r.Type.(*ast.Ident); ok && id.Name == "uintptr" {
					t.Errorf("%s:%d: %s returns a uintptr", rel, fset.Position(fn.Pos()).Line, fn.Name.Name)
				}
			}
		}
	}
}
