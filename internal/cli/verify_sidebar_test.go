package cli

import (
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/model"
)

// TestVerifyUnverifiedLine: rows that could not be read back are listed
// right after the replaced line, and verify still exits 0.
func TestVerifyUnverifiedLine(t *testing.T) {
	h := newHarness(t)
	h.svc.verify.Replaced = []model.RowID{model.RowGroupOrder}
	h.svc.verify.Unverified = []model.RowID{model.RowSidebarCollapse}
	code, out, errOut := h.run("", "verify")
	if code != 0 {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	rep := strings.Index(out, "nothing to do: group-order")
	unv := strings.Index(out, "checked at the next switch: sidebar-collapse")
	if rep < 0 || unv < rep {
		t.Fatalf("output %q", out)
	}
	h = newHarness(t)
	if _, out, _ := h.run("", "verify"); strings.Contains(out, "next switch") {
		t.Fatalf("unverified line without rows: %q", out)
	}
}
