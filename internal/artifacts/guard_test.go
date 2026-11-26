package artifacts

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/hook"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/online"
)

// atPublish drives a work sync until the round offers publish_files.
func atPublish(t *testing.T) (*harness, online.Call, string) {
	t.Helper()
	h := newHarness(t)
	h.servers["home"].add("A", page(2))
	h.sync("apply")
	h.switchTo("work")
	ctx := context.Background()
	tok := ""
	for i := 0; i < 60; i++ {
		out, err := h.svc.Next(ctx, NextArgs{Session: testSession, Token: tok})
		if err != nil {
			t.Fatal(err)
		}
		if out.Token != "" {
			tok = out.Token
		}
		switch out.State {
		case online.StateQuestion:
			_, _ = h.svc.Decide(ctx, online.Caller{Session: testSession, Token: tok}, QuestionID, "apply")
		case online.StateCalls:
			for _, c := range out.Calls {
				if c.Tool == ToolPublishFiles {
					return h, c, tok
				}
				h.record(c, h.servers["work"].call(c.Tool, c.Input))
			}
		default:
			t.Fatalf("state %s", out.State)
		}
	}
	t.Fatal("no publish")
	return nil, online.Call{}, ""
}

// withURI adds a client-filled uri to the call's file entry.
func withURI(t *testing.T, c online.Call, sha string) json.RawMessage {
	t.Helper()
	var in map[string]any
	_ = json.Unmarshal(c.Input, &in)
	files := in["files"].([]any)
	files[0].(map[string]any)["uri"] = "artifact-file://mac.local/" + sha
	b, _ := json.Marshal(in)
	return b
}

func guardIn(h *harness, tool string, input json.RawMessage) online.GuardInput {
	l, _ := h.ol.Store.ReadLease()
	q, _ := h.ol.Store.ReadQueue(online.PhaseReconcile)
	c, _ := model.Canonical(input)
	return online.GuardInput{Mode: online.GuardArmed, Lease: l, Queue: q, Session: testSession, Tool: tool, Input: c,
		Inflight: online.InflightCounts(h.ol.Store.Inflight(q.ID)), Recorded: map[online.StepID]bool{},
		Policies: online.Policies(h.ol.Providers), Now: h.clk.Now(), Mono: online.Read(h.mono)}
}

// TestGuardPublishURI: a publish_files call with the client's uri of the
// staged blob matches its step; a uri of another sha is denied; the
// recorder behaves the same (A13, U9).
func TestGuardPublishURI(t *testing.T) {
	h, call, _ := atPublish(t)
	var d struct {
		Files []struct{ Path string } `json:"files"`
	}
	_ = json.Unmarshal(call.Input, &d)
	base := d.Files[0].Path[strings.LastIndex(d.Files[0].Path, "/")+1:]
	blob := strings.TrimSuffix(base, ".html")
	if dec := online.Decide(guardIn(h, ToolPublishFiles, withURI(t, call, blob))); dec.Deny || dec.Step != call.ID {
		t.Fatalf("right uri: %+v", dec)
	}
	if dec := online.Decide(guardIn(h, ToolPublishFiles, withURI(t, call, strings.Repeat("0", 64)))); !dec.Deny {
		t.Fatal("other sha allowed")
	}
	// Model changes are denied: another title, an extra mode.
	var in map[string]any
	_ = json.Unmarshal(call.Input, &in)
	in["mode"] = "replace"
	b, _ := json.Marshal(in)
	if dec := online.Decide(guardIn(h, ToolPublishFiles, b)); !dec.Deny {
		t.Fatal("altered input allowed")
	}
	// The app's own artifact tools are denied while armed; outside the
	// round, request_directory gets no decision.
	if dec := online.Decide(guardIn(h, "ArtifactData", json.RawMessage(`{"action":"set"}`))); !dec.Deny || dec.Reason != i18n.GuardDenyArtifactTool {
		t.Fatalf("ArtifactData: %+v", dec)
	}
	if dec := online.Decide(guardIn(h, ToolRequestDir, json.RawMessage(`{"path":"/elsewhere"}`))); !dec.Deny {
		t.Fatal("unplanned request_directory allowed while armed")
	}
	// The recorder: a payload with the wrong uri records nothing, the
	// right one records the step.
	deps := hook.ToolDeps{Store: h.ol.Store, Mode: online.GuardArmed, Mono: h.mono, Clock: h.clk, Providers: h.ol.Providers}
	post := func(input json.RawMessage) {
		p, _ := json.Marshal(map[string]any{"session_id": testSession, "hook_event_name": "PostToolUse", "tool_name": ToolPublishFiles,
			"tool_input": input, "tool_response": map[string]any{"content": []map[string]string{{"type": "text",
				"text": `{"slug":"00000000-0000-4000-8000-000000000a01","version":"1700000000-00009"}`}}}})
		if err := hook.PostToolUse(context.Background(), bytes.NewReader(p), &bytes.Buffer{}, deps); err != nil {
			t.Fatal(err)
		}
	}
	q, _ := h.ol.Store.ReadQueue(online.PhaseReconcile)
	post(withURI(t, call, strings.Repeat("0", 64)))
	if _, ok := h.ol.Store.Results(q.ID, nil)[call.ID]; ok {
		t.Fatal("recorded a call with another sha")
	}
	post(withURI(t, call, blob))
	r, ok := h.ol.Store.Results(q.ID, func(online.StepID) int64 { return 8 << 20 })[call.ID]
	if !ok || r.Outcome != online.OutcomeOK {
		t.Fatalf("not recorded: %+v", r)
	}
}
