package render

import "github.com/kazupc/paritydesk/internal/i18n"

// OnlinePlanView is the online part of a plan's lines: what is applied
// inside Claude after the switch (counts only; never ids or names).
type OnlinePlanView struct {
	PinsAdd, PinsRemove, Sidebar int
	// PinsUnreadable: the pins row was skipped because a pin list could
	// not be read.
	PinsUnreadable bool
	// PinsPreviewUnavailable: the preview could not read a pin list the
	// app is writing right now (read again after Claude quits).
	PinsPreviewUnavailable bool
}

// pinsPreviewReasons are the pins reasons that only mean "Claude is
// writing the list" (spec-pins 6.4 item 4).
var pinsPreviewReasons = map[string]bool{"torn": true, "pending": true, "unclean": true}

// PinsLineFlags returns the two pins flags of OnlinePlanView for a run's
// pins source and reason: an approximate plan shows a reason that only
// means "Claude is writing" as not shown now, any other as unreadable.
func PinsLineFlags(source, reason string, approximate bool) (unreadable, preview bool) {
	if source != "none" || reason == "" {
		return false, false
	}
	if approximate && pinsPreviewReasons[reason] {
		return false, true
	}
	return true, false
}

// onlineLines is the online block of PlanLines: the counts line when any
// count is non-zero, then the pins-unreadable line.
func onlineLines(l i18n.Lang, v PlanView) []string {
	o := v.Online
	if o == nil {
		return nil
	}
	var lines []string
	if o.PinsAdd+o.PinsRemove+o.Sidebar > 0 {
		lines = append(lines, i18n.TIn(l, i18n.PlanOnlineLine, o.PinsAdd, o.PinsRemove, o.Sidebar))
	}
	switch {
	case o.PinsUnreadable:
		lines = append(lines, i18n.TIn(l, i18n.PlanOnlinePinsUnreadable))
	case o.PinsPreviewUnavailable:
		lines = append(lines, i18n.TIn(l, i18n.PlanOnlinePinsPreviewUnavailable))
	}
	return lines
}
