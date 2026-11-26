package render

import "github.com/kazupc/paritydesk/internal/i18n"

// OnlinePlanView is the online part of a plan's lines: what is applied
// inside Claude after the switch (counts only; never ids or names).
type OnlinePlanView struct {
	PinsAdd, PinsRemove, Sidebar int
	// PinsUnreadable: the pins row was skipped because a pin list could
	// not be read.
	PinsUnreadable bool
}

// onlineLines is the online block of PlanLines: the counts line when any
// count is non-zero, then the pins-unreadable line.
func onlineLines(v PlanView) []string {
	o := v.Online
	if o == nil {
		return nil
	}
	var lines []string
	if o.PinsAdd+o.PinsRemove+o.Sidebar > 0 {
		lines = append(lines, i18n.T(i18n.PlanOnlineLine, o.PinsAdd, o.PinsRemove, o.Sidebar))
	}
	if o.PinsUnreadable {
		lines = append(lines, i18n.T(i18n.PlanOnlinePinsUnreadable))
	}
	return lines
}
