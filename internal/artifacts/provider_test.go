package artifacts

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/online"
)

func TestToolPolicy(t *testing.T) {
	p := Provider{Spec: testSpec(t)}
	pol := p.Tools()
	if pol.BatchMax != 2 || pol.ResultGrace != 60*time.Second || pol.ResultMax != 8<<20 || pol.Normalize == nil {
		t.Fatalf("%+v", pol)
	}
	for _, tool := range []string{ToolListArtifacts, ToolGetArtifact, ToolReadDeclaration, ToolReadArtifact, ToolPublishFiles, ToolRequestDir} {
		if !slices.Contains(pol.Exact, tool) {
			t.Errorf("%s not exact", tool)
		}
	}
	if slices.Contains(pol.Exact, ToolPublishVersion) {
		t.Error("publish_version exact with transport files")
	}
	if !slices.Contains(pol.Reads, ToolGuide) || !slices.Contains(pol.Reads, ToolListFiles) {
		t.Error("reads")
	}
	v := testSpec(t)
	v.PublishVia = compat.ArtifactsPublishVer
	if !slices.Contains(Provider{Spec: v}.Tools().Exact, ToolPublishVersion) || !slices.Contains(Provider{Guard: true}.Tools().Exact, ToolPublishVersion) {
		t.Error("publish_version missing")
	}
}

// draft builds a round's draft as the service does.
func draft(t *testing.T, spec compat.ArtifactsSpec, plan RoundPlan) online.Draft {
	t.Helper()
	b, _ := json.Marshal(plan)
	return online.Build(online.BuildInput{Spec: manifest7(t).Online, Config: config.Default().Online, Phase: online.PhaseReconcile,
		To: "home", Replica: repHome, Extensions: map[string]json.RawMessage{ExtName: b}},
		[]online.Provider{online.ProbeProvider(), Provider{Spec: spec}})
}

func TestBuildValidates(t *testing.T) {
	spec := testSpec(t)
	// Without the extension the provider builds nothing.
	if steps, _, err := (Provider{Spec: spec}).Build(online.BuildInput{}); err != nil || steps != nil {
		t.Fatal("built without the extension")
	}
	in := roundIn(t, NewManifest(), &Chain{ID: "art-20261126T100000Z-00000001", Replica: repHome, State: ChainActive})
	plan, _, _, _ := PlanRound(in)
	d := draft(t, spec, plan)
	if !draftOK(d) || d.Steps[0].ID != online.ProbeStepID || d.Steps[1].ID != "a000" || d.Steps[2].Status != online.StatusDormant {
		t.Fatalf("%+v", d.Warnings)
	}
	// A step with a tool outside the policy is refused.
	bad := plan
	bad.Steps = append([]PlanStep(nil), plan.Steps[:1]...)
	bad.Steps[0].Tool = "mcp__claude-artifacts__edit_version"
	if d := draft(t, spec, bad); draftOK(d) {
		t.Fatal("edit_version accepted")
	}
	bad.Steps[0].Tool = ToolPublishVersion
	bad.Steps[0].Data = stepData(StepData{Role: RoleUpdateV, Parse: KnobsFor(RoleUpdateV, spec)})
	if d := draft(t, spec, bad); draftOK(d) {
		t.Fatal("publish_version accepted with transport files")
	}
	bad.Steps[0].Tool = "mcp__claude-artifacts__delete_artifact"
	if d := draft(t, spec, bad); draftOK(d) {
		t.Fatal("floor tool accepted")
	}
	// A role that does not call its step's tool.
	bad.Steps[0].Tool = ToolGetArtifact
	bad.Steps[0].Data = stepData(StepData{Role: RoleChunk})
	if d := draft(t, spec, bad); draftOK(d) {
		t.Fatal("role mismatch accepted")
	}
}

// queueOf turns a draft into a queue.
func queueOf(d online.Draft) *online.Queue {
	return &online.Queue{V: 1, ID: "art-20261126T100000Z-00000001-1", Phase: online.PhaseReconcile, State: online.QueueActive,
		Profile: "home", Steps: d.Steps}
}

func TestAdvancePages(t *testing.T) {
	spec := testSpec(t)
	in := roundIn(t, NewManifest(), &Chain{ID: "art-20261126T100000Z-00000001", Replica: repHome, State: ChainActive})
	plan, _, _, _ := PlanRound(in)
	q := queueOf(draft(t, spec, plan))
	p := Provider{Spec: spec}
	res := func(next *string) online.Result {
		b, _ := json.Marshal(ListPage{Items: []InvItem{}, NextCursor: next})
		return online.Result{Outcome: online.OutcomeOK, Parsed: b}
	}
	q.Step("a000").Status = online.StatusDone
	cur := "c2"
	results := map[online.StepID]online.Result{"a000": res(&cur)}
	if err := p.Advance(q, results, baseTime); err != nil {
		t.Fatal(err)
	}
	if s := q.Step("a001"); s.Status != online.StatusPending || string(s.Input) != `{"cursor":"c2","limit":100,"paged":true}` {
		t.Fatalf("%+v %s", s.Status, s.Input)
	}
	// Idempotent; an input written by anyone else is re-derived.
	q.Step("a001").Input = json.RawMessage(`{"cursor":"evil","limit":100,"paged":true}`)
	_ = p.Advance(q, results, baseTime)
	if string(q.Step("a001").Input) != `{"cursor":"c2","limit":100,"paged":true}` {
		t.Fatal("not re-derived")
	}
	// The next page without a cursor retires the rest.
	q.Step("a001").Status = online.StatusDone
	results["a001"] = res(nil)
	_ = p.Advance(q, results, baseTime)
	if s := q.Step("a002"); s.Status != online.StatusSkipped || s.Reason != online.ReasonNotNeeded {
		t.Fatalf("%+v", s)
	}
	if s := q.Step("a049"); s.Status != online.StatusSkipped {
		t.Fatalf("%+v", s)
	}
}

func TestAdvanceGrantAndQuestion(t *testing.T) {
	spec := testSpec(t)
	plan, _, _, _ := PlanRound(roundIn(t, publishManifest(GateExact), publishChain(PairIDFor(slugA))))
	q := queueOf(draft(t, spec, plan))
	p := Provider{Spec: spec}
	qs := p.Questions(q)
	if len(qs) != 1 || qs[0].ID != QuestionID || len(qs[0].Options) != 2 || qs[0].Options[0].ID != "apply" {
		t.Fatalf("%+v", qs)
	}
	q.Decisions = map[string]string{QuestionID: "apply"}
	if len(p.Questions(q)) != 0 {
		t.Fatal("asked again")
	}
	q.Step("a000").Status, q.Step("a000").Reason = online.StatusFailed, online.ReasonAppRefused
	_ = p.Advance(q, nil, baseTime)
	for _, id := range []online.StepID{"a001", "a002"} {
		if s := q.Step(id); s.Status != online.StatusSkipped || s.Reason != "grant_declined" {
			t.Fatalf("%s %+v", id, s)
		}
	}
}

// TestUnrecognizedFailsAtOnce: an unrecognized result resolves to
// failed(unrecognized) at the next `next`, without a grace (A14, M6).
func TestUnrecognizedFailsAtOnce(t *testing.T) {
	h := newHarness(t)
	h.servers["home"].add("A", page(2))
	out := h.sync("apply") // a normal sync first: the chain completes
	_ = out
	h.servers["home"].edit(h.servers["home"].sorted()[0], page(3))
	tok := ""
	for i := 0; i < 20; i++ {
		o, err := h.svc.Next(t.Context(), NextArgs{Session: testSession, Token: tok})
		if err != nil {
			t.Fatal(err)
		}
		if o.Token != "" {
			tok = o.Token
		}
		if o.State != online.StateCalls {
			break
		}
		for _, c := range o.Calls {
			resp := h.servers["home"].call(c.Tool, c.Input)
			if c.Tool == ToolReadArtifact {
				resp = textResp("not a summary")
			}
			h.record(c, resp)
		}
	}
	c, _ := h.svc.Archive.LoadChain()
	found := false
	for _, pr := range c.Report.Pairs {
		found = found || (pr.Result == "failed" && pr.Reason == online.ReasonUnrecognized)
	}
	if !found {
		t.Fatalf("report %+v", c.Report)
	}
}
