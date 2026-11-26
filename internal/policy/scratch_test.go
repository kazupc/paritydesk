package policy

import (
	"go/ast"
	"strings"
	"testing"
)

// TestScratchFSOpsOnlyInFsxApplyGen: only fsx, apply and gen call the
// fsx.Ops methods row scratch added (spec-scratch 14.6). Everything else
// plans; nothing else copies, creates or removes inside a data folder.
func TestScratchFSOpsOnlyInFsxApplyGen(t *testing.T) {
	methods := map[string]bool{"CloneFileAtomic": true, "RemoveEmptyDir": true, "BarrierSync": true, "Mkdir": true, "Symlink": true}
	// Receivers that are the standard library, not fsx.Ops.
	stdlib := map[string]bool{"os": true, "unix": true, "syscall": true}
	allowed := []string{"internal/fsx/", "internal/apply/", "internal/gen/"}
	for _, f := range goFiles(t, false) {
		ok := false
		for _, a := range allowed {
			ok = ok || strings.HasPrefix(f.rel, a)
		}
		if ok {
			continue
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			call, isCall := n.(*ast.CallExpr)
			if !isCall {
				return true
			}
			sel, isSel := call.Fun.(*ast.SelectorExpr)
			if !isSel || !methods[sel.Sel.Name] {
				return true
			}
			if id, isID := sel.X.(*ast.Ident); isID && stdlib[id.Name] {
				return true
			}
			t.Errorf("%s: %s calls %s; only fsx, apply and gen may", f.rel, f.fset.Position(call.Pos()), sel.Sel.Name)
			return true
		})
	}
}
