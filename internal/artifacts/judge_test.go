package artifacts

import (
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/ids"
)

const (
	slugC = "00000000-0000-4000-8000-000000000012"
	slugD = "00000000-0000-4000-8000-000000000013"
	verB  = "1700000000-0000b"
)

var (
	shaX = strings.Repeat("1", 64)
	shaY = strings.Repeat("2", 64)
	t0   = time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC)
)

// basePair is a pair with content from work and an in-step copy on home.
func basePair() *Pair {
	k := TitleKey("Notes", "")
	return &Pair{Title: "Notes", TitleSHA: k, CreatedAt: t0,
		Content: &Content{SHA256: shaX, SourceProfile: "work", SourceVersion: verA, ChangedAt: t0, SavedAt: t0},
		Copies: map[ids.Alias]*Copy{
			"home": {Replica: repHome, Slug: slugA, State: StateLive, Origin: OriginRecorded, SyncedVersion: verA, SyncedSHA: shaX,
				SeenVersion: verA, SeenTitleSHA: k},
		}}
}

func inv(complete bool, items ...InvItem) *Inventory {
	return &Inventory{V: 1, Profile: "home", Replica: repHome, Complete: complete, Items: items}
}

func item(s Slug, v VersionID, title string) InvItem {
	return InvItem{Slug: s, Mine: true, Version: v, TitleSHA: TitleKey(title, "")}
}

func TestJudgeRows(t *testing.T) {
	pol := BoundaryPolicy{}
	cfg := config.Default().Artifacts
	listed := inv(true, item(slugA, verA, "Notes"))
	cases := []struct {
		name string
		edit func(p *Pair)
		inv  *Inventory
		pol  *BoundaryPolicy
		want Verdict
	}{
		{"1 skipped", func(p *Pair) { p.Flags.Skipped = "user" }, listed, nil, Verdict{Action: ActSkipped, Reason: WhyUser}},
		{"2 multi file", func(p *Pair) { p.Flags.MultiFile = true }, listed, nil, Verdict{Action: ActSkipped, Reason: WhyMultiFile}},
		{"2 co-authored", func(p *Pair) { p.Flags.CoAuthored = true }, listed, nil, Verdict{Action: ActSkipped, Reason: WhyCoAuthored}},
		{"3 capabilities", func(p *Pair) { p.Flags.Capabilities = CapFlags{State: CapDeclared, Digest: shaY} }, listed, nil, Verdict{Action: ActSkipped, Reason: WhyCapabilities}},
		{"3 capabilities allowed", func(p *Pair) {
			p.Flags.Capabilities = CapFlags{State: CapDeclared, Digest: shaY, AllowedDigest: shaY}
		}, listed, nil, Verdict{Action: ActNone}},
		{"3 capabilities unknown, changed here", func(p *Pair) { p.Flags.Capabilities = CapFlags{State: CapUnknown, Digest: shaY} },
			inv(true, item(slugA, verB, "Notes")), nil, Verdict{Action: ActSkipped, Reason: WhyCapabilities}},
		{"3 capabilities unknown without digest", func(p *Pair) { p.Flags.Capabilities = CapFlags{State: CapUnknown} },
			inv(true, item(slugA, verB, "Notes")), nil, Verdict{Action: ActSkipped, Reason: WhyCapabilities}},
		{"3 capabilities unknown allowed", func(p *Pair) {
			p.Flags.Capabilities = CapFlags{State: CapUnknown, Digest: shaY, AllowedDigest: shaY}
		}, listed, nil, Verdict{Action: ActNone}},
		{"4 other org", func(p *Pair) { p.Copies["home"].Replica = repWork }, listed, nil, Verdict{Action: ActSkipped, Reason: WhyOtherOrg}},
		{"5 no copy, no content", func(p *Pair) { delete(p.Copies, "home"); p.Content = nil }, listed, nil, Verdict{Action: ActNone}},
		{"6 boundary", func(p *Pair) { delete(p.Copies, "home") }, inv(true), &BoundaryPolicy{Exports: map[ids.Alias]bool{"work": false}}, Verdict{Action: ActBlocked, Reason: WhyBoundary}},
		{"7 title invalid", func(p *Pair) { delete(p.Copies, "home"); p.Title = "" }, inv(true), nil, Verdict{Action: ActBlocked, Reason: WhyTitleInvalid}},
		{"8 create", func(p *Pair) { delete(p.Copies, "home") }, inv(true), nil, Verdict{Action: ActCreate}},
		{"9 title taken (one)", func(p *Pair) { delete(p.Copies, "home") }, inv(true, item(slugC, verA, "Notes")), nil, Verdict{Action: ActRead, Reason: WhyAdoptCheck, Slug: slugC}},
		{"9 title taken (two)", func(p *Pair) { delete(p.Copies, "home") }, inv(true, item(slugC, verA, "Notes"), item(slugD, verA, "Notes")), nil, Verdict{Action: ActBlocked, Reason: WhyTitleTaken}},
		{"9 title taken earlier", func(p *Pair) {
			delete(p.Copies, "home")
			p.Flags.TitleTaken = []ids.Alias{"home"}
		}, inv(true), nil, Verdict{Action: ActBlocked, Reason: WhyTitleTaken}},
		{"8 allow create", func(p *Pair) {
			delete(p.Copies, "home")
			p.Flags.AllowCreate = []ids.Alias{"home"}
		}, inv(true, item(slugC, verA, "Notes"), item(slugD, verA, "Notes")), nil, Verdict{Action: ActCreate}},
		{"10 create pending, adoption", func(p *Pair) {
			p.Copies["home"] = &Copy{Replica: repHome, State: StateCreatePending, Origin: OriginCreated,
				CreateAttempt: &CreateAttempt{At: t0, TitleSHA: p.TitleSHA, ContentSHA: shaX}}
		}, inv(true, item(slugC, verA, "Notes")), nil, Verdict{Action: ActRead, Reason: WhyAdoptCheck, Slug: slugC}},
		{"10 create unconfirmed", func(p *Pair) {
			p.Copies["home"] = &Copy{Replica: repHome, State: StateCreatePending, Origin: OriginCreated,
				CreateAttempt: &CreateAttempt{At: t0, TitleSHA: p.TitleSHA, ContentSHA: shaX}}
		}, inv(true), nil, Verdict{Action: ActBlocked, Reason: WhyCreateUnconfirmed}},
		{"11 deleted", func(p *Pair) { p.Copies["home"].State = StateDeleted }, listed, nil, Verdict{Action: ActDeleted}},
		{"11 restore", func(p *Pair) { p.Copies["home"].State = StateDeleted; p.Flags.Restore = []ids.Alias{"home"} }, listed, nil, Verdict{Action: ActCreate}},
		{"12 imported, complete, missing", func(p *Pair) { p.Copies["home"].State = StateUnverified }, inv(true), nil, Verdict{Action: ActDrop, Reason: WhyImportedNotFound}},
		{"12 imported, incomplete, missing", func(p *Pair) { p.Copies["home"].State = StateUnverified }, inv(false), nil, Verdict{Action: ActNone, Reason: WhyNotListed}},
		{"13 imported listed", func(p *Pair) { p.Copies["home"].State = StateUnverified }, listed, nil, Verdict{Action: ActRead, Reason: WhyVerifyImport}},
		{"14 incomplete never deletes", nil, inv(false), nil, Verdict{Action: ActNone, Reason: WhyNotListed}},
		{"15 tombstone", nil, inv(true), nil, Verdict{Action: ActDeleted}},
		{"15 soft deleted", nil, inv(true, InvItem{Slug: slugA, Mine: true, Version: verA, SoftDeleted: true}), nil, Verdict{Action: ActDeleted}},
		{"16a in step", nil, listed, nil, Verdict{Action: ActNone}},
		{"16b changed here", nil, inv(true, item(slugA, verB, "Notes")), nil, Verdict{Action: ActRead, Reason: WhyChangedHere}},
		{"16b rename here, same version", nil, inv(true, item(slugA, verA, "Notes 2")), nil, Verdict{Action: ActRead, Reason: WhyChangedHere}},
		{"16b not exported", nil, inv(true, item(slugA, verB, "Notes")), &BoundaryPolicy{Exports: map[ids.Alias]bool{"home": false}}, Verdict{Action: ActNone}},
		{"16c behind", func(p *Pair) { p.Content.SHA256 = shaY }, listed, nil, Verdict{Action: ActPublish, Reason: WhyChangedElsewhere}},
		{"16c title behind", func(p *Pair) { p.Title, p.TitleSHA = "Notes 2", TitleKey("Notes 2", "") }, listed, nil, Verdict{Action: ActPublish, Reason: WhyChangedElsewhere}},
		{"16a title not NFC-stable is never behind", func(p *Pair) {
			p.Title = "Cafe\u0301" // decomposed: an update leaves the title out (8.8)
			p.TitleSHA = TitleKey(p.Title, "")
		}, listed, nil, Verdict{Action: ActNone}},
		{"16c boundary", func(p *Pair) { p.Content.SHA256 = shaY }, listed, &BoundaryPolicy{Imports: map[ids.Alias]bool{"home": false}}, Verdict{Action: ActBlocked, Reason: WhyBoundary}},
		{"16d conflict", func(p *Pair) { p.Content.SHA256 = shaY }, inv(true, item(slugA, verB, "Notes")), nil, Verdict{Action: ActConflict, Reason: WhyConflictBothSides}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := basePair()
			if tc.edit != nil {
				tc.edit(p)
			}
			pp := pol
			if tc.pol != nil {
				pp = *tc.pol
			}
			id := PairIDFor(slugA)
			m := &Manifest{Pairs: map[PairID]*Pair{id: p}}
			got := Judge(p, id, "home", repHome, tc.inv, ChainFlags{Claimed: m.claimed()}, pp, cfg)
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestBoundary(t *testing.T) {
	p := BoundaryPolicy{Managed: map[ids.Alias]bool{"work": true}}
	if p.Boundary("work", "home") || p.Boundary("home", "work") || !p.Boundary("work", "work") || !p.Boundary("home", "other") {
		t.Fatal("managed edge")
	}
	p.CrossManaged = true
	if !p.Boundary("work", "home") {
		t.Fatal("cross_managed")
	}
	cfg := config.Default()
	cfg.Profiles = []config.Profile{{Alias: "home"}, {Alias: "work", ManagedByOrg: true}}
	cfg.Sync.Export = map[string][]string{"home": {"sessions"}}
	pol := PolicyFrom(cfg)
	if pol.exports("home") || !pol.exports("work") || !pol.Managed["work"] {
		t.Fatalf("%+v", pol)
	}
}

// Content harvested on a managed profile stays behind the boundary after
// that profile is removed from the config (8.7): an alias the config no
// longer names is treated as not exporting and managed.
func TestRemovedManagedSourceStaysBlocked(t *testing.T) {
	cfg := config.Default()
	cfg.Profiles = []config.Profile{{Alias: "home"}, {Alias: "work", ManagedByOrg: true}}
	p := basePair()
	delete(p.Copies, "home")
	id := PairIDFor(slugA)
	m := &Manifest{Pairs: map[PairID]*Pair{id: p}}
	judge := func() Verdict {
		return Judge(p, id, "home", repHome, inv(true), ChainFlags{Claimed: m.claimed()}, PolicyFrom(cfg), cfg.Artifacts)
	}
	if v := judge(); v.Action != ActBlocked || v.Reason != WhyBoundary {
		t.Fatalf("managed source: %+v", v)
	}
	cfg.Profiles = cfg.Profiles[:1]
	if v := judge(); v.Action != ActBlocked || v.Reason != WhyBoundary {
		t.Fatalf("removed managed source: %+v", v)
	}
	p.Content.SourceProfile = "" // cleared by RemoveProfile
	if v := judge(); v.Action != ActBlocked || v.Reason != WhyBoundary {
		t.Fatalf("cleared source: %+v", v)
	}
	cfg.Profiles = append(cfg.Profiles, config.Profile{Alias: "work"}) // the alias reused, unmanaged
	if v := judge(); v.Action != ActBlocked {
		t.Fatalf("cleared source with the alias reused: %+v", v)
	}
}

func TestDue(t *testing.T) {
	cfg := config.Default().Artifacts
	m := NewManifest()
	behind := basePair()
	behind.Content.SHA256 = shaY
	m.Pairs[PairIDFor(slugA)] = behind
	newOne := basePair()
	delete(newOne.Copies, "home")
	newOne.Copies["work"] = &Copy{Replica: repWork, Slug: slugB, State: StateLive, Origin: OriginRecorded}
	m.Pairs[PairIDFor(slugB)] = newOne
	if n := Due(m, "home", repHome, inv(true, item(slugA, verA, "Notes")), BoundaryPolicy{}, cfg, appVer); n != 2 {
		t.Fatalf("due %d", n)
	}
	// Without an inventory: the copy behind and the create count.
	if n := Due(m, "home", repHome, nil, BoundaryPolicy{}, cfg, appVer); n != 2 {
		t.Fatalf("due without inventory %d", n)
	}
	m.Gate = Gate{Roundtrip: GateMismatch, AppVersion: appVer}
	if n := Due(m, "home", repHome, nil, BoundaryPolicy{}, cfg, appVer); n != 0 {
		t.Fatalf("due with the gate closed %d", n)
	}
	if n := Due(m, "home", repHome, nil, BoundaryPolicy{}, cfg, "8.0.0"); n != 2 {
		t.Fatalf("due after an app update %d", n)
	}
}
