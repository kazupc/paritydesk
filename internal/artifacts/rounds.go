package artifacts

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/online"
)

// Tool names of the provider.
const (
	ToolListArtifacts   = "mcp__claude-artifacts__list_artifacts"
	ToolGetArtifact     = "mcp__claude-artifacts__get_artifact"
	ToolReadDeclaration = "mcp__claude-artifacts__read_declaration"
	ToolReadArtifact    = "mcp__claude-artifacts__read_artifact"
	ToolPublishVersion  = "mcp__claude-artifacts__publish_version"
	ToolListFiles       = "mcp__claude-artifacts__list_files"
	ToolGuide           = "mcp__claude-artifacts__guide"
	ToolRequestDir      = "mcp__ccd_directory__request_directory"
)

// QuestionID is the publish question.
const QuestionID = "artifacts_publish"

// Needs of every artifact step (5.3), and of the publish roles.
var (
	baseNeeds    = []string{online.NeedReplica, "read:" + string(online.ProbeStepID)}
	needDecision = "decision:" + QuestionID
)

// RoundPlan is a round's steps (the Extensions input of Build).
type RoundPlan struct {
	Chain string     `json:"chain"`
	Phase Phase      `json:"phase"`
	Steps []PlanStep `json:"steps"`
	// Notes are pairs the round could not plan a publish for (blob
	// missing, page too large): the service records them in the report.
	Notes []PairResult `json:"-"`
}

// PlanStep is one step of a round plan.
type PlanStep struct {
	Role      string          `json:"role"`
	Pair      PairID          `json:"pair,omitempty"`
	Slug      Slug            `json:"slug,omitempty"`
	Tool      string          `json:"tool"`
	Input     json.RawMessage `json:"input"`
	InputSHA  string          `json:"input_sha256,omitempty"`
	After     []int           `json:"after,omitempty"`
	Needs     []string        `json:"needs"`
	Data      json.RawMessage `json:"data"`
	Templated bool            `json:"templated,omitempty"`
	Dormant   bool            `json:"dormant,omitempty"`
}

// RoundInput is everything PlanRound reads.
type RoundInput struct {
	Spec      compat.ArtifactsSpec
	Config    config.Artifacts
	Policy    BoundaryPolicy
	Manifest  *Manifest
	Inventory *Inventory
	Partial   map[Slug]PartialState
	Chain     *Chain
	Profile   ids.Alias
	Replica   model.Replica
	Paths     ArchivePaths
	// AppVersion is the installed Claude's version (the gate's key).
	AppVersion string
	Now        time.Time
	// Stage copies a blob into the outbox and returns its absolute path
	// (5.8); nil computes the path only. Blob reads a blob's content
	// (transport version).
	Stage func(sha string) (string, error)
	Blob  func(sha string) (string, error)
}

// ErrReplica: PlanRound refuses a replica other than the chain's.
var ErrReplica = errors.New("artifacts: the replica is not the chain's")

// gateOpen reports the gate's state for the app version: exact,
// unverified (also after an app update) or mismatch.
func gateState(m *Manifest, appVersion string) string {
	g := m.Gate
	if g.AppVersion != appVersion {
		return GateUnverified
	}
	if g.Roundtrip == "" {
		return GateUnverified
	}
	return g.Roundtrip
}

// PlanRound returns the plan of the next non-empty phase after the
// chain's last round: inventory first, then inspect, read, and publish
// and verify rounds until nothing is left (done).
func PlanRound(in RoundInput) (RoundPlan, Phase, bool, error) {
	c := in.Chain
	if c == nil || in.Manifest == nil {
		return RoundPlan{}, "", false, errors.New("artifacts: no chain")
	}
	if in.Replica != c.Replica {
		return RoundPlan{}, "", false, ErrReplica
	}
	if c.Round >= in.Spec.MaxRounds && in.Spec.MaxRounds > 0 {
		return RoundPlan{Chain: c.ID}, PhaseDone, true, nil
	}
	if c.Round == 0 {
		return RoundPlan{Chain: c.ID, Phase: PhaseInventory, Steps: inventorySteps(in)}, PhaseInventory, false, nil
	}
	order := map[Phase]int{PhaseInventory: 0, PhaseInspect: 1, PhaseRead: 2, PhasePublish: 3, PhaseVerify: 3}
	at := order[c.Phase]
	if at <= 1 {
		if steps := inspectSteps(in); len(steps) > 0 {
			return RoundPlan{Chain: c.ID, Phase: PhaseInspect, Steps: steps}, PhaseInspect, false, nil
		}
	}
	if at <= 2 {
		if steps := readSteps(in, false); len(steps) > 0 {
			return RoundPlan{Chain: c.ID, Phase: PhaseRead, Steps: steps}, PhaseRead, false, nil
		}
	}
	// Publish and verify alternate: a publish that must be verified first
	// is verified before the next publish round.
	if steps := readSteps(in, true); len(steps) > 0 {
		return RoundPlan{Chain: c.ID, Phase: PhaseVerify, Steps: steps}, PhaseVerify, false, nil
	}
	if steps, notes := publishSteps(in); len(steps) > 0 || len(notes) > 0 {
		if len(steps) == 0 {
			return RoundPlan{Chain: c.ID, Phase: PhaseDone, Notes: notes}, PhaseDone, true, nil
		}
		return RoundPlan{Chain: c.ID, Phase: PhasePublish, Steps: steps, Notes: notes}, PhasePublish, false, nil
	}
	return RoundPlan{Chain: c.ID}, PhaseDone, true, nil
}

// stepData marshals a step's data.
func stepData(d StepData) json.RawMessage {
	b, err := json.Marshal(d)
	if err != nil {
		panic(fmt.Sprintf("artifacts: step data: %v", err))
	}
	return b
}

// canonical marshals v to its canonical JSON.
func canonical(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("artifacts: input: %v", err))
	}
	c, err := model.Canonical(b)
	if err != nil {
		panic(fmt.Sprintf("artifacts: input: %v", err))
	}
	return c
}

func needs(extra ...string) []string { return append(append([]string(nil), baseNeeds...), extra...) }

func listLimit(spec compat.ArtifactsSpec) int {
	if spec.ListPageLimit > 0 {
		return spec.ListPageLimit
	}
	return 100
}

// inventorySteps are page 0 and the dormant templated pages after it.
func inventorySteps(in RoundInput) []PlanStep {
	pages := max(in.Spec.MaxListPages, 1)
	limit := listLimit(in.Spec)
	k := KnobsFor(RoleInvPage, in.Spec)
	var out []PlanStep
	for p := 0; p < pages; p++ {
		s := PlanStep{Role: RoleInvPage, Tool: ToolListArtifacts, Needs: needs(),
			Data: stepData(StepData{Role: RoleInvPage, Page: p, Limit: limit, Parse: k})}
		if p == 0 {
			s.Input = canonical(map[string]any{"limit": limit, "paged": true})
		} else {
			s.Templated, s.Dormant = true, true
			s.After = []int{p - 1}
		}
		out = append(out, s)
	}
	return out
}

// sortedCandidates returns the chain's candidates in slug order.
func sortedCandidates(c *Chain) []Slug {
	out := make([]Slug, 0, len(c.Candidates))
	for s := range c.Candidates {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// inspectSteps are get + list_files (+ read_declaration for reads) of
// the candidates not inspected yet, at most inspect_per_round slugs.
func inspectSteps(in RoundInput) []PlanStep {
	var out []PlanStep
	n := 0
	for _, s := range sortedCandidates(in.Chain) {
		cand := in.Chain.Candidates[s]
		if cand.Inspected || cand.State != "" || cand.Verify {
			continue
		}
		if n >= max(in.Spec.InspectPerRound, 1) {
			break
		}
		n++
		in1 := canonical(map[string]any{"slug": string(s)})
		out = append(out,
			PlanStep{Role: RoleGet, Pair: cand.Pair, Slug: s, Tool: ToolGetArtifact, Input: in1, Needs: needs(),
				Data: stepData(StepData{Role: RoleGet, Pair: cand.Pair, Slug: s, Parse: KnobsFor(RoleGet, in.Spec)})},
			PlanStep{Role: RoleFiles, Pair: cand.Pair, Slug: s, Tool: ToolListFiles, Input: in1, Needs: needs(),
				Data: stepData(StepData{Role: RoleFiles, Pair: cand.Pair, Slug: s, Parse: KnobsFor(RoleFiles, in.Spec)})})
		if cand.Role == RoleRead {
			out = append(out, PlanStep{Role: RoleDecl, Pair: cand.Pair, Slug: s, Tool: ToolReadDeclaration, Input: in1,
				Needs: needs(), Data: stepData(StepData{Role: RoleDecl, Pair: cand.Pair, Slug: s, Parse: KnobsFor(RoleDecl, in.Spec)})})
		}
	}
	return out
}

// nextOffset is the next line to read of a slug (1 when nothing is
// recorded; 0 when the recorded parts end the document).
func nextOffset(ps PartialState) int {
	if len(ps.Chunks) == 0 {
		return 1
	}
	last := ps.Chunks[len(ps.Chunks)-1]
	return last.NextOffset
}

// estimate is the expected size of a slug's next part (5.7).
func estimate(in RoundInput, cand Candidate, ps PartialState) int64 {
	first := in.Spec.FirstChunkEstimate
	if first <= 0 {
		first = 96 << 10
	}
	if len(ps.Chunks) == 0 {
		if cand.Bytes > 0 && cand.Bytes < first {
			return cand.Bytes
		}
		return first
	}
	var bytes, lines int64
	for _, c := range ps.Chunks {
		bytes += int64(len(c.Body)) + 1
		lines += int64(c.Lines)
	}
	if lines == 0 {
		return first
	}
	last := ps.Chunks[len(ps.Chunks)-1]
	remaining := int64(last.TotalLines - last.NextOffset + 1)
	if in.Spec.ReadLimitLines > 0 && int64(in.Spec.ReadLimitLines) < remaining {
		remaining = int64(in.Spec.ReadLimitLines)
	}
	return bytes / lines * max(remaining, 1)
}

// readSteps are the next parts of the read candidates (verify false) or
// of the published copies to verify (verify true), within the round's
// byte budget and read_per_round.
func readSteps(in RoundInput, verify bool) []PlanStep {
	budget := in.Spec.RoundBytes
	if budget <= 0 {
		budget = 768 << 10
	}
	limit := max(in.Spec.ReadPerRound, 1)
	var out []PlanStep
	var used int64
	add := func(cand Candidate, s Slug) bool {
		ps := in.Partial[s]
		off := nextOffset(ps)
		if off == 0 {
			return true // assembled at the next ingest
		}
		est := estimate(in, cand, ps)
		if len(out) > 0 && used+est > budget {
			return false
		}
		if len(out) >= limit {
			return false
		}
		used += est
		input := map[string]any{"slug": string(s)}
		if in.Spec.ReadLimitLines > 0 {
			input["limit"] = in.Spec.ReadLimitLines
		}
		d := StepData{Role: RoleChunk, Pair: cand.Pair, Slug: s, Verify: verify, Parse: KnobsFor(RoleChunk, in.Spec)}
		if off > 1 {
			input["offset"] = off
			d.Offset = off
		}
		out = append(out, PlanStep{Role: RoleChunk, Pair: cand.Pair, Slug: s, Tool: ToolReadArtifact, Input: canonical(input),
			Needs: needs(), Data: stepData(d)})
		return true
	}
	if verify {
		for _, p := range in.Chain.Published {
			if !p.Verify || p.Verified {
				continue
			}
			if !add(Candidate{Pair: p.Pair, Alias: p.Alias}, p.Slug) {
				break
			}
		}
		return out
	}
	for _, s := range sortedCandidates(in.Chain) {
		cand := in.Chain.Candidates[s]
		if cand.Role != RoleRead || !cand.Inspected || cand.State != "" || cand.Verify {
			continue
		}
		if !add(cand, s) {
			break
		}
	}
	return out
}

// doctypeRE is the start a publish_version content must have.
var doctypeRE = regexp.MustCompile(`(?i)^<!DOCTYPE html>`)

// publishTarget is one planned publish.
type publishTarget struct {
	pair  PairID
	slug  Slug // "" for a create
	base  VersionID
	bytes int64
	cand  Candidate
}

// publishSteps plans the publish round (6.1 step 4).
func publishSteps(in RoundInput) ([]PlanStep, []PairResult) {
	c, m := in.Chain, in.Manifest
	via := in.Spec.PublishVia
	if via == "" || via == compat.ArtifactsPublishOff || c.NoPublish != "" || c.Decision == "skip" {
		return nil, nil
	}
	gate := gateState(m, in.AppVersion)
	if gate == GateMismatch {
		return nil, nil
	}
	var targets []publishTarget
	for _, s := range sortedCandidates(c) {
		cand := c.Candidates[s]
		if cand.Role != RoleTarget || !cand.Inspected || cand.State != "" || slices.Contains(c.Tried, cand.Pair) {
			continue
		}
		p := m.Pairs[cand.Pair]
		if p == nil || p.Content == nil || !in.Policy.Boundary(p.Content.SourceProfile, in.Profile) {
			continue // the data boundary is checked again here (8.7), last
		}
		base := cand.Base
		if base == "" {
			base = cand.GetVersion
		}
		targets = append(targets, publishTarget{pair: cand.Pair, slug: s, base: base, bytes: p.Content.Bytes, cand: cand})
	}
	flags := ChainFlags{Claimed: m.claimed()}
	for _, id := range sortedPairIDs(m.Pairs) {
		p := m.Pairs[id]
		if slices.Contains(c.Tried, id) || p.Content == nil {
			continue
		}
		if v := Judge(p, id, in.Profile, in.Replica, in.Inventory, flags, in.Policy, in.Config); v.Action == ActCreate {
			targets = append(targets, publishTarget{pair: id, bytes: p.Content.Bytes})
		}
	}
	if len(targets) == 0 {
		return nil, nil
	}
	if gate != GateExact {
		// Until a publish read back exactly on this app version: one
		// publish (the smallest page) and its verify first.
		sort.SliceStable(targets, func(i, j int) bool { return targets[i].bytes < targets[j].bytes })
		targets = targets[:1]
	}
	var steps []PlanStep
	var notes []PairResult
	grant := -1
	if via == compat.ArtifactsPublishFiles && !c.Granted {
		outbox := in.Paths.Outbox
		steps = append(steps, PlanStep{Role: RoleGrant, Tool: ToolRequestDir, Input: canonical(map[string]any{"path": outbox}),
			Needs: needs(needDecision), Data: stepData(StepData{Role: RoleGrant, Parse: KnobsFor(RoleGrant, in.Spec)})})
		grant = 0
	}
	perRound := max(in.Spec.PublishPerRound, 1)
	planned := 0
	for _, t := range targets {
		if planned >= perRound {
			break
		}
		p := m.Pairs[t.pair]
		st, note, ok := publishStep(in, t, p)
		if !ok {
			notes = append(notes, note)
			continue
		}
		if grant >= 0 {
			st.After = []int{grant}
		}
		steps = append(steps, st)
		planned++
	}
	if planned == 0 {
		return nil, notes
	}
	return steps, notes
}

// publishStep builds one update or create step.
func publishStep(in RoundInput, t publishTarget, p *Pair) (PlanStep, PairResult, bool) {
	sha := p.Content.SHA256
	via := in.Spec.PublishVia
	create := t.slug == ""
	role := RoleUpdate
	switch {
	case create && via == compat.ArtifactsPublishVer:
		role = RoleCreateV
	case create:
		role = RoleCreate
	case via == compat.ArtifactsPublishVer:
		role = RoleUpdateV
	}
	note := func(reason string) (PlanStep, PairResult, bool) {
		return PlanStep{}, PairResult{Pair: t.pair, Result: "skipped", Reason: reason}, false
	}
	input := map[string]any{}
	if create {
		if !ValidTitle(p.Title) || !NFCStable(p.Title) {
			return note(WhyTitleInvalid)
		}
		input["title"] = p.Title
		icon := p.Content.Icon
		if !ValidIcon(icon) {
			icon = in.Spec.DefaultIcon
		}
		input["icon"] = icon
	} else {
		input["slug"] = string(t.slug)
		input["base_version"] = string(t.base)
		if sendableTitle(p.Title) && p.TitleSHA != t.cand.TitleSHA {
			input["title"] = p.Title
		}
		switch t.cand.Read {
		case ReadUsers, ReadOrg:
			input["shared_live"] = t.cand.Read
		case ReadPublic:
			input["shared_live"] = ReadPublic
			input["artifact_title"] = t.cand.Title
		}
	}
	_, withTitle := input["title"]
	d := StepData{Role: role, Pair: t.pair, Slug: t.slug, BlobSHA: sha, Conflict: t.cand.Base != "", Title: withTitle,
		Parse: KnobsFor(role, in.Spec)}
	st := PlanStep{Role: role, Pair: t.pair, Slug: t.slug, Needs: needs(needDecision)}
	switch role {
	case RoleUpdate, RoleCreate:
		path := in.Paths.Outbox + "/" + sha + in.Spec.OutboxSuffix
		if in.Stage != nil {
			p, err := in.Stage(sha)
			if err != nil {
				return note("blob_missing")
			}
			path = p
		}
		input["files"] = []map[string]any{{"as": in.Spec.EntryPath, "path": path}}
		st.Tool = ToolPublishFiles
	default:
		if in.Blob == nil {
			return note("blob_missing")
		}
		content, err := in.Blob(sha)
		if err != nil {
			return note("blob_missing")
		}
		if int64(len(content)) > in.Spec.VersionMaxBytes || !doctypeRE.MatchString(content) {
			return note("too_large")
		}
		input["content"] = content
		st.Tool = ToolPublishVersion
	}
	st.Input = canonical(input)
	if st.Tool == ToolPublishVersion {
		st.InputSHA = online.InputSHA(st.Input)
	}
	st.Data = stepData(d)
	return st, PairResult{}, true
}

// versionTime is the change time a version id carries (L3), or zero.
func versionTime(v VersionID, spec compat.ArtifactsSpec) time.Time {
	if spec.VersionTimePrefix == "" {
		return time.Time{}
	}
	re, err := regexp.Compile(spec.VersionTimePrefix)
	if err != nil {
		return time.Time{}
	}
	m := re.FindStringSubmatch(string(v))
	if len(m) != 2 {
		return time.Time{}
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	return time.Unix(n, 0).UTC()
}

// outboxName is the file name of a staged blob.
func outboxName(sha string, spec compat.ArtifactsSpec) string { return sha + spec.OutboxSuffix }

// isOutboxName reports a staged file name.
func isOutboxName(name string, spec compat.ArtifactsSpec) bool {
	sha, ok := strings.CutSuffix(name, spec.OutboxSuffix)
	return ok && ValidSHA(sha)
}
