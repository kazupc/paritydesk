package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/kazupc/paritydesk/internal/clock"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/online"
)

// PreDeadline is PreToolUse's internal deadline; the whole process,
// start included, returns within 2.5 s.
const PreDeadline = 2 * time.Second

// ToolDeps are what PreToolUse and PostToolUse consult: the online folder
// (read side for Pre, create-only for Post), the guard mode from the
// config, the clocks and the providers' tool policies (code). No state,
// manifest, process table or main.log.
type ToolDeps struct {
	Store     *online.Store
	Mode      online.GuardMode
	Mono      online.MonoClock
	Clock     clock.Clock
	Providers []online.Provider
}

func (d ToolDeps) now() time.Time {
	if d.Clock == nil {
		return time.Now()
	}
	return d.Clock.Now()
}

// policies returns the providers' tool policies.
func (d ToolDeps) policies() []online.ToolPolicy { return online.Policies(d.Providers) }

// denyDoc is the PreToolUse deny document.
type denyDoc struct {
	HookSpecificOutput denyOutput `json:"hookSpecificOutput"`
}

type denyOutput struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision"`
	PermissionDecisionReason string `json:"permissionDecisionReason"`
}

// DenyDocument returns the exact deny document for a reason (one line).
func DenyDocument(reason i18n.Msg) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(denyDoc{denyOutput{HookEventName: EventPreToolUse, PermissionDecision: "deny",
		PermissionDecisionReason: i18n.En(reason)}})
	return buf.Bytes()
}

// armedFor reports a fresh lease that covers session (bound to it, or
// unbound); with any set, any fresh lease counts (the session is unknown).
func armedFor(l *online.Lease, d ToolDeps, stop bool, session string, any bool) bool {
	if !l.Fresh(d.now(), online.Read(d.Mono), stop) {
		return false
	}
	return any || l.Session == "" || l.Session == session
}

// armedWait bounds the wait for the armed marker once the deadline has
// passed, so the process still returns within 2.5 s.
const armedWait = 200 * time.Millisecond

// armedMarker reads the shim's armed marker in the background, so a
// decision that cannot be made in time (a hung store) or a lease that
// cannot be read still knows whether to fail closed.
type armedMarker struct {
	done  chan struct{}
	armed bool
}

func startArmed(d ToolDeps) *armedMarker {
	a := &armedMarker{done: make(chan struct{})}
	go func() {
		defer close(a.done)
		defer func() {
			if recover() != nil {
				a.armed = true
			}
		}()
		armed, err := d.Store.ArmedUntil(d.now())
		a.armed = armed || err != nil
	}()
	return a
}

// wait returns whether the marker holds a future deadline; a marker
// that cannot be read, or not within t, counts as armed (fail closed).
func (a *armedMarker) wait(t time.Duration) bool {
	timer := time.NewTimer(t)
	defer timer.Stop()
	select {
	case <-a.done:
		return a.armed
	case <-timer.C:
		return true
	}
}

// PreToolUse writes at most one deny document to out. It never writes
// "allow": a call that passes gets no output, so the app's own approval
// cards still appear. On any internal failure (including a panic or its
// own deadline) it denies when the session may be armed and stays silent
// otherwise. All of its file reads run under the deadline: the lease
// first (before the payload, so a malformed or oversized payload still
// fails closed while armed). A lease that cannot be read or judged while
// the armed marker holds a future deadline fails closed too.
func PreToolUse(ctx context.Context, in io.Reader, out io.Writer, d ToolDeps) (err error) {
	if d.Store == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, PreDeadline)
	defer cancel()
	armed := startArmed(d)
	type answer struct{ doc []byte }
	ch := make(chan answer, 1)
	go func() {
		var doc []byte
		known, armedAny := false, false
		defer func() {
			if recover() != nil {
				doc = nil
				if (known && armedAny) || (!known && armed.wait(armedWait)) {
					doc = DenyDocument(i18n.GuardDenyUnreadable)
				}
			}
			ch <- answer{doc}
		}()
		lease, lerr := d.Store.ReadLease()
		stop := d.Store.Stopped()
		mono := online.Read(d.Mono)
		armedAny = armedFor(lease, d, stop, "", true)
		// The lease cannot be judged: unreadable, or no monotonic clock
		// to check it with. The armed marker decides.
		unknown := !stop && !armedAny && (lerr != nil || (lease != nil && !mono.OK)) && armed.wait(PreDeadline)
		known = true
		if unknown {
			armedAny = true
		}
		doc = preDecide(in, d, lease, stop, armedAny, unknown)
	}()
	var doc []byte
	select {
	case a := <-ch:
		doc = a.doc
	case <-ctx.Done():
		if armed.wait(armedWait) {
			doc = DenyDocument(i18n.GuardDenyUnreadable)
		}
	}
	if len(doc) > 0 {
		_, _ = out.Write(doc)
	}
	return nil
}

// preDecide reads the payload, decides and creates the in-flight marker.
// leaseUnknown: armed, but the lease could not be read or judged.
func preDecide(in io.Reader, d ToolDeps, lease *online.Lease, stop, armedAny, leaseUnknown bool) []byte {
	b, rerr := readLimited(in, MaxPrePayload)
	p, ok := ParseToolPayload(b, EventPreToolUse)
	if rerr != nil || !ok {
		if armedAny {
			return DenyDocument(i18n.GuardDenyUnreadable)
		}
		return nil
	}
	var input json.RawMessage
	if c, err := model.Canonical(p.ToolInput); err == nil && len(p.ToolInput) > 0 {
		input = c
	}
	var q *online.Queue
	var qerr error
	// The queue carries the manifest's additions to the guard tables, so
	// it is read whenever a lease exists and, in guard = "always" mode,
	// also without one.
	if lease != nil || leaseUnknown || d.Mode == online.GuardAlways {
		q, qerr = d.Store.ReadQueue(online.PhaseReconcile)
		if qerr == nil && q != nil {
			qerr = q.Validate(d.policies(), q.Guard.AlwaysDeny)
		}
	}
	// A live capture's own reads run in every session (7.1).
	if lease != nil || leaseUnknown {
		if capq, err := d.Store.ReadQueue(online.PhaseCapture); err == nil && capq != nil &&
			capq.Validate(d.policies(), capq.Guard.AlwaysDeny) == nil &&
			online.CaptureLive(capq, d.now(), online.Read(d.Mono), capq.ExpiresAt.Sub(capq.CreatedAt)) &&
			online.CaptureCall(capq, p.SessionID, p.ToolName, input, d.policies()) {
			return nil
		}
	}
	var dec online.GuardDecision
	if leaseUnknown {
		var extra online.GuardExtra
		if q != nil {
			extra = q.Guard
		}
		dec = online.DecideLeaseUnknown(p.ToolName, input, extra, d.policies())
	} else {
		gi := online.GuardInput{Mode: d.Mode, Stop: stop, Lease: lease, Queue: q, QueueErr: qerr, Session: p.SessionID,
			Tool: p.ToolName, Input: input, Policies: d.policies(), Now: d.now(), Mono: online.Read(d.Mono)}
		if q != nil {
			gi.Inflight = online.InflightCounts(d.Store.Inflight(q.ID))
			gi.Recorded = map[online.StepID]bool{}
			for id := range d.Store.Results(q.ID, nil) {
				gi.Recorded[id] = true
			}
		}
		dec = online.Decide(gi)
	}
	if !dec.Deny && dec.Step != "" {
		mono := online.Read(d.Mono)
		err := d.Store.CreateInflight(q.ID, dec.Step, online.Marker{N: dec.N, At: d.now(), MonoNS: mono.NS, BootID: mono.BootID})
		switch {
		case err == nil:
			return nil
		case errors.Is(err, online.ErrExists):
			dec = online.GuardDecision{Deny: true, Reason: online.DenyReasonFor(q, i18n.GuardDenyDuplicate)}
		default:
			dec = online.GuardDecision{Deny: true, Reason: i18n.GuardDenyUnreadable}
		}
	}
	if !dec.Deny {
		return nil
	}
	queue := ""
	if q != nil {
		queue = q.ID
	}
	_ = d.Store.CreateDenial(online.Denial{At: d.now(), Tool: p.ToolName, Reason: string(dec.Reason), Queue: queue}, online.Read(d.Mono))
	return DenyDocument(dec.Reason)
}

// readLimited reads at most max bytes; a longer input is an error.
func readLimited(in io.Reader, max int64) ([]byte, error) {
	if in == nil {
		return nil, io.ErrUnexpectedEOF
	}
	b, err := io.ReadAll(io.LimitReader(in, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, errors.New("hook: payload too large")
	}
	return b, nil
}
