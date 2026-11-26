package online

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
)

// CaptureStepID is the built-in capture step: get_session of the calling
// session, for the reopen mapping (7.2).
const CaptureStepID StepID = "c001"

// CaptureInput is what a capture provider sees when `online capture`
// runs (leaving account, before the quit).
type CaptureInput struct {
	Spec    compat.OnlineSpec
	Config  config.Online
	Profile ids.Alias
	Replica model.Replica // leaving replica
	Session string        // validated --session, may be empty
}

// CaptureProvider adds read steps to the capture phase. Step ids use the
// letter c and indexes 100-999 (c000-c099 are built-in).
type CaptureProvider interface {
	Provider
	BuildCapture(in CaptureInput) ([]Step, error)
	// CaptureResults turns the usable capture results of its own steps
	// into the provider's BuildInput extension.
	CaptureResults(steps []Step, results map[StepID]Result) (json.RawMessage, error)
}

// CaptureSnapshot is what a usable capture feeds the run: the reopen
// mapping and the extension providers' results.
type CaptureSnapshot struct {
	ID            string
	ReopenSession ids.SessionID
	Extensions    map[string]json.RawMessage
}

// captureStep is c001.
func captureStep() Step {
	return Step{ID: CaptureStepID, Kind: KindCapture, Tool: ToolGetSession, Input: json.RawMessage(`{"session_id":"self"}`),
		Status: StatusPending, Needs: []string{}, After: []StepID{}, Expect: Expect{Format: FormatJSONObject, Fields: map[string]string{}}}
}

// BuildCapture builds capture.json for `online capture`: c001 plus the
// capture providers' steps (c100 on).
func BuildCapture(in CaptureInput, ps []Provider, id string, now time.Time, mono MonoNow, epoch uint64, maxAge time.Duration) (*Queue, error) {
	steps := []Step{captureStep()}
	next := 100
	for _, p := range ps {
		cp, ok := p.(CaptureProvider)
		if !ok {
			continue
		}
		extra, err := cp.BuildCapture(in)
		if err != nil {
			continue // the capture is optional
		}
		for _, s := range extra {
			if next > 999 {
				break
			}
			s.ID = StepID(fmt.Sprintf("c%03d", next))
			s.Kind = KindCapture
			normalizeStep(&s)
			steps = append(steps, s)
			next++
		}
	}
	session := ""
	if ids.IsUUID(in.Session) {
		session = in.Session
	}
	q := &Queue{V: 1, ID: id, Phase: PhaseCapture, State: QueueActive, Profile: in.Profile, Replica: in.Replica,
		Epoch: epoch, Session: session, CreatedAt: now, ExpiresAt: now.Add(maxAge), BootID: mono.BootID, MonoNS: mono.NS,
		Guard: GuardExtra{AlwaysDeny: in.Spec.AlwaysDeny, ServersExtra: in.Spec.GuardedServersExtra,
			ToolsExtra: in.Spec.GuardedToolsExtra, PostMaxPayload: in.Spec.PostMaxPayload, ToolsOff: in.Spec.Responses.ToolsOff},
		Steps: steps}
	if err := q.Validate(Policies(ps), in.Spec.AlwaysDeny); err != nil {
		return nil, err
	}
	return q, nil
}

// CaptureLive reports whether capture.json still records results: not
// complete and not expired (wall and monotonic, same boot).
func CaptureLive(q *Queue, now time.Time, mono MonoNow, maxAge time.Duration) bool {
	return q != nil && q.Phase == PhaseCapture && q.State != QueueComplete && q.State != QueueCancelled &&
		fresh(q, now, mono, maxAge)
}

// fresh applies the age rule of 7.3 rule 2.
func fresh(q *Queue, now time.Time, mono MonoNow, maxAge time.Duration) bool {
	if !mono.OK || q.BootID == "" || q.BootID != mono.BootID {
		return false
	}
	if mono.NS-q.MonoNS > int64(maxAge) || mono.NS < q.MonoNS {
		return false
	}
	return now.Sub(q.CreatedAt) <= maxAge && !now.Before(q.CreatedAt.Add(-time.Minute))
}

// CaptureUsable applies 7.3 rules 1 and 2: same profile, replica and
// epoch, same boot, and younger than maxAge by both clocks.
func CaptureUsable(q *Queue, alias ids.Alias, rep model.Replica, epoch uint64, now time.Time, mono MonoNow, maxAge time.Duration) bool {
	return q != nil && q.Phase == PhaseCapture && q.State != QueueCancelled && q.Profile == alias && q.Replica == rep &&
		q.Epoch == epoch && fresh(q, now, mono, maxAge)
}

// Snapshot turns a usable capture's results (recorded for q.Session only,
// 7.3 rule 3) into what the run uses. It never yields a removal or a
// base change; only the leaving side uses it.
func Snapshot(q *Queue, results map[StepID]Result, ps []Provider) *CaptureSnapshot {
	snap := &CaptureSnapshot{ID: q.ID}
	if q.Session == "" {
		return snap
	}
	if r, ok := results[CaptureStepID]; ok && r.Session == q.Session && r.Outcome == OutcomeOK && r.Tool == ToolGetSession {
		if id, ok := ProbeSession(r); ok {
			snap.ReopenSession = ids.SessionID(id)
		}
	}
	for _, p := range ps {
		cp, ok := p.(CaptureProvider)
		if !ok {
			continue
		}
		var own []Step
		mine := map[StepID]Result{}
		for _, s := range q.Steps {
			if s.ID == CaptureStepID || providerFor(ps, s) != p {
				continue
			}
			own = append(own, s)
			if r, ok := results[s.ID]; ok && r.Session == q.Session && r.Outcome == OutcomeOK {
				mine[s.ID] = r
			}
		}
		if len(own) == 0 {
			continue
		}
		ext, err := cp.CaptureResults(own, mine)
		if err != nil || len(ext) == 0 {
			continue
		}
		if snap.Extensions == nil {
			snap.Extensions = map[string]json.RawMessage{}
		}
		snap.Extensions[p.Name()] = ext
	}
	return snap
}
