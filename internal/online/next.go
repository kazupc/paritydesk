package online

import (
	"slices"
	"strings"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/state"
)

// Settings are the manifest and config values the queue logic uses.
type Settings struct {
	SettleAfterLaunch, ReplicaWait, ResultGrace, UnboundLeaseTTL, ReadMaxAge time.Duration
	LeaseTTL, QueueMaxAge, CaptureMaxAge                                     time.Duration
	BatchMax, SessionStartMaxShows                                           int
	PostMaxPayload                                                           int64
	Guard                                                                    GuardMode
	Reconcile                                                                string
	CreateSections                                                           bool
}

// Defaults of the manifest keys, used when a value is missing.
const (
	defSettle       = 45 * time.Second
	defReplicaWait  = 2 * time.Minute
	defResultGrace  = 10 * time.Minute
	defUnboundLease = 3 * time.Minute
	defReadMaxAge   = 2 * time.Minute
	defBatchMax     = 20
	defMaxShows     = 3
	defPostMax      = 8 << 20
	maxWait         = 60 * time.Second
)

func parseDur(s string, def time.Duration) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return def
	}
	return d
}

// NewSettings combines the manifest's [online] table and the config.
func NewSettings(spec compat.OnlineSpec, cfg config.Online) Settings {
	s := Settings{
		SettleAfterLaunch:    parseDur(spec.SettleAfterLaunch, defSettle),
		ReplicaWait:          parseDur(spec.ReplicaWait, defReplicaWait),
		ResultGrace:          parseDur(spec.ResultGrace, defResultGrace),
		UnboundLeaseTTL:      parseDur(spec.UnboundLeaseTTL, defUnboundLease),
		ReadMaxAge:           parseDur(spec.ReadMaxAge, defReadMaxAge),
		LeaseTTL:             cfg.LeaseTTL.Duration,
		QueueMaxAge:          cfg.QueueMaxAge.Duration,
		CaptureMaxAge:        cfg.CaptureMaxAge.Duration,
		BatchMax:             spec.BatchMax,
		SessionStartMaxShows: spec.SessionStartMaxShows,
		PostMaxPayload:       spec.PostMaxPayload,
		Guard:                GuardMode(cfg.Guard),
		Reconcile:            cfg.Reconcile,
		CreateSections:       cfg.CreateSections,
	}
	if s.BatchMax <= 0 {
		s.BatchMax = defBatchMax
	}
	if s.SessionStartMaxShows <= 0 {
		s.SessionStartMaxShows = defMaxShows
	}
	if s.PostMaxPayload <= 0 {
		s.PostMaxPayload = defPostMax
	}
	if s.LeaseTTL <= 0 {
		s.LeaseTTL = 20 * time.Minute
	}
	if s.QueueMaxAge <= 0 {
		s.QueueMaxAge = 336 * time.Hour
	}
	if s.CaptureMaxAge <= 0 {
		s.CaptureMaxAge = 10 * time.Minute
	}
	if s.ResultGrace > s.LeaseTTL {
		s.ResultGrace = s.LeaseTTL
	}
	if s.Guard != GuardAlways {
		s.Guard = GuardArmed
	}
	return s
}

// ReplicaObs is what main.log says about the app's replica since the
// queue's launch.
type ReplicaObs struct {
	Known   bool
	Replica model.Replica
}

// Caller identifies who calls `online next` and friends.
type Caller struct {
	Session string // validated UUID or ""
	Token   string // validated token or ""
}

// Validity reasons.
const (
	InvalidNoRecord = "no_record"
	InvalidMismatch = "mismatch"
	InvalidTampered = "tampered"
	InvalidExpired  = "expired"
)

// Valid checks a reconcile queue against root.Online (12.2 step 1): ""
// when valid, else the reason.
func Valid(q *Queue, rec *state.OnlineRecord, epoch uint64, active ids.Alias, now time.Time) string {
	switch {
	case rec == nil:
		return InvalidNoRecord
	case q.ID != rec.Queue || q.Profile != rec.Profile || q.Epoch != rec.Epoch || rec.Epoch != epoch ||
		rec.Profile != active || q.Replica != rec.Replica || q.Phase != PhaseReconcile:
		return InvalidMismatch
	case StepsSHA(q.Steps) != rec.StepsSHA:
		return InvalidTampered
	case !q.ExpiresAt.IsZero() && !now.Before(q.ExpiresAt):
		return InvalidExpired
	}
	return ""
}

// NextInput is everything Next needs; the caller reads it under the
// online lock.
type NextInput struct {
	Queue      *Queue
	Record     *state.OnlineRecord
	RootEpoch  uint64
	RootActive ids.Alias
	RunOpen    bool
	Results    map[StepID]Result
	Inflight   map[StepID][]Marker
	Lease      *Lease
	Stop       bool
	Caller     Caller
	// NewToken is used when the call gets a new lease token.
	NewToken    string
	Now         time.Time
	Mono        MonoNow
	Providers   []Provider
	Replica     ReplicaObs
	Settings    Settings
	Unavailable []string
}

// NextOutput is Next's answer and the file changes it asks for.
type NextOutput struct {
	Batch Batch
	// Queue is the queue to write (nil: unchanged).
	Queue *Queue
	// Lease is the lease to write (nil: none); RemoveLease removes it.
	Lease       *Lease
	RemoveLease bool
	RemoveQueue bool
	// Reset are steps whose result and markers are removed (reads offered
	// again).
	Reset []StepID
	// Wait is how long `--wait` may sleep before asking again.
	Wait time.Duration
}

func newBatch(state string) Batch {
	return Batch{State: state, Calls: []Call{}, Tools: []string{}, Lines: []string{}}
}

// lineBatch returns a batch with one fixed line.
func lineBatch(state, queue string, m i18n.Msg) Batch {
	b := newBatch(state)
	b.Queue = queue
	b.Lines = []string{i18n.T(m)}
	return b
}

// Next is `online next` (12.2). It is pure: every file change is in the
// output. A run that is open is NeedsRecover (exit 41).
func Next(in NextInput) (NextOutput, error) {
	if in.RunOpen {
		return NextOutput{}, pderr.New(pderr.NeedsRecover, "online next", "a run was interrupted; run paritydesk recover")
	}
	q := in.Queue
	if q == nil {
		return NextOutput{Batch: lineBatch(StateNone, "", i18n.OnlineStatusNone)}, nil
	}
	if in.Record != nil && in.Record.Kind != "" && in.Record.Queue == q.ID {
		return NextOutput{Batch: lineBatch(StateNone, q.ID, i18n.OnlineArtifactsQueue)}, nil
	}
	if why := Valid(q, in.Record, in.RootEpoch, in.RootActive, in.Now); why != "" {
		return NextOutput{Batch: lineBatch(StateStale, q.ID, i18n.OnlineStatusStale), RemoveQueue: true, RemoveLease: true}, nil
	}
	switch q.State {
	case QueueCancelled:
		return NextOutput{Batch: lineBatch(StateNone, q.ID, i18n.OnlineCancelled), RemoveLease: true}, nil
	case QueueComplete:
		b := newBatch(StateComplete)
		b.Queue, b.Counts = q.ID, QueueCounts(q, in.Providers)
		return NextOutput{Batch: b, RemoveLease: true}, nil
	}
	if in.Stop {
		// online cancel created the stop marker but could not finish:
		// finish it here.
		c := q.Clone()
		c.State = QueueCancelled
		return NextOutput{Batch: lineBatch(StateNone, q.ID, i18n.OnlineCancelled), Queue: c, RemoveLease: true}, nil
	}
	out := NextOutput{}
	q = q.Clone()
	s := in.Settings
	// A copy: Next is pure and its input may be reused.
	results := make(map[StepID]Result, len(in.Results))
	for k, v := range in.Results {
		results[k] = v
	}

	// Caller identity and the lease.
	lease := in.Lease
	fresh := lease.Fresh(in.Now, in.Mono, in.Stop) && lease.Queue == q.ID
	token := ""
	newLease := false
	switch {
	case fresh && lease.TokenOK(in.Caller.Token):
		lease = cloneLease(lease)
	case fresh && lease.Session != "" && in.Caller.Session == lease.Session:
		lease = cloneLease(lease)
		token = in.NewToken
		lease.TokenSHA256 = hashHex([]byte(token))
	case fresh:
		b := lineBatch(StateBusyElsewhere, q.ID, i18n.OnlineBusyElsewhere)
		return NextOutput{Batch: b}, nil
	default:
		newLease = true
		token = in.NewToken
		lease = &Lease{V: 1, Queue: q.ID, Phase: PhaseReconcile, TokenSHA256: hashHex([]byte(token)), Since: in.Now}
		if ids.IsUUID(in.Caller.Session) {
			lease.Session = in.Caller.Session
		}
		// A new lease proves the hooks again: every read is asked anew.
		for i := range q.Steps {
			st := &q.Steps[i]
			if st.Kind == KindRead && st.Status != StatusPending {
				st.Status, st.Offered, st.Attempts, st.Resets, st.Reason = StatusPending, 0, 0, 0, ""
				out.Reset = append(out.Reset, st.ID)
				delete(results, st.ID)
			}
		}
	}
	if q.FirstNextAt.IsZero() {
		q.FirstNextAt = in.Now
	}

	// Replica (12.2 step 2).
	if in.Replica.Known && in.Replica.Replica != q.Replica {
		b := lineBatch(StateStale, q.ID, i18n.OnlineStatusReplicaChanged)
		return NextOutput{Batch: b, RemoveQueue: true, RemoveLease: true}, nil
	}

	// Resolve offered steps (12.2 step 3).
	resetSet := map[StepID]bool{}
	for _, id := range out.Reset {
		resetSet[id] = true
	}
	if noHooks := resolve(q, in, results, resetSet); noHooks {
		r := q.Step(ProbeStepID)
		r.Status, r.Offered, r.Attempts, r.Reason = StatusPending, 0, 0, ""
		q.State = QueuePending
		b := lineBatch(StateNoHooks, q.ID, i18n.OnlineNoHooks)
		b.Counts = QueueCounts(q, in.Providers)
		return NextOutput{Batch: b, Queue: q, RemoveLease: true, Reset: append(out.Reset, ProbeStepID)}, nil
	}
	for i := range q.Steps {
		st := &q.Steps[i]
		if (st.Status == StatusPending || st.Status == StatusOffered) && slices.Contains(in.Unavailable, st.Tool) && results[st.ID].Step == "" {
			st.Status, st.Reason = StatusFailed, ReasonToolsUnavailable
		}
	}
	base := q.LaunchedAt
	if base.IsZero() {
		base = q.FirstNextAt
	}
	if !in.Replica.Known && !in.Now.Before(base.Add(s.ReplicaWait)) {
		for i := range q.Steps {
			st := &q.Steps[i]
			if (st.Status == StatusPending || st.Status == StatusDormant) && slices.Contains(st.Needs, NeedReplica) {
				st.Status, st.Reason = StatusSkipped, ReasonReplicaUnknown
			}
		}
	}
	agedReads(q, results, in.Now, s, &out)

	// Advance (12.2 step 4).
	for _, p := range in.Providers {
		if err := p.Advance(q, results, in.Now); err != nil {
			return NextOutput{}, pderr.Wrap(pderr.Internal, "online next", err)
		}
	}
	blockDependents(q)

	ctx := readyCtx{q: q, results: results, now: in.Now, base: base, settings: s, replica: in.Replica}
	b := newBatch(StateCalls)
	b.Queue = q.ID
	b.Token = token
	var offer []*Step
	// a. reads and captures.
	for i := range q.Steps {
		st := &q.Steps[i]
		if (st.Kind == KindRead || st.Kind == KindCapture) && st.Status == StatusPending && ctx.ready(st) {
			offer = append(offer, st)
		}
	}
	offer = capPerProvider(offer, in.Providers, s.BatchMax)
	// b. a question.
	if len(offer) == 0 {
		for _, p := range in.Providers {
			if qs := p.Questions(q); len(qs) > 0 {
				qq := qs[0]
				b.State, b.Question = StateQuestion, &qq
				break
			}
		}
	}
	// c. writes.
	if len(offer) == 0 && b.Question == nil {
		var cands []*Step
		for _, noCard := range []bool{true, false} {
			for i := range q.Steps {
				st := &q.Steps[i]
				if st.Kind == KindRead || st.Kind == KindCapture || st.Status != StatusPending || st.Card == noCard {
					continue
				}
				if ctx.ready(st) {
					cands = append(cands, st)
				}
			}
		}
		offer = capPerProvider(cands, in.Providers, s.BatchMax)
	}
	for _, st := range offer {
		st.Status = StatusOffered
		st.Offered++
		b.Calls = append(b.Calls, Call{ID: st.ID, Tool: st.Tool, Input: st.Input})
		if !slices.Contains(b.Tools, st.Tool) {
			b.Tools = append(b.Tools, st.Tool)
		}
	}
	switch {
	case len(offer) > 0 || b.Question != nil:
		q.State = QueueActive
	case anyLive(q):
		b.State = StateWait
		wait := ctx.nextChange(in.Inflight, in.Mono, in.Providers)
		b.RetryAfterMS = wait.Milliseconds()
		out.Wait = wait
	default:
		b.State = StateComplete
		q.State = QueueComplete
	}
	b.Counts = QueueCounts(q, in.Providers)
	b.RemainingBatches = remainingBatches(q, in.Providers, s.BatchMax)

	// The lease (12.2 step 7). A bound lease is refreshed to lease_ttl.
	// An unbound one keeps the deadlines it got when it was created
	// (unbound_lease_ttl, 4.4): it is never refreshed until the probe
	// binds it, so other sessions are blocked for at most that long.
	if b.State == StateComplete {
		out.RemoveLease = true
	} else {
		bound := lease.Session != ""
		if !bound {
			if r, ok := results[ProbeStepID]; ok && ids.IsUUID(r.Session) && q.Step(ProbeStepID).Status == StatusDone {
				lease.Session, bound = r.Session, true
			}
		}
		switch {
		case bound:
			lease.BootID = in.Mono.BootID
			lease.MonoDeadlineNS = in.Mono.NS + int64(s.LeaseTTL)
			lease.ExpiresAt = in.Now.Add(s.LeaseTTL)
		case newLease:
			lease.BootID = in.Mono.BootID
			lease.MonoDeadlineNS = in.Mono.NS + int64(s.UnboundLeaseTTL)
			lease.ExpiresAt = in.Now.Add(s.UnboundLeaseTTL)
		}
		out.Lease = lease
	}
	out.Batch = b
	out.Queue = q
	return out, nil
}

func cloneLease(l *Lease) *Lease {
	c := *l
	return &c
}

// markerOld reports whether an in-flight marker is older than grace by
// wall time or by the monotonic clock (another boot counts as old).
func markerOld(m Marker, now time.Time, mono MonoNow, grace time.Duration) bool {
	if now.Sub(m.At) >= grace {
		return true
	}
	if !mono.OK || m.BootID != mono.BootID {
		return true
	}
	return mono.NS-m.MonoNS >= int64(grace)
}

// graceOf is the result grace of a step: its provider's ResultGrace for
// no_card steps, else online.result_grace.
func graceOf(s *Step, ps []Provider, def time.Duration) time.Duration {
	if s.NoCard {
		if p := providerFor(ps, *s); p != nil && p.Tools().ResultGrace > 0 {
			return p.Tools().ResultGrace
		}
	}
	return def
}

// resolve applies recorded results and in-flight markers to offered steps
// (12.2 step 3); it reports the probe rules' no_hooks.
func resolve(q *Queue, in NextInput, results map[StepID]Result, reset map[StepID]bool) (noHooks bool) {
	for i := range q.Steps {
		s := &q.Steps[i]
		var markers []Marker
		if !reset[s.ID] {
			for _, m := range in.Inflight[s.ID] {
				// After `online retry` only the new attempt's markers count.
				if m.N > s.RetryBase {
					markers = append(markers, m)
				}
			}
			s.Attempts = len(in.Inflight[s.ID])
		}
		r, has := results[s.ID]
		resolvable := s.Status == StatusOffered || (s.Status == StatusFailed && s.Reason == ReasonNoResult)
		if has && resolvable {
			p := providerFor(in.Providers, *s)
			switch {
			case r.Verdict != nil && Reasons[r.Verdict.Reason] && (r.Verdict.Status == StatusFailed || r.Verdict.Status == StatusSkipped):
				s.Status, s.Reason = r.Verdict.Status, r.Verdict.Reason
			case r.Outcome == OutcomeOK && p != nil:
				s.Status, s.Reason = p.Interpret(*s, r)
			case r.Outcome == OutcomeOK:
				s.Status, s.Reason = InterpretBuiltin(*s, r)
			case r.Outcome == OutcomeError:
				s.Status, s.Reason = StatusFailed, ReasonAppRefused
			default: // unrecognized
				if _, ok := p.(ResultParser); ok && p != nil {
					s.Status, s.Reason = StatusFailed, ReasonUnrecognized
				} else {
					has = false
				}
			}
			if s.Status == StatusFailed && s.Reason == ReasonUnknownOutcome {
				s.EverUnknown = true
			}
			if has {
				continue
			}
		}
		if s.Status != StatusOffered {
			continue
		}
		grace := graceOf(s, in.Providers, in.Settings.ResultGrace)
		if len(markers) > 0 {
			latest := markers[0]
			for _, m := range markers[1:] {
				if m.N > latest.N {
					latest = m
				}
			}
			if !markerOld(latest, in.Now, in.Mono, grace) {
				continue // a card may still be open
			}
			if s.ID == ProbeStepID {
				return true // PreToolUse runs but PostToolUse does not
			}
			s.Status, s.Reason, s.EverUnknown = StatusFailed, ReasonNoResult, true
			continue
		}
		if s.Offered >= 2 {
			if s.ID == ProbeStepID {
				return true
			}
			s.Status, s.Reason = StatusFailed, ReasonNotCalled
			continue
		}
		s.Status = StatusPending
	}
	return false
}

// agedReads re-offers a read (other than the probe) whose result is older
// than online.read_max_age while steps still wait for it: once, then it
// fails no_result and its dependents are skipped by their provider.
func agedReads(q *Queue, results map[StepID]Result, now time.Time, s Settings, out *NextOutput) {
	for i := range q.Steps {
		rd := &q.Steps[i]
		if rd.Kind != KindRead || rd.ID == ProbeStepID || rd.Status != StatusDone {
			continue
		}
		r, ok := results[rd.ID]
		if !ok || now.Sub(r.At) <= s.ReadMaxAge || !waitedOn(q, rd.ID) {
			continue
		}
		if rd.Resets >= 1 {
			rd.Status, rd.Reason, rd.EverUnknown = StatusFailed, ReasonNoResult, true
			continue
		}
		rd.Resets++
		rd.Status, rd.Offered, rd.Attempts = StatusPending, 0, 0
		delete(results, rd.ID)
		out.Reset = append(out.Reset, rd.ID)
	}
}

// waitedOn reports whether a not yet offered step needs read id.
func waitedOn(q *Queue, id StepID) bool {
	for _, s := range q.Steps {
		if (s.Status == StatusPending || s.Status == StatusDormant) && slices.Contains(s.Needs, needRead(id)) {
			return true
		}
	}
	return false
}

// afterOK reports whether an after-step is satisfied: done, a retired
// create step, or a recorded read.
func afterOK(a *Step) bool {
	switch {
	case a == nil:
		return false
	case a.Status == StatusDone:
		return true
	case a.Status == StatusSkipped && a.Kind == KindSectionCreate && a.Reason == "":
		return true
	}
	return false
}

// blockDependents skips pending steps whose after- or read-step ended
// without success (they can never run), and retires dormant steps that
// nothing can activate any more.
func blockDependents(q *Queue) {
	for changed := true; changed; {
		changed = false
		for i := range q.Steps {
			s := &q.Steps[i]
			if s.Status != StatusPending && s.Status != StatusDormant {
				continue
			}
			for _, a := range s.After {
				as := q.Step(a)
				if as != nil && as.Status.Terminal() && !afterOK(as) {
					s.Status, s.Reason, changed = StatusSkipped, ReasonNotFound, true
					break
				}
			}
			if s.Status != StatusPending && s.Status != StatusDormant {
				continue
			}
			for _, n := range s.Needs {
				if id, ok := strings.CutPrefix(n, "read:"); ok {
					if r := q.Step(StepID(id)); r != nil && r.Status.Terminal() && r.Status != StatusDone {
						s.Status, s.Reason, changed = StatusSkipped, ReasonNotFound, true
						break
					}
				}
			}
		}
	}
}

// readyCtx evaluates preconditions.
type readyCtx struct {
	q        *Queue
	results  map[StepID]Result
	now      time.Time
	base     time.Time
	settings Settings
	replica  ReplicaObs
}

func (c readyCtx) settled() bool { return !c.now.Before(c.base.Add(c.settings.SettleAfterLaunch)) }

// ready reports whether every need and after of a pending step holds.
func (c readyCtx) ready(s *Step) bool {
	for _, n := range s.Needs {
		switch {
		case n == NeedSettled:
			if !c.settled() {
				return false
			}
		case n == NeedReplica:
			if !c.replica.Known || c.replica.Replica != c.q.Replica {
				return false
			}
		case strings.HasPrefix(n, "decision:"):
			if v := c.q.Decisions[strings.TrimPrefix(n, "decision:")]; v == "" || v == optionSkip {
				return false
			}
		case strings.HasPrefix(n, "read:"):
			id := StepID(strings.TrimPrefix(n, "read:"))
			r := c.q.Step(id)
			if _, ok := c.results[id]; r == nil || r.Status != StatusDone || !ok {
				return false
			}
		}
	}
	for _, a := range s.After {
		if !afterOK(c.q.Step(a)) {
			return false
		}
	}
	return !s.Templated || len(s.Input) > 0
}

// nextChange is how long until a held step can change: the settle time,
// the replica deadline, or the grace of an offered step's marker (at most
// 60 s, at least one second).
func (c readyCtx) nextChange(inflight map[StepID][]Marker, mono MonoNow, ps []Provider) time.Duration {
	best := maxWait
	consider := func(t time.Time) {
		if d := t.Sub(c.now); d > 0 && d < best {
			best = d
		}
	}
	if !c.settled() {
		consider(c.base.Add(c.settings.SettleAfterLaunch))
	}
	if !c.replica.Known {
		consider(c.base.Add(c.settings.ReplicaWait))
	}
	for _, s := range c.q.Steps {
		if s.Status != StatusOffered {
			continue
		}
		for _, m := range inflight[s.ID] {
			consider(m.At.Add(graceOf(&s, ps, c.settings.ResultGrace)))
		}
	}
	if best < time.Second {
		best = time.Second
	}
	return best
}

// anyLive reports a step that is still pending, dormant or offered.
func anyLive(q *Queue) bool {
	for _, s := range q.Steps {
		if s.Status == StatusPending || s.Status == StatusOffered {
			return true
		}
		if s.Status == StatusDormant && !retirable(q, s) {
			return true
		}
	}
	return false
}

// retirable reports a dormant step nothing will activate: every step
// that could (a pending sidebar step waiting for it) is terminal.
func retirable(q *Queue, d Step) bool {
	for _, s := range q.Steps {
		if slices.Contains(s.After, d.ID) && !s.Status.Terminal() {
			return false
		}
	}
	return true
}

// capPerProvider keeps at most batchMax calls in all and at most each
// provider's BatchMax of its kinds.
func capPerProvider(steps []*Step, ps []Provider, batchMax int) []*Step {
	var out []*Step
	per := map[string]int{}
	for _, s := range steps {
		if len(out) >= batchMax {
			break
		}
		if p := providerFor(ps, *s); p != nil {
			if lim := p.Tools().BatchMax; lim > 0 && per[p.Name()] >= lim {
				continue
			}
			per[p.Name()]++
		}
		out = append(out, s)
	}
	return out
}

// QueueCounts are the queue's counts: the build counts plus the outcomes
// of every step other than reads.
func QueueCounts(q *Queue, ps []Provider) Counts {
	c := CountDraft(q.Steps, ps)
	for _, s := range q.Steps {
		if s.Kind == KindRead || s.Kind == KindCapture {
			continue
		}
		switch s.Status {
		case StatusDone:
			c.Done++
		case StatusSkipped:
			if s.Reason != "" {
				c.Skipped++
			}
		case StatusFailed:
			c.Failed++
		}
	}
	return c
}

// remainingBatches is an upper bound of the further `next` answers
// needed: unresolved reads, one per open question, the writes in batches,
// one per provider holding dormant steps, plus one.
func remainingBatches(q *Queue, ps []Provider, batchMax int) int {
	reads, writes := 0, 0
	questions := map[string]bool{}
	dormant := map[string]bool{}
	for _, s := range q.Steps {
		if s.Status.Terminal() {
			continue
		}
		if s.Kind == KindRead || s.Kind == KindCapture {
			reads++
		} else {
			writes++
		}
		for _, n := range s.Needs {
			if d, ok := strings.CutPrefix(n, "decision:"); ok && q.Decisions[d] == "" {
				questions[d] = true
			}
		}
		if s.Status == StatusDormant {
			if p := providerFor(ps, s); p != nil {
				dormant[p.Name()] = true
			}
		}
	}
	if batchMax <= 0 {
		batchMax = defBatchMax
	}
	return reads + len(questions) + (writes+batchMax-1)/batchMax + len(dormant) + 1
}

// ApplyDecision records an answer to a question that `next` would ask
// now. An unknown question or option is a usage error (exit 2).
func ApplyDecision(q *Queue, ps []Provider, question, option string) error {
	for _, p := range ps {
		for _, qq := range p.Questions(q) {
			if qq.ID != question {
				continue
			}
			for _, o := range qq.Options {
				if o.ID != option {
					continue
				}
				if d, ok := p.(Decider); ok {
					return d.ApplyAnswer(q, question, option)
				}
				if q.Decisions == nil {
					q.Decisions = map[string]string{}
				}
				q.Decisions[question] = option
				if option == optionSkip {
					for i := range q.Steps {
						s := &q.Steps[i]
						if !s.Status.Terminal() && slices.Contains(s.Needs, needDecision(question)) {
							s.Status, s.Reason = StatusSkipped, ReasonDecisionSkip
						}
					}
				}
				return nil
			}
			return pderr.New(pderr.Usage, "online decide", "unknown answer for this question")
		}
	}
	return pderr.New(pderr.Usage, "online decide", "no such question is open")
}

// retryReasons are the failures `online retry` offers once more.
var retryReasons = []string{ReasonNoResult, ReasonNotCalled, ReasonAppRefused, ReasonMismatch, ReasonUnknownOutcome}

// Retry sets failed steps back to pending with one more attempt. inflight
// are the steps' in-flight markers: the retried step remembers how many
// it had (RetryBase), so only a marker of the new attempt counts as an
// open call, and the guard lets exactly one more call through. It returns
// how many steps it reset, the messages of providers that refused theirs,
// and the retried steps (whose consumed result the caller removes, so the
// new attempt's result can be recorded: results are first-wins).
func Retry(q *Queue, ps []Provider, inflight map[StepID][]Marker) (int, []i18n.Msg, []StepID) {
	n := 0
	var refused []i18n.Msg
	var retried []StepID
	for i := range q.Steps {
		s := &q.Steps[i]
		if s.Status != StatusFailed || !slices.Contains(retryReasons, s.Reason) || s.Retries > 0 {
			continue
		}
		base := 0
		for _, m := range inflight[s.ID] {
			base = max(base, m.N)
		}
		if base > 1 {
			continue // no in-flight marker number left for another attempt
		}
		if p := providerFor(ps, *s); p != nil {
			if r, ok := p.(Retryable); ok && !r.Retryable(q, *s) {
				if m := r.RetryRefused(); !slices.Contains(refused, m) {
					refused = append(refused, m)
				}
				continue
			}
		}
		s.Status, s.Reason, s.Offered, s.Retries, s.RetryBase = StatusPending, "", 0, 1, base
		retried = append(retried, s.ID)
		n++
	}
	if n > 0 && q.State == QueueComplete {
		q.State = QueueActive
	}
	return n, refused, retried
}
