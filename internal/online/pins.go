package online

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/rows"
)

// ToolSetPinned is the app's pin tool.
const ToolSetPinned = "mcp__ccd_sidebar__set_pinned"

// pinsName is the pins provider's name.
const pinsName = "pins"

// Questions and options of the pins provider.
const (
	QuestionPins         = "pins"
	QuestionPinsRemovals = "pins_removals"
	OptionApply          = "apply"
	OptionAddOnly        = "add_only"
	OptionSkip           = optionSkip
)

// Pin step operations (PinData.Op).
const (
	PinAdd    = "add"
	PinRemove = "remove"
	PinKeep   = "keep"
)

// maxPinData bounds the ids a pin step's data may list.
const maxPinData = 1000

// PinData is the data of a pin step. Union and Blocked repeat
// queue-level facts on every pin step, so the question and the counts
// can be derived from the queue alone.
type PinData struct {
	Op string `json:"op"`
	// Desc (removals) are the ids whose base entry is at risk if the
	// removal cascades (spec-pins 8.2), sorted.
	Desc []string `json:"desc,omitempty"`
	// Of (keeps) are the queued removals this keep belongs to, sorted.
	Of []string `json:"of,omitempty"`
	// Union: a pins base was invalid at P5, so both lists were combined.
	Union bool `json:"union,omitempty"`
	// Blocked counts removals not queued (blocked or hidden).
	Blocked int `json:"blocked,omitempty"`
}

// setPinnedInput is the input of a set_pinned call.
type setPinnedInput struct {
	Pinned    bool   `json:"pinned"`
	SessionID string `json:"session_id"`
}

// pinsProvider applies the pins row through set_pinned (spec-pins 8).
type pinsProvider struct{}

// PinsProvider returns the pins provider.
func PinsProvider() Provider { return pinsProvider{} }

func (pinsProvider) Name() string  { return pinsName }
func (pinsProvider) Kinds() []Kind { return []Kind{KindPin} }

func (pinsProvider) Tools() ToolPolicy {
	return ToolPolicy{Kinds: []Kind{KindPin}, Exact: []string{ToolSetPinned}, ValidateData: validatePinData}
}

// decodePin decodes a pin step's data and input.
func decodePin(s Step) (PinData, setPinnedInput, bool) {
	var d PinData
	var in setPinnedInput
	if s.Kind != KindPin || json.Unmarshal(s.Data, &d) != nil || json.Unmarshal(s.Input, &in) != nil {
		return d, in, false
	}
	return d, in, true
}

// validatePinData checks a pin step's data and input on every load.
func validatePinData(s Step) error {
	var d PinData
	dec := json.NewDecoder(bytes.NewReader(s.Data))
	dec.DisallowUnknownFields()
	if len(s.Data) == 0 || dec.Decode(&d) != nil {
		return fmt.Errorf("pin data is not readable")
	}
	var in setPinnedInput
	idec := json.NewDecoder(bytes.NewReader(s.Input))
	idec.DisallowUnknownFields()
	if idec.Decode(&in) != nil || !ValidLocalSession(in.SessionID) {
		return fmt.Errorf("pin input is not a local session")
	}
	switch d.Op {
	case PinRemove:
		if in.Pinned || len(d.Of) != 0 {
			return fmt.Errorf("pin removal data")
		}
	case PinAdd:
		if !in.Pinned || len(d.Desc) != 0 || len(d.Of) != 0 {
			return fmt.Errorf("pin add data")
		}
	case PinKeep:
		if !in.Pinned || len(d.Desc) != 0 || len(d.Of) == 0 {
			return fmt.Errorf("pin keep data")
		}
	default:
		return fmt.Errorf("pin op")
	}
	if len(d.Desc) > maxPinData || len(d.Of) > maxPinData || d.Blocked < 0 || d.Blocked > 1<<20 {
		return fmt.Errorf("pin data too large")
	}
	for _, id := range append(append([]string(nil), d.Desc...), d.Of...) {
		if !ValidLocalSession(id) {
			return fmt.Errorf("pin data id")
		}
	}
	return nil
}

// pinsPlan is what Build computes for the entering side.
type pinsPlan struct {
	removals []string            // queued removals, sorted
	adds     []string            // eligible additions, sorted
	keeps    map[string][]string // keep id -> removals it belongs to
	desc     map[string][]string // removal -> desc
	cards    map[string]bool     // id -> card
	blocked  int
	hidden   int
	capped   bool
	breaker  bool
}

// fieldTrue reports a record member at a dotted path that is JSON true.
func fieldTrue(v json.RawMessage, path string) bool {
	raw, err := jsonx.Get(v, jsonx.Path(strings.Split(path, ".")))
	return err == nil && strings.TrimSpace(string(raw)) == "true"
}

// fieldPresent reports a record member at a dotted path that is not null.
func fieldPresent(v json.RawMessage, path string) bool {
	raw, err := jsonx.Get(v, jsonx.Path(strings.Split(path, ".")))
	return err == nil && strings.TrimSpace(string(raw)) != "null"
}

// passes reports a record the app lets set_pinned change (P6).
func passes(it model.Item, sp compat.OnlinePinsSpec) bool {
	for _, f := range sp.SkipIfTrue {
		if fieldTrue(it.Value, f) {
			return false
		}
	}
	for _, f := range sp.SkipIfPresent {
		if fieldPresent(it.Value, f) {
			return false
		}
	}
	return true
}

func keySet(s *model.RowSnapshot) map[string]bool {
	out := map[string]bool{}
	if s != nil {
		for k := range s.Items {
			out[k] = true
		}
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		if v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// planPins computes the sets of spec-pins 8.2-8.4.
func planPins(in BuildInput) (*pinsPlan, []Warning) {
	sp := in.Spec.Pins
	if (in.PinsSource != PinsIDB && in.PinsSource != PinsMirror) || in.Outputs[model.RowPins] == nil {
		return nil, nil
	}
	out := in.Outputs[model.RowPins]
	D := keySet(out.Desired)
	C := keySet(in.Current[model.RowPins])
	Craw := map[string]bool{}
	if in.PinsRaw != nil {
		for _, id := range in.PinsRaw {
			Craw[id] = true
		}
	} else {
		for id := range C {
			Craw[id] = true
		}
	}
	var R *model.RowSnapshot
	if o := in.Outputs[model.RowSessions]; o != nil && o.Desired != nil {
		R = o.Desired
	} else {
		R = in.Current[model.RowSessions]
	}
	recs := rows.FamilyRecords([]*model.RowSnapshot{R, in.Current[model.RowSessions]}, sp.FamilyFields)
	fam := map[string]model.Item{}
	for _, r := range recs {
		fam[r.ID] = r.Item
	}
	eligible := func(x string) bool {
		if R == nil {
			return false
		}
		it, ok := R.Items[x]
		return ok && passes(it, sp)
	}
	keepable := func(y string) bool {
		it, ok := fam[y]
		return ok && passes(it, sp)
	}
	depth := sp.FamilyMaxDepth
	if depth <= 0 {
		depth = 64
	}
	anc := rows.Ancestors(recs, depth)
	// children[x] = {y != x : x in anc(y)}
	children := map[string][]string{}
	for _, r := range recs {
		for _, a := range anc(r.ID) {
			children[a] = append(children[a], r.ID)
		}
	}
	p := &pinsPlan{keeps: map[string][]string{}, desc: map[string][]string{}, cards: map[string]bool{}}
	remove0 := map[string]bool{}
	for id := range C {
		if !D[id] && eligible(id) {
			remove0[id] = true
		}
	}
	addSet := map[string]bool{}
	for id := range D {
		if !C[id] && eligible(id) {
			addSet[id] = true
		}
	}
	p.adds = sortedKeys(addSet)
	maxSteps := sp.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 200
	}
	var warns []Warning
	cappedDesc := 0
	for _, x := range sortedKeys(remove0) {
		var need, desc []string
		if sp.CascadeUnpin {
			for _, y := range children[x] {
				if Craw[y] && (D[y] || !C[y]) {
					need = append(need, y)
				}
				if (Craw[y] && D[y]) || addSet[y] {
					desc = append(desc, y)
				}
			}
		}
		blocked := false
		for _, y := range need {
			if !keepable(y) {
				blocked = true
			}
		}
		switch {
		case blocked:
			p.blocked++
			continue
		case in.Hidden != nil && in.Hidden[x]:
			p.hidden++
			continue
		case len(desc) > maxSteps:
			cappedDesc++
			continue
		}
		sort.Strings(desc)
		p.removals = append(p.removals, x)
		p.desc[x] = desc
		for _, y := range need {
			if !remove0[y] {
				p.keeps[y] = append(p.keeps[y], x)
			}
		}
	}
	threshold := max(in.Limits.MaxDeleteMin, int(math.Ceil(in.Limits.MaxDeleteFraction*float64(len(C)))))
	p.breaker = len(p.removals) > threshold
	for _, id := range append(append(slices.Clone(p.removals), p.adds...), sortedKeys(mapKeys(p.keeps))...) {
		p.cards[id] = in.ReopenSession == "" || id != string(in.ReopenSession)
	}
	p.cap(maxSteps)
	if cappedDesc > 0 {
		p.capped = true
	}
	if p.blocked > 0 {
		warns = append(warns, Warning{Msg: string(i18n.OnlineWarnPinsFamilyBlocked), Text: i18n.Tx(i18n.OnlineWarnPinsFamilyBlocked, p.blocked)})
	}
	if p.hidden > 0 {
		warns = append(warns, Warning{Msg: string(i18n.OnlineWarnPinsHidden), Text: i18n.Tx(i18n.OnlineWarnPinsHidden, p.hidden)})
	}
	if p.capped {
		warns = append(warns, Warning{Msg: string(i18n.OnlineWarnPinsCapped), Text: i18n.Tx(i18n.OnlineWarnPinsCapped, maxSteps)})
	}
	return p, warns
}

func mapKeys(m map[string][]string) map[string]bool {
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}

// cap applies max_steps: removals with all their keeps as atomic groups,
// in removal order, then adds; keeps list only queued removals.
func (p *pinsPlan) cap(maxSteps int) {
	ordered := p.removalOrder()
	taken := map[string]bool{}
	n := 0
	var removals []string
	for _, x := range ordered {
		group := 1
		for y, of := range p.keeps {
			if slices.Contains(of, x) && !taken[y] {
				group++
			}
		}
		if n+group > maxSteps {
			p.capped = true
			continue
		}
		n += group
		removals = append(removals, x)
		for y, of := range p.keeps {
			if slices.Contains(of, x) {
				taken[y] = true
			}
		}
	}
	queued := map[string]bool{}
	for _, x := range removals {
		queued[x] = true
	}
	for y, of := range p.keeps {
		var keep []string
		for _, x := range of {
			if queued[x] {
				keep = append(keep, x)
			}
		}
		sort.Strings(keep)
		if len(keep) == 0 {
			delete(p.keeps, y)
			continue
		}
		p.keeps[y] = keep
	}
	sort.Strings(removals)
	for x := range p.desc {
		if !queued[x] {
			delete(p.desc, x)
		}
	}
	p.removals = removals
	var adds []string
	for _, a := range p.adds {
		if n >= maxSteps {
			p.capped = true
			break
		}
		adds = append(adds, a)
		n++
	}
	p.adds = adds
}

// removalOrder is the step order of removals: card == false first.
func (p *pinsPlan) removalOrder() []string {
	out := slices.Clone(p.removals)
	sort.SliceStable(out, func(i, j int) bool { return !p.cards[out[i]] && p.cards[out[j]] })
	return out
}

// PinsBlocked returns how many removals planPins leaves pinned (blocked
// or hidden) for in; 0 when the provider plans nothing.
func PinsBlocked(in BuildInput) int {
	p, _ := planPins(in)
	if p == nil {
		return 0
	}
	return p.blocked + p.hidden
}

// Build builds the pin steps (spec-pins 8.5).
func (pinsProvider) Build(in BuildInput) ([]Step, []Warning, error) {
	p, warns := planPins(in)
	if p == nil {
		return nil, warns, nil
	}
	sp := in.Spec.Pins
	meta := PinData{Union: in.PinsUnion, Blocked: p.blocked + p.hidden}
	mk := func(id string, pinned bool, d PinData, status StepStatus, needs []string, ok []string, refusals []string) (Step, error) {
		d.Union, d.Blocked = meta.Union, meta.Blocked
		data, err := json.Marshal(d)
		if err != nil {
			return Step{}, err
		}
		input, err := model.Canonical(mustJSON(setPinnedInput{Pinned: pinned, SessionID: id}))
		if err != nil {
			return Step{}, err
		}
		return Step{Kind: KindPin, Tool: ToolSetPinned, Input: input, Status: status, Needs: needs, After: []StepID{},
			Card: p.cards[id], Data: data,
			Expect: Expect{Format: FormatText, OK: slices.Clone(ok), Refusals: slices.Clone(refusals), Unknown: slices.Clone(sp.UnknownOutcome)}}, nil
	}
	var steps []Step
	for _, x := range p.removalOrder() {
		needs := []string{needDecision(QuestionPins), needRead(ProbeStepID)}
		if p.breaker {
			needs = append(needs, needDecision(QuestionPinsRemovals))
		}
		s, err := mk(x, false, PinData{Op: PinRemove, Desc: p.desc[x]}, StatusPending, needs, sp.OKUnpinned, sp.RefusalsUnpin)
		if err != nil {
			return nil, warns, err
		}
		steps = append(steps, s)
	}
	addStatus := StatusPending
	if len(p.removals) > 0 {
		addStatus = StatusDormant
	}
	type later struct {
		id   string
		keep bool
	}
	var rest []later
	for _, a := range p.adds {
		rest = append(rest, later{id: a})
	}
	for y := range p.keeps {
		rest = append(rest, later{id: y, keep: true})
	}
	sort.SliceStable(rest, func(i, j int) bool {
		ci, cj := p.cards[rest[i].id], p.cards[rest[j].id]
		if ci != cj {
			return !ci
		}
		return rest[i].id < rest[j].id
	})
	for _, l := range rest {
		needs := []string{needDecision(QuestionPins), needRead(ProbeStepID)}
		var s Step
		var err error
		if l.keep {
			s, err = mk(l.id, true, PinData{Op: PinKeep, Of: p.keeps[l.id]}, StatusDormant, needs, sp.OKPinned, nil)
		} else {
			s, err = mk(l.id, true, PinData{Op: PinAdd}, addStatus, needs, sp.OKPinned, nil)
		}
		if err != nil {
			return nil, warns, err
		}
		steps = append(steps, s)
	}
	for i := range steps {
		steps[i].ID = StepID(fmt.Sprintf("p%03d", i))
	}
	return steps, warns, nil
}

// CountSteps adds the pin counts of a queue (Counter).
func (pinsProvider) CountSteps(steps []Step, c *Counts) {
	for _, s := range steps {
		d, _, ok := decodePin(s)
		if !ok {
			continue
		}
		switch d.Op {
		case PinAdd:
			c.PinsAdd++
		case PinRemove:
			c.PinsRemove++
		case PinKeep:
			c.PinsKeep++
		}
		c.PinsBlocked = max(c.PinsBlocked, d.Blocked)
	}
}

// pinFacts are the queue-level facts of the pin steps.
type pinFacts struct {
	any, union, breaker, allNoCard bool
	adds, removes, keeps, cards    int
	blocked                        int
	open                           bool // a pin step is pending or dormant
}

func factsOf(q *Queue) pinFacts {
	f := pinFacts{allNoCard: true}
	for _, s := range q.Steps {
		d, _, ok := decodePin(s)
		if !ok {
			continue
		}
		f.any = true
		f.union = f.union || d.Union
		f.blocked = max(f.blocked, d.Blocked)
		if s.Card {
			f.cards++
			f.allNoCard = false
		}
		if s.Status == StatusPending || s.Status == StatusDormant {
			f.open = true
		}
		switch d.Op {
		case PinAdd:
			f.adds++
		case PinRemove:
			f.removes++
			if slices.Contains(s.Needs, needDecision(QuestionPinsRemovals)) {
				f.breaker = true
			}
		case PinKeep:
			f.keeps++
		}
	}
	return f
}

// Questions offers the pins question while a pin step is open and the
// decision is unset (spec-pins 8.7).
func (pinsProvider) Questions(q *Queue) []Question {
	f := factsOf(q)
	if !f.open || q.Decisions[QuestionPins] != "" || questionSkipped(f) {
		return nil
	}
	text := i18n.Tx(i18n.OnlineQPinsText, string(q.From), f.adds, f.removes, f.cards)
	if f.union {
		text = i18n.Tx(i18n.WordSentences, text, i18n.Tx(i18n.OnlineQPinsUnion))
	}
	if f.keeps > 0 {
		text = i18n.Tx(i18n.WordSentences, text, i18n.Tx(i18n.OnlineQPinsKeep, f.keeps))
	}
	if f.blocked > 0 {
		text = i18n.Tx(i18n.WordSentences, text, i18n.Tx(i18n.OnlineQPinsBlocked, f.blocked))
	}
	apply := Option{ID: OptionApply, Label: i18n.Tx(i18n.OnlineQPinsApply), Description: i18n.Tx(i18n.OnlineQPinsApplyDesc)}
	skip := Option{ID: OptionSkip, Label: i18n.Tx(i18n.OnlineQPinsSkip), Description: i18n.Tx(i18n.OnlineQPinsSkipDesc)}
	opts := []Option{apply, skip}
	if f.breaker {
		opts = []Option{
			{ID: OptionAddOnly, Label: i18n.Tx(i18n.OnlineQPinsAddOnly), Description: i18n.Tx(i18n.OnlineQPinsAddOnlyDesc)},
			{ID: OptionApply, Label: i18n.Tx(i18n.OnlineQPinsApplyAll), Description: i18n.Tx(i18n.OnlineQPinsApplyDesc)},
			skip,
		}
	}
	return []Question{{ID: QuestionPins, Header: i18n.Tx(i18n.OnlineQPinsHeader), Text: text, Options: opts}}
}

// questionSkipped: no step shows a card, the breaker did not trip and the
// run is not in union mode, so the question is treated as "apply".
func questionSkipped(f pinFacts) bool { return f.allNoCard && !f.breaker && !f.union }

// ApplyAnswer records an answer to the pins question (Decider).
func (pinsProvider) ApplyAnswer(q *Queue, question, option string) error {
	if q.Decisions == nil {
		q.Decisions = map[string]string{}
	}
	f := factsOf(q)
	switch option {
	case OptionApply:
		q.Decisions[QuestionPins] = OptionApply
		if f.breaker {
			q.Decisions[QuestionPinsRemovals] = OptionApply
		}
	case OptionAddOnly:
		q.Decisions[QuestionPins] = OptionApply
		q.Decisions[QuestionPinsRemovals] = OptionSkip
	case OptionSkip:
		q.Decisions[QuestionPins] = OptionSkip
		for i := range q.Steps {
			s := &q.Steps[i]
			if s.Kind == KindPin && !s.Status.Terminal() {
				s.Status, s.Reason = StatusSkipped, ReasonDecisionSkip
			}
		}
	default:
		return fmt.Errorf("online: unknown pins answer %q", option)
	}
	_ = question
	return nil
}

// actedRemoval reports a removal whose call may have acted.
func actedRemoval(s *Step) bool {
	return s.Status == StatusDone ||
		(s.Status == StatusFailed && (s.Reason == ReasonNoResult || s.Reason == ReasonUnknownOutcome))
}

// Advance activates or retires the dormant pin steps (spec-pins 8.6).
func (pinsProvider) Advance(q *Queue, _ map[StepID]Result, _ time.Time) error {
	f := factsOf(q)
	if !f.any {
		return nil
	}
	if q.Decisions[QuestionPins] == "" && questionSkipped(f) {
		if q.Decisions == nil {
			q.Decisions = map[string]string{}
		}
		q.Decisions[QuestionPins] = OptionApply
	}
	removal := map[string]*Step{}
	for i := range q.Steps {
		s := &q.Steps[i]
		if d, in, ok := decodePin(*s); ok && d.Op == PinRemove {
			removal[in.SessionID] = s
		}
	}
	if q.Decisions[QuestionPins] == OptionSkip {
		for i := range q.Steps {
			s := &q.Steps[i]
			if s.Kind == KindPin && !s.Status.Terminal() {
				s.Status, s.Reason = StatusSkipped, ReasonDecisionSkip
			}
		}
		return nil
	}
	if q.Decisions[QuestionPinsRemovals] == OptionSkip {
		for _, s := range removal {
			if !s.Status.Terminal() {
				s.Status, s.Reason = StatusSkipped, ReasonDecisionSkip
			}
		}
	}
	for _, s := range removal {
		if !s.Status.Terminal() {
			return nil // dormant steps stay dormant until every removal settled
		}
	}
	for i := range q.Steps {
		s := &q.Steps[i]
		d, _, ok := decodePin(*s)
		if !ok || s.Status != StatusDormant {
			continue
		}
		switch d.Op {
		case PinAdd:
			s.Status = StatusPending
		case PinKeep:
			acted := false
			for _, x := range d.Of {
				if r := removal[x]; r != nil && actedRemoval(r) {
					acted = true
				}
			}
			if acted {
				s.Status = StatusPending
			} else {
				s.Status, s.Reason = StatusSkipped, ReasonNotNeeded
			}
		}
	}
	return nil
}

// textParsed is what PostToolUse records for a text answer.
type textParsed struct {
	Matched *int  `json:"matched"`
	Pinned  *bool `json:"pinned"`
}

// Interpret turns a recognised set_pinned answer into the step's status
// (spec-pins 8.8; tools_off, unknown, refusals, not_found and archived
// texts are decided when the result is recorded).
func (pinsProvider) Interpret(s Step, r Result) (StepStatus, string) {
	if r.Outcome != OutcomeOK {
		return StatusFailed, ReasonAppRefused
	}
	_, in, ok := decodePin(s)
	var p textParsed
	if !ok || json.Unmarshal(r.Parsed, &p) != nil {
		return StatusFailed, ReasonAppRefused
	}
	if p.Pinned != nil && *p.Pinned != in.Pinned {
		return StatusFailed, ReasonMismatch
	}
	if p.Matched == nil || *p.Matched < 0 || *p.Matched >= len(s.Expect.OK) {
		return StatusFailed, ReasonMismatch
	}
	return StatusDone, ""
}

// PinChanged reports whether a done pin step's recorded answer was the
// "changed" text (the first ok text of its direction; the others say
// "already"). False when unknown.
func PinChanged(r Result) bool {
	var p textParsed
	return r.Outcome == OutcomeOK && json.Unmarshal(r.Parsed, &p) == nil && p.Matched != nil && *p.Matched == 0
}

// Retryable: a removal is retried only while every add and keep is still
// dormant; afterwards a retried unpin could cascade over pins just
// restored. Adds and keeps are always retryable (spec-pins 8.9).
func (pinsProvider) Retryable(q *Queue, s Step) bool {
	d, _, ok := decodePin(s)
	if !ok || d.Op != PinRemove {
		return true
	}
	for _, o := range q.Steps {
		od, _, ok := decodePin(o)
		if ok && od.Op != PinRemove && o.Status != StatusDormant {
			return false
		}
	}
	return true
}

// RetryRefused is the line `online retry` prints for a removal it leaves.
func (pinsProvider) RetryRefused() i18n.Msg { return i18n.OnlineRetryPinsRemovalLate }

// Provides reports whether one of the service's providers builds steps of
// kind k. The orchestrator plans a row written through the app's tools
// only when it does (spec-pins 6.4 item 1).
func (s *Service) Provides(k Kind) bool {
	for _, p := range s.Providers {
		if slices.Contains(p.Kinds(), k) {
			return true
		}
	}
	return false
}

// QueueCounts are QueueCounts(q, s.Providers): the counts only providers
// know (pins_add, pins_remove, pins_keep, pins_blocked) included.
func (s *Service) QueueCounts(q *Queue) Counts { return QueueCounts(q, s.Providers) }
