package hook

import (
	"time"

	"context"
	"io"

	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/online"
)

// defaultPostMax is the PostToolUse payload limit when the queue does not
// name one (online.post_max_payload).
const defaultPostMax = 8 << 20

// PostToolUse records a result for an exactly matching step and writes
// nothing to out, on every path (panics included). Recording needs a
// fresh lease (bound to the payload's session, or unbound) for a
// reconcile step, or a live capture.json for a capture step (recorded
// per payload session). The first result of a step wins.
func PostToolUse(ctx context.Context, in io.Reader, out io.Writer, d ToolDeps) (err error) {
	defer func() {
		if recover() != nil {
			err = nil
		}
	}()
	_ = ctx
	_ = out
	if d.Store == nil {
		return nil
	}
	now, mono := d.now(), online.Read(d.Mono)
	lease, _ := d.Store.ReadLease()
	var q *online.Queue
	if lease.Fresh(now, mono, d.Store.Stopped()) {
		if rq, err := d.Store.ReadQueue(online.PhaseReconcile); err == nil && rq != nil && rq.ID == lease.Queue &&
			rq.Validate(d.policies(), rq.Guard.AlwaysDeny) == nil {
			q = rq
		}
	}
	var capq *online.Queue
	if c, err := d.Store.ReadQueue(online.PhaseCapture); err == nil && c != nil && c.Validate(d.policies(), c.Guard.AlwaysDeny) == nil {
		capq = c
	}
	limit := int64(defaultPostMax)
	for _, qq := range []*online.Queue{q, capq} {
		if qq != nil && qq.Guard.PostMaxPayload > 0 {
			limit = max(limit, qq.Guard.PostMaxPayload)
		}
	}
	for _, pol := range d.policies() {
		if pol.ResultMax > 0 {
			limit = max(limit, min(pol.ResultMax, online.MaxExtBytes)+1<<20)
		}
	}
	b, rerr := readLimited(in, limit)
	if rerr != nil {
		return nil
	}
	p, ok := ParseToolPayload(b, EventPostToolUse)
	if !ok {
		return nil
	}
	input, cerr := model.Canonical(p.ToolInput)
	if cerr != nil || len(p.ToolInput) == 0 {
		return nil
	}
	// A call can be both a reconcile step and a capture step (the probe
	// and c001 are the same get_session call): it is recorded for each.
	if q != nil && (lease.Session == "" || lease.Session == p.SessionID) {
		if s := matchStep(q, p.ToolName, input, d); s != nil && s.Kind != online.KindCapture {
			if int64(len(b)) <= postLimit(q, *s, d) {
				r := record(q, *s, p, input, d)
				_ = d.Store.CreateResult(r)
			}
		}
	}
	if capq != nil && online.CaptureLive(capq, now, mono, captureMaxAge(capq)) {
		if s := matchStep(capq, p.ToolName, input, d); s != nil && s.Kind == online.KindCapture {
			if int64(len(b)) <= postLimit(capq, *s, d) {
				r := record(capq, *s, p, input, d)
				_ = d.Store.CreateCaptureResult(r)
			}
		}
	}
	return nil
}

// captureMaxAge is the capture's own age limit (its expiry minus its
// creation).
func captureMaxAge(q *online.Queue) time.Duration {
	return q.ExpiresAt.Sub(q.CreatedAt)
}

// postLimit is the payload limit of one step: post_max_payload, or the
// provider's ResultMax + 1 MiB when larger.
func postLimit(q *online.Queue, s online.Step, d ToolDeps) int64 {
	limit := q.Guard.PostMaxPayload
	if limit <= 0 {
		limit = defaultPostMax
	}
	for _, p := range d.Providers {
		t := p.Tools()
		if t.ResultMax > 0 && ownsStep(t, s) {
			limit = max(limit, min(t.ResultMax, online.MaxExtBytes)+1<<20)
		}
	}
	return limit
}

func ownsStep(t online.ToolPolicy, s online.Step) bool {
	kind, tool := false, false
	for _, k := range t.Kinds {
		kind = kind || k == s.Kind
	}
	for _, x := range append(append([]string{}, t.Exact...), t.Reads...) {
		tool = tool || x == s.Tool
	}
	return kind && tool
}

// matchStep finds the step with the call's tool and canonical input (or
// input hash), after the owning policy's normalisation; any status.
func matchStep(q *online.Queue, tool string, input []byte, d ToolDeps) *online.Step {
	for i := range q.Steps {
		s := &q.Steps[i]
		if s.Tool != tool {
			continue
		}
		in := input
		for _, p := range d.Providers {
			if t := p.Tools(); ownsStep(t, *s) && t.Normalize != nil {
				n, _, ok := t.Normalize(tool, in)
				if !ok {
					in = nil
					break
				}
				if c, err := model.Canonical(n); err == nil {
					in = c
				}
				break
			}
		}
		if in == nil {
			continue
		}
		if (s.InputSHA256 != "" && online.InputSHA(in) == s.InputSHA256) || (len(s.Input) > 0 && string(s.Input) == string(in)) {
			return s
		}
	}
	return nil
}

// record recognises the response and builds the result file.
func record(q *online.Queue, s online.Step, p ToolPayload, input []byte, d ToolDeps) online.Result {
	mono := online.Read(d.Mono)
	r := online.Result{V: 1, Queue: q.ID, Step: s.ID, Tool: s.Tool, InputSHA: online.InputSHA(input), Session: p.SessionID,
		ToolUseID: p.ToolUseID, At: d.now(), MonoNS: mono.NS, BootID: mono.BootID}
	for _, pr := range d.Providers {
		if !ownsStep(pr.Tools(), s) {
			continue
		}
		if rp, ok := pr.(online.ResultParser); ok {
			r.Outcome, r.Parsed = rp.ParseResult(s, p.ToolResponse)
			return r
		}
		break
	}
	r.Outcome, r.Verdict, r.Parsed = online.Recognize(online.RecordInput{Step: s, ToolsOff: q.Guard.ToolsOff}, p.ToolResponse)
	return r
}
