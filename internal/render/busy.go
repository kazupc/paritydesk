package render

import (
	"fmt"
	"time"

	"github.com/kazupc/paritydesk/internal/i18n"
)

// BusyLine is one working session in a plan's lines: a masked id and an
// age only. Titles never go into lines: they are data written by other
// sessions (PlanBusyTitled prints them in the human output only).
type BusyLine struct {
	Masked string
	Age    time.Duration
	Known  bool // Age is known
	Stale  bool // started long ago with no end in the log; not waited for
}

// HumanAge formats a duration as <1m, 12m or 2h05m.
func HumanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return fmt.Sprintf("%dh%02dm", int(d/time.Hour), int(d%time.Hour/time.Minute))
}

// busyLines is the busy block of PlanLines: a header with the count and
// the wait, one line per working session, and a line counting the stale
// ones.
func busyLines(v PlanView) []string {
	working, stale := 0, 0
	for _, b := range v.Busy {
		if b.Stale {
			stale++
		} else {
			working++
		}
	}
	var lines []string
	if working > 0 {
		lines = append(lines, i18n.T(i18n.PlanBusyHeader, working, HumanAge(v.BusyWait)))
		for _, b := range v.Busy {
			if b.Stale {
				continue
			}
			masked := Sanitize(b.Masked, 48)
			if b.Known {
				lines = append(lines, "  "+i18n.T(i18n.PlanBusyLine, masked, HumanAge(b.Age)))
			} else {
				lines = append(lines, "  "+i18n.T(i18n.PlanBusyLineNoTime, masked))
			}
		}
	}
	if stale > 0 {
		lines = append(lines, i18n.T(i18n.PlanBusyStale, stale))
	}
	return lines
}

// BusyTitled is the human-only block of plan and a read-only switch:
// the working sessions with their (sanitized) titles.
func BusyTitled(titles, masked []string) []string {
	if len(titles) == 0 {
		return nil
	}
	lines := []string{i18n.T(i18n.PlanBusyTitles)}
	for i := range titles {
		lines = append(lines, "  "+i18n.T(i18n.PlanBusyTitled, Sanitize(titles[i], 80), Sanitize(masked[i], 48)))
	}
	return lines
}
