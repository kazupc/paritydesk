package artifacts

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/online"
)

// Provider constants (5.2).
const (
	Kind       online.Kind = online.KindArtifact
	StepLetter             = 'a'
	ExtName                = "artifacts" // key in online.BuildInput.Extensions
)

// Provider builds, advances and parses artifact steps. It is registered
// after the built-in providers; in a switch's P5 Build the input has no
// "artifacts" extension, so it returns no steps there.
type Provider struct {
	Spec compat.ArtifactsSpec
	// Guard declares the tools of every publish transport (the form the
	// hooks and the online commands register, which know no manifest):
	// a queue of either transport validates, and calls still pass only as
	// exact offered steps.
	Guard bool
}

var (
	_ online.Provider     = Provider{}
	_ online.ResultParser = Provider{}
)

// Name is the provider's name (its Extensions key).
func (Provider) Name() string { return ExtName }

// Kinds are the step kinds of the provider.
func (Provider) Kinds() []online.Kind { return []online.Kind{Kind} }

// roleTools are the tools each role may call.
var roleTools = map[string][]string{
	RoleInvPage: {ToolListArtifacts},
	RoleGet:     {ToolGetArtifact},
	RoleFiles:   {ToolListFiles},
	RoleDecl:    {ToolReadDeclaration},
	RoleChunk:   {ToolReadArtifact},
	RoleGrant:   {ToolRequestDir},
	RoleUpdate:  {ToolPublishFiles},
	RoleCreate:  {ToolPublishFiles},
	RoleUpdateV: {ToolPublishVersion},
	RoleCreateV: {ToolPublishVersion},
}

// Tools is the provider's fixed tool policy (5.2, A13).
func (p Provider) Tools() online.ToolPolicy {
	exact := []string{ToolListArtifacts, ToolGetArtifact, ToolReadDeclaration, ToolReadArtifact, ToolPublishFiles, ToolRequestDir}
	if p.Guard || p.Spec.PublishVia == compat.ArtifactsPublishVer {
		exact = append(exact, ToolPublishVersion)
	}
	grace := 60 * time.Second
	if d, err := time.ParseDuration(p.Spec.ResultGraceReads); err == nil && d > 0 {
		grace = d
	}
	batch := 2
	if p.Spec.BatchMax > 0 {
		batch = p.Spec.BatchMax
	}
	return online.ToolPolicy{
		Kinds:        []online.Kind{Kind},
		Exact:        exact,
		Reads:        []string{ToolGuide, ToolListFiles},
		InputMax:     max(p.Spec.MaxContentBytes, 1<<20) + 1<<20,
		ResultMax:    8 << 20,
		ValidateData: validateData,
		BatchMax:     batch,
		ResultGrace:  grace,
		Normalize:    NormalizePublishInput,
	}
}

// validateData checks an artifact step's data on every load: it decodes,
// and its role calls the step's tool.
func validateData(s online.Step) error {
	d, err := DecodeStepData(s.Data)
	if err != nil {
		return err
	}
	tools, ok := roleTools[d.Role]
	if !ok || !slices.Contains(tools, s.Tool) {
		return fmt.Errorf("role %q does not call %s", d.Role, s.Tool)
	}
	if d.Slug != "" && !ValidSlug(string(d.Slug)) {
		return errors.New("slug")
	}
	if d.Pair != "" && !ValidPairID(string(d.Pair)) {
		return errors.New("pair")
	}
	if d.BlobSHA != "" && !ValidSHA(d.BlobSHA) {
		return errors.New("blob sha")
	}
	return nil
}

// Build decodes the round plan in in.Extensions[ExtName] into steps.
// Without the extension it returns no steps.
func (p Provider) Build(in online.BuildInput) ([]online.Step, []online.Warning, error) {
	raw, ok := in.Extensions[ExtName]
	if !ok || len(raw) == 0 {
		return nil, nil, nil
	}
	var plan RoundPlan
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&plan); err != nil {
		return nil, nil, fmt.Errorf("artifacts: round plan: %w", err)
	}
	if len(plan.Steps) > 999 {
		return nil, nil, errors.New("artifacts: round too large")
	}
	steps := make([]online.Step, 0, len(plan.Steps))
	for i, ps := range plan.Steps {
		st := online.Step{ID: online.StepID(fmt.Sprintf("a%03d", i)), Kind: Kind, Tool: ps.Tool, Templated: ps.Templated,
			Status: online.StatusPending, Needs: append([]string(nil), ps.Needs...), After: []online.StepID{}, Data: ps.Data,
			Expect: online.Expect{Format: online.FormatJSONObject}}
		if ps.Dormant {
			st.Status = online.StatusDormant
		}
		if !ps.Templated {
			st.Input = ps.Input
		}
		if ps.InputSHA != "" {
			st.InputSHA256 = ps.InputSHA
		}
		for _, a := range ps.After {
			if a < 0 || a >= i {
				return nil, nil, errors.New("artifacts: round plan: after")
			}
			st.After = append(st.After, online.StepID(fmt.Sprintf("a%03d", a)))
		}
		if publishRoles[ps.Role] {
			st.Card = true
		} else {
			st.NoCard = true
		}
		if ps.Role == RoleChunk {
			st.Expect = online.Expect{Format: online.FormatText}
		}
		if err := validateData(st); err != nil {
			return nil, nil, fmt.Errorf("artifacts: round plan: %w", err)
		}
		steps = append(steps, st)
	}
	return steps, nil, nil
}

// Advance activates the next templated inventory page from the previous
// page's cursor, retires the remaining pages when a page has none (or
// failed), and retires a round's publishes when the outbox grant was
// refused (5.4). It is deterministic and idempotent.
func (p Provider) Advance(q *online.Queue, results map[online.StepID]online.Result, _ time.Time) error {
	var pages []*online.Step
	var grant *online.Step
	for i := range q.Steps {
		s := &q.Steps[i]
		if s.Kind != Kind {
			continue
		}
		d, err := DecodeStepData(s.Data)
		if err != nil {
			return err
		}
		switch d.Role {
		case RoleInvPage:
			pages = append(pages, s)
		case RoleGrant:
			grant = s
		}
	}
	for k := 0; k+1 < len(pages); k++ {
		cur, next := pages[k], pages[k+1]
		if next.Status != online.StatusDormant && !(next.Templated && (next.Status == online.StatusPending || next.Status == online.StatusOffered)) {
			continue
		}
		switch {
		case cur.Status == online.StatusDone:
			r, ok := results[cur.ID]
			var lp ListPage
			if !ok || r.Outcome != online.OutcomeOK || json.Unmarshal(r.Parsed, &lp) != nil || lp.NextCursor == nil {
				retirePages(pages[k+1:])
				continue
			}
			d, _ := DecodeStepData(next.Data)
			limit := d.Limit
			if limit <= 0 {
				limit = 100
			}
			next.Input = canonical(map[string]any{"cursor": *lp.NextCursor, "limit": limit, "paged": true})
			if next.Status == online.StatusDormant {
				next.Status = online.StatusPending
			}
		case cur.Status.Terminal():
			retirePages(pages[k+1:])
		}
	}
	if grant != nil && (grant.Status == online.StatusFailed || (grant.Status == online.StatusSkipped && grant.Reason != online.ReasonDecisionSkip)) {
		for i := range q.Steps {
			s := &q.Steps[i]
			if s.Kind != Kind || s.ID == grant.ID || s.Status.Terminal() {
				continue
			}
			if d, err := DecodeStepData(s.Data); err == nil && publishRoles[d.Role] {
				s.Status, s.Reason = online.StatusSkipped, "grant_declined"
			}
		}
	}
	return nil
}

// retirePages skips dormant pages nothing will activate.
func retirePages(pages []*online.Step) {
	for _, s := range pages {
		if s.Status == online.StatusDormant {
			s.Status, s.Reason = online.StatusSkipped, online.ReasonNotNeeded
		}
	}
}

// Interpret resolves a recognised result: the parser validated it.
func (Provider) Interpret(online.Step, online.Result) (online.StepStatus, string) {
	return online.StatusDone, ""
}

// ParseResult parses a tool response in PostToolUse (A4).
func (Provider) ParseResult(s online.Step, resp json.RawMessage) (online.Outcome, json.RawMessage) {
	d, err := DecodeStepData(s.Data)
	if err != nil {
		return online.OutcomeUnrecognized, nil
	}
	return ParseResult(d, resp)
}

// Questions asks once whether to publish (5.4) while a publish step
// waits for the answer.
func (Provider) Questions(q *online.Queue) []online.Question {
	if q.Decisions[QuestionID] != "" {
		return nil
	}
	updates, creates, conflicts, pending := 0, 0, 0, false
	for _, s := range q.Steps {
		if s.Kind != Kind || s.Status.Terminal() {
			continue
		}
		d, err := DecodeStepData(s.Data)
		if err != nil || !publishRoles[d.Role] {
			continue
		}
		pending = true
		switch d.Role {
		case RoleUpdate, RoleUpdateV:
			updates++
			if d.Conflict {
				conflicts++
			}
		case RoleCreate, RoleCreateV:
			creates++
		}
	}
	if !pending {
		return nil
	}
	return []online.Question{{
		ID: QuestionID, Header: i18n.T(i18n.ArtifactsQHeader),
		Text: i18n.T(i18n.ArtifactsQText, updates, creates, string(q.Profile), conflicts),
		Options: []online.Option{
			{ID: "apply", Label: i18n.T(i18n.ArtifactsQApply), Description: i18n.T(i18n.ArtifactsQApplyDesc)},
			{ID: "skip", Label: i18n.T(i18n.ArtifactsQSkip), Description: i18n.T(i18n.ArtifactsQSkipDesc)},
		},
	}}
}
