package render

import (
	"path"
	"strconv"
	"strings"

	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/sources/scratch"
)

// ScratchSummary is the "scratch" member of plan --json and switch
// --json: what row scratch carries (spec-scratch 6.11).
type ScratchSummary struct {
	Workspaces       int                 `json:"workspaces"`        // workspaces with any op
	FilesAdded       int                 `json:"files_added"`       // copies to new paths
	FilesUpdated     int                 `json:"files_updated"`     // copies over existing files
	FilesQuarantined int                 `json:"files_quarantined"` // removals into the generation
	ConflictCopies   int                 `json:"conflict_copies"`
	GitUnits         int                 `json:"git_units"` // units replaced whole
	GitDiverged      int                 `json:"git_diverged"`
	HeldBack         int                 `json:"held_back"` // folders not carried
	Deferred         int                 `json:"deferred"`  // removals deferred
	Bytes            int64               `json:"bytes"`     // sum of Copy.Size
	NotCarried       []ScratchNotCarried `json:"not_carried,omitempty"`
	// SourceChanged counts soft copies left as they were at apply
	// (switch --json after apply).
	SourceChanged int `json:"source_changed,omitempty"`
}

// ScratchNotCarried is one workspace that was not carried, with its
// note constant as the reason.
type ScratchNotCarried struct {
	Workspace string `json:"workspace"` // leaf only
	Reason    string `json:"reason"`    // a scratch.Note* constant
}

// notCarriedNotes are the per-workspace notes listed in NotCarried.
var notCarriedNotes = map[string]bool{
	scratch.NoteOversizeFiles: true, scratch.NoteOversizeBytes: true, scratch.NoteBudget: true,
	scratch.NoteRootLink: true, scratch.NoteUnreadable: true, scratch.NoteMissing: true, scratch.NoteReadOnly: true,
}

// SummarizeScratch returns the scratch summary of a plan (pure), or nil
// when the plan has no scratch change and no scratch skip.
func SummarizeScratch(p *plan.Plan) *ScratchSummary {
	if p == nil {
		return nil
	}
	_, skipped := p.Skipped[model.RowScratch]
	has := skipped
	s := &ScratchSummary{}
	ws := map[string]bool{}
	for _, op := range p.Ops {
		if op.Row != model.RowScratch {
			continue
		}
		has = true
		if w := opWorkspace(op.Path); w != "" {
			ws[w] = true
		}
		switch op.Kind {
		case plan.OpCopyFile:
			if op.PreHash == "" {
				s.FilesAdded++
			} else {
				s.FilesUpdated++
			}
			if op.Copy != nil {
				s.Bytes += op.Copy.Size
			}
		case plan.OpQuarantine:
			s.FilesQuarantined++
		}
	}
	s.Workspaces = len(ws)
	seenNC := map[string]bool{}
	for _, c := range p.Changes {
		if c.Row != model.RowScratch {
			continue
		}
		has = true
		switch {
		case c.Kind == model.ChangeSkipped && c.Note == scratch.NoteHeldBack:
			s.HeldBack++
		case c.Kind == model.ChangeSkipped && (c.Note == scratch.NoteGitDiverged || c.Note == scratch.NoteGitConflict):
			s.GitDiverged++
		case c.Kind == model.ChangeSkipped && c.Note == scratch.NoteDeletionDefer:
			s.Deferred++
		case c.Kind == model.ChangeSkipped && c.Note == scratch.NoteSourceChanged:
			s.SourceChanged++
		case c.Kind == model.ChangeSkipped && notCarriedNotes[c.Note]:
			id := c.Key + "\x00" + c.Note
			if !seenNC[id] {
				seenNC[id] = true
				s.NotCarried = append(s.NotCarried, ScratchNotCarried{Workspace: leafOf(c.Key), Reason: c.Note})
			}
		case c.Kind != model.ChangeSkipped && scratch.IsGitUnitKey(c.Key):
			s.GitUnits++
		}
	}
	for _, c := range p.Conflicts {
		if c.Row == model.RowScratch && c.Loser != "" {
			s.ConflictCopies++
		}
	}
	if !has {
		return nil
	}
	return s
}

// opWorkspace returns "<acct>/<org>/<leaf>" of a scratch op path.
func opWorkspace(p string) string {
	segs := strings.Split(p, "/")
	if len(segs) < 4 {
		return ""
	}
	return strings.Join(segs[1:4], "/")
}

// leafOf returns the leaf of a workspace key.
func leafOf(key string) string {
	segs := strings.Split(strings.TrimSuffix(key, "/"), "/")
	if len(segs) < 3 {
		return Sanitize(key, 80)
	}
	return Sanitize(segs[2], 80)
}

// ScratchKey renders a scratch item key without its account and
// organization: leaf plus the path inside the workspace.
func ScratchKey(key string) string {
	segs := strings.SplitN(key, "/", 4)
	if len(segs) < 3 {
		return Sanitize(key, 300)
	}
	out := segs[2]
	if len(segs) == 4 && segs[3] != "" {
		out += "/" + segs[3]
	}
	return Sanitize(out, 300)
}

// perWorkspaceNotes are the notes reported once per workspace.
var perWorkspaceNotes = map[string]bool{
	scratch.NoteRootLink: true, scratch.NoteUnreadable: true, scratch.NoteReadOnly: true,
	scratch.NoteOversizeFiles: true, scratch.NoteOversizeBytes: true, scratch.NoteBudget: true,
}

// ScratchNotices returns the plan notices of row scratch (spec-scratch
// 13.1) in catalog order. The notices that name limits (oversize, budget,
// no space) need the plan's ScratchNotes, which planning sets; a plan
// read from a file has none and shows them without those lines.
func ScratchNotices(p *plan.Plan, from, to string) []i18n.Text {
	if p == nil {
		return nil
	}
	from, to = Sanitize(from, 32), Sanitize(to, 32)
	count := map[string]int{}
	perWS := map[string][]string{}
	seen := map[string]bool{}
	for _, c := range p.Changes {
		if c.Row != model.RowScratch || c.Kind != model.ChangeSkipped {
			continue
		}
		if perWorkspaceNotes[c.Note] {
			// One notice (or one count) per workspace, whichever side
			// reported it.
			id := c.Key + "\x00" + c.Note
			if seen[id] {
				continue
			}
			seen[id] = true
			perWS[c.Note] = append(perWS[c.Note], leafOf(c.Key))
		}
		count[c.Note]++
	}
	conflicts, quarantined := 0, 0
	mirror := false
	for _, c := range p.Conflicts {
		if c.Row == model.RowScratch && c.Loser != "" {
			conflicts++
		}
	}
	for _, op := range p.Ops {
		if op.Row != model.RowScratch {
			continue
		}
		if op.Kind == plan.OpQuarantine {
			quarantined++
		}
		if w := opWorkspace(op.Path); w != "" && path.Dir(w) != p.Target.Replica.Key() {
			mirror = true
		}
	}
	var out []i18n.Text
	add := func(n int, id i18n.Msg, args ...any) {
		if n > 0 {
			out = append(out, i18n.Tx(id, append([]any{n}, args...)...))
		}
	}
	if lim := p.ScratchNotes; lim != nil {
		for _, note := range []string{scratch.NoteOversizeFiles, scratch.NoteOversizeBytes} {
			for _, leaf := range perWS[note] {
				out = append(out, i18n.Tx(i18n.ScratchOversize, leaf, lim.MaxFilesPerWorkspace, Bytes(lim.MaxWorkspaceBytes)))
			}
		}
		add(count[scratch.NoteBudget], i18n.ScratchBudget, Bytes(lim.MaxRunBytes), lim.MaxRunFiles)
	}
	for _, leaf := range perWS[scratch.NoteRootLink] {
		out = append(out, i18n.Tx(i18n.ScratchRootLink, leaf))
	}
	for _, leaf := range perWS[scratch.NoteUnreadable] {
		out = append(out, i18n.Tx(i18n.ScratchUnreadable, leaf))
	}
	for _, leaf := range perWS[scratch.NoteReadOnly] {
		out = append(out, i18n.Tx(i18n.ScratchReadOnly, leaf))
	}
	add(count[scratch.NoteHeldBack], i18n.ScratchHeldBack, to)
	add(conflicts, i18n.ScratchConflictCopy)
	add(count[scratch.NoteGitConflict], i18n.ScratchGitConflict)
	add(count[scratch.NoteGitDiverged], i18n.ScratchGitDiverged)
	add(count[scratch.NoteTypeConflict], i18n.ScratchTypeConflict, to)
	add(count[scratch.NoteLinkKept], i18n.ScratchLinkKept)
	add(count[scratch.NoteCollision], i18n.ScratchNameCollision, to)
	add(count[scratch.NoteReserved], i18n.ScratchReservedName)
	add(count[scratch.NoteName], i18n.ScratchUnsupportedName)
	add(count[scratch.NoteSpecial], i18n.ScratchSpecial)
	add(count[scratch.NoteBytes], i18n.ScratchBytesUnavailable)
	if count[scratch.NoteDeletionOff] > 0 {
		out = append(out, i18n.Tx(i18n.ScratchDeletionsOff, to))
	}
	add(count[scratch.NoteDeletionDefer], i18n.ScratchDeletionDeferred, to)
	add(quarantined, i18n.ScratchQuarantined, from, to)
	add(count[scratch.NoteSourceChanged], i18n.ScratchSourceChanged, to)
	add(count[scratch.NoteMissing], i18n.ScratchMissing)
	add(count[scratch.NoteUnrecognized], i18n.ScratchUnrecognized)
	if lim := p.ScratchNotes; lim != nil && lim.NeedBytes > 0 && p.Skipped[model.RowScratch] == ReasonScratchSpace {
		out = append(out, i18n.Tx(i18n.ScratchNoSpace, Bytes(lim.NeedBytes), Bytes(lim.FreeBytes)))
	}
	if p.Skipped[model.RowScratch] == ReasonScratchManaged {
		out = append(out, i18n.Tx(i18n.ScratchManaged))
	}
	if mirror {
		out = append(out, i18n.Tx(i18n.ScratchPreviewLimit))
	}
	return out
}

// ReasonScratchManaged is the Plan.Skipped reason of row scratch across a
// managed edge (spec-scratch 13.2).
const ReasonScratchManaged = "not carried across a managed account (scratch.cross_managed)"

// ReasonScratchSpace is the Plan.Skipped reason of row scratch when the
// disk may be too full for its copies (spec-scratch 13.2).
const ReasonScratchSpace = "not enough free disk space"

// WithScratch fills the scratch fields of a plan view from its plan.
func WithScratch(v PlanView, p *plan.Plan) PlanView {
	v.Scratch = SummarizeScratch(p)
	v.ScratchNotes = ScratchNotices(p, v.From, v.To)
	return v
}

// scratchLines returns the scratch notices of a plan view.
func scratchLines(l i18n.Lang, v PlanView) []string { return i18n.Strings(l, v.ScratchNotes) }

// Bytes formats a byte count for messages: "512 B", "1.5 KiB", "2.0 GiB".
func Bytes(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(n, 10) + " B"
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	f := float64(n) / 1024
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	return strconv.FormatFloat(f, 'f', 1, 64) + " " + units[i]
}
