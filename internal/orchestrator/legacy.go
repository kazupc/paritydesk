package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kazupc/paritydesk/internal/app"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/denylist"
	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/gen"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/legacy"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/profile"
	"github.com/kazupc/paritydesk/internal/runner"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/state"
	"github.com/kazupc/paritydesk/internal/store/kv"
)

// LegacyOptions are the flags of profile adopt --from-legacy.
type LegacyOptions struct {
	Apply      bool
	Quarantine bool                 // the CLI guarantees a terminal when Apply
	HintPath   string               // absolute, "" for none
	Aliases    map[string]ids.Alias // folder name ("Claude" or "Claude-*") -> alias
	Exclude    []string             // folder names
	Managed    map[ids.Alias]bool   // every new alias when Apply; nil in a dry run
	PlanHash   string               // required when Apply
	NoSeed     bool
}

// LegacyResult is the report of a dry run or an apply.
type LegacyResult struct {
	DryRun        bool               `json:"dry_run"`
	Applied       bool               `json:"applied"`
	Status        string             `json:"status"` // ok|no_active|no_active_identity
	PlanHash      string             `json:"plan_hash"`
	ApplyCommand  string             `json:"apply_command,omitempty"` // dry run only
	Folders       []LegacyFolder     `json:"folders"`
	Seed          LegacySeed         `json:"seed"`
	Hint          *LegacyHint        `json:"hint,omitempty"`
	Generations   []LegacyGeneration `json:"generations,omitempty"`
	Moves         int                `json:"moves"` // eligible (dry run) or moved (apply)
	MovesDeferred int                `json:"moves_deferred"`
	MovesFailed   int                `json:"moves_failed"`
	Warnings      []string           `json:"warnings,omitempty"` // constant ids (":<validated name>" for some)
	// Quarantine is the --quarantine flag (text output only).
	Quarantine bool `json:"-"`
	// HintAlias is the alias derived for Claude when the hint was used
	// (text output only; the label itself is never kept).
	HintAlias ids.Alias `json:"-"`
}

// LegacyFolder is one folder of the report.
type LegacyFolder struct {
	Folder      string           `json:"folder"`
	Active      bool             `json:"active"`
	Alias       ids.Alias        `json:"alias,omitempty"`
	AliasSource string           `json:"alias_source,omitempty"` // flag|folder|hint|config
	Account     string           `json:"account,omitempty"`      // ids.Mask
	Org         string           `json:"org,omitempty"`          // ids.Mask; "" unknown
	Status      string           `json:"status"`                 // adopt|finish|registered|skip
	Reason      string           `json:"reason,omitempty"`
	ReasonArg   string           `json:"reason_arg,omitempty"` // a validated folder name or alias
	Leftovers   []LegacyLeftover `json:"leftovers,omitempty"`
}

// LegacyLeftover is one directory of another account inside a folder.
type LegacyLeftover struct {
	Path      string    `json:"path"` // UUID components masked
	Kind      string    `json:"kind"`
	Owner     ids.Alias `json:"owner,omitempty"`
	Files     int       `json:"files"`
	Bytes     int64     `json:"bytes"`
	Unique    int       `json:"unique"` // -1 when not checked
	Action    string    `json:"action"` // eligible|report_only|deferred|moved|failed
	Why       string    `json:"why,omitempty"`
	ReasonArg string    `json:"reason_arg,omitempty"` // short_id: the likely owner's alias
	// Generation is the backup generation the directory was moved into.
	Generation uint64 `json:"generation,omitempty"`
	// OwnerFolder is the folder holding the owner's account (text output).
	OwnerFolder string `json:"-"`
}

// LegacySeed reports the seeded sync bases.
type LegacySeed struct {
	Seeded bool            `json:"seeded"`
	Reason string          `json:"reason,omitempty"`
	Rows   []LegacySeedRow `json:"rows,omitempty"`
}

// LegacySeedRow is one row of the seed report.
type LegacySeedRow struct {
	Row       model.RowID `json:"row"`
	Agreed    int         `json:"agreed"`
	Differing int         `json:"differing"`
	Seeded    bool        `json:"seeded"`
	Reason    string      `json:"reason,omitempty"`
}

// LegacyHint reports the use of the state file; its labels never appear.
type LegacyHint struct {
	Used     bool     `json:"used"`
	Warnings []string `json:"warnings,omitempty"`
}

// LegacyGeneration is one pinned generation of moved leftovers.
type LegacyGeneration struct {
	N       uint64    `json:"n"`
	Alias   ids.Alias `json:"alias"`
	Folder  string    `json:"folder"`
	Planned int       `json:"planned"`
	Moved   int       `json:"moved"`
}

// Leftover actions.
const (
	ActionEligible   = "eligible"
	ActionReportOnly = "report_only"
	ActionDeferred   = "deferred"
	ActionMoved      = "moved"
	ActionFailed     = "failed"
)

// Seed reason ids (spec-legacy-adopt 3.7).
const (
	SeedNoSeedFlag       = "no_seed_flag"
	SeedNoManifest       = "no_manifest"
	SeedTooFew           = "too_few"
	SeedHasBases         = "has_bases"
	SeedHasCanonical     = "has_canonical"
	SeedRunOpen          = "run_open"
	SeedHintMidSwitch    = "hint_mid_switch"
	SeedDuplicateAccount = "duplicate_account"
	SeedProbeFailed      = "probe_failed"
	SeedOrgUnknown       = "org_unknown"
	SeedUnreadable       = "unreadable"
	SeedNoAdapter        = "no_adapter"
)

// Warning ids of LegacyResult.Warnings. The ones ending in ":" carry a
// validated alias or folder name.
const (
	LegacyWarnRunOpen          = "run_open"
	LegacyWarnPendingAdd       = "pending_add"
	LegacyWarnNoActive         = "no_active"
	LegacyWarnNoActiveIdentity = "no_active_identity"
	LegacyWarnForeign          = "foreign_tool:"
	LegacyWarnMarker           = "marker:"
	LegacyWarnFolderChanged    = "folder_changed:"
	LegacyWarnLeftovers        = "leftovers_unreadable:"
	// LegacyWarnPrintedDiffers: the printed command cannot read the state
	// file, so its plan differs from the dry run's (Claude's parked name).
	LegacyWarnPrintedDiffers = "printed_differs"
)

// legacyOp is the operation name of profile adopt --from-legacy errors.
const legacyOp = "profile adopt"

// legacyPlan is the planner's result with the facts apply needs.
type legacyPlan struct {
	p        *legacy.Plan
	hash     string
	folders  []profile.Folder
	manifest *compat.Manifest
	tested   bool
	bundle   app.BundleIdentity
	located  bool
	hintOrg  ids.OrgID
	// hintGiven is set when --legacy-state was read; hintWarn are its
	// parse warnings (hint_type, hint_label).
	hintGiven bool
	hintWarn  []legacy.HintWarning
	foreign   []string
	// res is the provisional resolution (current config and root plus
	// what this run registers); newProfiles and newStates are what A4 and
	// A5 would add.
	res         *profile.Resolution
	newProfiles []config.Profile
	newStates   map[ids.Alias]*state.ProfileState
	leftWarn    []string
	// orgs caches legacy.OwnOrgs per folder name; boundOrg is the org the
	// current config's resolution gives a bound folder.
	orgs     map[string]folderOrgs
	boundOrg map[string]ids.OrgID
	// leftIn is the leftover input of every folder walked, for the
	// recomputation before moves (5.2 Q0).
	leftIn map[string]legacy.LeftoverInput
}

// folderOrgs are a folder's own and stub orgs.
type folderOrgs struct{ own, stubs []ids.OrgID }

func (lp *legacyPlan) folder(name string) *profile.Folder {
	for i := range lp.folders {
		if lp.folders[i].Name == name {
			return &lp.folders[i]
		}
	}
	return nil
}

// AdoptLegacy is profile adopt --from-legacy: a dry run by default, or
// with Apply the registration, the seeded bases and, with Quarantine, the
// moves of leftovers (spec-legacy-adopt 3 and 5).
func (o *Orchestrator) AdoptLegacy(ctx context.Context, opts LegacyOptions) (*LegacyResult, error) {
	if !opts.Apply {
		return o.legacyDryRun(ctx, opts)
	}
	return o.legacyApply(ctx, opts)
}

// legacyDryRun plans without a lock and writes nothing.
func (o *Orchestrator) legacyDryRun(ctx context.Context, opts LegacyOptions) (*LegacyResult, error) {
	root, err := o.loadRoot()
	if err != nil {
		return nil, err
	}
	if err := o.checkBinding(root); err != nil {
		return nil, err
	}
	lp, err := o.legacyPlan(ctx, root, opts)
	if err != nil {
		return nil, err
	}
	res := o.legacyResult(lp, true)
	switch {
	case root.Run != nil:
		res.Warnings = append(res.Warnings, LegacyWarnRunOpen)
	case root.PendingAdd != nil:
		res.Warnings = append(res.Warnings, LegacyWarnPendingAdd)
	}
	if lp.p.Status != legacy.PlanOK {
		return res, nil
	}
	seed, _ := o.legacySeed(ctx, root, lp, opts, false)
	res.Seed = seed
	cmd, differs := o.legacyApplyCommand(ctx, root, lp, opts)
	res.ApplyCommand = cmd
	if differs {
		res.Warnings = append(res.Warnings, LegacyWarnPrintedDiffers)
	}
	return res, nil
}

// legacyApplyCommand returns the command a dry run prints for applying
// without a terminal (3.8). Without a state file it is the plan's own.
// With one, the command cannot name the file, so the plan is rebuilt from
// exactly the flags the command carries (the hint's alias as
// --legacy-alias, --legacy-no-seed when the hint refuses seeding) and the
// command carries that plan's hash: applying it can never take a decision
// the hash does not cover. differs reports that this plan is not the dry
// run's (for example Claude's parked name from the hint is not kept).
func (o *Orchestrator) legacyApplyCommand(ctx context.Context, root *state.Root, lp *legacyPlan, opts LegacyOptions) (cmd string, differs bool) {
	if !lp.hintGiven {
		return legacy.ApplyCommand(lp.p), false
	}
	po := LegacyOptions{Aliases: map[string]ids.Alias{}, Exclude: slices.Clone(opts.Exclude), NoSeed: !lp.p.SeedAllowed()}
	for k, v := range opts.Aliases {
		po.Aliases[k] = v
	}
	if _, ok := po.Aliases[ids.DefaultFolder]; !ok && lp.p.HintAlias != "" {
		po.Aliases[ids.DefaultFolder] = lp.p.HintAlias
	}
	lp2, err := o.legacyPlan(ctx, root, po)
	if err != nil || lp2.p.Status != legacy.PlanOK {
		return legacy.ApplyCommand(lp.p), true
	}
	main := *lp.p
	main.Quarantine = false
	return legacy.ApplyCommand(lp2.p), legacy.PlanHash(&main) != legacy.PlanHash(lp2.p)
}

// legacyApply is section 5: registration and seeding under the run lock,
// then the quarantine runs.
func (o *Orchestrator) legacyApply(ctx context.Context, opts LegacyOptions) (*LegacyResult, error) {
	s, err := o.lockedSession(ctx, legacyOp)
	if err != nil {
		return nil, err
	}
	defer s.close()
	root := s.root
	lp, err := o.legacyPlan(ctx, root, opts)
	if err != nil {
		return nil, err
	}
	switch lp.p.Status {
	case legacy.PlanNoActive:
		return nil, pderr.NewT(pderr.NeedsRecover, legacyOp, i18n.LegacyWarnNoActive)
	case legacy.PlanNoActiveIdentity:
		return nil, pderr.NewT(pderr.IdentityMismatch, legacyOp, i18n.LegacyWarnNoActiveIdentity)
	}
	if err := checkLegacyHash(opts.PlanHash, lp.hash); err != nil {
		return nil, err
	}
	if len(lp.foreign) > 0 {
		return nil, pderr.NewT(pderr.ForeignTool, legacyOp, i18n.ErrForeignTool, lp.foreign[0])
	}
	res := o.legacyResult(lp, false)
	runID, err := runner.NewRunID(o.now())
	if err != nil {
		return nil, err
	}
	// A3: seeding reads and the base objects (unreferenced until A5).
	seed, bases := o.legacySeed(ctx, root, lp, opts, true)
	res.Seed = seed
	if err := o.legacyRegister(ctx, root, lp, opts, bases, runID, res); err != nil {
		return res, err
	}
	if opts.Quarantine {
		if err := o.legacyQuarantine(ctx, root, lp, res); err != nil {
			return res, err
		}
	}
	res.Applied = true
	if res.MovesDeferred+res.MovesFailed > 0 || hasPrefixed(res.Warnings, LegacyWarnMarker) {
		return res, pderr.NewT(pderr.Degraded, legacyOp, i18n.ErrLegacyDegraded)
	}
	return res, nil
}

func hasPrefixed(list []string, prefix string) bool {
	for _, s := range list {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

// checkLegacyHash compares the dry run's plan hash with the one computed
// under the lock: a different folders half means the folders changed, a
// different inputs half means the flags differ.
func checkLegacyHash(given, now string) error {
	if given == "" {
		return pderr.NewT(pderr.Declined, legacyOp, i18n.ErrorLegacyPlanRequired)
	}
	switch legacy.CompareHash(given, now) {
	case legacy.HashFoldersDiffer:
		return pderr.NewT(pderr.Declined, legacyOp, i18n.ErrorLegacyPlanChanged)
	case legacy.HashInputsDiffer:
		return pderr.NewT(pderr.Declined, legacyOp, i18n.ErrorLegacyPlanFlags)
	}
	return nil
}

// legacyPlan gathers the facts and runs the pure planner (section 3).
func (o *Orchestrator) legacyPlan(ctx context.Context, root *state.Root, opts LegacyOptions) (*legacyPlan, error) {
	d := o.d
	folders, err := d.Folders.ScanLenient(ctx)
	if err != nil {
		return nil, pderr.Wrap(pderr.Internal, "scan", err)
	}
	lp := &legacyPlan{folders: folders, newStates: map[ids.Alias]*state.ProfileState{}, orgs: map[string]folderOrgs{},
		boundOrg: map[string]ids.OrgID{}, leftIn: map[string]legacy.LeftoverInput{}}
	for name := range opts.Aliases {
		if name != ids.DefaultFolder && lp.folder(name) == nil {
			return nil, pderr.NewT(pderr.Usage, legacyOp, i18n.ErrLegacyAliasNotFolder, name)
		}
	}
	for _, name := range opts.Exclude {
		if name != ids.DefaultFolder && lp.folder(name) == nil {
			return nil, pderr.NewT(pderr.Usage, legacyOp, i18n.ErrLegacyExcludeNotFolder, name)
		}
	}
	var hint *legacy.Hint
	if opts.HintPath != "" {
		h, warn, err := legacy.ReadHint(opts.HintPath)
		if err != nil {
			return nil, pderr.NewT(pderr.Usage, legacyOp, i18n.ErrLegacyState, err.Error())
		}
		hint, lp.hintWarn, lp.hintGiven = h, warn, true
	}
	if b, err := d.App.Locate(ctx, d.Config.App.BundleID); err == nil {
		lp.bundle, lp.located = b, true
		if m, tested, err := d.Compat.Manifest(ctx, b.Version); err == nil && m != nil {
			lp.manifest, lp.tested = m, tested
		}
	}
	claude := lp.folder(ids.DefaultFolder)
	if m := lp.manifest; m != nil && claude != nil && claude.Identity != nil {
		if org, found, err := d.App.ActiveOrg(ctx, m.Signals, claude.Identity.Account, appStart(ctx, d.App, lp.bundle.Path)); err == nil && found {
			lp.hintOrg = org
		}
	}
	foreignFolders := map[string]bool{}
	if m := lp.manifest; m != nil {
		lp.foreign = o.foreignTools(m.Footprints)
		for _, fp := range m.Footprints {
			for _, g := range fp.FolderGlobs {
				for _, f := range folders {
					if ok, _ := filepath.Match(g, f.Name); ok {
						foreignFolders[f.Name] = true
					}
				}
			}
		}
	}
	// Existing registrations: the resolution of the current config.
	res0, err := d.Resolve(d.Config, root, folders, profile.Hints{ActiveOrg: lp.hintOrg})
	if err != nil {
		return nil, err
	}
	bound := map[string]ids.Alias{}
	for a, b := range res0.Bindings {
		if b.Folder != nil {
			bound[b.Folder.Name] = a
			lp.boundOrg[b.Folder.Name] = b.Replica.Org
		}
	}
	in := legacy.PlanInput{Hint: hint, Aliases: opts.Aliases, Exclude: opts.Exclude,
		Quarantine: opts.Quarantine, NoSeed: opts.NoSeed}
	if lp.manifest != nil {
		in.Reserved = lp.manifest.ReservedFolders
	}
	for _, p := range d.Config.Profiles {
		in.Existing = append(in.Existing, legacy.ExistingProfile{Alias: p.Alias, Parked: p.ParkedFolder()})
	}
	for _, f := range folders {
		ff := legacy.FolderFacts{Name: f.Name, ScanErr: f.ScanErr != "", IdentityErr: f.IdentityErr,
			Dev: f.ID.Dev, Ino: f.ID.Ino, Birth: f.ID.Birth, Foreign: foreignFolders[f.Name]}
		if f.Identity != nil {
			ff.Account = f.Identity.Account
			ff.ScanOrg = lp.candidateOrg(&f)
		}
		if a := bound[f.Name]; a != "" {
			ff.Bound, ff.BoundHasState = a, root.Profiles[a] != nil
		}
		in.Folders = append(in.Folders, ff)
	}
	lp.p = legacy.Build(in)
	if err := o.legacyProvisional(root, lp, opts); err != nil {
		return nil, err
	}
	if lp.p.Status == legacy.PlanOK {
		o.legacyLeftovers(lp)
	}
	lp.hash = legacy.PlanHash(lp.p)
	return lp, nil
}

// stubRules returns the manifest's stub rules (none without a manifest).
func (lp *legacyPlan) stubRules() legacy.StubRules {
	if lp.manifest == nil {
		return legacy.StubRules{}
	}
	return legacy.StubRulesFrom(lp.manifest.Legacy)
}

// ownOrgs returns a folder's own and stub orgs (claude-code-sessions),
// read once per plan.
func (lp *legacyPlan) ownOrgs(f *profile.Folder) (own, stubs []ids.OrgID) {
	if f == nil || f.Identity == nil || f.ScanErr != "" {
		return nil, nil
	}
	if c, ok := lp.orgs[f.Name]; ok {
		return slices.Clone(c.own), slices.Clone(c.stubs)
	}
	own, stubs, err := legacy.OwnOrgs(f.Path, sources.SessionsRoot, f.Identity.Account, lp.stubRules())
	if err != nil {
		own, stubs = nil, nil
	}
	lp.orgs[f.Name] = folderOrgs{own: own, stubs: stubs}
	return slices.Clone(own), slices.Clone(stubs)
}

// candidateOrg is step 1 of 3.5.
func (lp *legacyPlan) candidateOrg(f *profile.Folder) ids.OrgID {
	own, stubs := lp.ownOrgs(f)
	if f.Name == ids.DefaultFolder && lp.hintOrg != "" && (slices.Contains(own, lp.hintOrg) || slices.Contains(stubs, lp.hintOrg)) {
		return lp.hintOrg
	}
	if len(own) == 1 {
		return own[0]
	}
	return ""
}

// legacyProvisional builds the provisional config and root (what A4 and A5
// would write) and resolves them: the resolution is the authority for
// every profile's replica (3.5, step 2).
func (o *Orchestrator) legacyProvisional(root *state.Root, lp *legacyPlan, opts LegacyOptions) error {
	d := o.d
	cfg := *d.Config
	cfg.Profiles = slices.Clone(d.Config.Profiles)
	prov := *root
	prov.Profiles = make(map[ids.Alias]*state.ProfileState, len(root.Profiles)+4)
	for a, ps := range root.Profiles {
		prov.Profiles[a] = ps
	}
	for _, fp := range lp.p.Adopted() {
		f := lp.folder(fp.Name)
		if f == nil || f.Identity == nil {
			continue
		}
		if fp.Status == legacy.StatusAdopt {
			p := config.Profile{Alias: fp.Alias, Folder: fp.ConfigFolder, ManagedByOrg: opts.Managed[fp.Alias]}
			lp.newProfiles = append(lp.newProfiles, p)
			cfg.Profiles = append(cfg.Profiles, p)
		}
		rep := model.Replica{Account: f.Identity.Account, Org: lp.candidateOrg(f)}
		prov.Profiles[fp.Alias] = newProfileStateFor(fp.Alias, f, rep)
		if fp.Active {
			prov.Active = fp.Alias
		}
	}
	if len(lp.p.Adopted()) == 0 && len(cfg.Profiles) == 0 {
		lp.res = &profile.Resolution{Bindings: map[ids.Alias]*profile.Binding{}}
		return nil
	}
	res, err := d.Resolve(&cfg, &prov, lp.folders, profile.Hints{ActiveOrg: lp.hintOrg})
	if err != nil {
		return err
	}
	lp.res = res
	for i := range lp.p.Folders {
		fp := &lp.p.Folders[i]
		b := res.Bindings[fp.Alias]
		if fp.Alias == "" || b == nil {
			continue
		}
		fp.Org = b.Replica.Org
		if fp.Status == legacy.StatusAdopt || fp.Status == legacy.StatusFinish {
			ps := prov.Profiles[fp.Alias]
			ps.Replica = b.Replica
			lp.newStates[fp.Alias] = ps
		}
	}
	return nil
}

// legacyLeftovers fills the leftovers of every folder with an identity
// (3.6), whatever its status: the folders half of the plan hash covers
// them all, so a flag that leaves a folder out changes only the inputs
// half. Only those of folders that are not skipped are reported or moved.
func (o *Orchestrator) legacyLeftovers(lp *legacyPlan) {
	m := lp.manifest
	if m == nil || len(m.Legacy.ScopedDirs) == 0 {
		return
	}
	stub := lp.stubRules()
	plan := map[string]*legacy.FolderPlan{}
	aliasOf := map[string]ids.Alias{}
	for i := range lp.p.Folders {
		fp := &lp.p.Folders[i]
		plan[fp.Name] = fp
		if fp.Status != legacy.StatusSkip {
			aliasOf[fp.Name] = fp.Alias
		}
	}
	identified := func(f *profile.Folder) bool { return f.Identity != nil && f.ScanErr == "" && plan[f.Name] != nil }
	// A folder's own orgs: its own session directories, its candidate org,
	// the org a binding of the current config records, and the resolved
	// org.
	own := map[string][]ids.OrgID{}
	known := map[string]ids.Alias{}
	for i := range lp.folders {
		f := &lp.folders[i]
		if !identified(f) {
			continue
		}
		o, _ := lp.ownOrgs(f)
		fp := plan[f.Name]
		for _, x := range []ids.OrgID{fp.ScanOrg, lp.boundOrg[f.Name], fp.Org} {
			if x != "" && !slices.Contains(o, x) {
				o = append(o, x)
			}
		}
		own[f.Name] = o
		if a := aliasOf[f.Name]; a != "" {
			known[string(f.Identity.Account)] = a
			for _, x := range o {
				known[string(x)] = a
			}
		}
	}
	// rank orders the folders holding one account (or org) as its home:
	// Claude first (it always wins a duplicate), then a folder this run
	// keeps, then the others; ties keep scan order.
	rank := func(name string) int {
		switch {
		case name == ids.DefaultFolder:
			return 0
		case aliasOf[name] != "":
			return 1
		}
		return 2
	}
	for i := range lp.folders {
		f := &lp.folders[i]
		if !identified(f) {
			continue
		}
		fp := plan[f.Name]
		orgKnown := fp.Org != ""
		if fp.Status == legacy.StatusSkip {
			orgKnown = fp.ScanOrg != ""
		}
		in := legacy.LeftoverInput{Folder: f.Path, Account: f.Identity.Account, OwnOrgs: slices.Clone(own[f.Name]),
			OrgKnown: orgKnown, Others: map[ids.AccountID]legacy.OtherFolder{}, OtherOrgs: map[ids.OrgID]legacy.OtherFolder{},
			KnownIDs: known, Dirs: m.Legacy.ScopedDirs, Stub: stub, Protected: m.LegacyProtected}
		if f.Name == ids.DefaultFolder && lp.hintOrg != "" && !slices.Contains(in.OwnOrgs, lp.hintOrg) {
			_, stubs := lp.ownOrgs(f)
			if slices.Contains(stubs, lp.hintOrg) {
				in.OwnOrgs = append(in.OwnOrgs, lp.hintOrg)
			}
		}
		for j := range lp.folders {
			g := &lp.folders[j]
			if g.Name == f.Name || !identified(g) || g.Identity.Account == f.Identity.Account {
				continue
			}
			of := legacy.OtherFolder{Name: g.Name, Path: g.Path, Owner: aliasOf[g.Name]}
			if cur, ok := in.Others[g.Identity.Account]; !ok || rank(g.Name) < rank(cur.Name) {
				in.Others[g.Identity.Account] = of
			}
			for _, x := range own[g.Name] {
				if cur, ok := in.OtherOrgs[x]; !ok || rank(g.Name) < rank(cur.Name) {
					in.OtherOrgs[x] = of
				}
			}
		}
		lp.leftIn[f.Name] = in
		left, err := legacy.FindLeftovers(in)
		if err != nil {
			if fp.Status != legacy.StatusSkip {
				lp.leftWarn = append(lp.leftWarn, LegacyWarnLeftovers+f.Name)
			}
			continue
		}
		fp.Leftovers = left
	}
}

// legacyResult renders the plan as the report.
func (o *Orchestrator) legacyResult(lp *legacyPlan, dry bool) *LegacyResult {
	res := &LegacyResult{DryRun: dry, Status: lp.p.Status, PlanHash: lp.hash, Folders: []LegacyFolder{}, Quarantine: lp.p.Quarantine}
	switch lp.p.Status {
	case legacy.PlanNoActive:
		res.Warnings = append(res.Warnings, LegacyWarnNoActive)
	case legacy.PlanNoActiveIdentity:
		res.Warnings = append(res.Warnings, LegacyWarnNoActiveIdentity)
	}
	for _, t := range lp.foreign {
		res.Warnings = append(res.Warnings, LegacyWarnForeign+t)
	}
	res.Warnings = append(res.Warnings, lp.leftWarn...)
	if lp.hintGiven {
		h := &LegacyHint{Used: lp.p.HintUsed}
		for _, w := range append(append([]legacy.HintWarning(nil), lp.hintWarn...), lp.p.HintWarnings...) {
			h.Warnings = append(h.Warnings, string(w))
		}
		res.Hint = h
	}
	for _, fp := range lp.p.Folders {
		if fp.AliasSource == legacy.SourceHint {
			res.HintAlias = fp.Alias
		}
		lf := LegacyFolder{Folder: fp.Name, Active: fp.Active, Alias: fp.Alias, AliasSource: fp.AliasSource,
			Status: string(fp.Status), Reason: fp.Reason, ReasonArg: fp.ReasonArg}
		if fp.Account != "" {
			lf.Account = ids.Mask(string(fp.Account))
		}
		if fp.Org != "" {
			lf.Org = ids.Mask(string(fp.Org))
		}
		for _, l := range fp.Leftovers {
			if fp.Status == legacy.StatusSkip {
				break // a folder left out reports and moves nothing
			}
			ll := LegacyLeftover{Path: maskPath(l.Rel), Kind: string(l.Kind), Owner: l.Owner, Files: l.Files, Bytes: l.Bytes,
				Unique: l.Unique, Why: l.Why, ReasonArg: string(l.Possibly), OwnerFolder: l.OwnerFolder}
			if l.Eligible {
				ll.Action = ActionEligible
				res.Moves++
			} else {
				ll.Action = ActionReportOnly
			}
			lf.Leftovers = append(lf.Leftovers, ll)
		}
		res.Folders = append(res.Folders, lf)
	}
	if !dry {
		res.Moves = 0
	}
	return res
}

// maskPath masks every id component of a slash path (UUIDs, with or
// without the -builtins suffix, and 8-hex short names).
func maskPath(rel string) string {
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		base := strings.TrimSuffix(p, "-builtins")
		if ids.IsUUID(base) || isShortHex(p) {
			parts[i] = ids.Mask(p)
		}
	}
	return strings.Join(parts, "/")
}

func isShortHex(s string) bool {
	if len(s) != 8 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

// seedParticipant is one profile whose folder takes part in the seeding.
type seedParticipant struct {
	alias  ids.Alias
	folder *profile.Folder
	rep    model.Replica
	state  *state.ProfileState // nil for a profile this run registers
}

// seededBases are the base objects A3 stored, by participant and row.
type seededBases map[ids.Alias]map[model.RowID]state.Ref

// legacySeed is 3.7: the preconditions, then per row the agreement of
// every participant. With store set (apply) the bases are stored as
// objects (A3); nothing references them until A5.
func (o *Orchestrator) legacySeed(ctx context.Context, root *state.Root, lp *legacyPlan, opts LegacyOptions, store bool) (LegacySeed, seededBases) {
	parts := o.seedParticipants(root, lp)
	no := func(reason string) (LegacySeed, seededBases) { return LegacySeed{Reason: reason}, nil }
	m := lp.manifest
	switch {
	case opts.NoSeed:
		return no(SeedNoSeedFlag)
	case m == nil:
		return no(SeedNoManifest)
	case len(parts) < 2:
		return no(SeedTooFew)
	}
	for _, p := range parts {
		if hasBases(root, p.alias) {
			return no(SeedHasBases)
		}
		if p.state != nil && p.state.Replica != p.rep {
			return no(SeedOrgUnknown)
		}
	}
	switch {
	case len(root.Canonical) > 0 || len(root.Overlays) > 0:
		return no(SeedHasCanonical)
	case root.Run != nil:
		return no(SeedRunOpen)
	case slices.Contains(lp.p.HintWarnings, legacy.HintMidSwitch):
		return no(SeedHintMidSwitch)
	}
	for _, fp := range lp.p.Folders {
		if fp.Reason == legacy.ReasonDuplicate {
			return no(SeedDuplicateAccount)
		}
	}
	// One Local Storage reader (a snapshot clone) per participant, shared
	// by its probes and every row, so all of a folder's rows come from one
	// snapshot.
	readers := make([]kv.Reader, len(parts))
	defer func() {
		for _, ls := range readers {
			if ls != nil {
				closeReader(ls)
			}
		}
	}()
	for i, p := range parts {
		ls, err := o.lsReader(ctx, p.folder.Path)
		if err != nil {
			return no(SeedProbeFailed)
		}
		readers[i] = ls
	}
	skipped := map[model.RowID]bool{}
	for i, p := range parts {
		probes := o.seedProbes(ctx, m, p, readers[i])
		for _, r := range probes {
			if !r.OK && (r.Critical || r.Row == "") {
				return no(SeedProbeFailed)
			}
		}
		in := o.decideInput(ctx, root, m, lp.tested, lp.bundle, *p.folder)
		in.Probes, in.OrgUnknown = probes, p.rep.Org == ""
		dec := o.d.Compat.Decide(in)
		if dec.Mode == compat.ModeSwapOnly || dec.Mode == compat.ModeRefuse {
			return no(SeedNoManifest)
		}
		for _, r := range probes {
			if !r.OK && r.Row != "" {
				skipped[r.Row] = true
			}
		}
	}
	out := LegacySeed{}
	var bases seededBases
	if store {
		bases = seededBases{}
	}
	ignoreRoles := m.Legacy.AgreeIgnoringRoles
	for _, r := range model.AllRows {
		row := LegacySeedRow{Row: r}
		spec, ok := o.rowSpec(m, r)
		switch {
		case !ok || o.d.Adapters[r] == nil:
			row.Reason = SeedNoAdapter
		case skipped[r]:
			row.Reason = SeedProbeFailed
		case spec.PerOrg && slices.ContainsFunc(parts, func(p seedParticipant) bool { return p.rep.Org == "" }):
			row.Reason = SeedOrgUnknown
		}
		if row.Reason != "" {
			out.Rows = append(out.Rows, row)
			continue
		}
		snaps := make([]*model.RowSnapshot, 0, len(parts))
		for i, p := range parts {
			s, err := o.readRowLS(ctx, m, p.folder.Path, p.rep, r, readers[i], nil, false)
			if err != nil || s == nil {
				row.Reason = SeedUnreadable
				if err == nil {
					row.Reason = SeedNoAdapter
				}
				break
			}
			snaps = append(snaps, s)
		}
		if row.Reason != "" {
			out.Rows = append(out.Rows, row)
			continue
		}
		var ignore []string
		for _, role := range ignoreRoles {
			ignore = append(ignore, spec.Fields[role]...)
		}
		st, _ := legacy.Stats(snaps, ignore)
		row.Agreed, row.Differing = st.Agreed, st.Differing
		agreed, err := legacy.Agree(snaps, ignore)
		if err != nil {
			row.Reason = SeedUnreadable
			out.Rows = append(out.Rows, row)
			continue
		}
		if store {
			refs := map[ids.Alias]state.Ref{}
			for i, p := range parts {
				ref, err := o.d.State.PutSnapshot(agreed[i])
				if err != nil {
					row.Reason = SeedUnreadable
					break
				}
				refs[p.alias] = ref
			}
			if row.Reason != "" {
				out.Rows = append(out.Rows, row)
				continue
			}
			for a, ref := range refs {
				if bases[a] == nil {
					bases[a] = map[model.RowID]state.Ref{}
				}
				bases[a][r] = ref
			}
		}
		row.Seeded = true
		out.Seeded = true
		out.Rows = append(out.Rows, row)
	}
	if !out.Seeded && out.Reason == "" {
		out.Reason = SeedUnreadable
	}
	return out, bases
}

// seedParticipants lists the folders taking part in the seeding: every
// folder this run registers or completes, and every registered profile.
func (o *Orchestrator) seedParticipants(root *state.Root, lp *legacyPlan) []seedParticipant {
	var out []seedParticipant
	for _, fp := range lp.p.Folders {
		if fp.Status == legacy.StatusSkip || lp.res == nil {
			continue
		}
		b := lp.res.Bindings[fp.Alias]
		f := lp.folder(fp.Name)
		if b == nil || f == nil || f.Identity == nil {
			continue
		}
		p := seedParticipant{alias: fp.Alias, folder: f, rep: b.Replica}
		if fp.Status == legacy.StatusRegistered {
			p.state = root.Profiles[fp.Alias]
		}
		out = append(out, p)
	}
	return out
}

// seedProbes runs the manifest's probes on one participant's folder over
// its Local Storage reader.
func (o *Orchestrator) seedProbes(ctx context.Context, m *compat.Manifest, p seedParticipant, ls kv.Reader) []compat.ProbeResult {
	res, err := o.d.Compat.Probes(ctx, m, compat.ProbeEnv{Folder: p.folder.Path, Replica: p.rep, LS: ls})
	if err != nil {
		return []compat.ProbeResult{{ProbeID: "probes", OK: false, Critical: true}}
	}
	return res
}

// legacyRegister is A4 to A7: config, one root commit, markers and the
// active-folder checks.
func (o *Orchestrator) legacyRegister(ctx context.Context, root *state.Root, lp *legacyPlan, opts LegacyOptions, bases seededBases, runID string, res *LegacyResult) error {
	adopted := lp.p.Adopted()
	if len(adopted) == 0 && len(bases) == 0 {
		return nil
	}
	// A4: the new config entries, in folder order.
	if len(lp.newProfiles) > 0 {
		for _, p := range lp.newProfiles {
			p.ManagedByOrg = opts.Managed[p.Alias]
			o.d.Config.Profiles = append(o.d.Config.Profiles, p)
		}
		if err := o.saveConfig(); err != nil {
			return err
		}
	}
	// A5: one commit.
	registeredActive := false
	var names []string
	for _, fp := range adopted {
		ps := lp.newStates[fp.Alias]
		if ps == nil {
			continue
		}
		root.Profiles[fp.Alias] = ps
		names = append(names, string(fp.Alias))
		if fp.Active {
			root.Active = fp.Alias
			registeredActive = true
		}
	}
	seedHash := model.HashBytes([]byte("paritydesk-legacy-seed\x00" + runID))
	var marked []seedParticipant
	if len(bases) > 0 {
		e := max(root.Epoch, 1)
		root.Epoch = e
		for _, p := range o.seedParticipants(root, lp) {
			ps := root.Profiles[p.alias]
			if ps == nil {
				continue
			}
			ps.LiveEpoch = e
			rows := bases[p.alias]
			if len(rows) == 0 {
				continue
			}
			for r, ref := range rows {
				root.Bases[state.BaseKey{Alias: p.alias, Replica: p.rep, Row: r}.String()] = ref
			}
			ps.MarkerBase, ps.SeededFrom = seedHash, state.SeededLegacy
			marked = append(marked, p)
		}
	}
	root.LastCommitAt = o.now()
	if err := o.commit(root); err != nil {
		return err
	}
	if len(names) > 0 {
		o.progressNaming(P10Commit, "ok", i18n.Tx(i18n.ProgressP10Registered, len(names)), toAliases(names)...)
	}
	// A6: markers; a failure leaves that profile's bases invalid (union).
	for _, p := range marked {
		mk := model.Marker{Version: 1, Alias: p.alias, Replica: p.rep, Generation: 0, BaseHash: seedHash}
		if err := o.d.WriteMarker(o.d.FS, p.folder.Path, mk); err != nil {
			res.Warnings = append(res.Warnings, LegacyWarnMarker+string(p.alias))
		}
	}
	// A7.
	if registeredActive {
		o.registerActiveTail(ctx, root)
	}
	return nil
}

func toAliases(names []string) []ids.Alias {
	out := make([]ids.Alias, len(names))
	for i, n := range names {
		out[i] = ids.Alias(n)
	}
	return out
}

// registerActiveTail is the tail of Init for a newly registered active
// folder: other tools, the Automation question, and the Chrome note.
func (o *Orchestrator) registerActiveTail(ctx context.Context, root *state.Root) {
	o.initChecks(ctx, root)
	o.progress(P0Preflight, "ok", chromeNote)
}

// folderStillIs reports whether the folder name still holds the planned
// folder (device, inode and birth time) and account.
func (o *Orchestrator) folderStillIs(ctx context.Context, name string, want fsx.FileID, acct ids.AccountID) bool {
	f, err := o.d.Folders.Identify(ctx, name)
	if err != nil || f.Identity == nil || f.Identity.Account != acct {
		return false
	}
	return f.ID.Dev == want.Dev && f.ID.Ino == want.Ino && f.ID.Birth.Equal(want.Birth)
}

// legacyQuarantine runs one quarantine run per folder with eligible
// leftovers (5.2).
func (o *Orchestrator) legacyQuarantine(ctx context.Context, root *state.Root, lp *legacyPlan, res *LegacyResult) error {
	for i := range lp.p.Folders {
		fp := &lp.p.Folders[i]
		lf := &res.Folders[i]
		var items []int
		for j, l := range fp.Leftovers {
			if l.Eligible {
				items = append(items, j)
			}
		}
		if len(items) == 0 || fp.Alias == "" || root.Profiles[fp.Alias] == nil {
			continue
		}
		f := lp.folder(fp.Name)
		if f == nil || f.Identity == nil {
			continue
		}
		if err := o.quarantineFolder(ctx, root, lp, fp, lf, f, items, res); err != nil {
			return err
		}
	}
	return nil
}

// deferItems marks items (indexes into lf.Leftovers) as deferred with why.
func deferItems(res *LegacyResult, lf *LegacyFolder, items []int, why string) {
	for _, j := range items {
		lf.Leftovers[j].Action, lf.Leftovers[j].Why = ActionDeferred, why
		res.MovesDeferred++
	}
}

// quarantineFolder is Q0 to Q4 for one folder.
func (o *Orchestrator) quarantineFolder(ctx context.Context, root *state.Root, lp *legacyPlan, fp *legacy.FolderPlan, lf *LegacyFolder,
	f *profile.Folder, items []int, res *LegacyResult) error {
	d := o.d
	m := lp.manifest
	ps := root.Profiles[fp.Alias]
	planned := f.ID
	acct := f.Identity.Account
	// Q0: run-time checks.
	if !o.folderStillIs(ctx, f.Name, planned, acct) {
		deferItems(res, lf, items, legacy.WhyFolderChanged)
		res.Warnings = append(res.Warnings, LegacyWarnFolderChanged+f.Name)
		return nil
	}
	// Q0: the leftovers again, as they are now (seeding, registration and
	// the Automation question may have taken a long time): an item moves
	// only when it is still eligible with the same kind and unique count.
	fresh := map[string]legacy.Leftover{}
	if in, found := lp.leftIn[f.Name]; found {
		if now, err := legacy.FindLeftovers(in); err == nil {
			for _, l := range now {
				fresh[l.Rel] = l
			}
		}
	}
	var ok []int
	synced := m.SyncedFor(ps.Replica)
	for _, j := range items {
		l := fp.Leftovers[j]
		n, still := fresh[l.Rel]
		switch {
		case !still || !n.Eligible || n.Kind != l.Kind || n.Unique != l.Unique:
			deferItems(res, lf, []int{j}, legacy.WhyFolderChanged)
		case !legacyPathAllowed(f.Path, l.Rel, synced):
			lf.Leftovers[j].Action, lf.Leftovers[j].Why = ActionReportOnly, legacy.WhyDenied
		case l.Kind == legacy.KindMixedOrgStub && ps.Replica.Org == "":
			deferItems(res, lf, []int{j}, legacy.WhyOrgUnknown)
		default:
			ok = append(ok, j)
		}
	}
	if len(ok) == 0 {
		return nil
	}
	if why := o.quarantineBlocked(ctx, lp, f); why != "" {
		deferItems(res, lf, ok, why)
		return nil
	}
	// Q1: the run.
	runID, err := runner.NewRunID(o.now())
	if err != nil {
		return err
	}
	n := o.nextGeneration(root)
	root.Run = &state.Run{ID: runID, Kind: state.RunQuarantine, From: fp.Alias, To: fp.Alias, Generation: n, Started: o.now()}
	data := map[string]string{"n": fmt.Sprint(n), "alias": string(fp.Alias)}
	if err := o.begin(root, P6Backup, state.IntentQuarantining, data); err != nil {
		return err
	}
	cctx, cancel := uncancel(ctx)
	defer cancel()
	closeRun := func() error {
		root.Run = nil
		return o.commit(root)
	}
	// Q2: the pinned generation, finished before anything moves.
	w, err := d.Gens.Begin(n, runID, fp.Alias, ps.Replica)
	if err != nil {
		if cerr := closeRun(); cerr != nil {
			return cerr
		}
		return pderr.Wrap(pderr.Internal, "generation", err)
	}
	w.Pin(gen.PurposeLegacyQuarantine)
	// SaveTree refuses one item (no longer a directory, a symlink now, or
	// grown past the fingerprint cap): that item is kept for now; the
	// others go on.
	saved := ok[:0:0]
	for _, j := range ok {
		if _, err := w.SaveTree(f.Path, fp.Leftovers[j].Rel); err != nil {
			why := legacy.WhyFolderChanged
			if errors.Is(err, gen.ErrTreeTooLarge) {
				why = legacy.WhyTooLarge
			}
			deferItems(res, lf, []int{j}, why)
			continue
		}
		saved = append(saved, j)
	}
	if len(saved) == 0 {
		_ = w.Abort()
		return closeRun()
	}
	ok = saved
	if err := o.crashPoint(P6Backup); err != nil {
		return err
	}
	if !o.folderStillIs(cctx, f.Name, planned, acct) {
		_ = w.Abort()
		deferItems(res, lf, ok, legacy.WhyFolderChanged)
		res.Warnings = append(res.Warnings, LegacyWarnFolderChanged+f.Name)
		return closeRun()
	}
	if _, err := w.Finish(); err != nil {
		_ = w.Abort()
		if cerr := closeRun(); cerr != nil {
			return cerr
		}
		return pderr.Wrap(pderr.Internal, "generation", err)
	}
	if err := o.crashPoint(P7Apply); err != nil {
		return err
	}
	// Q3: one rename per item, each after a fresh identity check.
	moved := 0
	stop := ""
	for k, j := range ok {
		if stop == "" {
			switch {
			case !o.folderStillIs(cctx, f.Name, planned, acct):
				stop = legacy.WhyFolderChanged
				res.Warnings = append(res.Warnings, LegacyWarnFolderChanged+f.Name)
			case f.Name == ids.DefaultFolder && o.appRunning(cctx, lp):
				stop = legacy.WhyAppRunning
			}
		}
		if stop != "" {
			deferItems(res, lf, ok[k:k+1], stop)
			continue
		}
		if err := d.FS.MoveInto(f.Path, fp.Leftovers[j].Rel, w.QuarantineDir()); err != nil {
			lf.Leftovers[j].Action = ActionFailed
			res.MovesFailed++
			stop = legacy.WhyMoveFailed
			continue
		}
		lf.Leftovers[j].Action, lf.Leftovers[j].Why, lf.Leftovers[j].Generation = ActionMoved, "", n
		moved++
		res.Moves++
		if err := o.crashPoint(P7Apply); err != nil {
			return err
		}
	}
	// Q4.
	if moved == 0 {
		if _, err := d.Gens.DeleteUnmoved(n); err != nil {
			o.progress(P10Commit, "warn", i18n.Tx(i18n.ProgressP10EmptyGenerationKept))
		}
	} else {
		root.Generations = append(root.Generations, state.GenerationRef{N: n, RunID: runID, Target: fp.Alias, At: o.now(), Purpose: gen.PurposeLegacyQuarantine})
		res.Generations = append(res.Generations, LegacyGeneration{N: n, Alias: fp.Alias, Folder: f.Name, Planned: len(ok), Moved: moved})
	}
	if err := o.done(root, P10Commit, state.IntentQuarantining); err != nil {
		return err
	}
	return closeRun()
}

// legacyPathAllowed is the run-time floor of a move: contained, not
// covered by the deny floor, and not below a synced glob of the folder's
// own replica.
func legacyPathAllowed(folder, rel string, synced []string) bool {
	if _, err := fsx.Contained(folder, filepath.FromSlash(rel)); err != nil {
		return false
	}
	if denylist.MatchPath(rel) || denylist.MatchPath(rel+"/x") {
		return false
	}
	for _, g := range synced {
		if denylist.MatchGlob(g, rel) {
			return false
		}
	}
	return true
}

// quarantineBlocked returns why nothing may move out of f now: the app or
// another process uses it, or the generations directory is on another
// volume. "" means the moves may start.
func (o *Orchestrator) quarantineBlocked(ctx context.Context, lp *legacyPlan, f *profile.Folder) string {
	d := o.d
	live := f.Name == ids.DefaultFolder
	if live && (!lp.located || o.appRunning(ctx, lp)) {
		return legacy.WhyAppRunning
	}
	bundlePath := ""
	if live {
		bundlePath = lp.bundle.Path
	}
	if rep, err := d.App.CheckQuiescence(ctx, o.quiescenceInput(bundlePath, f.Path)); err != nil || !rep.Quiet {
		return legacy.WhyInUse
	}
	if d.Paths.GenDir == "" {
		return ""
	}
	fid, err := d.FS.ID(f.Path)
	if err != nil {
		return legacy.WhyFolderChanged
	}
	gid, err := d.FS.ID(nearestExisting(d.Paths.GenDir))
	if err != nil || gid.Dev != fid.Dev {
		return legacy.WhyOtherVolume
	}
	return ""
}

// appRunning reports whether an app process runs (or cannot be checked).
func (o *Orchestrator) appRunning(ctx context.Context, lp *legacyPlan) bool {
	if !lp.located {
		return true
	}
	procs, err := o.d.App.Running(ctx, lp.bundle.Path)
	return err != nil || len(procs) > 0
}

// nearestExisting returns p or its nearest existing parent.
func nearestExisting(p string) string {
	for p != "" {
		if _, err := os.Lstat(p); err == nil {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
		p = parent
	}
	return p
}

// seeded reports whether either side of the run still has the bases
// profile adopt --from-legacy seeded (its first switch).
func (r *run) seeded() bool {
	for _, a := range []ids.Alias{r.S.Profile.Alias, r.T.Profile.Alias} {
		if ps := r.root.Profiles[a]; ps != nil && ps.SeededFrom != "" {
			return true
		}
	}
	return false
}
