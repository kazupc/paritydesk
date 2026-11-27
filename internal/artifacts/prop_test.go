package artifacts

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/online"
)

// genContent is random page text: printable and non-ASCII runes, LF and
// CRLF, a final newline or not, and quoted wrapper tags.
func genContent(t *rapid.T) string {
	parts := rapid.SliceOfN(rapid.SampledFrom([]string{
		"<p>a</p>", "x", "", " ", "\r", "\u00e9", "\uac00", "<untrusted-document-00000000>", "</untrusted-document-00000000>",
		"<" + testTag + "-not>", "\t",
	}), 1, 30).Draw(t, "parts")
	var b strings.Builder
	for i, p := range parts {
		b.WriteString(p)
		if rapid.Bool().Draw(t, fmt.Sprintf("nl%d", i)) {
			b.WriteString("\n")
		}
	}
	s := b.String()
	if lineCount(s) == 0 {
		s = "x"
	}
	return s
}

// serverChunks splits content like a server: random cuts, a random form
// and a random block layout; it returns the texts of the parts.
func serverChunks(t *rapid.T, content string) ([]string, []ChunkStep) {
	lines := strings.Split(content, "\n")
	n := len(lines)
	form := rapid.SampledFrom([]string{"bare", "nl"}).Draw(t, "form")
	var texts []string
	var steps []ChunkStep
	off := 1
	for off <= n {
		size := rapid.IntRange(1, n-off+1).Draw(t, "size")
		next := off + size
		body := strings.Join(lines[off-1:next-1], "\n")
		if form == "nl" {
			body = "\n" + body + "\n"
		}
		nx := any(next)
		if next > n {
			nx = nil
		}
		sum := jsonText(map[string]any{"slug": slugA, "version": verA, "total_lines": n, "offset": off, "next_offset": nx,
			"notice": "wrapped in <" + testTag + ">"})
		html := "<" + testTag + ">" + body + "</" + testTag + ">"
		switch rapid.IntRange(0, 2).Draw(t, "layout") {
		case 0:
			texts = append(texts, sum+"\n"+html)
		case 1:
			texts = append(texts, sum+html)
		default:
			texts = append(texts, sum+"\n"+html+"\nReminder: data")
		}
		st := ChunkStep{Slug: slugA}
		if off > 1 {
			st.Offset = off
		}
		steps = append(steps, st)
		off = next
	}
	return texts, steps
}

// heavyRuns bounds the cases of the properties that run whole syncs.
const heavyRuns = 8

// Property 1: ParseChunk + Assemble return the original bytes or refuse.
func TestPropExactCapture(t *testing.T) {
	k := chunkKnobs(t)
	k.Reminders = []string{"Reminder:"}
	exact := 0
	defer func() {
		if exact < 10 {
			t.Errorf("only %d cases assembled: the property is vacuous", exact)
		}
	}()
	rapid.Check(t, func(rt *rapid.T) {
		content := genContent(rt)
		texts, steps := serverChunks(rt, content)
		var cs []Chunk
		for i, tx := range texts {
			c, err := ParseChunk(tx, steps[i], k)
			if err != nil {
				return // refused: never different bytes
			}
			cs = append(cs, c)
		}
		a, err := Assemble(cs, 0)
		if err != nil {
			return
		}
		if a.Content != content {
			rt.Fatalf("assembled %q, want %q", a.Content, content)
		}
		exact++
	})
}

// Property 2: under random loss of chunk results, the pair's content is
// absent or exactly the document.
func TestPropNoPartialSave(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		content := genContent(rt)
		lines := strings.Split(content, "\n")
		m := NewManifest()
		c := readChain(map[Slug]Candidate{slugA: {Alias: "home", Verdict: WhyNewHere, Version: verA, Role: RoleRead, Inspected: true}})
		partial := map[Slug]PartialState{}
		off := 1
		for round := 0; round < 20 && off <= len(lines); round++ {
			size := rapid.IntRange(1, len(lines)-off+1).Draw(rt, "size")
			next := off + size
			ch := Chunk{Slug: slugA, Version: verA, Offset: off, TotalLines: len(lines), Lines: size,
				Body: strings.Join(lines[off-1:next-1], "\n"), Form: "bare"}
			if next <= len(lines) {
				ch.NextOffset = next
			}
			lost := rapid.Bool().Draw(rt, "lost")
			rr := RoundResult{Step: online.Step{Status: online.StatusFailed, Reason: online.ReasonNoResult}, Data: StepData{Role: RoleChunk, Slug: slugA}}
			if !lost {
				b, _ := json.Marshal(ch)
				rr = RoundResult{Step: online.Step{Status: online.StatusDone}, Data: StepData{Role: RoleChunk, Slug: slugA},
					Result: &online.Result{Outcome: online.OutcomeOK, Parsed: b}}
			}
			c.Round++
			in := ingestIn(t, m, c, rr)
			in.Partial = partial
			out := Ingest(in)
			m, c = out.Manifest, out.Chain
			partial = map[Slug]PartialState{}
			for _, pc := range out.PartialPut {
				ps := partial[pc.Slug]
				ps.Version = pc.Version
				ps.Chunks = append(ps.Chunks, pc)
				partial[pc.Slug] = ps
			}
			for _, p := range m.Pairs {
				if p.Content.SHA256 != sha(content) || out.Blobs[p.Content.SHA256] != content {
					rt.Fatalf("partial content saved")
				}
			}
			if lost {
				break
			}
			off = next
		}
	})
}

// Property 3: newest wins. Random edits on two profiles, then each
// profile syncs twice: every live copy is in step and the content is the
// latest edit.
func TestPropNewestWins(t *testing.T) {
	runs := 0
	rapid.Check(t, func(rt *rapid.T) {
		// Each case runs several whole syncs: the first cases are enough.
		if runs++; runs > heavyRuns {
			return
		}
		h := newHarness(t)
		home, work := h.servers["home"], h.servers["work"]
		a := home.add("Shared page", page(2))
		h.sync("apply")
		h.switchTo("work")
		h.sync("apply")
		id := PairIDFor(a.slug)
		wslug := h.manifest().Pairs[id].Copies["work"].Slug
		latest := page(2)
		active := ids.Alias("work")
		n := rapid.IntRange(1, 4).Draw(rt, "edits")
		for i := 0; i < n; i++ {
			h.clk.Advance(time.Duration(rapid.IntRange(1, 120).Draw(rt, "gap")) * time.Minute)
			body := page(3 + i)
			if rapid.Bool().Draw(rt, "onHome") {
				home.edit(home.arts[a.slug], body)
			} else {
				work.edit(work.arts[wslug], body)
			}
			latest = body
			if rapid.Bool().Draw(rt, "syncNow") {
				h.sync("apply")
			}
			if rapid.Bool().Draw(rt, "switch") {
				if active == "work" {
					active = "home"
				} else {
					active = "work"
				}
				h.switchTo(active)
				h.sync("apply")
			}
		}
		for i := 0; i < 2; i++ {
			for _, al := range []ids.Alias{"home", "work"} {
				if h.root.Active != al {
					h.switchTo(al)
				}
				h.sync("apply")
			}
		}
		p := h.manifest().Pairs[id]
		if p.Content.SHA256 != sha(latest) {
			rt.Fatalf("content is not the latest edit")
		}
		for al, c := range p.Copies {
			if c.State == StateLive && c.SyncedSHA != p.Content.SHA256 {
				rt.Fatalf("%s not in step", al)
			}
		}
		if home.arts[a.slug].content != latest || work.arts[wslug].content != latest {
			rt.Fatal("an account does not have the latest edit")
		}
	})
}

// Property 4: a tombstoned copy gets a create only after allow
// --restore; Judge never answers create for it otherwise.
func TestPropNeverResurrects(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		p := basePair()
		p.Copies["home"].State = StateDeleted
		restore := rapid.Bool().Draw(rt, "restore")
		if restore {
			p.Flags.Restore = []ids.Alias{"home"}
		}
		complete := rapid.Bool().Draw(rt, "complete")
		var items []InvItem
		if rapid.Bool().Draw(rt, "listed") {
			items = append(items, item(slugA, VersionID(rapid.SampledFrom([]string{verA, verB}).Draw(rt, "v")), "Notes"))
		}
		v := Judge(p, PairIDFor(slugA), "home", repHome, inv(complete, items...), ChainFlags{}, BoundaryPolicy{}, config.Default().Artifacts)
		if (v.Action == ActCreate) != restore {
			rt.Fatalf("%+v (restore %v)", v, restore)
		}
		if v.Action == ActPublish {
			rt.Fatal("publish to a deleted copy")
		}
	})
}

// Properties 5 and 6: no publish step names a slug that is not a mine
// item of this inventory of this replica recorded for the alias, and
// content harvested from S never goes to T when Boundary(S -> T) is
// false.
func TestPropOwnershipAndBoundary(t *testing.T) {
	spec := testSpec(t)
	rapid.Check(t, func(rt *rapid.T) {
		m := NewManifest()
		m.Gate = Gate{Roundtrip: GateExact, AppVersion: appVer}
		id := PairIDFor(slugA)
		p := basePair()
		p.Content.SHA256 = sha(page(3))
		p.Content.SourceProfile = rapid.SampledFrom([]ids.Alias{"home", "work"}).Draw(rt, "src")
		if rapid.Bool().Draw(rt, "otherOrg") {
			p.Copies["home"].Replica = repWork
		}
		if rapid.Bool().Draw(rt, "noCopy") {
			delete(p.Copies, "home")
		}
		m.Pairs[id] = p
		pol := BoundaryPolicy{Exports: map[ids.Alias]bool{"work": rapid.Bool().Draw(rt, "exp"), "home": true},
			Imports: map[ids.Alias]bool{"home": rapid.Bool().Draw(rt, "imp")},
			Managed: map[ids.Alias]bool{"work": rapid.Bool().Draw(rt, "managed")}}
		var items []InvItem
		if rapid.Bool().Draw(rt, "listed") {
			items = append(items, item(slugA, verA, "Notes"))
		}
		if rapid.Bool().Draw(rt, "stranger") {
			items = append(items, item(slugC, verA, "Stranger"))
		}
		chain := &Chain{V: 1, ID: "art-20261126T100000Z-00000001", Profile: "home", Replica: repHome, Round: 1, Phase: PhaseInventory, State: ChainActive}
		lp, _ := json.Marshal(ListPage{Items: items})
		in := IngestInput{Spec: spec, Config: config.Default().Artifacts, Policy: pol, Manifest: m, Chain: chain, Profile: "home",
			Replica: repHome, AppVersion: appVer, Now: baseTime,
			Results: []RoundResult{{Step: online.Step{Status: online.StatusDone}, Data: StepData{Role: RoleInvPage},
				Result: &online.Result{Outcome: online.OutcomeOK, Parsed: lp}}}}
		out := Ingest(in)
		chain, m = out.Chain, out.Manifest
		// Inspect every target as the server would answer.
		var rs []RoundResult
		for s, c := range chain.Candidates {
			if c.Role != RoleTarget {
				continue
			}
			g, _ := json.Marshal(GetParsed{Slug: s, Version: verA, Read: ReadOwner})
			f, _ := json.Marshal(FilesParsed{Count: 1, EntryOnly: true})
			rs = append(rs, RoundResult{Step: online.Step{Status: online.StatusDone}, Data: StepData{Role: RoleGet, Slug: s},
				Result: &online.Result{Outcome: online.OutcomeOK, Parsed: g}},
				RoundResult{Step: online.Step{Status: online.StatusDone}, Data: StepData{Role: RoleFiles, Slug: s},
					Result: &online.Result{Outcome: online.OutcomeOK, Parsed: f}})
		}
		chain.Round, chain.Phase = 2, PhaseInspect
		in.Manifest, in.Chain, in.Inventory, in.Results = m, chain, out.Inventory, rs
		out = Ingest(in)
		chain, m = out.Chain, out.Manifest
		chain.Phase = PhaseRead
		for s, c := range chain.Candidates {
			if c.Role == RoleRead {
				c.State = CandDone
				chain.Candidates[s] = c
			}
		}
		ri := RoundInput{Spec: spec, Config: config.Default().Artifacts, Policy: pol, Manifest: m, Inventory: in.Inventory, Chain: chain,
			Profile: "home", Replica: repHome, Paths: ArchivePaths{Outbox: "/o"}, AppVersion: appVer}
		plan, _, _, err := PlanRound(ri)
		if err != nil {
			rt.Fatal(err)
		}
		for _, st := range plan.Steps {
			if !publishRoles[st.Role] || st.Role == RoleGrant {
				continue
			}
			pp := m.Pairs[st.Pair]
			if !pol.Boundary(pp.Content.SourceProfile, "home") {
				rt.Fatalf("published across the boundary")
			}
			if st.Role == RoleUpdate {
				it := in.Inventory.Item(st.Slug)
				c := pp.Copies["home"]
				if it == nil || !it.Mine || c == nil || c.Slug != st.Slug || c.Replica != repHome {
					rt.Fatalf("update to a slug that is not the profile's own copy")
				}
			}
		}
	})
}

// Property 7: any mix of results, missing results, refusals and
// unrecognized answers ends the chain within the bounds, and no lease
// outlives pause or done.
func TestPropTermination(t *testing.T) {
	runs := 0
	rapid.Check(t, func(rt *rapid.T) {
		if runs++; runs > 3*heavyRuns {
			return
		}
		h := newHarness(t)
		srv := h.servers["home"]
		for i := 0; i < rapid.IntRange(0, 4).Draw(rt, "arts"); i++ {
			srv.add(fmt.Sprintf("Page %d", i), page(1+i))
		}
		ctx := context.Background()
		token := ""
		ended := false
		for i := 0; i < 200 && !ended; i++ {
			out, err := h.svc.Next(ctx, NextArgs{Session: testSession, Token: token})
			if err != nil {
				rt.Fatal(err)
			}
			if out.Token != "" {
				token = out.Token
			}
			if len(out.Calls) > 3 {
				rt.Fatalf("%d calls in one answer", len(out.Calls))
			}
			switch out.State {
			case online.StateCalls:
				var refused []online.StepID
				for _, c := range out.Calls {
					switch rapid.IntRange(0, 5).Draw(rt, "fate") {
					case 0:
						refused = append(refused, c.ID)
					case 1:
						h.record(c, textResp("garbage"))
					case 2: // no result
					default:
						h.record(c, srv.call(c.Tool, c.Input))
					}
				}
				if len(refused) > 0 {
					if _, err := h.svc.Next(ctx, NextArgs{Session: testSession, Token: token, Refused: refused}); err != nil {
						rt.Fatal(err)
					}
				}
			case online.StateQuestion:
				_, _ = h.svc.Decide(ctx, online.Caller{Session: testSession, Token: token}, out.Question.ID, "apply")
			case online.StateWait:
				h.clk.Advance(2 * time.Minute)
				h.mono.Advance(int64(2 * time.Minute))
			default:
				ended = true
			}
		}
		if !ended {
			if _, err := h.svc.Pause(ctx, online.Caller{Session: testSession}); err != nil {
				rt.Fatal(err)
			}
		} else {
			_, _ = h.svc.Done(ctx, online.Caller{Session: testSession})
		}
		if l, _ := h.ol.Store.ReadLease(); l != nil {
			rt.Fatal("a lease outlived pause or done")
		}
		if c, _ := h.svc.Archive.LoadChain(); c != nil && c.Round > h.svc.Spec.MaxRounds {
			rt.Fatal("over max_rounds")
		}
	})
}

// Property 9: a read round holds at most read_per_round steps and stays
// within round_bytes unless it holds one step.
func TestPropBudget(t *testing.T) {
	spec := testSpec(t)
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 40).Draw(rt, "cands")
		c := &Chain{ID: "art-x", Replica: repHome, Round: 2, Ingested: 2, Phase: PhaseInspect, Candidates: map[Slug]Candidate{}}
		for i := 0; i < n; i++ {
			s := Slug(fmt.Sprintf("00000000-0000-4000-8000-%012d", 500+i))
			c.Candidates[s] = Candidate{Alias: "home", Verdict: WhyNewHere, Role: RoleRead, Inspected: true,
				Bytes: int64(rapid.IntRange(0, 4<<20).Draw(rt, "bytes"))}
		}
		in := RoundInput{Spec: spec, Manifest: NewManifest(), Chain: c, Profile: "home", Replica: repHome}
		in.Spec.ReadPerRound = rapid.IntRange(1, 20).Draw(rt, "per")
		plan, _, _, _ := PlanRound(in)
		if len(plan.Steps) > in.Spec.ReadPerRound {
			rt.Fatalf("%d steps", len(plan.Steps))
		}
		var sum int64
		for _, st := range plan.Steps {
			sum += estimate(in, c.Candidates[st.Slug], PartialState{})
		}
		if len(plan.Steps) > 1 && sum > spec.RoundBytes {
			rt.Fatalf("estimate %d over %d", sum, spec.RoundBytes)
		}
	})
}

// Property 8: ingesting the same round twice gives the same manifest.
func TestPropIdempotentIngest(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		content := genContent(rt)
		m := NewManifest()
		c := readChain(map[Slug]Candidate{slugA: {Alias: "home", Verdict: WhyNewHere, Version: verA, Role: RoleRead, Inspected: true}})
		in := ingestIn(t, m, c, chunkRR(slugA, verA, content, "T", false))
		a := Ingest(in)
		in.Manifest, in.Chain = a.Manifest, a.Chain
		b := Ingest(in)
		if mustEncode(t, a.Manifest) != mustEncode(t, b.Manifest) {
			rt.Fatal("not idempotent")
		}
	})
}
