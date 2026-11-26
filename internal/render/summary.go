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
	// Online is what the switch applies inside Claude afterwards.
	Online *OnlinePlanView
}

// PlanLines returns the human summary of a plan: a header, one line per
// category with changes (in model.AllCategories order), and the
// DESTRUCTIVE line, always last (H7). `plan --json` returns these lines
// so the plugin's skill can show them verbatim.
func PlanLines(v PlanView) []string {
	head := i18n.PlanHeader
	if v.Approximate {
		head = i18n.PlanHeaderApprox
	}
	lines := []string{i18n.T(head, Sanitize(v.From, 32), Sanitize(v.To, 32))}
	if v.FirstSync {
		lines = append(lines, i18n.T(i18n.PlanFirstSync))
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
	// v0.2 anchor: plan busy
	lines = append(lines, busyLines(v)...)

	// v0.2 anchor: plan scratch

	// v0.2 anchor: plan online
	lines = append(lines, onlineLines(v)...)

	// v0.2 anchor: plan artifacts

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

// DestructiveLine returns the fixed line with the destructive counts.
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
