package render

import "github.com/kazupc/paritydesk/internal/i18n"

// ArtifactsPlanView is the artifacts part of a plan's lines: the leaving
// profile was not checked since it became active, and how many
// artifacts wait for the entering profile (aliases and counts only).
type ArtifactsPlanView struct {
	// Unsynced is the leaving profile's alias ("" when it was checked).
	Unsynced string
	// Due is the number of artifacts to publish on To.
	Due int
	To  string
}

// artifactsLines is the artifacts block of PlanLines: the leaving side's
// line first, then the entering side's.
func artifactsLines(v PlanView) []string {
	a := v.Artifacts
	if a == nil {
		return nil
	}
	var lines []string
	if a.Unsynced != "" {
		lines = append(lines, i18n.T(i18n.PlanArtifactsUnsynced, Sanitize(a.Unsynced, 32)))
	}
	if a.Due > 0 {
		lines = append(lines, i18n.T(i18n.PlanArtifactsDue, a.Due, Sanitize(a.To, 32)))
	}
	return lines
}
