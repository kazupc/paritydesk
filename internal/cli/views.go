package cli

import (
	"sort"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/online"
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
	Saved             *bool            `json:"saved,omitempty"`
	Generation        uint64           `json:"generation,omitempty"`
	Code              pderr.Code       `json:"code"`
	Detached          bool             `json:"detached,omitempty"`
	QuitPending       bool             `json:"quit_pending,omitempty"`
	Summary           plan.Summary     `json:"summary"`
	Lines             []string         `json:"lines"` // English always
	Warnings          []i18n.Text      `json:"warnings,omitempty"`
	TimingsMS         map[string]int64 `json:"timings_ms,omitempty"`
	// Busy lists the sessions still working; titles are data written by
	// other sessions and never appear in Lines.
	Busy []orchestrator.BusyView `json:"busy,omitempty"`
	// humanBusy is the titled busy block of the human output only.
	humanBusy []string

	Scratch *render.ScratchSummary `json:"scratch,omitempty"`

	// Online counts what the switch applies inside Claude afterwards
	// (counts and fixed words only).
	Online *online.Summary `json:"online,omitempty"`

	// Lang and DisplayLines are set only when the display language is
	// not English: the same lines as Lines in that language, for the
	// plugin's skill to show verbatim.
	Lang         i18n.Lang `json:"lang,omitempty"`
	DisplayLines []string  `json:"display_lines,omitempty"`
	// pv is the plan behind Lines (nil without a plan); not encoded.
	pv *render.PlanView
}

// HumanLines returns the plan lines in the process language (nil without
// a plan): human output never uses the display language.
func (v runView) HumanLines() []string {
	if v.pv == nil {
		return nil
	}
	return render.PlanLinesIn(i18n.Current(), *v.pv)
}

// newRunView builds the view of a result. Lines are the human summary,
// DESTRUCTIVE line last.
func newRunView(r *orchestrator.Result) runView {
	v := runView{
		Run: r.RunID, From: r.From, To: r.To, Mode: r.Mode,
		PlanHash: r.PlanHash, DestructiveDigest: r.DestructiveDigest,
		Generation: r.Generation, Code: r.Code, Detached: r.Detached, QuitPending: r.QuitPending,
		Warnings: sanitizeTexts(r.Warnings),
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
	}
	switch {
	case r.Summary != nil:
		v.Summary = *r.Summary
	case r.Plan != nil:
		v.Summary = r.Plan.Summarize()
	default:
		v.Summary = (*plan.Plan)(nil).Summarize()
	}
	var busy []render.BusyLine
	var titles, masked []string
	for _, b := range r.Busy {
		b.Title = render.Sanitize(b.Title, 80)
		b.Session = render.Sanitize(b.Session, 48)
		v.Busy = append(v.Busy, b)
		busy = append(busy, render.BusyLine{Masked: b.Session, Age: time.Duration(b.WorkingForS) * time.Second,
			Known: b.StartedKnown || b.WorkingForS > 0, Stale: b.Stale})
		if !b.Stale {
			titles, masked = append(titles, b.Title), append(masked, b.Session)
		}
	}
	var ov *render.OnlinePlanView
	if o := r.Online; o != nil {
		s := *o
		s.PinsSource = render.Sanitize(s.PinsSource, 16)
		s.PinsReason = render.Sanitize(s.PinsReason, 32)
		v.Online = &s
		unreadable, preview := render.PinsLineFlags(s.PinsSource, s.PinsReason, v.Approximate)
		ov = &render.OnlinePlanView{PinsAdd: s.PinsAdd, PinsRemove: s.PinsRemove, Sidebar: s.Sidebar,
			PinsUnreadable: unreadable, PinsPreviewUnavailable: preview}
	}
	var av *render.ArtifactsPlanView
	if a := r.Artifacts; a != nil {
		av = &render.ArtifactsPlanView{Unsynced: string(a.Unsynced), Due: a.Due, To: string(a.To)}
	}
	// Without a target the run stopped before planning: no plan to show.
	if v.To != "" {
		pv := render.WithScratch(render.PlanView{
			From: string(v.From), To: string(v.To), Approximate: v.Approximate,
			FirstSync: v.NeedsConfirmation, Summary: v.Summary, Busy: busy, BusyWait: r.BusyWait, Online: ov, Artifacts: av,
			Seeded: r.Plan != nil && r.Plan.Seeded,
		}, r.Plan)
		v.Scratch = pv.Scratch
		v.pv = &pv
		v.Lines = render.PlanLinesIn(i18n.English, pv)
		if l := i18n.DisplayLang(); l != i18n.English {
			v.Lang, v.DisplayLines = l, render.PlanLinesIn(l, pv)
		}
		v.humanBusy = render.BusyTitled(titles, masked)
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

// sanitizeTexts sanitizes the arguments of every text of a list and
// bounds its rendering to 300 runes in every language.
func sanitizeTexts(in []i18n.Text) []i18n.Text {
	if len(in) == 0 {
		return nil
	}
	out := make([]i18n.Text, len(in))
	for i, t := range in {
		out[i] = sanitizeText(t, 300)
	}
	return out
}

// sanitizeText sanitizes t's arguments and bounds its rendering.
func sanitizeText(t i18n.Text, max int) i18n.Text {
	return t.MapStrings(func(s string) string { return render.Sanitize(s, 0) }).Cut(max)
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
	c.Warnings = sanitizeTexts(c.Warnings)
	for i := range c.Profiles {
		c.Profiles[i].Problem = sanitizeText(c.Profiles[i].Problem, 300)
	}
	sort.SliceStable(c.Profiles, func(i, j int) bool { return c.Profiles[i].Alias < c.Profiles[j].Alias })
	return &c
}
