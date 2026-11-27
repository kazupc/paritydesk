package render

import (
	"strings"
	"time"

	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/plan"
)

// PlanView is what PlanLines needs to describe a plan.
type PlanView struct {
	From, To    string // validated aliases
	Approximate bool
	// FirstSync is set when the plan needs the first-sync confirmation
	// (plan.Plan.NeedsConfirmation).
	FirstSync bool
	Summary   plan.Summary
	// Busy lists the sessions still working (masked ids and ages only)
	// and BusyWait how long the switch waits for them.
	Busy     []BusyLine
	BusyWait time.Duration
	// Scratch and ScratchNotes describe row scratch (render/scratch.go,
	// WithScratch).
	Scratch      *ScratchSummary
	ScratchNotes []i18n.Text
	// Online is what the switch applies inside Claude afterwards.
	Online *OnlinePlanView
	// Artifacts are the artifact sync lines (render/artifacts.go).
	Artifacts *ArtifactsPlanView
	// Seeded: the first switch after profile adopt --from-legacy seeded
	// the bases (plan.Plan.Seeded); its first-sync line says so.
	Seeded bool
}

// PlanLines returns the summary of a plan: a header, one line per
// category with changes (in model.AllCategories order), and the
// DESTRUCTIVE line, always last (H7). `plan --json` returns these lines
// (lines) so the plugin's skill can show every one of them.
func PlanLines(v PlanView) []string {
	head := i18n.PlanHeader
	if v.Approximate {
		head = i18n.PlanHeaderApprox
	}
	lines := []string{i18n.T(head, Sanitize(v.From, 32), Sanitize(v.To, 32))}
	if v.FirstSync {
		first := i18n.PlanFirstSync
		if v.Seeded {
			first = i18n.PlanFirstSyncSeeded
		}
		lines = append(lines, i18n.T(first))
	}
	n := len(lines)
	for _, cat := range model.AllCategories {
		if parts := countParts(v.Summary.Categories[cat]); len(parts) > 0 {
			lines = append(lines, i18n.T(i18n.PlanCategory, string(cat), strings.Join(parts, "  ")))
		}
	}
	if len(lines) == n {
		lines = append(lines, i18n.T(i18n.PlanNoChanges))
	}
	lines = append(lines, busyLines(v)...)
	lines = append(lines, scratchLines(v)...)
	lines = append(lines, onlineLines(v)...)
	lines = append(lines, artifactsLines(v)...)
	return append(lines, DestructiveLine(v.Summary))
}

// countParts formats the non-zero counts of one category.
func countParts(c plan.Counts) []string {
	var parts []string
	add := func(n int, id i18n.Msg) {
		if n > 0 {
			parts = append(parts, i18n.T(id, n))
		}
	}
	add(c.Added, i18n.CountAdded)
	add(c.Updated, i18n.CountUpdated)
	add(c.Deleted, i18n.CountDeleted)
	add(c.Revived, i18n.CountRevived)
	add(c.Moved, i18n.CountMoved)
	add(c.Conflicts, i18n.CountConflicts)
	return parts
}

// DestructiveLine returns the fixed line with the destructive counts;
// DESTRUCTIVE: is its first token.
func DestructiveLine(s plan.Summary) string {
	total := 0
	var parts []string
	for _, cat := range model.AllCategories {
		if n := s.Destructive[cat]; n > 0 {
			total += n
			parts = append(parts, i18n.T(i18n.DestructivePart, string(cat), n))
		}
	}
	if total == 0 {
		return i18n.T(i18n.DestructiveNone)
	}
	return i18n.T(i18n.DestructiveSome, total, strings.Join(parts, ", "))
}
