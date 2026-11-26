package policy

import (
	"encoding/hex"
	"go/ast"
	"path"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/model"
)

const (
	idbDir      = "internal/store/idb/"
	idbtestPkg  = "github.com/kazupc/paritydesk/internal/store/idb/idbtest"
	appsimPkg   = "github.com/kazupc/paritydesk/internal/online/appsim"
	pinsPkgDir  = "internal/sources/pins/"
	sourcesPkg  = "github.com/kazupc/paritydesk/internal/sources"
	idbtestDir  = "internal/store/idb/idbtest/"
	appsimDir   = "internal/online/appsim/"
	fakeUpdated = "1700000000000"
)

// TestIDBReaderReadOnly (spec-pins 15.5): the IndexedDB reader opens files
// only read-only, never opens a database with goleveldb, calls no mutating
// fsx method outside clone.go (its own temporary root, through the
// confined Cloner), and names LOG only in its skip list.
func TestIDBReaderReadOnly(t *testing.T) {
	mutating := map[string]bool{"SwapDirs": true, "RenameExcl": true, "WriteFileAtomic": true, "MoveInto": true,
		"CloneFileAtomic": true, "Mkdir": true, "Symlink": true, "RemoveEmptyDir": true, "SetXattr": true,
		"Clone": true, "MkdirAll": true, "RemoveAll": true, "CloneFileOnly": true}
	writeFlags := []string{"O_WRONLY", "O_RDWR", "O_CREATE", "O_APPEND", "O_TRUNC"}
	for _, g := range goFiles(t, false) {
		if !strings.HasPrefix(g.rel, idbDir) || strings.HasPrefix(g.rel, idbtestDir) {
			continue
		}
		ldb := importName(g.file, "github.com/syndtr/goleveldb/leveldb")
		osName := importName(g.file, "os")
		ast.Inspect(g.file, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.SelectorExpr:
				x, ok := v.X.(*ast.Ident)
				if !ok {
					return true
				}
				if ldb != "" && x.Name == ldb && strings.HasPrefix(v.Sel.Name, "Open") {
					t.Errorf("%s:%d: %s.%s opens a database", g.rel, g.fset.Position(v.Pos()).Line, x.Name, v.Sel.Name)
				}
				if x.Name == osName && (v.Sel.Name == "Create" || v.Sel.Name == "WriteFile" || v.Sel.Name == "Remove" ||
					v.Sel.Name == "RemoveAll" || v.Sel.Name == "Rename" || v.Sel.Name == "Mkdir" || v.Sel.Name == "MkdirAll") {
					t.Errorf("%s:%d: os.%s writes", g.rel, g.fset.Position(v.Pos()).Line, v.Sel.Name)
				}
				for _, f := range writeFlags {
					if v.Sel.Name == f {
						t.Errorf("%s:%d: %s opens for writing", g.rel, g.fset.Position(v.Pos()).Line, f)
					}
				}
			case *ast.CallExpr:
				sel, ok := v.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if x, isID := sel.X.(*ast.Ident); isID && (x.Name == "slices" || x.Name == "bytes" || x.Name == "maps") {
					return true
				}
				if mutating[sel.Sel.Name] && g.rel != idbDir+"clone.go" {
					t.Errorf("%s:%d: %s outside clone.go", g.rel, g.fset.Position(v.Pos()).Line, sel.Sel.Name)
				}
			case *ast.BasicLit:
				if s, err := strconv.Unquote(v.Value); err == nil && strings.Contains(s, "LOG") && g.rel != idbDir+"scan.go" {
					t.Errorf("%s:%d: names LOG outside the skip list", g.rel, g.fset.Position(v.Pos()).Line)
				}
			}
			return true
		})
	}
	// In scan.go, LOG appears only in skipFile.
	for _, g := range goFiles(t, false) {
		if g.rel != idbDir+"scan.go" {
			continue
		}
		for _, d := range g.file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Name.Name == "skipFile" {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				if lit, ok := n.(*ast.BasicLit); ok && strings.Contains(lit.Value, "LOG") {
					t.Errorf("scan.go: %s names LOG", fn.Name.Name)
				}
				return true
			})
		}
	}
}

// TestPinsTestPackagesOnlyInTests: idbtest and appsim are imported only
// by test files.
func TestPinsTestPackagesOnlyInTests(t *testing.T) {
	for _, g := range goFiles(t, false) {
		if strings.HasPrefix(g.rel, idbtestDir) || strings.HasPrefix(g.rel, appsimDir) {
			continue
		}
		for _, pkg := range []string{idbtestPkg, appsimPkg} {
			if importName(g.file, pkg) != "" {
				t.Errorf("%s imports %s; only test files may", g.rel, pkg)
			}
		}
	}
}

// TestOnlyPinsReturnsSourceUnavailable: only the pins adapter returns
// sources.ErrSourceUnavailable (it alone may skip its row).
func TestOnlyPinsReturnsSourceUnavailable(t *testing.T) {
	for _, g := range goFiles(t, false) {
		if !strings.HasPrefix(g.rel, "internal/sources/") || strings.HasPrefix(g.rel, pinsPkgDir) || g.rel == "internal/sources/sources.go" {
			continue
		}
		name := importName(g.file, sourcesPkg)
		ast.Inspect(g.file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "ErrSourceUnavailable" {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == name {
				t.Errorf("%s:%d: only the pins adapter may return sources.ErrSourceUnavailable", g.rel, g.fset.Position(sel.Pos()).Line)
			}
			return true
		})
	}
}

// TestNoRowWritesThePinsMirror: no manifest row addresses the app's own
// mirror of the pin list (Local Storage or the config bucket), and the
// Local Storage mirror is never touched.
func TestNoRowWritesThePinsMirror(t *testing.T) {
	ms, err := compat.Load("")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		covered := false
		for _, g := range m.Classification.LSNeverTouched {
			if ok, _ := path.Match(strings.ToLower(g), "lss-persisted.starred-local-code-sessions"); ok {
				covered = true
			}
		}
		if !covered {
			t.Errorf("%s: the pins mirror is not in ls_never_touched", m.Name)
		}
		for _, r := range m.Rows {
			for _, k := range r.LSKeys {
				if strings.Contains(strings.ToLower(k), "starred-local-code-sessions") {
					t.Errorf("%s: row %s names the pins mirror", m.Name, r.Row)
				}
			}
			for _, p := range r.MirrorPaths {
				if strings.Contains(strings.Join(p, "/"), "starred-local-code-sessions") {
					t.Errorf("%s: row %s writes the pins mirror", m.Name, r.Row)
				}
			}
			if r.Row == model.RowPins && r.WritePath != compat.PathOn && r.WritePath != compat.PathNone {
				t.Errorf("%s: row pins is written offline (%s)", m.Name, r.WritePath)
			}
		}
	}
}

var (
	localIDRE   = regexp.MustCompile(`local_[0-9a-f-]{36}`)
	fakeLocalRE = regexp.MustCompile(`^local_00000000-0000-4000-8000-[0-9a-f]{12}$`)
	updatedRE   = regexp.MustCompile(`"updatedAt"\s*:\s*([0-9]+)`)
)

// TestPinsFixturesFake: every .hex and .json fixture under testdata that
// holds local_ ids holds only fake ones, and no updatedAt but the fixed
// one (hex fixtures are decoded first).
func TestPinsFixturesFake(t *testing.T) {
	for _, f := range textFiles(t, func(rel string) bool {
		return strings.Contains("/"+rel, "/testdata/") && (strings.HasSuffix(rel, ".hex") || strings.HasSuffix(rel, ".json") || strings.HasSuffix(rel, ".golden"))
	}) {
		data := string(f.data)
		if strings.HasSuffix(f.rel, ".hex") {
			b, err := hex.DecodeString(strings.TrimSpace(data))
			if err != nil {
				t.Errorf("%s: not hex", f.rel)
				continue
			}
			data = string(b)
		}
		for _, id := range localIDRE.FindAllString(data, -1) {
			if !fakeLocalRE.MatchString(id) {
				t.Errorf("%s holds a local id that is not a fake", f.rel)
			}
		}
		for _, m := range updatedRE.FindAllStringSubmatch(data, -1) {
			if m[1] != fakeUpdated {
				t.Errorf("%s holds updatedAt %s", f.rel, m[1])
			}
		}
	}
}

// TestPinsTextsOnlyInCatalog: the English texts of the pins messages live
// only in internal/i18n.
func TestPinsTextsOnlyInCatalog(t *testing.T) {
	ids := []i18n.Msg{i18n.OnlineQPinsKeep, i18n.OnlineQPinsUnion, i18n.OnlineQPinsBlocked, i18n.OnlineWarnPinsFamilyBlocked,
		i18n.OnlineWarnPinsHidden, i18n.OnlineWarnPinsMirror, i18n.OnlineRetryPinsRemovalLate, i18n.PlanOnlinePinsPreviewUnavailable,
		i18n.DoctorOnlinePinsIDBCount, i18n.DoctorOnlinePinsMirrorCount, i18n.DoctorOnlinePinsUnavailableReason,
		i18n.DoctorOnlinePinsStale, i18n.DoctorOnlinePinsMirrorDiffers, i18n.DoctorOnlinePinsOff,
		i18n.PinsReasonAmbiguous, i18n.PinsReasonUnsupported, i18n.PinsReasonBlobWrapped, i18n.PinsReasonUnclean,
		i18n.PinsReasonBackend, i18n.PinsReasonStaleStore}
	var needles []string
	for _, id := range ids {
		text := i18n.T(id)
		// The fixed part before the first verb is distinctive enough.
		if i := strings.Index(text, "%"); i > 0 {
			text = text[:i]
		}
		if len(text) >= 24 {
			needles = append(needles, text)
		}
	}
	for _, g := range goFiles(t, false) {
		if strings.HasPrefix(g.rel, "internal/i18n/") {
			continue
		}
		ast.Inspect(g.file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			for _, needle := range needles {
				if strings.Contains(s, needle) {
					t.Errorf("%s:%d: holds the text of a catalog message", g.rel, g.fset.Position(lit.Pos()).Line)
				}
			}
			return true
		})
	}
}
