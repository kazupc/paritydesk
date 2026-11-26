package artifacts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/online"
	"github.com/kazupc/paritydesk/internal/plan"
)

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// chunkRR is a one-part read of content as a done step.
func chunkRR(s Slug, v VersionID, content, title string, verify bool) RoundResult {
	c := Chunk{Slug: s, Version: v, Offset: 1, TotalLines: lineCount(content), Lines: lineCount(content), Body: content,
		BodySHA: sha(content), Form: "bare", Title: title, TitleSHA: TitleKey(title, "")}
	b, _ := json.Marshal(c)
	return RoundResult{Step: online.Step{ID: "a000", Kind: Kind, Status: online.StatusDone},
		Data: StepData{Role: RoleChunk, Slug: s, Verify: verify}, Result: &online.Result{Outcome: online.OutcomeOK, Parsed: b}}
}

func stepRR(role string, d StepData, status online.StepStatus, reason string, outcome online.Outcome, parsed any) RoundResult {
	d.Role = role
	rr := RoundResult{Step: online.Step{ID: "a001", Kind: Kind, Status: status, Reason: reason}, Data: d}
	if outcome != "" {
		b, _ := json.Marshal(parsed)
		rr.Result = &online.Result{Outcome: outcome, Parsed: b}
	}
	return rr
}

// ingestIn is an ingest of one round on home.
func ingestIn(t *testing.T, m *Manifest, c *Chain, results ...RoundResult) IngestInput {
	t.Helper()
	blobs := map[string]string{}
	return IngestInput{Spec: testSpec(t), Config: config.Default().Artifacts, Limits: plan.Limits{MaxDeleteFraction: 0.1, MaxDeleteMin: 2},
		Manifest: m, Chain: c, Inventory: inv(true, item(slugA, verB, "Notes")), Results: results, Profile: "home", Replica: repHome,
		AppVersion: appVer, Now: baseTime, Blob: func(s string) (string, error) {
			if b, ok := blobs[s]; ok {
				return b, nil
			}
			return "", fmt.Errorf("no blob")
		}}
}

func readChain(cands map[Slug]Candidate) *Chain {
	return &Chain{V: 1, ID: "art-20261126T100000Z-00000001", Profile: "home", Replica: repHome, Round: 2, Ingested: 1,
		Phase: PhaseRead, State: ChainActive, Candidates: cands}
}

func TestConflictMatrix(t *testing.T) {
	later, earlier := t0.Add(time.Hour), t0.Add(-time.Hour)
	cases := []struct {
		name        string
		here, other time.Time
		hereWins    bool
	}{
		{"here newer", later, t0, true},
		{"other newer", earlier, t0, false},
		{"equal", t0, t0, false},
		{"here unknown", time.Time{}, t0, false},
		{"other unknown", later, time.Time{}, true},
		{"both unknown", time.Time{}, time.Time{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManifest()
			p := basePair()
			p.Content.SHA256 = shaY
			p.Content.ChangedAt = tc.other
			id := PairIDFor(slugA)
			m.Pairs[id] = p
			cand := Candidate{Pair: id, Alias: "home", Verdict: WhyConflictBothSides, Version: verB, Role: RoleRead, Inspected: true,
				UpdatedAt: tc.here, GetVersion: verB, Read: ReadOwner, EntryOnly: true}
			in := ingestIn(t, m, readChain(map[Slug]Candidate{slugA: cand}), chunkRR(slugA, verB, "here\nedit", "Notes", false))
			in.Spec.VersionTimePrefix = "" // only the inventory time counts here
			out := Ingest(in)
			np := out.Manifest.Pairs[id]
			if tc.hereWins {
				if np.Content.SHA256 != sha("here\nedit") || np.Content.SourceProfile != "home" {
					t.Fatalf("here should win: %+v", np.Content)
				}
				if len(np.History) != 1 || np.History[0].SHA256 != shaY || np.History[0].Reason != HistConflict {
					t.Fatalf("history %+v", np.History)
				}
			} else {
				if np.Content.SHA256 != shaY {
					t.Fatal("other should win")
				}
				if len(np.History) != 1 || np.History[0].SHA256 != sha("here\nedit") || np.History[0].Title != "Notes" {
					t.Fatalf("history %+v", np.History)
				}
				c := out.Chain.Candidates[slugA]
				if c.Role != RoleTarget || c.Base != verB {
					t.Fatalf("loser not a target: %+v", c)
				}
			}
			if out.Blobs[sha("here\nedit")] != "here\nedit" {
				t.Fatal("read content not written")
			}
			if out.Chain.Report.Conflicts != 1 {
				t.Fatalf("report %+v", out.Chain.Report)
			}
		})
	}
}

// A conflict the archive side wins never makes the copy a publish target
// across the data boundary (8.7): the copy's edit is still kept in
// history, nothing is published there.
func TestConflictKeepsBoundary(t *testing.T) {
	for name, pol := range map[string]BoundaryPolicy{
		"home does not import": {Imports: map[ids.Alias]bool{"home": false}},
		"work is managed":      {Managed: map[ids.Alias]bool{"work": true}},
	} {
		t.Run(name, func(t *testing.T) {
			m := NewManifest()
			p := basePair()
			p.Content.SHA256 = shaY
			id := PairIDFor(slugA)
			m.Pairs[id] = p
			if v := Judge(p, id, "home", repHome, inv(true, item(slugA, verB, "Notes")), ChainFlags{Claimed: m.claimed()}, pol, config.Default().Artifacts); v.Action != ActConflict {
				t.Fatalf("judge %+v", v)
			}
			cand := Candidate{Pair: id, Alias: "home", Verdict: WhyConflictBothSides, Version: verB, Role: RoleRead, Inspected: true,
				UpdatedAt: t0.Add(-time.Hour), GetVersion: verB, Read: ReadOwner, EntryOnly: true}
			in := ingestIn(t, m, readChain(map[Slug]Candidate{slugA: cand}), chunkRR(slugA, verB, "here\nedit", "Notes", false))
			in.Spec.VersionTimePrefix = ""
			in.Policy = pol
			out := Ingest(in)
			c := out.Chain.Candidates[slugA]
			if c.Role == RoleTarget && c.State == "" {
				t.Fatalf("archive content became a publish target across the boundary: %+v", c)
			}
			if c.Reason != WhyBoundary {
				t.Fatalf("reason %q", c.Reason)
			}
			if h := out.Manifest.Pairs[id].History; len(h) != 1 || h[0].SHA256 != sha("here\nedit") {
				t.Fatalf("the copy's edit is not in history: %+v", h)
			}
			// The publish round plans nothing for it either.
			ri := RoundInput{Spec: in.Spec, Config: in.Config, Policy: pol, Manifest: out.Manifest, Chain: out.Chain,
				Inventory: in.Inventory, Profile: "home", Replica: repHome, AppVersion: appVer, Now: baseTime}
			c.State = ""
			ri.Chain.Candidates[slugA] = c
			if steps, _ := publishSteps(ri); len(steps) != 0 {
				t.Fatalf("publish steps across the boundary: %+v", steps)
			}
		})
	}
}

func TestReadCases(t *testing.T) {
	id := PairIDFor(slugA)
	// changed_here with the archive's content: a rename only.
	m := NewManifest()
	m.Pairs[id] = basePair()
	cand := Candidate{Pair: id, Alias: "home", Verdict: WhyChangedHere, Version: verB, Role: RoleRead, Inspected: true}
	out := Ingest(ingestIn(t, m, readChain(map[Slug]Candidate{slugA: cand}), chunkRR(slugA, verB, "x", "Notes 2", false)))
	p := out.Manifest.Pairs[id]
	// Content differs ("x" vs shaX): accepted here.
	if p.Content.SHA256 != sha("x") || p.Title != "Notes 2" || p.Copies["home"].SyncedVersion != verB {
		t.Fatalf("%+v", p)
	}
	// verify_import with unknown times: the live copy wins (M8).
	m = NewManifest()
	ip := basePair()
	ip.Content.ChangedAt = time.Time{}
	ip.Copies["home"].State = StateUnverified
	m.Pairs[id] = ip
	cand = Candidate{Pair: id, Alias: "home", Verdict: WhyVerifyImport, Version: verB, Role: RoleRead, Inspected: true}
	in := ingestIn(t, m, readChain(map[Slug]Candidate{slugA: cand}), chunkRR(slugA, verB, "live", "Notes", false))
	in.Spec.VersionTimePrefix = ""
	out = Ingest(in)
	if c := out.Manifest.Pairs[id].Copies["home"]; c.State != StateLive || out.Manifest.Pairs[id].Content.SHA256 != sha("live") {
		t.Fatalf("live should win: %+v", out.Manifest.Pairs[id])
	}
	// verify_import with the archive's time known and the live copy's
	// unknown: the live copy still wins (strictly newer cannot be shown).
	for name, live := range map[string]time.Time{"live unknown": {}, "live newer": t0.Add(time.Hour), "archive newer": t0.Add(-time.Hour)} {
		m = NewManifest()
		ip = basePair()
		ip.Content.ChangedAt = t0
		ip.Copies["home"].State = StateUnverified
		m.Pairs[id] = ip
		cand = Candidate{Pair: id, Alias: "home", Verdict: WhyVerifyImport, Version: verB, Role: RoleRead, Inspected: true, UpdatedAt: live}
		in = ingestIn(t, m, readChain(map[Slug]Candidate{slugA: cand}), chunkRR(slugA, verB, "live", "Notes", false))
		in.Spec.VersionTimePrefix = ""
		out = Ingest(in)
		archiveWins := name == "archive newer"
		if got := out.Manifest.Pairs[id].Content.SHA256 == shaX; got != archiveWins {
			t.Fatalf("%s: archive won = %v, content %+v", name, got, out.Manifest.Pairs[id].Content)
		}
	}
	// new_here makes a pair.
	m = NewManifest()
	cand = Candidate{Alias: "home", Verdict: WhyNewHere, Version: verB, Role: RoleRead, Inspected: true, Icon: "notes"}
	out = Ingest(ingestIn(t, m, readChain(map[Slug]Candidate{slugC: cand}), chunkRR(slugC, verB, "new", "Fresh", false)))
	np := out.Manifest.Pairs[PairIDFor(slugC)]
	if np == nil || np.Copies["home"].Origin != OriginRecorded || np.Content.Icon != "notes" || np.Title != "Fresh" {
		t.Fatalf("%+v", np)
	}
}

func TestAdoption(t *testing.T) {
	id := PairIDFor(slugA)
	mk := func() *Manifest {
		m := NewManifest()
		p := basePair()
		p.Copies["home"] = &Copy{Replica: repHome, State: StateCreatePending, Origin: OriginCreated,
			CreateAttempt: &CreateAttempt{At: t0, TitleSHA: p.TitleSHA, ContentSHA: sha("same")}}
		m.Pairs[id] = p
		return m
	}
	cand := Candidate{Pair: id, Alias: "home", Verdict: WhyAdoptCheck, Version: verB, Role: RoleRead, Inspected: true}
	out := Ingest(ingestIn(t, mk(), readChain(map[Slug]Candidate{slugC: cand}), chunkRR(slugC, verB, "same", "Notes", false)))
	if c := out.Manifest.Pairs[id].Copies["home"]; c.Slug != slugC || c.State != StateLive || c.CreateAttempt != nil {
		t.Fatalf("not adopted: %+v", c)
	}
	out = Ingest(ingestIn(t, mk(), readChain(map[Slug]Candidate{slugC: cand}), chunkRR(slugC, verB, "other", "Notes", false)))
	if c := out.Manifest.Pairs[id].Copies["home"]; c.State != StateCreatePending {
		t.Fatalf("pending copy changed: %+v", c)
	}
	if out.Manifest.Pairs[PairIDFor(slugC)] == nil {
		t.Fatal("the other artifact did not become a pair")
	}
	// Title taken: a different artifact blocks the create until allow.
	m := NewManifest()
	p := basePair()
	delete(p.Copies, "home")
	m.Pairs[id] = p
	out = Ingest(ingestIn(t, m, readChain(map[Slug]Candidate{slugC: cand}), chunkRR(slugC, verB, "different", "Notes", false)))
	if tt := out.Manifest.Pairs[id].Flags.TitleTaken; len(tt) != 1 || tt[0] != "home" {
		t.Fatalf("title taken %+v", tt)
	}
}

func TestInventoryIngest(t *testing.T) {
	pageRR := func(p int, items []InvItem, next *string) RoundResult {
		return stepRR(RoleInvPage, StepData{Page: p}, online.StatusDone, "", online.OutcomeOK, ListPage{Items: items, NextCursor: next})
	}
	chain := &Chain{V: 1, ID: "art-20261126T100000Z-00000001", Profile: "home", Replica: repHome, Round: 1, Phase: PhaseInventory, State: ChainActive}
	// Foreign: a slug recorded for another profile aborts with nothing.
	m := NewManifest()
	p := basePair()
	p.Copies["work"] = &Copy{Replica: repWork, Slug: slugB, State: StateLive, Origin: OriginRecorded}
	m.Pairs[PairIDFor(slugA)] = p
	in := ingestIn(t, m, chain, pageRR(0, []InvItem{item(slugB, verA, "X")}, nil))
	in.Inventory = nil
	out := Ingest(in)
	if out.Chain.State != ChainStale || out.Chain.StaleWhy != "inventory_foreign" || out.Inventory != nil {
		t.Fatalf("%+v", out.Chain)
	}
	if b1, b2 := mustEncode(t, m), mustEncode(t, out.Manifest); b1 != b2 {
		t.Fatal("manifest changed by a foreign inventory")
	}
	// Tombstone breaker: many missing copies, none tombstoned.
	m = NewManifest()
	for i := 0; i < 5; i++ {
		s := Slug(fmt.Sprintf("00000000-0000-4000-8000-%012d", 100+i))
		bp := basePair()
		bp.Copies["home"].Slug = s
		m.Pairs[PairIDFor(s)] = bp
	}
	in = ingestIn(t, m, chain, pageRR(0, []InvItem{}, nil))
	in.Inventory = nil
	out = Ingest(in)
	for _, p := range out.Manifest.Pairs {
		if p.Copies["home"].State != StateLive {
			t.Fatal("tombstoned past the breaker")
		}
	}
	if len(out.Warnings) != 1 {
		t.Fatalf("warnings %v", out.Warnings)
	}
	// Under the breaker: one tombstone; an incomplete inventory never
	// tombstones.
	in.Limits.MaxDeleteMin = 10
	out = Ingest(in)
	dead := 0
	for _, p := range out.Manifest.Pairs {
		if p.Copies["home"].State == StateDeleted {
			dead++
		}
	}
	if dead != 5 || out.Chain.Report.DeletedHere != 5 {
		t.Fatalf("dead %d", dead)
	}
	cursor := "c2"
	in = ingestIn(t, m, chain, pageRR(0, []InvItem{}, &cursor),
		stepRR(RoleInvPage, StepData{Page: 1}, online.StatusFailed, online.ReasonNoResult, "", nil))
	in.Inventory = nil
	out = Ingest(in)
	if out.Inventory.Complete {
		t.Fatal("incomplete inventory marked complete")
	}
	for _, p := range out.Manifest.Pairs {
		if p.Copies["home"].State != StateLive {
			t.Fatal("tombstoned from an incomplete inventory")
		}
	}
}

func mustEncode(t *testing.T, m *Manifest) string {
	t.Helper()
	m2 := m.Clone()
	m2.Seq, m2.UpdatedAt = 0, time.Time{}
	b, err := Encode(m2)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPublishIngest(t *testing.T) {
	id := PairIDFor(slugA)
	newChain := func() *Chain {
		c := readChain(map[Slug]Candidate{slugA: {Pair: id, Alias: "home", Verdict: ActPublish, Role: RoleTarget, Inspected: true}})
		c.Phase = PhasePublish
		return c
	}
	mk := func() *Manifest {
		m := NewManifest()
		p := basePair()
		p.Content.SHA256 = shaY
		m.Pairs[id] = p
		return m
	}
	upd := StepData{Pair: id, Slug: slugA, BlobSHA: shaY, Title: true}
	ok := stepRR(RoleUpdate, upd, online.StatusDone, "", online.OutcomeOK, PublishParsed{Slug: slugA, Version: verB})
	out := Ingest(ingestIn(t, mk(), newChain(), ok))
	c := out.Manifest.Pairs[id].Copies["home"]
	if c.SyncedSHA != shaY || c.SyncedVersion != verB || len(out.Chain.Published) != 1 || !out.Chain.Published[0].Verify {
		t.Fatalf("%+v %+v", c, out.Chain.Published)
	}
	if len(out.Unstage) != 1 || out.Unstage[0] != shaY {
		t.Fatalf("unstage %v", out.Unstage)
	}
	// verify = "all": every publish is read back, not only the first.
	all := ingestIn(t, mk(), newChain(), ok)
	all.Config.Verify = config.ArtifactsVerifyAll
	all.Chain.Published = []Published{{Pair: id, Alias: "home", Slug: slugA, Version: verA, SHA: shaY, Verify: true, Verified: true}}
	if out := Ingest(all); len(out.Chain.Published) != 2 || !out.Chain.Published[1].Verify {
		t.Fatalf("verify all: %+v", out.Chain.Published)
	}
	exact := func() *Manifest {
		m := mk()
		m.Gate = Gate{Roundtrip: GateExact, AppVersion: appVer, At: baseTime}
		return m
	}
	first := ingestIn(t, exact(), newChain(), ok)
	first.Chain.Published = all.Chain.Published
	if out := Ingest(first); out.Chain.Published[1].Verify {
		t.Fatal("a second publish verified with verify = first")
	}
	// While the gate is not exact on this app version (the first verify
	// was inconclusive, or the app was updated), every publish is read
	// back before the next one (6.1 step 4).
	for name, m := range map[string]*Manifest{"unverified": mk(), "other app version": func() *Manifest {
		m := exact()
		m.Gate.AppVersion = "6.0.0"
		return m
	}()} {
		second := ingestIn(t, m, newChain(), ok)
		second.Chain.Published = all.Chain.Published
		if out := Ingest(second); !out.Chain.Published[1].Verify {
			t.Fatalf("%s: a second publish was not verified while the gate is not exact", name)
		}
	}
	// version_conflict -> version_moved; client_refused stops the chain.
	vc := stepRR(RoleUpdate, upd, online.StatusFailed, online.ReasonAppRefused, online.OutcomeError, errParsed{Reason: "version_conflict"})
	out = Ingest(ingestIn(t, mk(), newChain(), vc))
	if r := out.Chain.Report.Pairs; len(r) != 1 || r[0].Reason != "version_moved" {
		t.Fatalf("%+v", r)
	}
	cr := stepRR(RoleUpdate, upd, online.StatusFailed, online.ReasonAppRefused, online.OutcomeError, errParsed{Reason: "client_refused"})
	if out = Ingest(ingestIn(t, mk(), newChain(), cr)); out.Chain.NoPublish != "client_refused" {
		t.Fatal("client_refused did not stop publishing")
	}
	// Creates.
	m := mk()
	delete(m.Pairs[id].Copies, "home")
	cd := StepData{Pair: id, BlobSHA: shaY}
	out = Ingest(ingestIn(t, m, newChain(), stepRR(RoleCreate, cd, online.StatusDone, "", online.OutcomeOK, PublishParsed{Slug: slugD, Version: verA})))
	if c := out.Manifest.Pairs[id].Copies["home"]; c == nil || c.Origin != OriginCreated || c.Slug != slugD || c.Read != ReadOwner {
		t.Fatalf("%+v", c)
	}
	lost := stepRR(RoleCreate, cd, online.StatusFailed, online.ReasonNoResult, "", nil)
	lost.Step.Attempts = 1
	out = Ingest(ingestIn(t, m, newChain(), lost))
	if c := out.Manifest.Pairs[id].Copies["home"]; c == nil || c.State != StateCreatePending || c.CreateAttempt == nil {
		t.Fatalf("lost create: %+v", c)
	}
	refused := stepRR(RoleCreate, cd, online.StatusFailed, "user_refused", "", nil)
	out = Ingest(ingestIn(t, m, newChain(), refused))
	if out.Manifest.Pairs[id].Copies["home"] != nil {
		t.Fatal("a refused create without a marker made a copy")
	}
	// A result for a forgotten pair is ignored (m11).
	m = NewManifest()
	out = Ingest(ingestIn(t, m, newChain(), ok))
	if len(out.Manifest.Pairs) != 0 || len(out.Chain.Published) != 0 {
		t.Fatal("forgotten pair touched")
	}
}

func TestIngestOnceAndHistoryTrim(t *testing.T) {
	id := PairIDFor(slugA)
	m := NewManifest()
	p := basePair()
	for i := 0; i < 10; i++ {
		p.History = append(p.History, HistoryEntry{SHA256: shaY, Title: "old", SavedAt: t0, SourceProfile: "work", Reason: HistReplaced})
	}
	m.Pairs[id] = p
	cand := Candidate{Pair: id, Alias: "home", Verdict: WhyChangedHere, Version: verB, Role: RoleRead, Inspected: true}
	in := ingestIn(t, m, readChain(map[Slug]Candidate{slugA: cand}), chunkRR(slugA, verB, "next", "Notes", false))
	out := Ingest(in)
	if h := out.Manifest.Pairs[id].History; len(h) != 10 || h[9].SHA256 != shaX || h[9].Title != "Notes" {
		t.Fatalf("history %d %+v", len(h), h[len(h)-1])
	}
	// The same round again changes nothing (Ingested).
	in.Manifest, in.Chain = out.Manifest, out.Chain
	again := Ingest(in)
	if mustEncode(t, again.Manifest) != mustEncode(t, out.Manifest) {
		t.Fatal("second ingest changed the manifest")
	}
	_ = ids.Alias("")
	_ = strings.TrimSpace
}

func TestVerifyIngest(t *testing.T) {
	id := PairIDFor(slugA)
	content := "<!DOCTYPE html>\n<title>A</title>"
	mk := func() (*Manifest, *Chain) {
		m := NewManifest()
		p := basePair()
		p.Content.SHA256 = sha(content)
		p.Copies["home"].SyncedSHA, p.Copies["home"].SyncedVersion = sha(content), verB
		m.Pairs[id] = p
		c := readChain(nil)
		c.Phase = PhaseVerify
		c.Published = []Published{{Pair: id, Alias: "home", Slug: slugA, Version: verB, SHA: sha(content), Verify: true}}
		return m, c
	}
	run := func(back string, v VersionID) IngestOutput {
		m, c := mk()
		in := ingestIn(t, m, c, chunkRR(slugA, v, back, "A", true))
		in.Blob = func(string) (string, error) { return content, nil }
		return Ingest(in)
	}
	if out := run(content, verB); out.Manifest.Gate.Roundtrip != GateExact || out.Manifest.Pairs[id].Copies["home"].Roundtrip != ClassExact {
		t.Fatalf("exact: %+v", out.Manifest.Gate)
	}
	out := run(strings.Replace(content, "<title>A</title>", "<title>B</title>", 1), verB)
	if c := out.Manifest.Pairs[id].Copies["home"]; c.Roundtrip != ClassTitleOnly || c.ServedSHA == "" || out.Manifest.Gate.Roundtrip != GateExact {
		t.Fatalf("title only: %+v %+v", c, out.Manifest.Gate)
	}
	out = run("<untrusted-document-ffffffff>"+content+"</untrusted-document-ffffffff>", verB)
	if out.Manifest.Gate.Roundtrip != GateMismatch || out.Chain.NoPublish != "gate_closed" ||
		out.Manifest.Pairs[id].History[len(out.Manifest.Pairs[id].History)-1].Reason != HistReadback {
		t.Fatalf("wrapped: %+v", out.Manifest.Gate)
	}
	out = run(content, verA)
	if out.Manifest.Gate.Roundtrip != GateUnverified || !out.Chain.Published[0].Verified {
		t.Fatalf("inconclusive: %+v", out.Manifest.Gate)
	}
}
