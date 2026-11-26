package artifacts

import (
	"encoding/json"
	"math"
	"slices"
	"sort"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/online"
	"github.com/kazupc/paritydesk/internal/plan"
)

// RoundResult is one step of a completed round: its resolved state, its
// data and its recorded result (nil when none).
type RoundResult struct {
	Step   online.Step
	Data   StepData
	Result *online.Result
}

// IngestInput is everything Ingest reads.
type IngestInput struct {
	Spec      compat.ArtifactsSpec
	Config    config.Artifacts
	Limits    plan.Limits
	Policy    BoundaryPolicy
	Manifest  *Manifest
	Chain     *Chain
	Inventory *Inventory // the profile's inventory of this chain (nil before round 1)
	Partial   map[Slug]PartialState
	Results   []RoundResult
	Profile   ids.Alias
	Replica   model.Replica
	// AppVersion is the installed Claude's version (the gate's key).
	AppVersion string
	Now        time.Time
	// Blob reads archived content (verify).
	Blob func(sha string) (string, error)
}

// IngestOutput is what the service writes: the new manifest and chain,
// a replaced inventory, blobs, partial chunks and outbox files to remove.
type IngestOutput struct {
	Manifest    *Manifest
	Chain       *Chain
	Inventory   *Inventory
	Blobs       map[string]string
	PartialPut  []Chunk
	PartialDrop []Slug
	Unstage     []string
	Warnings    []string
}

// ing is one ingest's working state.
type ing struct {
	in    IngestInput
	m     *Manifest
	c     *Chain
	inv   *Inventory
	out   *IngestOutput
	parts map[Slug]PartialState
}

// Ingest applies a completed round to the archive (sections 7 and 8). It
// is pure: the caller has checked the replica rule (6.0) and writes the
// output. Ingesting the same round twice gives the same manifest.
func Ingest(in IngestInput) IngestOutput {
	g := &ing{in: in, m: in.Manifest.Clone(), c: cloneChain(in.Chain), inv: in.Inventory,
		out: &IngestOutput{Blobs: map[string]string{}}, parts: map[Slug]PartialState{}}
	for k, v := range in.Partial {
		g.parts[k] = PartialState{Version: v.Version, Chunks: append([]Chunk(nil), v.Chunks...)}
	}
	if g.c.Candidates == nil {
		g.c.Candidates = map[Slug]Candidate{}
	}
	if g.m.Gate.AppVersion != in.AppVersion {
		g.m.Gate = Gate{Roundtrip: GateUnverified, AppVersion: in.AppVersion, At: in.Now}
	}
	if g.c.Ingested >= g.c.Round {
		g.out.Manifest, g.out.Chain = in.Manifest.Clone(), cloneChain(in.Chain)
		return *g.out
	}
	switch g.c.Phase {
	case PhaseInventory:
		g.inventory()
	case PhaseInspect:
		g.inspect()
	case PhaseRead, PhaseVerify:
		g.reads()
	case PhasePublish:
		g.publish()
		if stickyRefusal(g.c.NoPublish) {
			g.m.PublishRefused = g.c.NoPublish
		}
	}
	g.c.Ingested = g.c.Round
	g.trimHistory()
	g.out.Manifest, g.out.Chain = g.m, g.c
	return *g.out
}

func cloneChain(c *Chain) *Chain {
	if c == nil {
		return &Chain{}
	}
	b, err := json.Marshal(c)
	if err != nil {
		panic(err)
	}
	var out Chain
	if err := json.Unmarshal(b, &out); err != nil {
		panic(err)
	}
	return &out
}

// report adds a pair result (bounded).
func (g *ing) report(pr PairResult) {
	r := &g.c.Report
	switch pr.Result {
	case "saved":
		r.Saved++
	case "published":
		r.Published++
	case "created":
		r.Published++
		r.Created++
	case "conflict":
		r.Conflicts++
	case "deleted":
		r.DeletedHere++
	case "skipped":
		r.Skipped++
	case "failed":
		r.Failed++
	case "ignored":
		r.Ignored++
	}
	r.Pairs = append(r.Pairs, pr)
	if len(r.Pairs) > MaxReportPairs {
		r.Pairs = r.Pairs[len(r.Pairs)-MaxReportPairs:]
	}
}

// ---------------------------------------------------------------- inventory

// inventory ingests the list pages (7.1) and plans the chain's work.
func (g *ing) inventory() {
	var pages []RoundResult
	for _, r := range g.in.Results {
		if r.Data.Role == RoleInvPage {
			pages = append(pages, r)
		}
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].Data.Page < pages[j].Data.Page })
	complete, capped, n := true, false, 0
	seen := map[Slug]bool{}
	var items []InvItem
	ended := false
	for i, p := range pages {
		if p.Step.Status != online.StatusDone || p.Result == nil || p.Result.Outcome != online.OutcomeOK {
			complete = false
			break
		}
		var lp ListPage
		if json.Unmarshal(p.Result.Parsed, &lp) != nil {
			complete = false
			break
		}
		n++
		if lp.Invalid > 0 || lp.CursorInvalid {
			complete = false
		}
		for _, it := range lp.Items {
			if !seen[it.Slug] {
				seen[it.Slug] = true
				items = append(items, it)
			}
		}
		if lp.NextCursor == nil {
			ended = !lp.CursorInvalid
			break
		}
		if i == len(pages)-1 {
			capped = true
		}
	}
	if !ended || capped {
		complete = false
	}
	alias := g.in.Profile
	// The foreign check: a slug recorded for another profile or replica.
	for _, it := range items {
		if id, a, ok := g.m.copyOwner(it.Slug); ok {
			if a != alias || g.m.Pairs[id].Copies[a].Replica != g.in.Replica {
				g.foreign()
				return
			}
		}
		if ig := g.m.Ignored[it.Slug]; ig != nil && ig.Alias != alias {
			g.foreign()
			return
		}
	}
	if items == nil {
		items = []InvItem{}
	}
	inv := &Inventory{V: 1, Profile: alias, Replica: g.in.Replica, Chain: g.c.ID, At: g.in.Now, Complete: complete,
		Pages: n, Capped: capped, Items: items}
	g.inv, g.out.Inventory = inv, inv
	pr := g.m.Profiles[alias]
	if pr == nil {
		pr = &ProfileRecord{}
		g.m.Profiles[alias] = pr
	}
	pr.Replica, pr.LastInventoryAt = g.in.Replica, g.in.Now
	for _, id := range sortedPairIDs(g.m.Pairs) {
		if c := g.m.Pairs[id].Copies[alias]; c != nil && c.State == StateLive && c.Replica == g.in.Replica {
			c.SeenAt = g.in.Now
		}
	}
	for s, ig := range g.m.Ignored {
		if ig.Alias != alias {
			continue
		}
		if it := inv.Item(s); it != nil && it.Version != ig.Version {
			delete(g.m.Ignored, s)
		}
	}
	g.planWork()
}

// foreign aborts an inventory that holds another profile's artifacts.
func (g *ing) foreign() {
	g.m = g.in.Manifest.Clone()
	g.c.State, g.c.StaleWhy = ChainStale, "inventory_foreign"
	g.out.Warnings = append(g.out.Warnings, i18n.En(i18n.ArtifactsInventoryForeign))
}

// planWork judges every pair and unclaimed item on this inventory and
// records the chain's read candidates and publish targets; tombstones
// and dropped imported copies are applied here.
func (g *ing) planWork() {
	alias, rep, inv := g.in.Profile, g.in.Replica, g.inv
	flags := ChainFlags{Claimed: g.m.claimed()}
	consumed := map[Slug]bool{}
	var tomb []PairID
	g.c.Candidates = map[Slug]Candidate{}
	for _, id := range sortedPairIDs(g.m.Pairs) {
		p := g.m.Pairs[id]
		v := Judge(p, id, alias, rep, inv, flags, g.in.Policy, g.in.Config)
		c := p.Copies[alias]
		switch v.Action {
		case ActRead:
			slug := v.Slug
			if slug == "" && c != nil {
				slug = c.Slug
			}
			if consumed[slug] {
				g.report(PairResult{Pair: id, Result: "skipped", Reason: WhyTitleTaken})
				continue
			}
			consumed[slug] = true
			g.c.Candidates[slug] = g.candidate(id, slug, v.Reason, RoleRead)
		case ActConflict:
			g.c.Candidates[c.Slug] = g.candidate(id, c.Slug, WhyConflictBothSides, RoleRead)
		case ActPublish:
			g.c.Candidates[c.Slug] = g.candidate(id, c.Slug, ActPublish, RoleTarget)
		case ActDeleted:
			if c != nil && c.State == StateLive {
				tomb = append(tomb, id)
			}
		case ActDrop:
			delete(p.Copies, alias)
			g.report(PairResult{Pair: id, Result: "skipped", Reason: WhyImportedNotFound})
		case ActSkipped, ActBlocked:
			g.report(PairResult{Pair: id, Result: "skipped", Reason: v.Reason})
		}
	}
	if len(tomb) > 0 {
		live := 0
		for _, p := range g.m.Pairs {
			if c := p.Copies[alias]; c != nil && c.State == StateLive {
				live++
			}
		}
		limit := max(g.in.Limits.MaxDeleteMin, int(math.Ceil(g.in.Limits.MaxDeleteFraction*float64(live))))
		if len(tomb) > limit {
			g.out.Warnings = append(g.out.Warnings, i18n.En(i18n.ArtifactsWarnInventorySuspect, string(alias)))
		} else {
			for _, id := range tomb {
				c := g.m.Pairs[id].Copies[alias]
				c.State, c.DeletedAt = StateDeleted, g.in.Now
				g.report(PairResult{Pair: id, Result: "deleted"})
			}
		}
	}
	if inv == nil {
		return
	}
	for _, it := range inv.Items {
		if !it.Mine || it.SoftDeleted || consumed[it.Slug] {
			continue
		}
		if _, claimed := flags.Claimed[it.Slug]; claimed {
			continue
		}
		if ig := g.m.Ignored[it.Slug]; ig != nil && ig.Version == it.Version {
			g.c.Report.Ignored++
			continue
		}
		if !g.in.Policy.exports(alias) {
			continue
		}
		g.c.Candidates[it.Slug] = g.candidate("", it.Slug, WhyNewHere, RoleRead)
	}
}

// candidate builds a candidate from the inventory item.
func (g *ing) candidate(id PairID, s Slug, verdict, role string) Candidate {
	cand := Candidate{Pair: id, Alias: g.in.Profile, Verdict: verdict, Role: role}
	if it := g.inv.Item(s); it != nil {
		cand.Version, cand.UpdatedAt, cand.TitleSHA = it.Version, it.UpdatedAt, it.TitleSHA
	}
	return cand
}

// ---------------------------------------------------------------- inspect

// stepFailed is the reason of a step that ended without a usable result
// ("" when it has one).
func stepFailed(r RoundResult) string {
	switch {
	case r.Step.Status == online.StatusSkipped:
		if r.Step.Reason == "" {
			return online.ReasonNotNeeded
		}
		return r.Step.Reason
	case r.Result == nil:
		if r.Step.Reason != "" {
			return r.Step.Reason
		}
		return online.ReasonNoResult
	case r.Result.Outcome == online.OutcomeUnrecognized:
		return online.ReasonUnrecognized
	case r.Result.Outcome == online.OutcomeError:
		var e errParsed
		if json.Unmarshal(r.Result.Parsed, &e) == nil && e.Reason != "" {
			return e.Reason
		}
		return online.ReasonAppRefused
	}
	return ""
}

// skipCand ends a candidate: a new item is remembered in ignored for the
// reasons that last until it changes; a pair gets its flag.
func (g *ing) skipCand(s Slug, cand *Candidate, reason string) {
	if cand.State != "" {
		return
	}
	cand.State, cand.Reason = CandSkipped, reason
	ignoredWhy := slices.Contains(ignoredWhys, reason)
	if cand.Pair == "" {
		if ignoredWhy && reason != "not_owned" {
			g.ignore(s, cand.Version, reason)
			g.report(PairResult{Slug: s, Result: "ignored", Reason: reason})
			return
		}
		g.report(PairResult{Slug: s, Result: "skipped", Reason: reason})
		return
	}
	if p := g.m.Pairs[cand.Pair]; p != nil {
		switch reason {
		case WhyMultiFile:
			p.Flags.MultiFile = true
		case WhyTypeMade:
			p.Flags.TypeMade = true
		case WhyLiveDoc:
			p.Flags.LiveDoc = true
		case WhyCoAuthored:
			p.Flags.CoAuthored = true
		}
	}
	g.report(PairResult{Pair: cand.Pair, Result: "skipped", Reason: reason})
}

// failCand ends a candidate for this chain only.
func (g *ing) failCand(s Slug, cand *Candidate, reason string) {
	if cand.State != "" {
		return
	}
	if cand.Pair == "" && (reason == online.ReasonAppRefused || reason == online.ReasonUnrecognized) {
		g.skipCand(s, cand, reason)
		return
	}
	cand.State, cand.Reason = CandFailed, reason
	g.report(PairResult{Pair: cand.Pair, Slug: slugIfNew(cand, s), Result: "failed", Reason: reason})
}

func slugIfNew(c *Candidate, s Slug) Slug {
	if c.Pair == "" {
		return s
	}
	return ""
}

// ignore records a slug that cannot be synced until it changes.
func (g *ing) ignore(s Slug, v VersionID, reason string) {
	if g.m.Ignored == nil {
		g.m.Ignored = map[Slug]*IgnoredItem{}
	}
	g.m.Ignored[s] = &IgnoredItem{Alias: g.in.Profile, Reason: reason, Version: v, At: g.in.Now}
	if len(g.m.Ignored) > MaxIgnored {
		var oldest Slug
		var at time.Time
		for k, it := range g.m.Ignored {
			if oldest == "" || it.At.Before(at) || (it.At.Equal(at) && k < oldest) {
				oldest, at = k, it.At
			}
		}
		delete(g.m.Ignored, oldest)
	}
}

// inspect ingests get, list_files and read_declaration (7.2).
func (g *ing) inspect() {
	touched := map[Slug]bool{}
	var order []Slug
	for _, r := range g.in.Results {
		s := r.Data.Slug
		cand, ok := g.c.Candidates[s]
		if !ok {
			continue
		}
		if !touched[s] {
			touched[s] = true
			order = append(order, s)
		}
		if why := stepFailed(r); why != "" {
			switch why {
			case WhyTypeMade, WhyLiveDoc:
				g.skipCand(s, &cand, why)
			default:
				g.failCand(s, &cand, why)
			}
			g.c.Candidates[s] = cand
			continue
		}
		switch r.Data.Role {
		case RoleGet:
			var gp GetParsed
			if json.Unmarshal(r.Result.Parsed, &gp) != nil {
				g.failCand(s, &cand, online.ReasonUnrecognized)
				break
			}
			cand.GetVersion, cand.Read, cand.Icon, cand.Title = gp.Version, gp.Read, gp.Icon, gp.Title
			if gp.TitleSHA != "" {
				cand.TitleSHA = gp.TitleSHA
			}
			if gp.Others {
				g.skipCand(s, &cand, WhyCoAuthored)
				break
			}
			if p := g.m.Pairs[cand.Pair]; p != nil && cand.Role == RoleTarget {
				if c := p.Copies[g.in.Profile]; c != nil && c.Slug == s {
					c.Read = gp.Read
				}
			}
			if cand.Role == RoleRead && cand.Version != "" && gp.Version != cand.Version {
				g.skipCand(s, &cand, "version_moved")
			}
		case RoleFiles:
			var fp FilesParsed
			if json.Unmarshal(r.Result.Parsed, &fp) != nil {
				g.failCand(s, &cand, online.ReasonUnrecognized)
				break
			}
			cand.EntryOnly = fp.EntryOnly
			if !fp.EntryOnly {
				g.skipCand(s, &cand, WhyMultiFile)
			}
		case RoleDecl:
			var dp DeclParsed
			if json.Unmarshal(r.Result.Parsed, &dp) != nil {
				g.failCand(s, &cand, online.ReasonUnrecognized)
				break
			}
			if dp.Others {
				g.skipCand(s, &cand, WhyCoAuthored)
				break
			}
			cand.Cap, cand.CapDigest = dp.State, dp.Digest
			if p := g.m.Pairs[cand.Pair]; p != nil {
				p.Flags.Capabilities.State, p.Flags.Capabilities.Digest = dp.State, dp.Digest
				p.Flags.Capabilities.Names = append([]string{}, dp.Names...)
				if p.Flags.Capabilities.Blocked() {
					g.skipCand(s, &cand, WhyCapabilities)
				}
			} else if dp.State != CapNone {
				g.skipCand(s, &cand, WhyCapabilities)
			}
		}
		g.c.Candidates[s] = cand
	}
	for _, s := range order {
		cand := g.c.Candidates[s]
		cand.Inspected = true
		if cand.State == "" && cand.Role == RoleTarget {
			if ok, why := g.targetEligible(s, cand); !ok {
				g.skipCand(s, &cand, why)
			}
		}
		g.c.Candidates[s] = cand
	}
}

// targetEligible checks the publish permission of a target (8.4).
func (g *ing) targetEligible(s Slug, cand Candidate) (bool, string) {
	p := g.m.Pairs[cand.Pair]
	if p == nil {
		return false, online.ReasonNotNeeded
	}
	// The data boundary (8.7) holds for every publish, also for a conflict
	// the archive side won: a profile that does not import, or a managed
	// and an unmanaged profile, never get each other's content.
	if p.Content != nil && !g.in.Policy.Boundary(p.Content.SourceProfile, g.in.Profile) {
		return false, WhyBoundary
	}
	c := p.Copies[g.in.Profile]
	switch {
	case c == nil || c.Slug != s:
		return false, online.ReasonNotNeeded
	case c.Origin != OriginCreated && c.Origin != OriginRecorded && c.Origin != OriginImported:
		return false, "not_owned"
	case c.State != StateLive:
		return false, online.ReasonNotNeeded
	}
	if it := g.inv.Item(s); it == nil || !it.Mine {
		return false, "not_owned"
	}
	want := c.SeenVersion
	if cand.Base != "" {
		want = cand.Base
	}
	if cand.GetVersion != want {
		return false, "version_moved"
	}
	switch cand.Read {
	case ReadOwner:
	case ReadUsers, ReadOrg:
		if !p.Flags.AllowShared {
			return false, "shared"
		}
	case ReadPublic:
		if !p.Flags.AllowPublic || !ValidTitle(cand.Title) {
			return false, "shared"
		}
	default:
		return false, "shared"
	}
	if !cand.EntryOnly {
		return false, WhyMultiFile
	}
	return true, ""
}

// ---------------------------------------------------------------- reads

// reads ingests read and verify chunks (7.3, 8.2, 7.6).
func (g *ing) reads() {
	for _, r := range g.in.Results {
		if r.Data.Role != RoleChunk {
			continue
		}
		if r.Data.Verify {
			g.verifyChunk(r)
			continue
		}
		s := r.Data.Slug
		cand, ok := g.c.Candidates[s]
		if !ok || cand.State != "" {
			continue
		}
		g.readChunk(r, s, &cand)
		g.c.Candidates[s] = cand
	}
	for s, ps := range g.parts {
		g.out.PartialPut = append(g.out.PartialPut, ps.Chunks...)
		_ = s
	}
	sort.Slice(g.out.PartialPut, func(i, j int) bool {
		a, b := g.out.PartialPut[i], g.out.PartialPut[j]
		if a.Slug != b.Slug {
			return a.Slug < b.Slug
		}
		return a.Offset < b.Offset
	})
}

// addPart records a chunk of s; it reports the chunks when the document
// is complete. A part of another version restarts the read once.
func (g *ing) addPart(s Slug, ch Chunk, cand *Candidate) ([]Chunk, bool) {
	ps := g.parts[s]
	if len(ps.Chunks) > 0 && ps.Version != ch.Version {
		delete(g.parts, s)
		g.out.PartialDrop = append(g.out.PartialDrop, s)
		cand.Restarts++
		ps = PartialState{}
		if ch.Offset != 1 {
			return nil, false
		}
	}
	if len(ps.Chunks) > 0 {
		last := ps.Chunks[len(ps.Chunks)-1]
		if last.NextOffset != ch.Offset {
			return nil, false
		}
	} else if ch.Offset != 1 {
		return nil, false
	}
	ps.Version = ch.Version
	ps.Chunks = append(ps.Chunks, ch)
	g.parts[s] = ps
	if ch.NextOffset != 0 {
		return nil, false
	}
	delete(g.parts, s)
	g.out.PartialDrop = append(g.out.PartialDrop, s)
	return ps.Chunks, true
}

// maxChunks is artifacts.max_chunks (64 when unset).
func (g *ing) maxChunks() int {
	if g.in.Spec.MaxChunks > 0 {
		return g.in.Spec.MaxChunks
	}
	return 64
}

func (g *ing) maxContent() int64 {
	if g.in.Spec.MaxContentBytes > 0 {
		return g.in.Spec.MaxContentBytes
	}
	return 16 << 20
}

func (g *ing) readChunk(r RoundResult, s Slug, cand *Candidate) {
	if why := stepFailed(r); why != "" {
		switch why {
		case "not_owned", WhyTypeMade, WhyLiveDoc, "too_large":
			g.skipCand(s, cand, why)
		default:
			g.failCand(s, cand, why)
		}
		g.dropParts(s)
		return
	}
	var ch Chunk
	if json.Unmarshal(r.Result.Parsed, &ch) != nil || ch.Slug != s {
		g.failCand(s, cand, online.ReasonUnrecognized)
		g.dropParts(s)
		return
	}
	switch {
	case ch.Others:
		g.skipCand(s, cand, WhyCoAuthored)
		g.dropParts(s)
		return
	case ch.FilesMulti:
		g.skipCand(s, cand, WhyMultiFile)
		g.dropParts(s)
		return
	}
	chunks, done := g.addPart(s, ch, cand)
	if cand.Restarts >= 2 {
		g.skipCand(s, cand, "version_moved")
		g.dropParts(s)
		return
	}
	if !done {
		if len(g.parts[s].Chunks) >= g.maxChunks() {
			g.skipCand(s, cand, "too_large")
			g.dropParts(s)
		}
		return
	}
	a, err := Assemble(chunks, g.maxContent())
	if err != nil {
		g.failCand(s, cand, online.ReasonUnrecognized)
		return
	}
	g.applyRead(s, cand, a)
}

// dropParts discards a slug's recorded parts.
func (g *ing) dropParts(s Slug) {
	if _, ok := g.parts[s]; ok {
		delete(g.parts, s)
	}
	if !slices.Contains(g.out.PartialDrop, s) {
		g.out.PartialDrop = append(g.out.PartialDrop, s)
	}
}

// changeTime is the read's change time: the inventory's, else the
// version's time prefix, else zero (unknown).
func (g *ing) changeTime(cand *Candidate, v VersionID) time.Time {
	if !cand.UpdatedAt.IsZero() {
		return cand.UpdatedAt.UTC()
	}
	return versionTime(v, g.in.Spec)
}

// titleOf is the read's title (the chunk summary's, else get's).
func titleOf(a Assembled, cand *Candidate) (string, string) {
	if a.TitleSHA != "" {
		return a.Title, a.TitleSHA
	}
	return cand.Title, cand.TitleSHA
}

// saveBlob queues a blob write.
func (g *ing) saveBlob(sha, content string) { g.out.Blobs[sha] = content }

// syncCopy marks a copy in step with the read version.
func (g *ing) syncCopy(c *Copy, v VersionID, sha, titleKey string) {
	c.SyncedVersion, c.SyncedSHA, c.SeenVersion, c.SeenTitleSHA, c.SeenAt = v, sha, v, titleKey, g.in.Now
}

// acceptHere makes the read the pair's content and title (8.2).
func (g *ing) acceptHere(p *Pair, cand *Candidate, a Assembled, histReason string) {
	t := g.changeTime(cand, a.Version)
	if p.Content != nil && p.Content.SHA256 != a.SHA {
		p.History = append(p.History, HistoryEntry{SHA256: p.Content.SHA256, Title: p.Title, SavedAt: p.Content.SavedAt,
			SourceProfile: p.Content.SourceProfile, SourceVersion: p.Content.SourceVersion, Reason: histReason})
	}
	icon := cand.Icon
	if !ValidIcon(icon) {
		icon = ""
	}
	p.Content = &Content{SHA256: a.SHA, Bytes: a.Bytes, Lines: a.Lines, SavedAt: g.in.Now, SourceProfile: g.in.Profile,
		SourceVersion: a.Version, ChangedAt: t, Icon: icon}
	g.saveBlob(a.SHA, a.Content)
	title, key := titleOf(a, cand)
	if key != p.TitleSHA {
		p.Title, p.TitleSHA, p.TitleChangedAt = title, key, t
	}
	if c := p.Copies[g.in.Profile]; c != nil {
		g.syncCopy(c, a.Version, a.SHA, key)
		c.State, c.ServedSHA = StateLive, ""
	}
}

// historyOf keeps a losing read in the pair's history.
func (g *ing) historyOf(p *Pair, cand *Candidate, a Assembled) {
	title, _ := titleOf(a, cand)
	g.saveBlob(a.SHA, a.Content)
	p.History = append(p.History, HistoryEntry{SHA256: a.SHA, Title: title, SavedAt: g.in.Now, SourceProfile: g.in.Profile,
		SourceVersion: a.Version, Reason: HistConflict})
}

// newPair records an artifact first seen on this profile.
func (g *ing) newPair(s Slug, cand *Candidate, a Assembled, origin string) {
	id := PairIDFor(s)
	if _, exists := g.m.Pairs[id]; exists {
		g.out.Warnings = append(g.out.Warnings, i18n.En(i18n.ArtifactsWarnPairCollision))
		g.report(PairResult{Slug: s, Result: "skipped", Reason: "collision"})
		return
	}
	title, key := titleOf(a, cand)
	t := g.changeTime(cand, a.Version)
	icon := cand.Icon
	if !ValidIcon(icon) {
		icon = ""
	}
	p := &Pair{Title: title, TitleSHA: key, TitleChangedAt: t, CreatedAt: g.in.Now,
		Content: &Content{SHA256: a.SHA, Bytes: a.Bytes, Lines: a.Lines, SavedAt: g.in.Now, SourceProfile: g.in.Profile,
			SourceVersion: a.Version, ChangedAt: t, Icon: icon},
		Copies: map[ids.Alias]*Copy{}, Flags: Flags{Capabilities: CapFlags{State: capState(cand), Digest: cand.CapDigest}}}
	c := &Copy{Replica: g.in.Replica, Slug: s, URL: g.urlOf(s), State: StateLive, Origin: origin, Read: cand.Read}
	g.syncCopy(c, a.Version, a.SHA, key)
	p.Copies[g.in.Profile] = c
	g.m.Pairs[id] = p
	g.saveBlob(a.SHA, a.Content)
	cand.Pair = id
	g.report(PairResult{Pair: id, Result: "saved"})
}

func capState(c *Candidate) string {
	if c.Cap == "" {
		return CapNone
	}
	return c.Cap
}

// urlOf is the inventory URL of a slug.
func (g *ing) urlOf(s Slug) string {
	if it := g.inv.Item(s); it != nil {
		return it.URL
	}
	return ""
}

// applyRead applies an assembled read to the archive (8.2, 8.3, 8.5).
func (g *ing) applyRead(s Slug, cand *Candidate, a Assembled) {
	cand.State = CandDone
	alias := g.in.Profile
	if cand.Pair == "" {
		g.newPair(s, cand, a, OriginRecorded)
		return
	}
	p := g.m.Pairs[cand.Pair]
	if p == nil {
		return // forgotten meanwhile
	}
	c := p.Copies[alias]
	_, key := titleOf(a, cand)
	switch cand.Verdict {
	case WhyChangedHere:
		if c == nil {
			return
		}
		if p.Content != nil && (a.SHA == p.Content.SHA256 || (c.ServedSHA != "" && a.SHA == c.ServedSHA)) {
			title, _ := titleOf(a, cand)
			if key != p.TitleSHA {
				p.Title, p.TitleSHA, p.TitleChangedAt = title, key, g.changeTime(cand, a.Version)
			}
			g.syncCopy(c, a.Version, p.Content.SHA256, key)
			g.report(PairResult{Pair: cand.Pair, Result: "saved", Reason: "title"})
			return
		}
		g.acceptHere(p, cand, a, HistReplaced)
		g.report(PairResult{Pair: cand.Pair, Result: "saved"})
	case WhyVerifyImport:
		if c == nil {
			return
		}
		c.State = StateLive
		if p.Content != nil && a.SHA == p.Content.SHA256 {
			g.syncCopy(c, a.Version, a.SHA, key)
			return
		}
		t := g.changeTime(cand, a.Version)
		// The live copy wins unless both times are known and the archive's
		// is strictly newer (8.2, M8): an unknown time never lets the
		// archive (possibly a wrapped legacy page) replace a live edit.
		if p.Content != nil && !p.Content.ChangedAt.IsZero() && !t.IsZero() && p.Content.ChangedAt.After(t) {
			g.historyOf(p, cand, a)
			g.syncCopy(c, a.Version, a.SHA, key)
			g.toTarget(s, cand, a.Version)
			g.report(PairResult{Pair: cand.Pair, Result: "conflict", Reason: "archive_newer"})
			return
		}
		g.acceptHere(p, cand, a, HistReplaced)
		g.report(PairResult{Pair: cand.Pair, Result: "saved"})
	case WhyAdoptCheck:
		g.adopt(s, cand, p, c, a)
	case WhyConflictBothSides:
		if c == nil {
			return
		}
		here := g.changeTime(cand, a.Version)
		if p.Content != nil && (a.SHA == p.Content.SHA256 || (c.ServedSHA != "" && a.SHA == c.ServedSHA)) {
			// Only the titles differ.
			if !here.IsZero() && (p.TitleChangedAt.IsZero() || here.After(p.TitleChangedAt)) {
				title, _ := titleOf(a, cand)
				p.Title, p.TitleSHA, p.TitleChangedAt = title, key, here
				g.syncCopy(c, a.Version, p.Content.SHA256, key)
				g.report(PairResult{Pair: cand.Pair, Result: "saved", Reason: "title"})
				return
			}
			g.syncCopy(c, a.Version, p.Content.SHA256, key)
			g.toTarget(s, cand, a.Version)
			g.report(PairResult{Pair: cand.Pair, Result: "conflict", Reason: "title"})
			return
		}
		other := time.Time{}
		if p.Content != nil {
			other = p.Content.ChangedAt
		}
		if !here.IsZero() && (other.IsZero() || here.After(other)) {
			g.acceptHere(p, cand, a, HistConflict)
			g.report(PairResult{Pair: cand.Pair, Result: "conflict", Reason: "here_newer"})
			return
		}
		g.historyOf(p, cand, a)
		g.syncCopy(c, a.Version, a.SHA, key)
		g.toTarget(s, cand, a.Version)
		g.report(PairResult{Pair: cand.Pair, Result: "conflict", Reason: "other_newer"})
	}
}

// toTarget makes a read candidate a publish target with base_version v.
func (g *ing) toTarget(s Slug, cand *Candidate, v VersionID) {
	cand.Role, cand.Verdict, cand.Base, cand.State = RoleTarget, ActPublish, v, ""
	if ok, why := g.targetEligible(s, *cand); !ok {
		g.skipCand(s, cand, why)
	}
}

// adopt resolves an adoption check (8.5).
func (g *ing) adopt(s Slug, cand *Candidate, p *Pair, c *Copy, a Assembled) {
	alias := g.in.Profile
	_, key := titleOf(a, cand)
	if c != nil && c.State == StateCreatePending {
		if c.CreateAttempt != nil && a.SHA == c.CreateAttempt.ContentSHA {
			c.Slug, c.URL, c.Origin, c.State, c.CreateAttempt = s, g.urlOf(s), OriginCreated, StateLive, nil
			g.syncCopy(c, a.Version, a.SHA, key)
			g.report(PairResult{Pair: cand.Pair, Result: "saved", Reason: "adopted"})
			return
		}
		cand.Pair = ""
		g.newPair(s, cand, a, OriginRecorded)
		return
	}
	if c == nil && p.Content != nil && a.SHA == p.Content.SHA256 {
		nc := &Copy{Replica: g.in.Replica, Slug: s, URL: g.urlOf(s), State: StateLive, Origin: OriginRecorded, Read: cand.Read}
		g.syncCopy(nc, a.Version, a.SHA, key)
		p.Copies[alias] = nc
		g.report(PairResult{Pair: cand.Pair, Result: "saved", Reason: "adopted"})
		return
	}
	// Another artifact with the same title: it becomes its own pair, and
	// this pair is not created here until `allow --create`.
	if c == nil && !slices.Contains(p.Flags.TitleTaken, alias) {
		p.Flags.TitleTaken = append(p.Flags.TitleTaken, alias)
	}
	cand.Pair = ""
	g.newPair(s, cand, a, OriginRecorded)
}

// verifyChunk ingests a read-back of a published copy (7.6).
func (g *ing) verifyChunk(r RoundResult) {
	s := r.Data.Slug
	idx := -1
	for i, p := range g.c.Published {
		if p.Slug == s && p.Verify && !p.Verified {
			idx = i
			break
		}
	}
	if idx < 0 {
		return
	}
	pub := &g.c.Published[idx]
	done := func() { pub.Verified = true; g.c.Verified++ }
	if stepFailed(r) != "" {
		g.dropParts(s)
		done()
		return
	}
	var ch Chunk
	if json.Unmarshal(r.Result.Parsed, &ch) != nil || ch.Slug != s {
		g.dropParts(s)
		done()
		return
	}
	if ch.Version != pub.Version {
		// Someone edited the copy in between: inconclusive.
		g.dropParts(s)
		done()
		return
	}
	var tmp Candidate
	chunks, complete := g.addPart(s, ch, &tmp)
	if !complete {
		if len(g.parts[s].Chunks) >= g.maxChunks() {
			g.dropParts(s)
			done()
		}
		return
	}
	done()
	a, err := Assemble(chunks, g.maxContent())
	if err != nil || g.in.Blob == nil {
		return
	}
	archived, err := g.in.Blob(pub.SHA)
	if err != nil {
		return
	}
	p := g.m.Pairs[pub.Pair]
	if p == nil {
		return
	}
	c := p.Copies[pub.Alias]
	if c == nil || c.Slug != s {
		return
	}
	switch class := Compare(archived, a.Content, KnobsFor(RoleChunk, g.in.Spec)); class {
	case ClassExact:
		c.Roundtrip = ClassExact
		g.m.Gate = Gate{Roundtrip: GateExact, AppVersion: g.in.AppVersion, At: g.in.Now}
	case ClassTitleOnly:
		c.Roundtrip, c.ServedSHA = ClassTitleOnly, a.SHA
		if g.m.Gate.Roundtrip == GateUnverified {
			g.m.Gate = Gate{Roundtrip: GateExact, AppVersion: g.in.AppVersion, At: g.in.Now}
		}
	default:
		c.Roundtrip = "mismatch:" + class
		g.m.Gate = Gate{Roundtrip: GateMismatch, Class: class, AppVersion: g.in.AppVersion, At: g.in.Now}
		g.saveBlob(a.SHA, a.Content)
		p.History = append(p.History, HistoryEntry{SHA256: a.SHA, SavedAt: g.in.Now, SourceProfile: pub.Alias,
			SourceVersion: a.Version, Reason: HistReadback})
		g.c.NoPublish = "gate_closed"
	}
}

// ---------------------------------------------------------------- publish

// publish ingests a publish round (7.5).
func (g *ing) publish() {
	alias := g.in.Profile
	for _, r := range g.in.Results {
		d := r.Data
		if d.BlobSHA != "" && !slices.Contains(g.out.Unstage, d.BlobSHA) {
			g.out.Unstage = append(g.out.Unstage, d.BlobSHA)
		}
		if d.Role == RoleGrant {
			switch why := stepFailed(r); why {
			case "":
				g.c.Granted = true
			case online.ReasonDecisionSkip:
			default:
				if g.c.NoPublish == "" {
					g.c.NoPublish = "grant_declined"
				}
			}
			continue
		}
		if !publishRoles[d.Role] {
			continue
		}
		if d.Pair != "" && !slices.Contains(g.c.Tried, d.Pair) {
			g.c.Tried = append(g.c.Tried, d.Pair)
		}
		p := g.m.Pairs[d.Pair]
		if p == nil || p.Content == nil {
			continue // forgotten meanwhile (m11)
		}
		create := d.Role == RoleCreate || d.Role == RoleCreateV
		why := stepFailed(r)
		if why == "" {
			var pp PublishParsed
			if json.Unmarshal(r.Result.Parsed, &pp) != nil {
				why = online.ReasonUnrecognized
			} else {
				g.published(p, d, pp, create)
				continue
			}
		}
		if create {
			marker := r.Step.Attempts > 0 || r.Step.EverUnknown
			if why == online.ReasonNoResult || r.Result != nil || (why == "user_refused" && marker) {
				p.Copies[alias] = &Copy{Replica: g.in.Replica, State: StateCreatePending,
					Origin: OriginCreated, CreateAttempt: &CreateAttempt{At: g.in.Now, TitleSHA: p.TitleSHA, ContentSHA: p.Content.SHA256}}
				g.report(PairResult{Pair: d.Pair, Result: "failed", Reason: WhyCreateUnconfirmed})
				continue
			}
			g.report(PairResult{Pair: d.Pair, Result: "skipped", Reason: why})
			continue
		}
		switch why {
		case "version_conflict":
			g.report(PairResult{Pair: d.Pair, Result: "skipped", Reason: "version_moved"})
		case "client_refused":
			g.c.NoPublish = "client_refused"
			g.report(PairResult{Pair: d.Pair, Result: "failed", Reason: why})
		case online.ReasonDecisionSkip, "grant_declined", online.ReasonNotNeeded:
			g.report(PairResult{Pair: d.Pair, Result: "skipped", Reason: why})
		default:
			if r.Result != nil && r.Result.Outcome == online.OutcomeError {
				why = online.ReasonAppRefused
			}
			g.report(PairResult{Pair: d.Pair, Result: "failed", Reason: why})
		}
		if cand, ok := g.c.Candidates[d.Slug]; ok && cand.State == "" {
			cand.State, cand.Reason = CandFailed, why
			g.c.Candidates[d.Slug] = cand
		}
	}
}

// stickyRefusal reports the refusals status keeps until the next
// successful publish (9.1).
func stickyRefusal(why string) bool { return why == "client_refused" || why == "grant_declined" }

// published records a successful publish.
func (g *ing) published(p *Pair, d StepData, pp PublishParsed, create bool) {
	alias := g.in.Profile
	g.m.PublishRefused = ""
	// The first publish of every chain is read back (6.1 step 5), every
	// one with verify = "all", and, while the round-trip gate is not exact
	// on this app version, every one too: such a round holds one publish
	// and the chain verifies it before the next (6.1 step 4).
	verify := len(g.c.Published) == 0 || g.in.Config.Verify == config.ArtifactsVerifyAll ||
		gateState(g.m, g.in.AppVersion) != GateExact
	if create {
		c := &Copy{Replica: g.in.Replica, Slug: pp.Slug, URL: pp.URL, State: StateLive, Origin: OriginCreated, Read: ReadOwner,
			SyncedVersion: pp.Version, SyncedSHA: p.Content.SHA256, SeenVersion: pp.Version, SeenTitleSHA: p.TitleSHA, SeenAt: g.in.Now}
		p.Copies[alias] = c
		p.Flags.Restore = slices.DeleteFunc(p.Flags.Restore, func(a ids.Alias) bool { return a == alias })
		p.Flags.AllowCreate = slices.DeleteFunc(p.Flags.AllowCreate, func(a ids.Alias) bool { return a == alias })
		g.report(PairResult{Pair: d.Pair, Result: "created"})
	} else {
		c := p.Copies[alias]
		if c == nil || c.Slug != pp.Slug {
			return
		}
		c.SyncedVersion, c.SyncedSHA, c.SeenVersion, c.State, c.ServedSHA, c.SeenAt = pp.Version, p.Content.SHA256, pp.Version, StateLive, "", g.in.Now
		if d.Title {
			c.SeenTitleSHA = p.TitleSHA
		}
		if pp.URL != "" {
			c.URL = pp.URL
		}
		g.report(PairResult{Pair: d.Pair, Result: "published"})
		if cand, ok := g.c.Candidates[pp.Slug]; ok {
			cand.State = CandDone
			g.c.Candidates[pp.Slug] = cand
		}
	}
	g.c.Published = append(g.c.Published, Published{Pair: d.Pair, Alias: alias, Slug: pp.Slug, Version: pp.Version,
		SHA: p.Content.SHA256, Verify: verify})
	g.listPublished(p, pp)
}

// listPublished records a publish in this chain's inventory: the
// artifact is the account's own at the published version, so status and
// due counts made from the inventory do not read it as deleted or
// changed here.
func (g *ing) listPublished(p *Pair, pp PublishParsed) {
	if g.inv == nil {
		return
	}
	inv := *g.inv
	inv.Items = append([]InvItem(nil), g.inv.Items...)
	it := InvItem{Slug: pp.Slug, Mine: true, Version: pp.Version, TitleSHA: p.TitleSHA, URL: pp.URL}
	found := false
	for i := range inv.Items {
		if inv.Items[i].Slug == pp.Slug {
			it.UpdatedAt, it.URL = inv.Items[i].UpdatedAt, firstNonEmpty(pp.URL, inv.Items[i].URL)
			inv.Items[i], found = it, true
		}
	}
	if !found {
		inv.Items = append(inv.Items, it)
	}
	g.inv, g.out.Inventory = &inv, &inv
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// trimHistory keeps at most history_keep entries per pair.
func (g *ing) trimHistory() {
	trimPairs(g.m.Pairs, g.in.Config.HistoryKeep)
}

// trimPairs drops the oldest history entries above keep (10 when keep
// is not set).
func trimPairs(pairs map[PairID]*Pair, keep int) {
	if keep <= 0 {
		keep = 10
	}
	for _, p := range pairs {
		if p != nil && len(p.History) > keep {
			p.History = p.History[len(p.History)-keep:]
		}
	}
}

// TrimHistory applies history_keep to a loaded manifest before it is
// validated, so lowering artifacts.history_keep in the config shortens
// the history at the next write instead of making the archive invalid.
func TrimHistory(m *Manifest, keep int) {
	if m == nil {
		return
	}
	trimPairs(m.Pairs, keep)
	trimPairs(m.Forgotten, keep)
}
