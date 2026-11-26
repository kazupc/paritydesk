package online

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/rows"
)

// PinsSource says where the entering side's pins came from.
type PinsSource string

// Pins sources.
const (
	PinsIDB    PinsSource = "idb"
	PinsMirror PinsSource = "mirror"
	PinsNone   PinsSource = "none" // row skipped
)

// BuildInput is everything a provider sees at P2 (Approximate) and P5.
type BuildInput struct {
	// Spec is the manifest [online] table of the entering side's app
	// version; Config is the config's [online] table.
	Spec     compat.OnlineSpec
	Config   config.Online
	From, To ids.Alias
	// Replica is the entering replica.
	Replica model.Replica
	// Outputs are the merge outputs (Desired) of the planned rows.
	Outputs map[model.RowID]*rows.Output
	// Current are the entering side's reads of the same plan; nil =
	// unknown.
	Current    map[model.RowID]*model.RowSnapshot
	PinsSource PinsSource
	// ReopenSession is the local id of the session P11 reopens; empty for
	// Terminal runs. Only used to estimate approval cards.
	ReopenSession ids.SessionID
	Limits        plan.Limits
	Approximate   bool
	// Phase is PhaseReconcile for every reconcile Build.
	Phase Phase
	// PinsRaw are the entering side's pin ids before exclusion; Hidden the
	// ids predicted hidden (spec-pins 8.1, 8.3); nil when unknown.
	PinsRaw []string
	Hidden  map[string]bool
	// PinsUnion: either side's pins base was invalid at P5, so this run
	// combines both lists (the pins question says so, spec-pins 8.7).
	PinsUnion bool
	// Extensions: provider name -> its CaptureResults output, or the
	// artifacts round plan.
	Extensions map[string]json.RawMessage
}

// ToolPolicy is fixed in code per provider: the step kinds it owns and the
// tools it may call. Validate refuses a step whose tool is not in its
// kind's policy.
type ToolPolicy struct {
	Kinds []Kind
	// Exact tools run only as exact queue steps (every mutation, and reads
	// that return other sessions' text); Reads are allowed while armed
	// without a step.
	Exact, Reads []string
	// InputMax and ResultMax bound steps/ input files and result files
	// (0: inline inputs only, results MaxResultBytes; at most MaxExtBytes).
	InputMax, ResultMax int64
	// ValidateData checks a step's data on every load.
	ValidateData func(Step) error
	// BatchMax: at most that many calls of the provider's kinds per `next`
	// answer (0: no extra limit). ResultGrace: grace for no_card steps (0:
	// online.result_grace). Normalize is applied to a call's input by the
	// guard and the recorder before the canonical comparison.
	BatchMax    int
	ResultGrace time.Duration
	Normalize   func(tool string, in json.RawMessage) (out json.RawMessage, boundSHAs []string, ok bool)
}

// owns reports whether the policy covers kind k.
func (p ToolPolicy) owns(k Kind) bool { return slices.Contains(p.Kinds, k) }

// allows reports whether tool is one of the policy's tools.
func (p ToolPolicy) allows(tool string) bool {
	return slices.Contains(p.Exact, tool) || slices.Contains(p.Reads, tool)
}

// Provider builds and advances one kind family. Providers are pure.
type Provider interface {
	// Name identifies the provider (Extensions key).
	Name() string
	Kinds() []Kind
	// Build returns this provider's steps for the entering replica.
	Build(in BuildInput) ([]Step, []Warning, error)
	// Advance runs inside `online next` before a batch is chosen. It
	// activates or skips its dormant steps, fills templated inputs from
	// step data and recorded results, and marks its steps done or skipped
	// from read results. It never adds or removes steps and never changes
	// an immutable field. It is deterministic and idempotent.
	Advance(q *Queue, results map[StepID]Result, now time.Time) error
	// Interpret turns a recognised response into the step's outcome.
	Interpret(s Step, r Result) (StepStatus, string)
	// Tools returns the tools this provider may call.
	Tools() ToolPolicy
	// Questions returns at most one question per provider for `next`.
	Questions(q *Queue) []Question
}

// Retryable is consulted by `online retry`: a step whose provider says
// false stays failed and the CLI prints RetryRefused().
type Retryable interface {
	Retryable(q *Queue, s Step) bool
	RetryRefused() i18n.Msg
}

// ResultParser replaces the built-in recognition in PostToolUse for the
// provider's kinds.
type ResultParser interface {
	ParseResult(s Step, resp json.RawMessage) (Outcome, json.RawMessage)
}

// Decider applies an answer to the provider's own question. Without it,
// the answer is stored in Queue.Decisions and "skip" skips every step that
// needs the decision.
type Decider interface {
	ApplyAnswer(q *Queue, question, option string) error
}

// Counter adds the provider's own counts (pins) to a draft's counts.
type Counter interface {
	CountSteps(steps []Step, c *Counts)
}

// Draft is the result of Build.
type Draft struct {
	Steps []Step `json:"steps"`
	// Inputs are the large inputs of steps with InputSHA256, by step id.
	Inputs   map[StepID]json.RawMessage `json:"inputs,omitempty"`
	Warnings []Warning                  `json:"warnings,omitempty"`
	Counts   Counts                     `json:"counts"`
}

// Empty reports a draft without steps.
func (d Draft) Empty() bool { return len(d.Steps) == 0 }

// Registry returns the built-in providers in order: probe, sidebar,
// pins. Extension providers (artifacts) are appended by the wiring.
func Registry() []Provider {
	return []Provider{ProbeProvider(), SidebarProvider(), PinsProvider()}
}

// Policies returns the tool policies of ps.
func Policies(ps []Provider) []ToolPolicy {
	out := make([]ToolPolicy, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Tools())
	}
	return out
}

// providerFor returns the provider whose policy owns the step's kind and
// declares its tool.
func providerFor(ps []Provider, s Step) Provider {
	for _, p := range ps {
		if t := p.Tools(); t.owns(s.Kind) && t.allows(s.Tool) {
			return p
		}
	}
	return nil
}

// policyFor returns the policy that owns the step, or a zero policy.
func policyFor(policies []ToolPolicy, s Step) ToolPolicy {
	for _, p := range policies {
		if p.owns(s.Kind) && p.allows(s.Tool) {
			return p
		}
	}
	return ToolPolicy{}
}

// Build runs every provider, assigns step ids (kind letter + index in
// provider order), dedupes identical (tool, input) pairs, validates the
// queue and returns the draft. A provider error drops that provider's
// steps and becomes a warning; Build never fails the run. When no
// provider returns a step, the draft is empty (no probe either).
func Build(in BuildInput, ps []Provider) Draft {
	if in.Phase == "" {
		in.Phase = PhaseReconcile
	}
	d := Draft{}
	var probe Provider
	var built []Step
	counters := map[byte]int{}
	for _, p := range ps {
		if p.Name() == probeName {
			probe = p
			continue
		}
		steps, warns, err := p.Build(in)
		d.Warnings = append(d.Warnings, warns...)
		if err != nil {
			d.Warnings = append(d.Warnings, Warning{Msg: string(i18n.OnlineWarnBuildFailed), Text: i18n.T(i18n.OnlineWarnBuildFailed)})
			continue
		}
		built = append(built, renumber(steps, counters)...)
	}
	if len(built) == 0 || probe == nil {
		return d
	}
	ps0, _, err := probe.Build(in)
	if err != nil || len(ps0) != 1 {
		d.Warnings = append(d.Warnings, Warning{Msg: string(i18n.OnlineWarnBuildFailed), Text: i18n.T(i18n.OnlineWarnBuildFailed)})
		return Draft{Warnings: d.Warnings}
	}
	steps := append(ps0, built...)
	for i := range steps[1:] {
		s := &steps[i+1]
		if !slices.Contains(s.Needs, needRead(ProbeStepID)) {
			s.Needs = append([]string{needRead(ProbeStepID)}, s.Needs...)
		}
	}
	steps = dedupe(steps)
	d.Inputs = map[StepID]json.RawMessage{}
	for i := range steps {
		normalizeStep(&steps[i])
		if steps[i].InputSHA256 != "" && len(steps[i].Input) > 0 {
			d.Inputs[steps[i].ID] = steps[i].Input
			steps[i].Input = nil
		}
	}
	if len(d.Inputs) == 0 {
		d.Inputs = nil
	}
	q := &Queue{V: 1, ID: "draft", Phase: in.Phase, Steps: steps}
	if err := q.Validate(Policies(ps), in.Spec.AlwaysDeny); err != nil {
		d.Warnings = append(d.Warnings, Warning{Msg: string(i18n.OnlineWarnBuildFailed), Text: i18n.T(i18n.OnlineWarnBuildFailed)})
		return Draft{Warnings: d.Warnings}
	}
	d.Steps = steps
	d.Counts = CountDraft(steps, ps)
	return d
}

// CountDraft counts a draft's steps: sidebar steps, extension steps
// (other), plus each Counter provider's own counts.
func CountDraft(steps []Step, ps []Provider) Counts {
	var c Counts
	for _, s := range steps {
		switch s.Kind {
		case KindSidebar:
			c.Sidebar++
		case KindRead, KindCapture, KindSectionCreate, KindPin:
		default:
			c.Other++
		}
	}
	for _, p := range ps {
		if ct, ok := p.(Counter); ok {
			ct.CountSteps(steps, &c)
		}
	}
	return c
}

// renumber gives a provider's steps their final ids, rewriting its own
// after and read: references; references to other providers' steps
// (read:r000) stay.
func renumber(steps []Step, counters map[byte]int) []Step {
	m := map[StepID]StepID{}
	out := make([]Step, len(steps))
	for i, s := range steps {
		l := letterOf(s.Kind)
		if l == 'r' && counters[l] == 0 {
			counters[l] = 1 // r000 is the probe
		}
		id := StepID(fmt.Sprintf("%c%03d", l, counters[l]))
		counters[l]++
		m[s.ID] = id
		s.ID = id
		out[i] = s
	}
	for i := range out {
		for j, a := range out[i].After {
			if n, ok := m[a]; ok {
				out[i].After[j] = n
			}
		}
		for j, n := range out[i].Needs {
			if id, ok := strings.CutPrefix(n, "read:"); ok {
				if nid, ok := m[StepID(id)]; ok {
					out[i].Needs[j] = needRead(nid)
				}
			}
		}
	}
	return out
}

// dedupe drops later steps with the same tool and canonical input as an
// earlier one, pointing references at the kept step.
func dedupe(steps []Step) []Step {
	seen := map[string]StepID{}
	alias := map[StepID]StepID{}
	var out []Step
	for _, s := range steps {
		if !s.Templated && (len(s.Input) > 0 || s.InputSHA256 != "") {
			key := s.Tool + "\x00" + stepInputSHA(s)
			if keep, ok := seen[key]; ok {
				alias[s.ID] = keep
				continue
			}
			seen[key] = s.ID
		}
		out = append(out, s)
	}
	for i := range out {
		for j, a := range out[i].After {
			if k, ok := alias[a]; ok {
				out[i].After[j] = k
			}
		}
		for j, n := range out[i].Needs {
			if id, ok := strings.CutPrefix(n, "read:"); ok {
				if k, ok := alias[StepID(id)]; ok {
					out[i].Needs[j] = needRead(k)
				}
			}
		}
	}
	return out
}

// stepInputSHA is the hash of a step's input (inline or by hash).
func stepInputSHA(s Step) string {
	if s.InputSHA256 != "" {
		return s.InputSHA256
	}
	return InputSHA(s.Input)
}

// normalizeStep canonicalizes the input and makes list fields non-nil.
func normalizeStep(s *Step) {
	if len(s.Input) > 0 {
		if c, err := model.Canonical(s.Input); err == nil {
			s.Input = c
		}
	}
	if s.Needs == nil {
		s.Needs = []string{}
	}
	if s.After == nil {
		s.After = []StepID{}
	}
	if s.Status == "" {
		s.Status = StatusPending
	}
}

// needRead is the need on a recorded read.
func needRead(id StepID) string { return "read:" + string(id) }

// Needs.
const (
	NeedSettled = "settled"
	NeedReplica = "replica"
)

// needDecision is the need on an answered question.
func needDecision(q string) string { return "decision:" + q }

// questionIDRE validates question and option ids.
func validID(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return true
}

// ErrInvalidQueue marks a queue that violates an invariant of 4.2.
var ErrInvalidQueue = errors.New("online: invalid queue")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalidQueue}, args...)...)
}

// Validate checks the invariants of 4.2 against the providers' tool
// policies (code, never the queue or the manifest) and the manifest's
// additional floor tools.
func (q *Queue) Validate(policies []ToolPolicy, alwaysDeny []string) error {
	if q == nil {
		return invalid("no queue")
	}
	if q.V != 1 {
		return invalid("version %d", q.V)
	}
	ids := map[StepID]*Step{}
	for i := range q.Steps {
		s := &q.Steps[i]
		if !ValidStepID(string(s.ID)) || s.ID[0] != letterOf(s.Kind) {
			return invalid("step id %q", s.ID)
		}
		if ids[s.ID] != nil {
			return invalid("duplicate step %s", s.ID)
		}
		ids[s.ID] = s
	}
	inputs := map[string]StepID{}
	for i := range q.Steps {
		s := &q.Steps[i]
		if !ValidTool(s.Tool) {
			return invalid("step %s: tool", s.ID)
		}
		if InFloor(s.Tool, alwaysDeny) || inUntrusted(s.Tool) || guardedToolName(s.Tool) {
			return invalid("step %s: tool is never allowed", s.ID)
		}
		owner := -1
		for pi, p := range policies {
			if p.owns(s.Kind) && p.allows(s.Tool) {
				owner = pi
				break
			}
		}
		if owner < 0 {
			return invalid("step %s: tool not declared by its provider", s.ID)
		}
		if f := policies[owner].ValidateData; f != nil {
			if err := f(*s); err != nil {
				return invalid("step %s: data: %v", s.ID, err)
			}
		}
		switch s.Status {
		case StatusDormant, StatusPending, StatusOffered, StatusDone, StatusSkipped, StatusFailed:
		default:
			return invalid("step %s: status %q", s.ID, s.Status)
		}
		if !Reasons[s.Reason] {
			return invalid("step %s: reason", s.ID)
		}
		if s.Retries < 0 || s.Retries > 1 || s.RetryBase < 0 || s.RetryBase > s.Retries || s.Offered < 0 || s.Attempts < 0 {
			return invalid("step %s: counters", s.ID)
		}
		hasInput, hasSHA := len(s.Input) > 0, s.InputSHA256 != ""
		if hasInput && hasSHA {
			return invalid("step %s: both input and input hash", s.ID)
		}
		if !s.Templated && !hasInput && !hasSHA {
			return invalid("step %s: no input", s.ID)
		}
		if s.Templated && hasSHA {
			return invalid("step %s: templated step with an input hash", s.ID)
		}
		if hasSHA && (len(s.InputSHA256) != 64 || strings.Trim(s.InputSHA256, "0123456789abcdef") != "") {
			return invalid("step %s: input hash", s.ID)
		}
		if hasInput {
			c, err := model.Canonical(s.Input)
			if err != nil || string(c) != string(s.Input) || len(c) == 0 || c[0] != '{' {
				return invalid("step %s: input is not a canonical object", s.ID)
			}
			key := s.Tool + "\x00" + InputSHA(s.Input)
			if other, ok := inputs[key]; ok {
				return invalid("steps %s and %s are the same call", other, s.ID)
			}
			inputs[key] = s.ID
		}
		if hasSHA {
			key := s.Tool + "\x00" + s.InputSHA256
			if other, ok := inputs[key]; ok {
				return invalid("steps %s and %s are the same call", other, s.ID)
			}
			inputs[key] = s.ID
		}
		for _, a := range s.After {
			if ids[a] == nil || a == s.ID {
				return invalid("step %s: after %q", s.ID, a)
			}
		}
		for _, n := range s.Needs {
			switch {
			case n == NeedSettled, n == NeedReplica:
			case strings.HasPrefix(n, "decision:"):
				if !validID(strings.TrimPrefix(n, "decision:")) {
					return invalid("step %s: need %q", s.ID, n)
				}
			case strings.HasPrefix(n, "read:"):
				r := ids[StepID(strings.TrimPrefix(n, "read:"))]
				if r == nil || (r.Kind != KindRead && r.Kind != KindCapture) || r.ID == s.ID {
					return invalid("step %s: need %q", s.ID, n)
				}
			default:
				return invalid("step %s: need %q", s.ID, n)
			}
		}
		if len(s.Data) > 0 && !json.Valid(s.Data) {
			return invalid("step %s: data", s.ID)
		}
	}
	for k, v := range q.Decisions {
		if !validID(k) || !validID(v) {
			return invalid("decision %q", k)
		}
	}
	return nil
}

// immutable is the immutable projection of a step (4.2).
type immutable struct {
	ID          StepID          `json:"id"`
	Kind        Kind            `json:"kind"`
	Tool        string          `json:"tool"`
	Input       json.RawMessage `json:"input,omitempty"`
	InputSHA256 string          `json:"input_sha256,omitempty"`
	Templated   bool            `json:"templated,omitempty"`
	Needs       []string        `json:"needs"`
	After       []StepID        `json:"after"`
	Card        bool            `json:"card"`
	NoCard      bool            `json:"no_card,omitempty"`
	Expect      Expect          `json:"expect"`
	Data        json.RawMessage `json:"data,omitempty"`
}

// StepsSHA is the hash of every step's immutable projection, in order:
// hex sha256 of the canonical JSON array.
func StepsSHA(steps []Step) string {
	proj := make([]immutable, len(steps))
	for i, s := range steps {
		p := immutable{ID: s.ID, Kind: s.Kind, Tool: s.Tool, InputSHA256: s.InputSHA256, Templated: s.Templated,
			Needs: s.Needs, After: s.After, Card: s.Card, NoCard: s.NoCard, Expect: s.Expect, Data: s.Data}
		if !s.Templated {
			p.Input = s.Input
		}
		if p.Needs == nil {
			p.Needs = []string{}
		}
		if p.After == nil {
			p.After = []StepID{}
		}
		proj[i] = p
	}
	b, err := json.Marshal(proj)
	if err != nil {
		return ""
	}
	c, err := model.Canonical(b)
	if err != nil {
		return ""
	}
	return hashHex(c)
}

// probeName is the probe provider's name.
const probeName = "probe"

// ProbeStepID is the probe step of every reconcile queue.
const ProbeStepID StepID = "r000"

// ToolGetSession is the probe's tool.
const ToolGetSession = "mcp__ccd_session_mgmt__get_session"

// probeProvider adds r000: get_session of the calling session. Its result
// proves that both hooks run there and binds an unbound lease.
type probeProvider struct{}

// ProbeProvider returns the probe provider (kind read, get_session as an
// exact step only: for another session it returns that session's title).
func ProbeProvider() Provider { return probeProvider{} }

func (probeProvider) Name() string  { return probeName }
func (probeProvider) Kinds() []Kind { return []Kind{KindRead, KindCapture} }

// ProbeStep returns r000.
func ProbeStep() Step {
	return Step{ID: ProbeStepID, Kind: KindRead, Tool: ToolGetSession, Input: json.RawMessage(`{"session_id":"self"}`),
		Status: StatusPending, Needs: []string{}, After: []StepID{}, Expect: Expect{Format: FormatJSONObject, Fields: map[string]string{}}}
}

func (probeProvider) Build(BuildInput) ([]Step, []Warning, error) {
	return []Step{ProbeStep()}, nil, nil
}

func (probeProvider) Advance(*Queue, map[StepID]Result, time.Time) error { return nil }

func (probeProvider) Interpret(s Step, r Result) (StepStatus, string) { return InterpretBuiltin(s, r) }

func (probeProvider) Tools() ToolPolicy {
	return ToolPolicy{Kinds: []Kind{KindRead, KindCapture}, Exact: []string{ToolGetSession}}
}

func (probeProvider) Questions(*Queue) []Question { return nil }

// ProbeSession returns the local session id the probe's result carries.
func ProbeSession(r Result) (string, bool) {
	var p IDParsed
	if r.Outcome != OutcomeOK || json.Unmarshal(r.Parsed, &p) != nil || !ValidLocalSession(p.ID) {
		return "", false
	}
	return p.ID, true
}
