package artifacts

import (
	"slices"
	"time"

	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
)

// Verdict actions.
const (
	ActNone     = "none"
	ActRead     = "read"
	ActPublish  = "publish"
	ActCreate   = "create"
	ActConflict = "conflict"
	ActDeleted  = "deleted"
	ActSkipped  = "skipped"
	ActBlocked  = "blocked"
	ActDrop     = "drop" // an unverified imported copy missing from a complete inventory
)

// Verdict reasons and read kinds.
const (
	WhyChangedHere       = "changed_here"
	WhyNewHere           = "new_here"
	WhyVerifyImport      = "verify_import"
	WhyAdoptCheck        = "adopt_check"
	WhyChangedElsewhere  = "changed_elsewhere"
	WhyNotListed         = "not_listed"
	WhyImportedNotFound  = "imported_copy_not_found"
	WhyOtherOrg          = "other_org"
	WhyBoundary          = "boundary"
	WhyTitleInvalid      = "title_invalid"
	WhyTitleTaken        = "title_taken"
	WhyCreateUnconfirmed = "create_unconfirmed"
	WhyUser              = "user"
	WhyCapabilities      = "capabilities"
	WhyMultiFile         = "multi_file"
	WhyTypeMade          = "type_made"
	WhyLiveDoc           = "live_doc"
	WhyCoAuthored        = "co_authored"
	WhyConflictBothSides = "conflict"
	adoptWindow          = 5 * time.Minute
)

// Verdict is Judge's answer for one pair and profile.
type Verdict struct {
	Action string
	Reason string
	// Slug is the item to read for an adoption check.
	Slug Slug
}

// PolicyFrom builds the data boundary from the config (8.7).
func PolicyFrom(cfg *config.Config) BoundaryPolicy {
	p := BoundaryPolicy{Exports: map[ids.Alias]bool{}, Imports: map[ids.Alias]bool{}, Managed: map[ids.Alias]bool{}, Strict: true}
	if cfg == nil {
		return p
	}
	p.CrossManaged = cfg.Artifacts.CrossManaged
	for _, pr := range cfg.Profiles {
		p.Exports[pr.Alias] = config.CategoryListed(cfg.Sync.Export, pr.Alias, model.CatArtifacts)
		p.Imports[pr.Alias] = config.CategoryListed(cfg.Sync.Import, pr.Alias, model.CatArtifacts)
		p.Managed[pr.Alias] = pr.ManagedByOrg
	}
	return p
}

// exports reports whether a profile's artifacts may be harvested. A
// profile the policy does not know exports (the config default), unless
// the policy is strict (a profile that is not configured any more).
func (p BoundaryPolicy) exports(a ids.Alias) bool {
	if v, ok := p.Exports[a]; ok {
		return v
	}
	return !p.Strict
}

func (p BoundaryPolicy) imports(a ids.Alias) bool {
	if v, ok := p.Imports[a]; ok {
		return v
	}
	return !p.Strict
}

// managed reports a profile's managed_by_org; an unknown alias of a
// strict policy counts as managed.
func (p BoundaryPolicy) managed(a ids.Alias) bool {
	if v, ok := p.Managed[a]; ok {
		return v
	}
	return p.Strict
}

// Boundary reports whether content harvested on s may be published on t.
func (p BoundaryPolicy) Boundary(s, t ids.Alias) bool {
	if !p.exports(s) || !p.imports(t) {
		return false
	}
	return s == t || (!p.managed(s) && !p.managed(t)) || p.CrossManaged
}

// titleHolders lists this inventory's unclaimed mine items with the
// pair's title key.
func titleHolders(p *Pair, inv *Inventory, flags ChainFlags) []Slug {
	if inv == nil || p.TitleSHA == "" {
		return nil
	}
	var unclaimed []Slug
	for _, it := range inv.Items {
		if !it.Mine || it.SoftDeleted || it.TitleSHA != p.TitleSHA {
			continue
		}
		if _, claimed := flags.Claimed[it.Slug]; !claimed {
			unclaimed = append(unclaimed, it.Slug)
		}
	}
	return unclaimed
}

// Judge decides what a sync on alias does with pair p (8.1). id is the
// pair's id; inv is the alias' inventory of this chain (nil: none yet).
func Judge(p *Pair, id PairID, alias ids.Alias, rep model.Replica, inv *Inventory, flags ChainFlags, pol BoundaryPolicy, cfg config.Artifacts) Verdict {
	c := p.Copies[alias]
	complete := inv != nil && inv.Complete
	f := p.Flags
	switch {
	case f.Skipped != "":
		return Verdict{Action: ActSkipped, Reason: WhyUser}
	case f.MultiFile:
		return Verdict{Action: ActSkipped, Reason: WhyMultiFile}
	case f.TypeMade:
		return Verdict{Action: ActSkipped, Reason: WhyTypeMade}
	case f.LiveDoc:
		return Verdict{Action: ActSkipped, Reason: WhyLiveDoc}
	case f.CoAuthored:
		return Verdict{Action: ActSkipped, Reason: WhyCoAuthored}
	case f.Capabilities.Blocked():
		return Verdict{Action: ActSkipped, Reason: WhyCapabilities}
	case c != nil && c.Replica != rep:
		return Verdict{Action: ActSkipped, Reason: WhyOtherOrg}
	}
	if c == nil {
		switch {
		case p.Content == nil:
			return Verdict{Action: ActNone}
		case !pol.Boundary(p.Content.SourceProfile, alias):
			return Verdict{Action: ActBlocked, Reason: WhyBoundary}
		case p.Title == "" || !ValidTitle(p.Title) || !NFCStable(p.Title):
			return Verdict{Action: ActBlocked, Reason: WhyTitleInvalid}
		case slices.Contains(f.AllowCreate, alias):
			return Verdict{Action: ActCreate}
		}
		if slices.Contains(f.TitleTaken, alias) {
			return Verdict{Action: ActBlocked, Reason: WhyTitleTaken}
		}
		switch unclaimed := titleHolders(p, inv, flags); len(unclaimed) {
		case 0:
			return Verdict{Action: ActCreate}
		case 1:
			return Verdict{Action: ActRead, Reason: WhyAdoptCheck, Slug: unclaimed[0]}
		}
		return Verdict{Action: ActBlocked, Reason: WhyTitleTaken}
	}
	if c.State == StateCreatePending {
		if s, ok := adoptionMatch(p, id, c, inv, flags); ok {
			return Verdict{Action: ActRead, Reason: WhyAdoptCheck, Slug: s}
		}
		return Verdict{Action: ActBlocked, Reason: WhyCreateUnconfirmed}
	}
	if c.State == StateDeleted && !slices.Contains(f.Restore, alias) {
		return Verdict{Action: ActDeleted}
	}
	if c.State == StateDeleted {
		// Restored: a new copy is created (the deleted slug is forgotten).
		if p.Content == nil || !pol.Boundary(p.Content.SourceProfile, alias) {
			return Verdict{Action: ActBlocked, Reason: WhyBoundary}
		}
		if p.Title == "" || !NFCStable(p.Title) {
			return Verdict{Action: ActBlocked, Reason: WhyTitleInvalid}
		}
		return Verdict{Action: ActCreate}
	}
	it := inv.Item(c.Slug)
	if c.State == StateUnverified {
		switch {
		case it == nil && complete:
			return Verdict{Action: ActDrop, Reason: WhyImportedNotFound}
		case it == nil:
			return Verdict{Action: ActNone, Reason: WhyNotListed}
		}
		return Verdict{Action: ActRead, Reason: WhyVerifyImport}
	}
	if !complete && it == nil {
		return Verdict{Action: ActNone, Reason: WhyNotListed}
	}
	if it == nil || it.SoftDeleted {
		return Verdict{Action: ActDeleted}
	}
	changed := it.Version != c.SyncedVersion || (it.TitleSHA != "" && c.SeenTitleSHA != "" && it.TitleSHA != c.SeenTitleSHA)
	// A title counts as behind only when a publish can carry it (8.8: valid
	// and NFC-stable); otherwise the update would leave it out, the copy
	// would stay "behind" and every sync would publish the page again.
	titleBehind := sendableTitle(p.Title) && p.TitleSHA != c.SeenTitleSHA
	behind := (p.Content != nil && c.SyncedSHA != p.Content.SHA256) || titleBehind
	switch {
	case !changed && !behind:
		return Verdict{Action: ActNone}
	case changed && !behind:
		if !pol.exports(alias) {
			return Verdict{Action: ActNone}
		}
		return Verdict{Action: ActRead, Reason: WhyChangedHere}
	case !changed && behind:
		if p.Content != nil && !pol.Boundary(p.Content.SourceProfile, alias) {
			return Verdict{Action: ActBlocked, Reason: WhyBoundary}
		}
		return Verdict{Action: ActPublish, Reason: WhyChangedElsewhere}
	}
	if !pol.exports(alias) {
		return Verdict{Action: ActBlocked, Reason: WhyBoundary}
	}
	return Verdict{Action: ActConflict, Reason: WhyConflictBothSides}
}

// sendableTitle reports whether a title may be passed in a publish input
// (8.8): valid and NFC-stable.
func sendableTitle(t string) bool {
	return t != "" && ValidTitle(t) && NFCStable(t)
}

// adoptionMatch finds the one unclaimed item a lost create may have made
// (8.5): the attempt's title key, changed no earlier than five minutes
// before the attempt (or at an unknown time).
func adoptionMatch(p *Pair, id PairID, c *Copy, inv *Inventory, flags ChainFlags) (Slug, bool) {
	ca := c.CreateAttempt
	if inv == nil || ca == nil || ca.TitleSHA == "" {
		return "", false
	}
	var found []Slug
	for _, it := range inv.Items {
		if !it.Mine || it.SoftDeleted || it.TitleSHA != ca.TitleSHA {
			continue
		}
		if _, claimed := flags.Claimed[it.Slug]; claimed {
			continue
		}
		if !it.UpdatedAt.IsZero() && it.UpdatedAt.Before(ca.At.Add(-adoptWindow)) {
			continue
		}
		found = append(found, it.Slug)
	}
	if len(found) != 1 {
		return "", false
	}
	return found[0], true
}

// Due is the number of pairs a sync on alias would publish (8.6): Judge
// on the alias' last inventory (or, without one, on copies alone, with
// "not listed" read as "in step") says publish or create, and the gate
// is not closed for appVersion ("" = any).
func Due(m *Manifest, alias ids.Alias, rep model.Replica, inv *Inventory, pol BoundaryPolicy, cfg config.Artifacts, appVersion string) int {
	if m == nil || (m.Gate.Roundtrip == GateMismatch && (appVersion == "" || m.Gate.AppVersion == appVersion)) {
		return 0
	}
	flags := ChainFlags{Claimed: m.claimed()}
	n := 0
	for _, id := range sortedPairIDs(m.Pairs) {
		p := m.Pairs[id]
		v := Judge(p, id, alias, rep, inv, flags, pol, cfg)
		if inv == nil && v.Action == ActNone && v.Reason == WhyNotListed {
			// Without an inventory a live copy behind the pair is due.
			if c := p.Copies[alias]; c != nil && c.State == StateLive && p.Content != nil && c.SyncedSHA != p.Content.SHA256 &&
				pol.Boundary(p.Content.SourceProfile, alias) {
				n++
			}
			continue
		}
		if v.Action == ActPublish || v.Action == ActCreate {
			n++
		}
	}
	return n
}
