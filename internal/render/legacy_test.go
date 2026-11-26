package render

import (
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/i18n"
)

func TestPlanLinesSeededFirstSync(t *testing.T) {
	lines := PlanLines(PlanView{From: "a", To: "b", FirstSync: true, Seeded: true})
	if lines[1] != i18n.T(i18n.PlanFirstSyncSeeded) {
		t.Fatalf("lines %v", lines)
	}
	lines = PlanLines(PlanView{From: "a", To: "b", FirstSync: true})
	if lines[1] != i18n.T(i18n.PlanFirstSync) {
		t.Fatalf("lines %v", lines)
	}
	// Seeded without a first sync adds nothing; DESTRUCTIVE stays last.
	lines = PlanLines(PlanView{From: "a", To: "b", Seeded: true})
	if strings.Contains(strings.Join(lines, "\n"), "adopting") || !strings.HasPrefix(lines[len(lines)-1], "DESTRUCTIVE") {
		t.Fatalf("lines %v", lines)
	}
}
