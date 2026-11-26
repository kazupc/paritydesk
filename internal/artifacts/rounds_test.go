package artifacts

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/online"
)

func roundIn(t *testing.T, m *Manifest, c *Chain) RoundInput {
	t.Helper()
	return RoundInput{Spec: testSpec(t), Config: config.Default().Artifacts, Manifest: m, Chain: c, Profile: "home", Replica: repHome,
		Paths: ArchivePaths{Root: "/SUPPORT/artifacts", Outbox: "/SUPPORT/artifacts/outbox"}, AppVersion: appVer, Now: baseTime,
		Inventory: inv(true, item(slugA, verA, "Notes"))}
}

func planJSON(t *testing.T, p RoundPlan) string {
	t.Helper()
	b, err := json.MarshalIndent(p, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

func checkNeeds(t *testing.T, p RoundPlan) {
	t.Helper()
	for i, s := range p.Steps {
		if !slices.Contains(s.Needs, online.NeedReplica) || !slices.Contains(s.Needs, "read:r000") {
			t.Fatalf("step %d needs %v", i, s.Needs)
		}
		if publishRoles[s.Role] && !slices.Contains(s.Needs, needDecision) {
			t.Fatalf("publish step %d without the decision", i)
		}
		if s.Input != nil {
			c, _ := json.Marshal(s.Input)
			if string(c) != string(s.Input) {
				t.Fatalf("input not canonical")
			}
		}
	}
}

func TestPlanInventory(t *testing.T) {
	c := &Chain{ID: "art-20261126T100000Z-00000001", Replica: repHome, Phase: PhaseInventory, State: ChainActive}
	p, ph, done, err := PlanRound(roundIn(t, NewManifest(), c))
	if err != nil || done || ph != PhaseInventory || len(p.Steps) != 50 || p.Steps[1].Dormant != true || p.Steps[0].Templated {
		t.Fatalf("%v %v %v %d", err, ph, done, len(p.Steps))
	}
	checkNeeds(t, p)
	p.Steps = p.Steps[:2]
	golden(t, "plan_inventory.golden", planJSON(t, p))
	c.Replica = repWork
	if _, _, _, err := PlanRound(roundIn(t, NewManifest(), c)); !errors.Is(err, ErrReplica) {
		t.Fatalf("replica: %v", err)
	}
}

func TestPlanInspectAndRead(t *testing.T) {
	id := PairIDFor(slugA)
	m := NewManifest()
	m.Pairs[id] = basePair()
	c := &Chain{ID: "art-20261126T100000Z-00000001", Replica: repHome, Round: 1, Ingested: 1, Phase: PhaseInventory, State: ChainActive,
		Candidates: map[Slug]Candidate{
			slugA: {Pair: id, Alias: "home", Verdict: ActPublish, Role: RoleTarget},
			slugC: {Alias: "home", Verdict: WhyNewHere, Role: RoleRead},
		}}
	in := roundIn(t, m, c)
	p, ph, _, err := PlanRound(in)
	if err != nil || ph != PhaseInspect || len(p.Steps) != 5 {
		t.Fatalf("%v %v %d", err, ph, len(p.Steps))
	}
	roles := []string{}
	for _, s := range p.Steps {
		roles = append(roles, s.Role+":"+string(s.Slug)[len(s.Slug)-2:])
	}
	if strings.Join(roles, ",") != "get:10,files:10,get:12,files:12,decl:12" {
		t.Fatalf("roles %v", roles)
	}
	checkNeeds(t, p)
	golden(t, "plan_inspect.golden", planJSON(t, p))
	in.Spec.InspectPerRound = 1
	if p, _, _, _ := PlanRound(in); len(p.Steps) != 2 {
		t.Fatalf("inspect_per_round: %d", len(p.Steps))
	}
	// Reads: the inspected read candidate, first part without offset;
	// a continuation from a partial state with its offset.
	c.Candidates[slugA] = Candidate{Pair: id, Alias: "home", Role: RoleTarget, Inspected: true, State: CandSkipped}
	c.Candidates[slugC] = Candidate{Alias: "home", Verdict: WhyNewHere, Role: RoleRead, Inspected: true}
	c.Phase = PhaseInspect
	in = roundIn(t, m, c)
	p, ph, _, _ = PlanRound(in)
	if ph != PhaseRead || len(p.Steps) != 1 || string(p.Steps[0].Input) != `{"limit":1500,"slug":"`+slugC+`"}` {
		t.Fatalf("%v %s", ph, p.Steps[0].Input)
	}
	golden(t, "plan_read.golden", planJSON(t, p))
	in.Partial = map[Slug]PartialState{slugC: {Version: verA, Chunks: []Chunk{{Slug: slugC, Version: verA, Offset: 1, NextOffset: 1501,
		TotalLines: 2000, Lines: 1500, Body: strings.Repeat("x\n", 1499) + "x"}}}}
	p, _, _, _ = PlanRound(in)
	if string(p.Steps[0].Input) != `{"limit":1500,"offset":1501,"slug":"`+slugC+`"}` {
		t.Fatalf("continuation %s", p.Steps[0].Input)
	}
	// The byte budget: at least one step, then while under round_bytes.
	for i := 0; i < 20; i++ {
		c.Candidates[Slug(strings.Replace(slugD, "13", "9"+string(rune('a'+i%6))+"", 1))] = Candidate{Alias: "home", Verdict: WhyNewHere, Role: RoleRead, Inspected: true}
	}
	in = roundIn(t, m, c)
	p, _, _, _ = PlanRound(in)
	est := int64(len(p.Steps)) * in.Spec.FirstChunkEstimate
	if len(p.Steps) == 0 || len(p.Steps) > in.Spec.ReadPerRound || (len(p.Steps) > 1 && est > in.Spec.RoundBytes) {
		t.Fatalf("budget: %d steps", len(p.Steps))
	}
}

// publishChain is a chain whose reads are done and that has one target.
func publishChain(id PairID) *Chain {
	return &Chain{ID: "art-20261126T100000Z-00000001", Replica: repHome, Round: 3, Ingested: 3, Phase: PhaseRead, State: ChainActive,
		Candidates: map[Slug]Candidate{slugA: {Pair: id, Alias: "home", Verdict: ActPublish, Role: RoleTarget, Inspected: true,
			GetVersion: verA, TitleSHA: TitleKey("Old", ""), Read: ReadOwner, EntryOnly: true}}}
}

func publishManifest(gate string) *Manifest {
	m := NewManifest()
	m.Gate = Gate{Roundtrip: gate, AppVersion: appVer}
	p := basePair()
	p.Content.SHA256, p.Content.Bytes = sha(page(3)), int64(len(page(3)))
	m.Pairs[PairIDFor(slugA)] = p
	n := basePair()
	delete(n.Copies, "home")
	n.Title, n.TitleSHA = "Other", TitleKey("Other", "")
	n.Content.SHA256, n.Content.Bytes = sha(page(1)), int64(len(page(1)))
	n.Copies["work"] = &Copy{Replica: repWork, Slug: slugB, State: StateLive, Origin: OriginRecorded}
	m.Pairs[PairIDFor(slugB)] = n
	return m
}

func TestPlanPublish(t *testing.T) {
	id := PairIDFor(slugA)
	in := roundIn(t, publishManifest(GateExact), publishChain(id))
	p, ph, _, err := PlanRound(in)
	if err != nil || ph != PhasePublish || len(p.Steps) != 3 || p.Steps[0].Role != RoleGrant {
		t.Fatalf("%v %v %+v", err, ph, p.Steps)
	}
	for _, s := range p.Steps[1:] {
		if len(s.After) != 1 || s.After[0] != 0 {
			t.Fatal("publish not after the grant")
		}
	}
	if !strings.Contains(string(p.Steps[1].Input), `"base_version":"`+verA+`"`) || !strings.Contains(string(p.Steps[1].Input), `"title":"Notes"`) {
		t.Fatalf("update input %s", p.Steps[1].Input)
	}
	if !strings.Contains(string(p.Steps[2].Input), `"icon":"page"`) || strings.Contains(string(p.Steps[2].Input), "slug") {
		t.Fatalf("create input %s", p.Steps[2].Input)
	}
	checkNeeds(t, p)
	golden(t, "plan_publish_files.golden", planJSON(t, p))
	// Granted already: no grant step.
	c := publishChain(id)
	c.Granted = true
	if p, _, _, _ := PlanRound(roundIn(t, publishManifest(GateExact), c)); p.Steps[0].Role == RoleGrant {
		t.Fatal("grant planned twice")
	}
	// Gate unverified: exactly one publish (the smallest page).
	p, _, _, _ = PlanRound(roundIn(t, publishManifest(GateUnverified), publishChain(id)))
	if len(p.Steps) != 2 || p.Steps[1].Role != RoleCreate {
		t.Fatalf("unverified gate: %+v", p.Steps)
	}
	// Mismatch: nothing; off: nothing.
	if _, ph, done, _ := PlanRound(roundIn(t, publishManifest(GateMismatch), publishChain(id))); ph != PhaseDone || !done {
		t.Fatal("mismatch planned a publish")
	}
	in = roundIn(t, publishManifest(GateExact), publishChain(id))
	in.Spec.PublishVia = compat.ArtifactsPublishOff
	if _, _, done, _ := PlanRound(in); !done {
		t.Fatal("off planned a publish")
	}
	// Staging failure: no step, a blob_missing note.
	in = roundIn(t, publishManifest(GateExact), publishChain(id))
	in.Stage = func(string) (string, error) { return "", errors.New("gone") }
	p, _, done, _ := PlanRound(in)
	if !done || len(p.Notes) != 2 || p.Notes[0].Reason != "blob_missing" {
		t.Fatalf("staging failure: %v %+v", done, p.Notes)
	}
}

func TestPlanPublishVersion(t *testing.T) {
	id := PairIDFor(slugA)
	in := roundIn(t, publishManifest(GateExact), publishChain(id))
	in.Spec.PublishVia = compat.ArtifactsPublishVer
	in.Blob = func(s string) (string, error) {
		if s == sha(page(1)) {
			return page(1), nil
		}
		return page(3), nil
	}
	p, _, _, err := PlanRound(in)
	if err != nil || len(p.Steps) != 2 || p.Steps[0].Tool != ToolPublishVersion || p.Steps[0].InputSHA != online.InputSHA(p.Steps[0].Input) {
		t.Fatalf("%v %+v", err, p.Steps)
	}
	golden(t, "plan_publish_version.golden", planJSON(t, p))
	in.Spec.VersionMaxBytes = 10
	if p, _, _, _ := PlanRound(in); len(p.Notes) != 2 || p.Notes[0].Reason != "too_large" {
		t.Fatalf("over the limit: %+v", p.Notes)
	}
	in.Spec.VersionMaxBytes = 1 << 20
	in.Blob = func(string) (string, error) { return "<html>no doctype</html>", nil }
	if p, _, _, _ := PlanRound(in); len(p.Notes) != 2 {
		t.Fatalf("no doctype: %+v", p.Notes)
	}
}

func TestPlanVerifyAndCaps(t *testing.T) {
	id := PairIDFor(slugA)
	c := publishChain(id)
	c.Phase = PhasePublish
	c.Published = []Published{{Pair: id, Alias: "home", Slug: slugA, Version: verB, SHA: shaX, Verify: true}}
	// Verify comes first, even with the gate exact (m17).
	p, ph, _, _ := PlanRound(roundIn(t, publishManifest(GateExact), c))
	if ph != PhaseVerify || len(p.Steps) != 1 || p.Steps[0].Slug != slugA {
		t.Fatalf("%v %+v", ph, p.Steps)
	}
	var d StepData
	_ = json.Unmarshal(p.Steps[0].Data, &d)
	if !d.Verify {
		t.Fatal("not a verify read")
	}
	golden(t, "plan_verify.golden", planJSON(t, p))
	c.Published[0].Verified = true
	c.Tried = []PairID{id, PairIDFor(slugB)}
	if _, _, done, _ := PlanRound(roundIn(t, publishManifest(GateExact), c)); !done {
		t.Fatal("tried pairs planned again")
	}
	// max_rounds.
	c = publishChain(id)
	c.Round = 40
	if _, ph, done, _ := PlanRound(roundIn(t, publishManifest(GateExact), c)); !done || ph != PhaseDone {
		t.Fatal("max_rounds")
	}
	// Decision "skip" plans no further publish rounds.
	c = publishChain(id)
	c.Decision = "skip"
	if _, _, done, _ := PlanRound(roundIn(t, publishManifest(GateExact), c)); !done {
		t.Fatal("skip decision")
	}
}

func TestStageChecks(t *testing.T) {
	h := newHarness(t)
	a := h.svc.Archive
	content := page(2)
	s := sha(content)
	if _, err := a.Stage(s); err == nil {
		t.Fatal("staged a missing blob")
	}
	if err := a.PutBlob(s, []byte(content)); err != nil {
		t.Fatal(err)
	}
	p, err := a.Stage(s)
	if err != nil || !strings.HasSuffix(p, "/outbox/"+s+".html") {
		t.Fatalf("%s %v", p, err)
	}
	if p2, err := a.Stage(s); err != nil || p2 != p {
		t.Fatal("restage")
	}
}
