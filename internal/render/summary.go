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

// PlanLines returns the human summary of a plan in the process language.
func PlanLines(v PlanView) []string { return PlanLinesIn(i18n.Current(), v) }

// PlanLinesIn returns the summary of a plan in l: a header, one line per
// category with changes (in model.AllCategories order), and the
// DESTRUCTIVE line, always last (H7). `plan --json` returns these lines
// in English (lines) and, for another display language, in that language
// (display_lines), so the plugin's skill can show them verbatim.
func PlanLinesIn(l i18n.Lang, v PlanView) []string {
	head := i18n.PlanHeader
	if v.Approximate {
		head = i18n.PlanHeaderApprox
	}
	lines := []string{i18n.TIn(l, head, Sanitize(v.From, 32), Sanitize(v.To, 32))}
	if v.FirstSync {
		first := i18n.PlanFirstSync
		if v.Seeded {
			first = i18n.PlanFirstSyncSeeded
		}
		lines = append(lines, i18n.TIn(l, first))
	}
	n := len(lines)
	for _, cat := range model.AllCategories {
		if parts := countParts(l, v.Summary.Categories[cat]); len(parts) > 0 {
			lines = append(lines, i18n.TIn(l, i18n.PlanCategory, string(cat), strings.Join(parts, "  ")))
		}
	}
	if len(lines) == n {
		lines = append(lines, i18n.TIn(l, i18n.PlanNoChanges))
	}
	lines = append(lines, busyLines(l, v)...)
	lines = append(lines, scratchLines(l, v)...)
	lines = append(lines, onlineLines(l, v)...)
	lines = append(lines, artifactsLines(l, v)...)
	return append(lines, DestructiveLineIn(l, v.Summary))
}

// countParts formats the non-zero counts of one category.
func countParts(l i18n.Lang, c plan.Counts) []string {
	var parts []string
	add := func(n int, id i18n.Msg) {
		if n > 0 {
			parts = append(parts, i18n.TIn(l, id, n))
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

// DestructiveLine returns the fixed line with the destructive counts in
// the process language.
func DestructiveLine(s plan.Summary) string { return DestructiveLineIn(i18n.Current(), s) }

// DestructiveLineIn returns the fixed line in l; DESTRUCTIVE: is its
// first token in every language.
func DestructiveLineIn(l i18n.Lang, s plan.Summary) string {
	total := 0
	var parts []string
	for _, cat := range model.AllCategories {
		if n := s.Destructive[cat]; n > 0 {
			total += n
			parts = append(parts, i18n.TIn(l, i18n.DestructivePart, string(cat), n))
		}
	}
	if total == 0 {
		return i18n.TIn(l, i18n.DestructiveNone)
	}
	return i18n.TIn(l, i18n.DestructiveSome, total, strings.Join(parts, ", "))
}
