package online

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/kazupc/paritydesk/internal/clock"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/state"
)

// Service is the online layer over one Store: what the orchestrator
// calls (Build, Publish, Invalidate, Cleanup, ReadHead, Resolved,
// Capture, BreakLease, SetLaunched, Finish) and what the `online`
// commands call (Status, Cancel, StartCapture, FinishCapture, Next,
// Decide, Retry).
type Service struct {
	Store     *Store
	Config    config.Online
	Clock     clock.Clock
	Mono      MonoClock
	Providers []Provider
	// Settings are the manifest's [online] values with the config; the
	// orchestrator's Build passes its own manifest table instead.
	Settings Settings
	// Rand is the token source (crypto/rand when nil).
	Rand io.Reader
}

// QueueHeader is what Publish needs besides the draft.
type QueueHeader struct {
	ID         string
	Kind       string
	Profile    ids.Alias
	From       ids.Alias
	Replica    model.Replica
	Epoch      uint64
	LaunchedAt time.Time
	// Decisions are preset answers (only question ids the queue's steps
	// need are kept).
	Decisions map[string]string
	Spec      compat.OnlineSpec
}

// RootInfo is what the queue commands know of root.json.
type RootInfo struct {
	Record  *state.OnlineRecord
	Epoch   uint64
	Active  ids.Alias
	RunOpen bool
}

// RootInfoOf extracts RootInfo from a root (nil-safe).
func RootInfoOf(r *state.Root) RootInfo {
	if r == nil {
		return RootInfo{}
	}
	return RootInfo{Record: r.Online, Epoch: r.Epoch, Active: r.Active, RunOpen: r.Run != nil}
}

// ErrBusyElsewhere: a fresh lease belongs to another caller.
var ErrBusyElsewhere = errors.New("online: another session holds the lease")

func (s *Service) now() time.Time {
	if s.Clock == nil {
		return time.Now()
	}
	return s.Clock.Now()
}

func (s *Service) mono() MonoNow { return Read(s.Mono) }

func (s *Service) token() (string, error) {
	b := make([]byte, 16)
	r := s.Rand
	if r == nil {
		r = rand.Reader
	}
	if _, err := io.ReadFull(r, b); err != nil {
		return "", fmt.Errorf("online: token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// resultMax returns the result size limit per step of q.
func (s *Service) resultMax(q *Queue) func(StepID) int64 {
	pols := Policies(s.Providers)
	return func(id StepID) int64 {
		st := q.Step(id)
		if st == nil {
			return MaxResultBytes
		}
		if p := policyFor(pols, *st); p.ResultMax > 0 {
			return min(p.ResultMax, MaxExtBytes)
		}
		return MaxResultBytes
	}
}

// loadQueue reads and validates a queue file.
func (s *Service) loadQueue(p Phase) (*Queue, error) {
	q, err := s.Store.ReadQueue(p)
	if err != nil {
		return nil, pderr.Wrap(pderr.Internal, "online", fmt.Errorf("%s: %w", i18n.T(i18n.OnlineErrQueueInvalid), err))
	}
	if q == nil {
		return nil, nil
	}
	if err := q.Validate(Policies(s.Providers), q.Guard.AlwaysDeny); err != nil {
		return nil, pderr.New(pderr.Internal, "online", i18n.T(i18n.OnlineErrQueueInvalid))
	}
	return q, nil
}

// Build runs the providers (pure).
func (s *Service) Build(in BuildInput) Draft { return Build(in, s.Providers) }

// filterDecisions keeps preset answers for questions the steps need.
func filterDecisions(steps []Step, d map[string]string) map[string]string {
	var out map[string]string
	for k, v := range d {
		need := needDecision(k)
		for _, st := range steps {
			if slices.Contains(st.Needs, need) && validID(k) && validID(v) {
				if out == nil {
					out = map[string]string{}
				}
				out[k] = v
				break
			}
		}
	}
	return out
}

// Publish writes queue.json (and steps/ inputs) for a draft and returns
// the steps hash for root.Online.StepsSHA. Publishing the same id again
// keeps the existing file (recover republishes). It supersedes any older
// queue: the lease, armed, stop and the old queue's files go.
func (s *Service) Publish(ctx context.Context, d Draft, h QueueHeader) (string, error) {
	if d.Empty() {
		return "", fmt.Errorf("online: publish: empty draft")
	}
	if !ValidQueueID(h.ID) {
		return "", ErrBadName
	}
	var sha string
	err := s.Store.Locked(ctx, func() error {
		now, mono := s.now(), s.mono()
		st := NewSettings(h.Spec, s.Config)
		steps := make([]Step, len(d.Steps))
		copy(steps, d.Steps)
		sha = StepsSHA(steps)
		if old, err := s.Store.ReadQueue(PhaseReconcile); err == nil && old != nil && old.ID == h.ID && StepsSHA(old.Steps) == sha {
			return nil
		}
		q := &Queue{V: 1, ID: h.ID, Phase: PhaseReconcile, State: QueuePending, Kind: h.Kind, Profile: h.Profile, From: h.From,
			Replica: h.Replica, Epoch: h.Epoch, CreatedAt: now, LaunchedAt: h.LaunchedAt, ExpiresAt: now.Add(st.QueueMaxAge),
			BootID: mono.BootID, MonoNS: mono.NS, Decisions: filterDecisions(steps, h.Decisions),
			SettleMS: st.SettleAfterLaunch.Milliseconds(),
			Guard: GuardExtra{AlwaysDeny: h.Spec.AlwaysDeny, ServersExtra: h.Spec.GuardedServersExtra,
				ToolsExtra: h.Spec.GuardedToolsExtra, PostMaxPayload: st.PostMaxPayload, ToolsOff: h.Spec.Responses.ToolsOff},
			Steps: steps}
		if err := q.Validate(Policies(s.Providers), h.Spec.AlwaysDeny); err != nil {
			return err
		}
		for id, in := range d.Inputs {
			if err := s.Store.WriteStepInput(h.ID, id, in); err != nil {
				return err
			}
		}
		if err := s.Store.RemoveLease(); err != nil {
			return err
		}
		if err := s.Store.Cleanup(CleanupInput{Queue: h.ID, Now: now, CaptureMaxAge: st.CaptureMaxAge, QueueMaxAge: st.QueueMaxAge,
			Capture: s.captureID()}); err != nil {
			return err
		}
		if err := s.Store.RemoveStop(); err != nil {
			return err
		}
		return s.Store.WriteQueue(q)
	})
	return sha, err
}

// captureID is the id of the current capture.json ("" when none).
func (s *Service) captureID() string {
	if q, err := s.Store.ReadQueue(PhaseCapture); err == nil && q != nil && ValidCaptureID(q.ID) {
		return q.ID
	}
	return ""
}

// Invalidate drops the queue, the lease and armed (rollback, rebase,
// profile rename or remove, an empty draft).
func (s *Service) Invalidate(ctx context.Context, reason string) error {
	_ = reason
	return s.Store.Locked(ctx, func() error {
		if err := s.Store.RemoveLease(); err != nil {
			return err
		}
		if err := s.Store.RemoveQueue(PhaseReconcile); err != nil {
			return err
		}
		return s.Store.Cleanup(CleanupInput{Now: s.now(), Capture: s.captureID(),
			CaptureMaxAge: s.settings().CaptureMaxAge, QueueMaxAge: s.settings().QueueMaxAge})
	})
}

func (s *Service) settings() Settings {
	if s.Settings.LeaseTTL == 0 {
		return NewSettings(compat.OnlineSpec{}, s.Config)
	}
	return s.Settings
}

// Cleanup removes a queue that no longer matches root.Online, a stale
// lease, and the files of other queues and old captures.
func (s *Service) Cleanup(ctx context.Context, root *state.Root) error {
	return s.Store.Locked(ctx, func() error {
		now, mono := s.now(), s.mono()
		keep := ""
		q, err := s.Store.ReadQueue(PhaseReconcile)
		info := RootInfoOf(root)
		switch {
		case err != nil || q == nil:
			if rmErr := s.Store.RemoveQueue(PhaseReconcile); rmErr != nil {
				return rmErr
			}
		case info.Record != nil && info.Record.Kind != "" && info.Record.Queue == q.ID:
			keep = q.ID
		case Valid(q, info.Record, info.Epoch, info.Active, now) != "":
			if rmErr := s.Store.RemoveQueue(PhaseReconcile); rmErr != nil {
				return rmErr
			}
		case q.State == QueueCancelled:
		default:
			keep = q.ID
		}
		l, _ := s.Store.ReadLease()
		if l != nil && (keep == "" || l.Queue != keep || !l.Fresh(now, mono, s.Store.Stopped())) {
			if err := s.Store.RemoveLease(); err != nil {
				return err
			}
		}
		capID := ""
		if c, err := s.Store.ReadQueue(PhaseCapture); err == nil && c != nil {
			if fresh(c, now, mono, s.settings().CaptureMaxAge) {
				capID = c.ID
			} else if err := s.Store.RemoveQueue(PhaseCapture); err != nil {
				return err
			}
		}
		return s.Store.Cleanup(CleanupInput{Queue: keep, Capture: capID, Now: now,
			CaptureMaxAge: s.settings().CaptureMaxAge, QueueMaxAge: s.settings().QueueMaxAge})
	})
}

// ReadHead reads the head of queue.json.
func (s *Service) ReadHead() (*QueueHead, error) { return s.Store.ReadHead() }

// Resolved returns the queue's steps with statuses resolved from the
// recorded results (12.2 step 3, without offering anything); nil, nil
// when there is no such queue.
func (s *Service) Resolved(_ context.Context, queueID string) ([]Step, error) {
	q, err := s.loadQueue(PhaseReconcile)
	if err != nil || q == nil || q.ID != queueID {
		return nil, err
	}
	results := s.Store.Results(q.ID, s.resultMax(q))
	in := NextInput{Queue: q, Inflight: s.Store.Inflight(q.ID), Now: s.now(), Mono: s.mono(), Providers: s.Providers,
		Settings: s.settings()}
	resolve(q, in, results, nil)
	return q.Steps, nil
}

// Capture returns the usable capture of the leaving side (7.3), or nil.
func (s *Service) Capture(_ context.Context, alias ids.Alias, rep model.Replica, epoch uint64) (*CaptureSnapshot, error) {
	q, err := s.Store.ReadQueue(PhaseCapture)
	if err != nil || q == nil {
		return nil, nil
	}
	if !CaptureUsable(q, alias, rep, epoch, s.now(), s.mono(), s.settings().CaptureMaxAge) {
		return nil, nil
	}
	results := s.Store.CaptureResults(q.ID, q.Session, s.resultMax(q))
	return Snapshot(q, results, s.Providers), nil
}

// BreakLease stops every call of the old queue while a run works (P0):
// armed and the lease go under the online lock; when the lock stays busy
// the stop marker is created instead.
func (s *Service) BreakLease(ctx context.Context) error {
	if err := s.Store.RemoveArmed(); err != nil {
		return err
	}
	err := s.Store.Locked(ctx, s.Store.RemoveLease)
	if pderr.CodeOf(err) == pderr.RunLockBusy {
		return s.Store.CreateStop()
	}
	return err
}

// SetLaunched records the launch time of the published queue (P11).
func (s *Service) SetLaunched(ctx context.Context, queueID string, t time.Time) error {
	return s.Store.Locked(ctx, func() error {
		q, err := s.Store.ReadQueue(PhaseReconcile)
		if err != nil || q == nil || q.ID != queueID {
			return err
		}
		q.LaunchedAt = t
		return s.Store.WriteQueue(q)
	})
}

// authorize checks the caller against a fresh lease: nil when the caller
// holds it (token) or no fresh lease exists, ErrBusyElsewhere otherwise.
func (s *Service) authorize(q *Queue, c Caller) error {
	l, _ := s.Store.ReadLease()
	if !l.Fresh(s.now(), s.mono(), s.Store.Stopped()) || l.Queue != q.ID {
		return nil
	}
	if l.TokenOK(c.Token) || (l.Session != "" && l.Session == c.Session) {
		return nil
	}
	return ErrBusyElsewhere
}

// Finish is the queue part of `online done`: under the online lock it
// checks the caller, marks the queue complete (steps still live keep
// their state and count as not applied), removes the lease and returns
// the resolved steps. (nil, nil) when there is no such queue.
func (s *Service) Finish(ctx context.Context, queueID string, c Caller) (*Queue, error) {
	var out *Queue
	err := s.Store.Locked(ctx, func() error {
		q, err := s.loadQueue(PhaseReconcile)
		if err != nil || q == nil || q.ID != queueID {
			return err
		}
		if err := s.authorize(q, c); err != nil {
			return err
		}
		results := s.Store.Results(q.ID, s.resultMax(q))
		in := NextInput{Queue: q, Inflight: s.Store.Inflight(q.ID), Now: s.now(), Mono: s.mono(), Providers: s.Providers,
			Settings: s.settings()}
		resolve(q, in, results, nil)
		if q.State != QueueCancelled {
			q.State = QueueComplete
		}
		if err := s.Store.WriteQueue(q); err != nil {
			return err
		}
		out = q
		return s.Store.RemoveLease()
	})
	return out, err
}

// ---------------------------------------------------------------- status

// StepView is one step in `online status`: no inputs, no session ids.
type StepView struct {
	ID     StepID     `json:"id"`
	Kind   Kind       `json:"kind"`
	Status StepStatus `json:"status"`
	Reason string     `json:"reason"`
}

// DenialsView is the denial part of `online status`.
type DenialsView struct {
	Count  int      `json:"count"`
	Recent []Denial `json:"recent"`
}

// ConfirmView is the confirmation part of `online done`.
type ConfirmView struct {
	BasesAdvanced    int  `json:"bases_advanced"`
	ContestedCleared int  `json:"contested_cleared"`
	ContestedLeft    int  `json:"contested_left"`
	RootBusy         bool `json:"root_busy"`
}

// StatusView is the document of `online status`, `cancel` and `done`.
type StatusView struct {
	State             string       `json:"state"`
	Queue             string       `json:"queue,omitempty"`
	Profile           ids.Alias    `json:"profile,omitempty"`
	From              ids.Alias    `json:"from,omitempty"`
	CreatedAt         *time.Time   `json:"created_at,omitempty"`
	WaitingForSession bool         `json:"waiting_for_session"`
	Counts            Counts       `json:"counts"`
	Steps             []StepView   `json:"steps"`
	Denials           *DenialsView `json:"denials,omitempty"`
	Confirmed         *ConfirmView `json:"confirmed,omitempty"`
	Lines             []string     `json:"lines"`
}

// stateRank orders queue states so the newer of the root record and the
// queue head wins.
func stateRank(s string) int {
	switch s {
	case string(QueuePending):
		return 1
	case string(QueueActive):
		return 2
	case string(QueueComplete), string(QueueCancelled):
		return 3
	}
	return 0
}

// Status is `online status` (no lock, reads only).
func (s *Service) Status(root RootInfo) StatusView {
	v := StatusView{State: StateNone, Steps: []StepView{}, Lines: []string{}}
	q, qerr := s.Store.ReadQueue(PhaseReconcile)
	rec := root.Record
	switch {
	case rec == nil && q == nil:
		v.Lines = append(v.Lines, i18n.T(i18n.OnlineStatusNone))
	case rec == nil || q == nil || qerr != nil || (rec.Kind == "" && Valid(q, rec, root.Epoch, root.Active, s.now()) != ""):
		v.State = StateStale
		if rec != nil {
			v.Queue, v.Profile, v.From, v.Counts = rec.Queue, rec.Profile, rec.From, rec.Counts
		}
		if rec != nil && rec.State == string(QueueComplete) && q == nil {
			v.State = rec.State
			v.Lines = append(v.Lines, statusLine(v))
		} else {
			v.Lines = append(v.Lines, i18n.T(i18n.OnlineStatusStale))
		}
	default:
		v.State, v.Queue, v.Profile, v.From = rec.State, q.ID, q.Profile, q.From
		if stateRank(string(q.State)) > stateRank(rec.State) {
			v.State = string(q.State)
		}
		created := q.CreatedAt
		v.CreatedAt = &created
		results := s.Store.Results(q.ID, s.resultMax(q))
		in := NextInput{Queue: q, Inflight: s.Store.Inflight(q.ID), Now: s.now(), Mono: s.mono(), Providers: s.Providers,
			Settings: s.settings()}
		rq := q.Clone()
		resolve(rq, in, results, nil)
		v.Counts = QueueCounts(rq, s.Providers)
		offered := false
		for _, st := range rq.Steps {
			v.Steps = append(v.Steps, StepView{ID: st.ID, Kind: st.Kind, Status: st.Status, Reason: st.Reason})
			offered = offered || st.Offered > 0
		}
		v.WaitingForSession = v.State == string(QueuePending) && !offered
		if rec.Kind != "" {
			v.Lines = append(v.Lines, i18n.T(i18n.OnlineArtifactsQueue))
		}
		v.Lines = append(v.Lines, statusLine(v))
		if v.Counts.Other > 0 && (v.State == string(QueuePending) || v.State == string(QueueActive)) {
			v.Lines = append(v.Lines, i18n.T(i18n.OnlineStatusOther, v.Counts.Other))
		}
	}
	if ds := s.Store.Denials(); len(ds) > 0 {
		recent := ds
		if len(recent) > 5 {
			recent = recent[len(recent)-5:]
		}
		v.Denials = &DenialsView{Count: len(ds), Recent: recent}
		v.Lines = append(v.Lines, i18n.T(i18n.OnlineStatusDenials, len(ds)))
	}
	return v
}

// statusLine is the fixed summary line of a queue state.
func statusLine(v StatusView) string {
	switch v.State {
	case string(QueuePending), string(QueueActive):
		return i18n.T(i18n.OnlineStatusPending, v.Counts.PinsAdd, v.Counts.PinsRemove, v.Counts.Sidebar, string(v.Profile))
	}
	return i18n.T(i18n.OnlineStatusState, string(v.Profile), v.State, v.Counts.Done, v.Counts.Skipped, v.Counts.Failed)
}

// Cancel is `online cancel`: the stop marker and armed go first without
// any lock (every lease is stale for hooks at once); then, under the
// online lock, the lease goes and the queue is marked cancelled.
func (s *Service) Cancel(ctx context.Context) (StatusView, error) {
	v := StatusView{State: string(QueueCancelled), Steps: []StepView{}, Lines: []string{i18n.T(i18n.OnlineCancelled)}}
	if err := s.Store.CreateStop(); err != nil {
		return v, pderr.Wrap(pderr.Internal, "online cancel", err)
	}
	if err := s.Store.RemoveArmed(); err != nil {
		return v, pderr.Wrap(pderr.Internal, "online cancel", err)
	}
	err := s.Store.Locked(ctx, func() error {
		if err := s.Store.RemoveLease(); err != nil {
			return err
		}
		q, err := s.Store.ReadQueue(PhaseReconcile)
		if err != nil {
			return s.Store.RemoveQueue(PhaseReconcile)
		}
		if q == nil {
			return nil
		}
		v.Queue, v.Profile, v.From = q.ID, q.Profile, q.From
		q.State = QueueCancelled
		return s.Store.WriteQueue(q)
	})
	if err != nil {
		return v, err
	}
	return v, nil
}

// ---------------------------------------------------------------- capture

// CaptureStart is `online capture` without --finish: it writes
// capture.json and lists its calls. It takes no lease and arms nothing.
func (s *Service) CaptureStart(ctx context.Context, in CaptureInput, epoch uint64) (Batch, error) {
	b := newBatch(StateCalls)
	err := s.Store.Locked(ctx, func() error {
		now, mono := s.now(), s.mono()
		suffix, err := s.Store.randHex(4)
		if err != nil {
			return err
		}
		id := "cap-" + now.UTC().Format("20060102T150405Z") + "-" + suffix
		q, err := BuildCapture(in, s.Providers, id, now, mono, epoch, s.settings().CaptureMaxAge)
		if err != nil {
			return err
		}
		if err := s.Store.WriteQueue(q); err != nil {
			return err
		}
		b.Queue = ""
		for _, st := range q.Steps {
			b.Calls = append(b.Calls, Call{ID: st.ID, Tool: st.Tool, Input: st.Input})
			if !slices.Contains(b.Tools, st.Tool) {
				b.Tools = append(b.Tools, st.Tool)
			}
		}
		b.Queue = q.ID
		return nil
	})
	return b, err
}

// CaptureFinish closes capture.json (--finish).
func (s *Service) CaptureFinish(ctx context.Context) (Batch, error) {
	b := newBatch(StateComplete)
	err := s.Store.Locked(ctx, func() error {
		q, err := s.Store.ReadQueue(PhaseCapture)
		if err != nil || q == nil {
			return err
		}
		q.State = QueueComplete
		return s.Store.WriteQueue(q)
	})
	return b, err
}

// ---------------------------------------------------------------- next

// NextCall is one `online next`.
type NextCall struct {
	Caller      Caller
	Root        RootInfo
	Unavailable []string
	Wait        bool
	// Replica reads main.log for the newest replica since t (the queue's
	// launch; zero: the tail window).
	Replica func(ctx context.Context, since time.Time) ReplicaObs
}

// Next is `online next` with the store: it reads under the online lock,
// runs Next and writes what it asks for. With Wait, a wait answer sleeps
// (at most 60 s, cancellable) and asks once more.
func (s *Service) Next(ctx context.Context, c NextCall) (Batch, error) {
	b, wait, err := s.nextOnce(ctx, c)
	if err != nil || !c.Wait || b.State != StateWait {
		return b, err
	}
	clk := s.Clock
	if clk == nil {
		clk = clock.System{}
	}
	if err := clk.Sleep(ctx, min(wait, maxWait)); err != nil {
		return b, nil
	}
	b2, _, err := s.nextOnce(ctx, c)
	if err != nil {
		return b, err
	}
	if b2.Token == "" {
		b2.Token = b.Token
	}
	return b2, nil
}

func (s *Service) nextOnce(ctx context.Context, c NextCall) (Batch, time.Duration, error) {
	var b Batch
	var wait time.Duration
	err := s.Store.Locked(ctx, func() error {
		q, err := s.loadQueue(PhaseReconcile)
		if err != nil {
			return err
		}
		tok, err := s.token()
		if err != nil {
			return err
		}
		lease, _ := s.Store.ReadLease()
		in := NextInput{Queue: q, Record: c.Root.Record, RootEpoch: c.Root.Epoch, RootActive: c.Root.Active, RunOpen: c.Root.RunOpen,
			Lease: lease, Stop: s.Store.Stopped(), Caller: c.Caller, NewToken: tok, Now: s.now(), Mono: s.mono(),
			Providers: s.Providers, Settings: s.settings(), Unavailable: c.Unavailable}
		if q != nil {
			in.Results = s.Store.Results(q.ID, s.resultMax(q))
			in.Inflight = s.Store.Inflight(q.ID)
			if c.Replica != nil {
				in.Replica = c.Replica(ctx, q.LaunchedAt)
			}
		}
		out, err := Next(in)
		if err != nil {
			return err
		}
		b, wait = out.Batch, out.Wait
		return s.apply(q, out)
	})
	return b, wait, err
}

// apply writes Next's file changes.
func (s *Service) apply(q *Queue, out NextOutput) error {
	if q != nil {
		for _, id := range out.Reset {
			if err := s.Store.ResetStepFiles(q.ID, id); err != nil {
				return err
			}
		}
	}
	if out.RemoveLease {
		if err := s.Store.RemoveLease(); err != nil {
			return err
		}
	}
	if out.RemoveQueue {
		if err := s.Store.RemoveQueue(PhaseReconcile); err != nil {
			return err
		}
		return s.Store.Cleanup(CleanupInput{Now: s.now(), Capture: s.captureID(),
			CaptureMaxAge: s.settings().CaptureMaxAge, QueueMaxAge: s.settings().QueueMaxAge})
	}
	if out.Queue != nil {
		if err := s.Store.WriteQueue(out.Queue); err != nil {
			return err
		}
	}
	if out.Lease != nil {
		return s.Store.WriteLease(out.Lease)
	}
	return nil
}

// DecideView is the document of `online decide` and `online retry`.
type DecideView struct {
	Queue string   `json:"queue,omitempty"`
	State string   `json:"state"`
	Count int      `json:"count,omitempty"`
	Lines []string `json:"lines"`
}

// State of a recorded decision or retry.
const StateRecorded = "recorded"

// withQueue runs f on the valid queue for a caller under the online lock
// and writes it back; it answers none, stale and busy_elsewhere itself.
func (s *Service) withQueue(ctx context.Context, c Caller, root RootInfo, op string, f func(q *Queue) (DecideView, error)) (DecideView, error) {
	v := DecideView{State: StateNone, Lines: []string{}}
	if root.RunOpen {
		return v, pderr.New(pderr.NeedsRecover, op, "a run was interrupted; run paritydesk recover")
	}
	err := s.Store.Locked(ctx, func() error {
		q, err := s.loadQueue(PhaseReconcile)
		if err != nil {
			return err
		}
		switch {
		case q == nil:
			v.Lines = []string{i18n.T(i18n.OnlineStatusNone)}
			return nil
		case root.Record != nil && root.Record.Kind != "" && root.Record.Queue == q.ID:
			v.Queue, v.Lines = q.ID, []string{i18n.T(i18n.OnlineArtifactsQueue)}
			return nil
		case Valid(q, root.Record, root.Epoch, root.Active, s.now()) != "":
			v.State, v.Queue, v.Lines = StateStale, q.ID, []string{i18n.T(i18n.OnlineStatusStale)}
			if err := s.Store.RemoveLease(); err != nil {
				return err
			}
			return s.Store.RemoveQueue(PhaseReconcile)
		}
		if err := s.authorize(q, c); err != nil {
			v.State, v.Queue, v.Lines = StateBusyElsewhere, q.ID, []string{i18n.T(i18n.OnlineBusyElsewhere)}
			return nil
		}
		nv, err := f(q)
		if err != nil {
			return err
		}
		v = nv
		v.Queue = q.ID
		return s.Store.WriteQueue(q)
	})
	return v, err
}

// Decide is `online decide`.
func (s *Service) Decide(ctx context.Context, c Caller, root RootInfo, question, option string) (DecideView, error) {
	return s.withQueue(ctx, c, root, "online decide", func(q *Queue) (DecideView, error) {
		if err := ApplyDecision(q, s.Providers, question, option); err != nil {
			return DecideView{}, err
		}
		return DecideView{State: StateRecorded, Lines: []string{}}, nil
	})
}

// Retry is `online retry`.
func (s *Service) Retry(ctx context.Context, c Caller, root RootInfo) (DecideView, error) {
	return s.withQueue(ctx, c, root, "online retry", func(q *Queue) (DecideView, error) {
		results := s.Store.Results(q.ID, s.resultMax(q))
		inflight := s.Store.Inflight(q.ID)
		in := NextInput{Queue: q, Inflight: inflight, Now: s.now(), Mono: s.mono(), Providers: s.Providers,
			Settings: s.settings()}
		resolve(q, in, results, nil)
		n, refused, retried := Retry(q, s.Providers, inflight)
		// The failed attempt's result was consumed (it made the step
		// fail); remove it so the new attempt's result can be recorded.
		for _, id := range retried {
			if _, ok := results[id]; ok {
				if err := s.Store.RemoveResult(q.ID, id); err != nil {
					return DecideView{}, err
				}
			}
		}
		v := DecideView{State: StateRecorded, Count: n, Lines: []string{i18n.T(i18n.OnlineRetried, n)}}
		for _, m := range refused {
			v.Lines = append(v.Lines, i18n.T(m))
		}
		return v, nil
	})
}
