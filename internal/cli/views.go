package cli

import (
	"sort"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/orchestrator"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/render"
)

// runView is the --json document of plan, switch and sync. Its fields are
// a stable schema (v1): the plugin's skill reads plan_hash,
// needs_confirmation and lines.
type runView struct {
	Run               string           `json:"run,omitempty"`
	From              ids.Alias        `json:"from,omitempty"`
	To                ids.Alias        `json:"to,omitempty"`
	Mode              compat.Mode      `json:"mode,omitempty"`
	PlanHash          string           `json:"plan_hash"`
	DestructiveDigest string           `json:"destructive_digest"`
	NeedsConfirmation bool             `json:"needs_confirmation"`
	Approximate       bool             `json:"approximate"`
	ExpiresAt         *time.Time       `json:"expires_at,omitempty"`
	Saved             *bool            `json:"saved,omitempty"`
	Generation        uint64           `json:"generation,omitempty"`
	Code              pderr.Code       `json:"code"`
	Detached          bool             `json:"detached,omitempty"`
	QuitPending       bool             `json:"quit_pending,omitempty"`
	Summary           plan.Summary     `json:"summary"`
	Lines             []string         `json:"lines"`
	Warnings          []string         `json:"warnings,omitempty"`
	TimingsMS         map[string]int64 `json:"timings_ms,omitempty"`
}

// newRunView builds the view of a result. Lines are the human summary,
// DESTRUCTIVE line last.
func newRunView(r *orchestrator.Result) runView {
	v := runView{
		Run: r.RunID, From: r.From, To: r.To, Mode: r.Mode,
		PlanHash: r.PlanHash, DestructiveDigest: r.DestructiveDigest,
		Generation: r.Generation, Code: r.Code, Detached: r.Detached, QuitPending: r.QuitPending,
		Warnings: sanitizeAll(r.Warnings),
	}
	if p := r.Plan; p != nil {
		if v.PlanHash == "" {
			v.PlanHash = p.Hash()
		}
		if v.DestructiveDigest == "" {
			v.DestructiveDigest = p.DestructiveDigest()
		}
		if v.From == "" {
			v.From = p.From
		}
		if v.To == "" {
			v.To = p.Target.Alias
		}
		v.NeedsConfirmation = p.NeedsConfirmation
		v.Approximate = p.Approximate
		if !p.ExpiresAt.IsZero() {
			t := p.ExpiresAt.UTC()
			v.ExpiresAt = &t
		}
	}
	switch {
	case r.Summary != nil:
		v.Summary = *r.Summary
	case r.Plan != nil:
		v.Summary = r.Plan.Summarize()
	default:
		v.Summary = (*plan.Plan)(nil).Summarize()
	}
	// Without a target the run stopped before planning: no plan to show.
	if v.To != "" {
		v.Lines = render.PlanLines(render.PlanView{
			From: string(v.From), To: string(v.To), Approximate: v.Approximate,
			FirstSync: v.NeedsConfirmation, Summary: v.Summary,
		})
	}
	if len(r.Timings) > 0 {
		v.TimingsMS = map[string]int64{}
		for ph, d := range r.Timings {
			v.TimingsMS[string(ph)] = d.Milliseconds()
		}
	}
	return v
}

// sanitizeAll sanitizes every string of a list.
func sanitizeAll(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = render.Sanitize(s, 300)
	}
	return out
}

// okView is the --json document of commands without other output.
type okView struct {
	OK    bool      `json:"ok"`
	Alias ids.Alias `json:"alias,omitempty"`
	From  ids.Alias `json:"from,omitempty"`
	To    ids.Alias `json:"to,omitempty"`
}

// normalizeStatus makes list fields encode as [] rather than null.
func normalizeStatus(s *orchestrator.Status) *orchestrator.Status {
	c := *s
	if c.Profiles == nil {
		c.Profiles = []orchestrator.ProfileStatus{}
	}
	c.Warnings = sanitizeAll(c.Warnings)
	sort.SliceStable(c.Profiles, func(i, j int) bool { return c.Profiles[i].Alias < c.Profiles[j].Alias })
	return &c
}
