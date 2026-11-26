package artifacts

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
)

// ---------------------------------------------------------------- status

// StatusOut is the JSON of `artifacts status` (9.1).
type StatusOut struct {
	V          int           `json:"-"`
	Enabled    bool          `json:"enabled"`
	Publishing string        `json:"publishing"`
	Gate       GateView      `json:"gate"`
	Chain      *ChainView    `json:"chain"`
	Profiles   []ProfileView `json:"profiles"`
	Counts     StatusCounts  `json:"counts"`
	Pairs      []PairView    `json:"pairs"`
	Ignored    []IgnoredView `json:"ignored"`
	Forgotten  []PairView    `json:"forgotten,omitempty"`
	Lines      []i18n.Text   `json:"lines"`
	// titles are the sanitized titles of the text output only.
	Titles map[PairID]string `json:"-"`
}

// GateView is the gate in status.
type GateView struct {
	Roundtrip  string `json:"roundtrip"`
	Class      string `json:"class"`
	AppVersion string `json:"app_version"`
}

// ChainView is the chain in status.
type ChainView struct {
	ID    string `json:"id"`
	Phase Phase  `json:"phase"`
	State string `json:"state"`
	Round int    `json:"round"`
}

// ProfileView is one profile in status.
type ProfileView struct {
	Alias             ids.Alias  `json:"alias"`
	LastSyncAt        *time.Time `json:"last_sync_at,omitempty"`
	LastInventoryAt   *time.Time `json:"last_inventory_at,omitempty"`
	InventoryComplete bool       `json:"inventory_complete"`
	Due               int        `json:"due"`
}

// StatusCounts are the counts of status.
type StatusCounts struct {
	Pairs       int `json:"pairs"`
	InStep      int `json:"in_step"`
	Due         int `json:"due"`
	ChangedHere int `json:"changed_here"`
	Conflicts   int `json:"conflicts"`
	Deleted     int `json:"deleted"`
	Skipped     int `json:"skipped"`
	Blocked     int `json:"blocked"`
	Ignored     int `json:"ignored"`
}

// PairView is one pair in status.
type PairView struct {
	Pair     PairID       `json:"pair"`
	TitleSHA string       `json:"title_sha"`
	Title    string       `json:"title,omitempty"`
	Content  *ContentView `json:"content"`
	Copies   []CopyView   `json:"copies"`
	Flags    []string     `json:"flags"`
}

// ContentView is a pair's content in status.
type ContentView struct {
	SHA256        string     `json:"sha256"`
	SourceProfile ids.Alias  `json:"source_profile"`
	ChangedAt     *time.Time `json:"changed_at,omitempty"`
}

// CopyView is one copy in status.
type CopyView struct {
	Profile   ids.Alias `json:"profile"`
	State     string    `json:"state"`
	Status    string    `json:"status"`
	Reason    string    `json:"reason"`
	URL       string    `json:"url,omitempty"`
	Roundtrip string    `json:"roundtrip,omitempty"`
}

// IgnoredView is one ignored slug in status.
type IgnoredView struct {
	Slug    Slug      `json:"slug"`
	Profile ids.Alias `json:"profile"`
	Reason  string    `json:"reason"`
}

// StatusInput are the flags of status.
type StatusInput struct {
	Profile ids.Alias
	All     bool
	Titles  bool
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

// publishingWord is the publish state of status and doctor.
func (s *Service) publishingWord(m *Manifest) string {
	switch {
	case !s.Spec.Enabled || s.Spec.PublishVia == compat.ArtifactsPublishOff || s.Spec.PublishVia == "":
		return "off"
	case gateState(m, s.AppVersion) == GateMismatch:
		return "gate_closed"
	}
	if c, err := s.Archive.LoadChain(); err == nil && c != nil && stickyRefusal(c.NoPublish) {
		return c.NoPublish
	}
	if m != nil && stickyRefusal(m.PublishRefused) {
		return m.PublishRefused // sticky until the next successful publish (9.1)
	}
	return "available"
}

// aliases are the profiles status shows: the config's and the
// manifest's.
func (s *Service) aliases(m *Manifest) []ids.Alias {
	set := map[ids.Alias]bool{}
	if s.Config != nil {
		for _, p := range s.Config.Profiles {
			set[p.Alias] = true
		}
	}
	for a := range m.Profiles {
		set[a] = true
	}
	for _, p := range m.Pairs {
		for a := range p.Copies {
			set[a] = true
		}
	}
	out := make([]ids.Alias, 0, len(set))
	for a := range set {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// currentReplicas are the profiles' current replicas from root.
func (s *Service) currentReplicas() map[ids.Alias]model.Replica {
	out := map[ids.Alias]model.Replica{}
	root, err := s.loadRoot()
	if err != nil || root == nil {
		return out
	}
	for a, ps := range root.Profiles {
		if ps != nil {
			out[a] = ps.Replica
		}
	}
	return out
}

// copyStatus is a copy's status word and reason from Judge on its
// profile's last inventory.
func copyStatus(v Verdict) (string, string) {
	switch v.Action {
	case ActNone:
		return "in_step", v.Reason
	case ActRead:
		return v.Reason, ""
	case ActPublish, ActCreate:
		return "due", v.Reason
	case ActConflict:
		return "conflict", ""
	case ActDeleted:
		return "deleted", ""
	case ActSkipped:
		if v.Reason == WhyOtherOrg {
			return WhyOtherOrg, ""
		}
		return "skipped", v.Reason
	case ActBlocked:
		return "blocked", v.Reason
	}
	return v.Action, v.Reason
}

// Status is `artifacts status` (no lock).
func (s *Service) Status(in StatusInput) (StatusOut, error) {
	out := StatusOut{V: 1, Enabled: s.enabled(), Profiles: []ProfileView{}, Pairs: []PairView{}, Ignored: []IgnoredView{},
		Lines: []i18n.Text{}, Titles: map[PairID]string{}}
	m, err := s.manifest()
	if err != nil {
		return out, err
	}
	out.Publishing = s.publishingWord(m)
	out.Gate = GateView{Roundtrip: m.Gate.Roundtrip, Class: m.Gate.Class, AppVersion: m.Gate.AppVersion}
	if c, err := s.Archive.LoadChain(); err == nil && c != nil {
		out.Chain = &ChainView{ID: c.ID, Phase: c.Phase, State: c.State, Round: c.Round}
	}
	reps := s.currentReplicas()
	invs := map[ids.Alias]*Inventory{}
	aliases := s.aliases(m)
	if in.Profile != "" {
		aliases = []ids.Alias{in.Profile}
	}
	pol, cfg := s.policy(), s.cfgArtifacts()
	for _, a := range aliases {
		inv, _ := s.Archive.LoadInventory(a)
		if inv != nil && reps[a] != (model.Replica{}) && inv.Replica != reps[a] {
			inv = nil
		}
		invs[a] = inv
		pv := ProfileView{Alias: a}
		if pr := m.Profiles[a]; pr != nil {
			pv.LastSyncAt, pv.LastInventoryAt = timePtr(pr.LastSyncAt), timePtr(pr.LastInventoryAt)
		}
		if inv != nil {
			pv.InventoryComplete = inv.Complete
		}
		rep := reps[a]
		if rep == (model.Replica{}) {
			if pr := m.Profiles[a]; pr != nil {
				rep = pr.Replica
			}
		}
		pv.Due = s.due(m, a, rep, inv)
		out.Counts.Due += pv.Due
		out.Profiles = append(out.Profiles, pv)
	}
	otherOrg := false
	flags := ChainFlags{Claimed: m.claimed()}
	view := func(id PairID, p *Pair, judge bool) PairView {
		pv := PairView{Pair: id, TitleSHA: p.TitleSHA, Copies: []CopyView{}, Flags: pairFlags(p)}
		if in.Titles {
			pv.Title = p.Title
		}
		if p.Content != nil {
			pv.Content = &ContentView{SHA256: p.Content.SHA256, SourceProfile: p.Content.SourceProfile, ChangedAt: timePtr(p.Content.ChangedAt)}
		}
		for _, a := range sortedAliases(p.Copies) {
			c := p.Copies[a]
			cv := CopyView{Profile: a, State: c.State, URL: c.URL, Roundtrip: c.Roundtrip}
			if judge {
				rep, known := reps[a]
				switch {
				case !known && len(reps) > 0:
					// The profile was removed: its copy is kept, orphaned.
					cv.Status = "orphaned"
					pv.Copies = append(pv.Copies, cv)
					continue
				case !known:
					rep = c.Replica
				}
				v := Judge(p, id, a, rep, invs[a], flags, pol, cfg)
				cv.Status, cv.Reason = copyStatus(v)
				if cv.Status == WhyOtherOrg {
					otherOrg = true
				}
			}
			pv.Copies = append(pv.Copies, cv)
		}
		return pv
	}
	out.Counts.Pairs = len(m.Pairs)
	for _, id := range sortedPairIDs(m.Pairs) {
		p := m.Pairs[id]
		pv := view(id, p, true)
		if in.Profile != "" {
			kept := pv.Copies[:0]
			for _, c := range pv.Copies {
				if c.Profile == in.Profile {
					kept = append(kept, c)
				}
			}
			pv.Copies = kept
		}
		inStep, skipped, blocked := len(pv.Copies) > 0, false, false
		for _, c := range pv.Copies {
			switch c.Status {
			case "in_step":
			case "changed_here":
				out.Counts.ChangedHere++
				inStep = false
			case "conflict":
				out.Counts.Conflicts++
				inStep = false
			case "deleted":
				out.Counts.Deleted++
				inStep = false
			case "skipped", WhyOtherOrg, "orphaned":
				skipped, inStep = true, false
			case "blocked":
				blocked, inStep = true, false
			default:
				inStep = false
			}
		}
		if inStep {
			out.Counts.InStep++
		}
		if skipped {
			out.Counts.Skipped++
		}
		if blocked {
			out.Counts.Blocked++
		}
		out.Titles[id] = p.Title
		out.Pairs = append(out.Pairs, pv)
	}
	out.Counts.Ignored = len(m.Ignored)
	slugs := make([]Slug, 0, len(m.Ignored))
	for sl := range m.Ignored {
		slugs = append(slugs, sl)
	}
	sort.Slice(slugs, func(i, j int) bool { return slugs[i] < slugs[j] })
	for _, sl := range slugs {
		it := m.Ignored[sl]
		if in.Profile != "" && it.Alias != in.Profile {
			continue
		}
		out.Ignored = append(out.Ignored, IgnoredView{Slug: sl, Profile: it.Alias, Reason: it.Reason})
	}
	if in.All {
		for _, id := range sortedPairIDs(m.Forgotten) {
			out.Forgotten = append(out.Forgotten, view(id, m.Forgotten[id], false))
		}
	}
	if !out.Enabled {
		out.Lines = append(out.Lines, i18n.Tx(s.offLine()))
	}
	if len(m.Pairs) == 0 {
		out.Lines = append(out.Lines, i18n.Tx(i18n.ArtifactsStatusNone))
	}
	switch out.Publishing {
	case "gate_closed":
		out.Lines = append(out.Lines, i18n.Tx(i18n.ArtifactsGateClosed))
	case "client_refused":
		out.Lines = append(out.Lines, i18n.Tx(i18n.ArtifactsPublishUnavailable))
	case "grant_declined":
		out.Lines = append(out.Lines, i18n.Tx(i18n.ArtifactsGrantDeclined))
	}
	if otherOrg {
		out.Lines = append(out.Lines, i18n.Tx(i18n.ArtifactsOtherOrgHint))
	}
	out.Lines = append(out.Lines, i18n.Tx(i18n.ArtifactsStatusSummary, out.Counts.Pairs, out.Counts.InStep, out.Counts.Due,
		out.Counts.Deleted, out.Counts.Skipped, out.Counts.Ignored))
	return out, nil
}

// pairFlags are a pair's flag words.
func pairFlags(p *Pair) []string {
	f := p.Flags
	out := []string{}
	add := func(on bool, w string) {
		if on {
			out = append(out, w)
		}
	}
	add(f.Skipped != "", "skipped")
	add(f.MultiFile, "multi_file")
	add(f.TypeMade, "type_made")
	add(f.LiveDoc, "live_doc")
	add(f.CoAuthored, "co_authored")
	add(f.Capabilities.State == CapDeclared || f.Capabilities.State == CapUnknown, "capabilities")
	add(f.AllowShared, "allow_shared")
	add(f.AllowPublic, "allow_public")
	return out
}

// ---------------------------------------------------------------- plan

// PlanOut is the JSON of `artifacts plan`.
type PlanOut struct {
	V           int          `json:"-"`
	Profile     ids.Alias    `json:"profile"`
	InventoryAt *time.Time   `json:"inventory_at,omitempty"`
	Approximate bool         `json:"approximate"`
	Read        []PairResult `json:"read"`
	Publish     []PairResult `json:"publish"`
	Create      []PairResult `json:"create"`
	Deleted     []PairResult `json:"deleted"`
	Skipped     []PairResult `json:"skipped"`
	NewHere     int          `json:"new_here"`
	Lines       []i18n.Text  `json:"lines"`
}

// Plan is `artifacts plan`: what a sync on alias would do, from its last
// inventory (it never contacts the app).
func (s *Service) Plan(alias ids.Alias) (PlanOut, error) {
	out := PlanOut{V: 1, Profile: alias, Approximate: true, Read: []PairResult{}, Publish: []PairResult{}, Create: []PairResult{},
		Deleted: []PairResult{}, Skipped: []PairResult{}, Lines: []i18n.Text{}}
	m, err := s.manifest()
	if err != nil {
		return out, err
	}
	rep := s.currentReplicas()[alias]
	inv, _ := s.Archive.LoadInventory(alias)
	if inv != nil && rep != (model.Replica{}) && inv.Replica != rep {
		inv = nil
	}
	if inv != nil {
		out.InventoryAt = timePtr(inv.At)
	}
	flags := ChainFlags{Claimed: m.claimed()}
	pol, cfg := s.policy(), s.cfgArtifacts()
	for _, id := range sortedPairIDs(m.Pairs) {
		v := Judge(m.Pairs[id], id, alias, rep, inv, flags, pol, cfg)
		pr := PairResult{Pair: id, Result: v.Action, Reason: v.Reason}
		switch v.Action {
		case ActRead, ActConflict:
			out.Read = append(out.Read, pr)
		case ActPublish:
			out.Publish = append(out.Publish, pr)
		case ActCreate:
			out.Create = append(out.Create, pr)
		case ActDeleted:
			out.Deleted = append(out.Deleted, pr)
		case ActSkipped, ActBlocked:
			out.Skipped = append(out.Skipped, pr)
		}
	}
	if inv != nil {
		for _, it := range inv.Items {
			if _, claimed := flags.Claimed[it.Slug]; !claimed && !it.SoftDeleted {
				if ig := m.Ignored[it.Slug]; ig == nil || ig.Version != it.Version {
					out.NewHere++
				}
			}
		}
	}
	n := len(out.Publish) + len(out.Create)
	if !s.publishes() {
		n = 0 // capture only: nothing can be published (5.8)
	}
	if gateState(m, s.AppVersion) == GateMismatch && s.AppVersion != "" {
		n = 0
		out.Lines = append(out.Lines, i18n.Tx(i18n.ArtifactsGateClosed))
	}
	if n > 0 {
		out.Lines = append(out.Lines, i18n.Tx(i18n.ArtifactsPlanDue, n, string(alias)))
	}
	return out, nil
}

// ---------------------------------------------------------------- decisions

// Confirm asks a question in Terminal; it returns nil when accepted, a
// Declined error when declined, and an error with
// artifacts.err_needs_yes when no answer is possible.
type Confirm func(question string) error

// AllowInput is `artifacts allow`.
type AllowInput struct {
	Ref     string // pair id, copy slug or ignored slug; "" for --roundtrip
	What    string // capabilities | shared | public | restore | create | resume | rehome | roundtrip
	Profile ids.Alias
}

// DecisionOut is the answer of allow, skip and forget.
type DecisionOut struct {
	V     int         `json:"-"`
	State string      `json:"state"`
	Pair  PairID      `json:"pair,omitempty"`
	Slug  string      `json:"slug,omitempty"`
	Lines []i18n.Text `json:"lines"`
}

// errUnknownPair is the usage error of an unknown <pair>.
func errUnknownPair() error {
	return pderr.NewT(pderr.Usage, "artifacts", i18n.ArtifactsErrUnknownPair)
}

// mutate runs f on the manifest under the artifacts lock and commits it.
func (s *Service) mutate(ctx context.Context, f func(m *Manifest) error) error {
	if s.ReadOnly {
		return pderr.NewT(pderr.Usage, "artifacts", i18n.ArtifactsErrReadOnly)
	}
	return s.withArchive(ctx, LockWait, func() error {
		m, err := s.manifest()
		if err != nil {
			return err
		}
		if err := f(m); err != nil {
			return err
		}
		return s.Archive.CommitManifest(m, s.now())
	})
}

// activeAlias is the profile flag or the active profile.
func (s *Service) activeAlias(a ids.Alias) ids.Alias {
	if a != "" {
		return a
	}
	if root, err := s.loadRoot(); err == nil && root != nil {
		return root.Active
	}
	return ""
}

// Allow is `artifacts allow` (9.2). The CLI refuses it inside Claude
// before it gets here (9.3).
func (s *Service) Allow(ctx context.Context, in AllowInput, confirm Confirm) (DecisionOut, error) {
	out := DecisionOut{V: 1, State: "allowed", Lines: []i18n.Text{i18n.Tx(i18n.ArtifactsAllowed)}}
	if in.What == "roundtrip" {
		if err := confirm(i18n.T(i18n.ArtifactsAskRoundtrip)); err != nil {
			return out, err
		}
		return out, s.mutate(ctx, func(m *Manifest) error {
			m.Gate = Gate{Roundtrip: GateUnverified, AppVersion: s.AppVersion, At: s.now()}
			return nil
		})
	}
	m, err := s.manifest()
	if err != nil {
		return out, err
	}
	if in.What == "resume" {
		if sl := Slug(strings.ToLower(in.Ref)); ValidSlug(string(sl)) && m.Ignored[sl] != nil {
			if err := confirm(i18n.T(i18n.ArtifactsAskResume)); err != nil {
				return out, err
			}
			out.Slug = ids.Mask(string(sl))
			return out, s.mutate(ctx, func(m *Manifest) error { delete(m.Ignored, sl); return nil })
		}
	}
	id, ok := findPair(m, in.Ref)
	if !ok {
		return out, errUnknownPair()
	}
	out.Pair = id
	p := m.Pairs[id]
	alias := s.activeAlias(in.Profile)
	var question string
	switch in.What {
	case "capabilities":
		question = i18n.T(i18n.ArtifactsAskCapabilities, strings.Join(p.Flags.Capabilities.Names, ", "))
	case "shared", "public":
		question = i18n.T(i18n.ArtifactsAskShared)
	case "restore":
		question = i18n.T(i18n.ArtifactsAskRestore, string(alias))
	case "create":
		question = i18n.T(i18n.ArtifactsAskCreate, string(alias))
	case "resume":
		question = i18n.T(i18n.ArtifactsAskResume)
	case "rehome":
		question = i18n.T(i18n.ArtifactsAskRehome, string(alias))
	default:
		return out, pderr.NewT(pderr.Usage, "artifacts allow", i18n.ErrArtifactsUnknownDecision)
	}
	if (in.What == "restore" || in.What == "create" || in.What == "rehome") && !validAlias(alias) {
		return out, pderr.NewT(pderr.Usage, "artifacts allow", i18n.ErrArtifactsProfileNamesProfile)
	}
	if in.What == "rehome" {
		c := p.Copies[alias]
		cur, known := s.currentReplicas()[alias]
		if c == nil || !known || c.Replica == cur {
			return out, pderr.NewT(pderr.Usage, "artifacts allow", i18n.ErrArtifactsCopyNotOtherOrg)
		}
		out.Slug = ids.Mask(string(c.Slug))
	}
	if err := confirm(question); err != nil {
		return out, err
	}
	return out, s.mutate(ctx, func(m *Manifest) error {
		p := m.Pairs[id]
		if p == nil {
			return errUnknownPair()
		}
		f := &p.Flags
		switch in.What {
		case "capabilities":
			f.Capabilities.AllowedDigest = f.Capabilities.Digest
		case "shared":
			f.AllowShared = true
		case "public":
			f.AllowShared, f.AllowPublic = true, true
		case "restore":
			if !slices.Contains(f.Restore, alias) {
				f.Restore = append(f.Restore, alias)
			}
		case "create":
			if !slices.Contains(f.AllowCreate, alias) {
				f.AllowCreate = append(f.AllowCreate, alias)
			}
			f.TitleTaken = slices.DeleteFunc(f.TitleTaken, func(a ids.Alias) bool { return a == alias })
			if c := p.Copies[alias]; c != nil && c.State == StateCreatePending {
				delete(p.Copies, alias)
			}
		case "resume":
			f.Skipped, f.CoAuthored = "", false
		case "rehome":
			delete(p.Copies, alias)
		}
		return nil
	})
}

// Skip is `artifacts skip`: the pair is kept but not synced.
func (s *Service) Skip(ctx context.Context, ref string) (DecisionOut, error) {
	out := DecisionOut{V: 1, State: "skipped", Lines: []i18n.Text{i18n.Tx(i18n.ArtifactsSkippedPair)}}
	m, err := s.manifest()
	if err != nil {
		return out, err
	}
	id, ok := findPair(m, ref)
	if !ok {
		return out, errUnknownPair()
	}
	out.Pair = id
	return out, s.mutate(ctx, func(m *Manifest) error {
		if p := m.Pairs[id]; p != nil {
			p.Flags.Skipped = "user"
		}
		return nil
	})
}

// Forget is `artifacts forget`: the pair moves to forgotten (content and
// history kept; the artifacts in the accounts are untouched).
func (s *Service) Forget(ctx context.Context, ref string, confirm Confirm) (DecisionOut, error) {
	out := DecisionOut{V: 1, State: "forgotten", Lines: []i18n.Text{i18n.Tx(i18n.ArtifactsForgotten)}}
	m, err := s.manifest()
	if err != nil {
		return out, err
	}
	id, ok := findPair(m, ref)
	if !ok {
		return out, errUnknownPair()
	}
	out.Pair = id
	if s.chainHasPair(id) {
		out.State, out.Lines = StateBlocked, []i18n.Text{i18n.Tx(i18n.ArtifactsForgetChainActive)}
		return out, nil
	}
	if err := confirm(i18n.T(i18n.ArtifactsAskForget)); err != nil {
		return out, err
	}
	return out, s.mutate(ctx, func(m *Manifest) error {
		p := m.Pairs[id]
		if p == nil {
			return errUnknownPair()
		}
		if m.Forgotten == nil {
			m.Forgotten = map[PairID]*Pair{}
		}
		m.Forgotten[id] = p
		delete(m.Pairs, id)
		return nil
	})
}

// ---------------------------------------------------------------- legacy import

// ImportLegacy is `artifacts import-legacy` (section 10). It is also
// called by `profile adopt --from-legacy`; the Terminal-only rule
// applies to the CLI leaf, not here.
func (s *Service) ImportLegacy(ctx context.Context, in ImportInput, confirm Confirm) (ImportReport, error) {
	rep := ImportReport{V: 1, Unmapped: []string{}, Lines: []i18n.Text{}}
	dir, err := openLegacyDir(in.From, s.SupportDir, s.DataRoot)
	if err != nil {
		return rep, pderr.NewT(pderr.Usage, "artifacts import-legacy", i18n.ArtifactsErrLegacyPath)
	}
	b, err := dir.Read("manifest.json", MaxLegacyManifest)
	if errors.Is(err, ErrTooLarge) || err != nil || b == nil {
		return rep, pderr.NewT(pderr.Usage, "artifacts import-legacy", i18n.ArtifactsErrLegacyFormat)
	}
	lm, err := DecodeLegacy(b)
	if err != nil {
		return rep, pderr.NewT(pderr.Usage, "artifacts import-legacy", i18n.ArtifactsErrLegacyFormat)
	}
	m, err := s.manifest()
	if err != nil {
		return rep, err
	}
	pl := planImport(lm, dir, m, s.currentReplicas(), in.Map, s.cfgArtifacts().HistoryKeep, s.Spec, s.now())
	rep = pl.report
	rep.LegacySHA256 = shaHex(b)
	rep.Lines = append(rep.Lines, i18n.Tx(i18n.ArtifactsImportSummary, rep.Pairs.Found, rep.Pairs.Importable, rep.Pairs.WithoutContent))
	if n := len(rep.Unmapped); n > 0 {
		rep.Lines = append(rep.Lines, i18n.Tx(i18n.ArtifactsImportMapHint, n))
	}
	if !in.Apply {
		return rep, nil
	}
	if s.chainOpen() {
		rep.State, rep.Lines = StateBlocked, []i18n.Text{i18n.Tx(i18n.ArtifactsImportChainActive)}
		return rep, nil
	}
	if confirm != nil {
		if err := confirm(i18n.T(i18n.ArtifactsAskImport)); err != nil {
			return rep, err
		}
	}
	if s.ReadOnly {
		return rep, pderr.NewT(pderr.Usage, "artifacts import-legacy", i18n.ArtifactsErrReadOnly)
	}
	err = s.withArchive(ctx, LockWait, func() error {
		m, err := s.manifest()
		if err != nil {
			return err
		}
		shas := make([]string, 0, len(pl.blobs))
		for sha := range pl.blobs {
			shas = append(shas, sha)
		}
		sort.Strings(shas)
		// Each file is read and hashed again; one that changed or went
		// away since the plan is not stored, and its pair is imported
		// without that content, as a mismatch is at planning (10.3).
		lost := map[string]bool{}
		for _, sha := range shas {
			b, err := dir.Read(pl.blobs[sha], s.Archive.maxContent())
			if err != nil || b == nil || shaHex(b) != sha || legacyWrapped(b) {
				lost[sha] = true
				continue
			}
			if err := s.Archive.PutBlob(sha, b); err != nil {
				return blobErr(err)
			}
		}
		n := 0
		claimed := m.claimed()
		for _, id := range sortedPairIDs(pl.pairs) {
			p := pl.pairs[id]
			if p.Content != nil && lost[p.Content.SHA256] {
				p.Content, p.History = nil, nil
			}
			p.History = slices.DeleteFunc(p.History, func(h HistoryEntry) bool { return lost[h.SHA256] })
			clash := m.Pairs[id] != nil
			for _, c := range p.Copies {
				if _, ok := claimed[c.Slug]; ok {
					clash = true
				}
			}
			if clash {
				continue
			}
			m.Pairs[id] = p
			n++
		}
		m.Imports = append(m.Imports, ImportRecord{LegacySHA256: rep.LegacySHA256, Format: lm.Format, At: s.now(), Pairs: n})
		if err := s.Archive.CommitManifest(m, s.now()); err != nil {
			return err
		}
		rep.Lines = append(rep.Lines, i18n.Tx(i18n.ArtifactsImportApplied, n))
		return nil
	})
	if err != nil {
		return rep, err
	}
	rep.Applied = true
	return rep, nil
}
