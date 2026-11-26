package legacy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/kazupc/paritydesk/internal/ids"
)

// Status of a folder in the plan.
type Status string

// Folder statuses.
const (
	StatusAdopt      Status = "adopt"      // registered by this run
	StatusFinish     Status = "finish"     // in the config, no state: an earlier adopt is completed
	StatusRegistered Status = "registered" // already a profile
	StatusSkip       Status = "skip"       // left out (Reason says why)
)

// Reason ids of a skipped folder.
const (
	ReasonExcluded        = "excluded"
	ReasonUnsupportedName = "unsupported_name"
	ReasonReserved        = "reserved"
	ReasonUnreadable      = "unreadable"
	ReasonForeign         = "foreign"
	ReasonNoIdentity      = "no_identity"
	ReasonDuplicate       = "duplicate"
	ReasonNoAlias         = "no_alias"
	ReasonNeedsAlias      = "needs_alias"
	ReasonAliasTaken      = "alias_taken"
	ReasonAliasCollision  = "alias_collision"
	ReasonParkCollision   = "park_collision"
)

// Plan statuses.
const (
	PlanOK               = "ok"
	PlanNoActive         = "no_active"
	PlanNoActiveIdentity = "no_active_identity"
)

// Alias sources.
const (
	SourceFlag   = "flag"
	SourceFolder = "folder"
	SourceHint   = "hint"
	SourceConfig = "config"
)

// folderRE is ids' parked folder pattern, used to tell an unsupported name
// from a reserved one.
var folderRE = regexp.MustCompile(`^Claude-[A-Za-z0-9._-]{1,48}$`)

// FolderFacts are what the scan found about one folder, plus the
// provisional resolution's binding to an existing profile.
type FolderFacts struct {
	Name        string
	ScanErr     bool          // the folder could not be read
	Account     ids.AccountID // "" when it holds no identity
	IdentityErr string        // constant text when Account is ""
	Dev, Ino    uint64
	Birth       time.Time
	Foreign     bool // matched by another tool's footprint
	// ScanOrg is the folder's candidate organization from its own
	// directories and the log (spec-legacy-adopt 3.5 step 1), "" when
	// unknown. Unlike the resolved Org it does not depend on any flag, so
	// the folders half of the plan hash uses it.
	ScanOrg ids.OrgID
	// Bound is the existing config profile the resolution binds this
	// folder to ("" none); BoundHasState is set when root.Profiles has it.
	Bound         ids.Alias
	BoundHasState bool
}

// ExistingProfile is a profile of the current config.
type ExistingProfile struct {
	Alias  ids.Alias
	Parked string // config.Profile.ParkedFolder()
}

// PlanInput is everything Plan needs; it is pure.
type PlanInput struct {
	Folders  []FolderFacts
	Hint     *Hint                // nil: no state file
	Aliases  map[string]ids.Alias // --legacy-alias: folder name -> alias
	Exclude  []string             // --legacy-exclude
	Reserved []string             // the manifest's reserved folder names
	Existing []ExistingProfile
	// Quarantine and NoSeed are --quarantine and --legacy-no-seed.
	Quarantine, NoSeed bool
}

// FolderPlan is one folder's line of the plan.
type FolderPlan struct {
	Name        string
	Active      bool
	Account     ids.AccountID
	IdentityErr string
	Org         ids.OrgID // set by the orchestrator from the resolution
	ScanOrg     ids.OrgID // FolderFacts.ScanOrg
	Dev, Ino    uint64
	Birth       time.Time
	Status      Status
	Reason      string
	ReasonArg   string // a validated folder name or alias
	Alias       ids.Alias
	AliasSource string
	// ConfigFolder is config.Profile.Folder for a new profile ("" for the
	// default parked name).
	ConfigFolder string
	// Leftovers are set by the orchestrator for every folder with an
	// identity, whatever its status (the plan hash covers them all); only
	// those of folders that are not skipped are reported or moved.
	Leftovers []Leftover
}

// Plan is the pure plan of profile adopt --from-legacy.
type Plan struct {
	Status       string
	Folders      []FolderPlan // Claude first, then by ids.FoldName
	HintUsed     bool
	HintWarnings []HintWarning
	Quarantine   bool
	NoSeed       bool
	Exclude      []string // sorted
	// FlagAliases are the --legacy-alias flags as given (folder name ->
	// alias), kept whatever status the folder ends in, so the printed
	// command repeats every one of them.
	FlagAliases map[string]ids.Alias
	// HintAlias is the alias the consistent hint gives Claude when no flag
	// does ("" otherwise), kept even when Claude ends skipped.
	HintAlias ids.Alias
}

// SeedAllowed reports whether the flags and the hint allow seeding: not
// --legacy-no-seed, and no hint saying the earlier script's switch did not
// finish (3.4, 3.7). The plan hash covers this effective decision, so a
// command without the hint must say --legacy-no-seed to match.
func (p *Plan) SeedAllowed() bool {
	return !p.NoSeed && !slices.Contains(p.HintWarnings, HintMidSwitch)
}

// Folder returns the plan line of a folder, or nil.
func (p *Plan) Folder(name string) *FolderPlan {
	for i := range p.Folders {
		if p.Folders[i].Name == name {
			return &p.Folders[i]
		}
	}
	return nil
}

// Adopted returns the folders this run registers (adopt) or completes
// (finish), in plan order.
func (p *Plan) Adopted() []*FolderPlan {
	var out []*FolderPlan
	for i := range p.Folders {
		if s := p.Folders[i].Status; s == StatusAdopt || s == StatusFinish {
			out = append(out, &p.Folders[i])
		}
	}
	return out
}

// sortFolders puts Claude first, then the others by folded name.
func sortFolders(fs []FolderFacts) []FolderFacts {
	out := slices.Clone(fs)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Name == ids.DefaultFolder, out[j].Name == ids.DefaultFolder
		if a != b {
			return a
		}
		fa, fb := ids.FoldName(out[i].Name), ids.FoldName(out[j].Name)
		if fa != fb {
			return fa < fb
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// hintConsistent reports whether the hint names the label in Claude: its
// in_default label has no parked folder.
func hintConsistent(h *Hint, folded map[string]bool) bool {
	return h != nil && h.InDefault != "" && !folded[ids.FoldName(ids.DefaultFolder+"-"+h.InDefault)]
}

// Build decides every folder's status and alias (spec-legacy-adopt 3.1
// to 3.4). Org and leftovers are filled in by the orchestrator afterwards.
func Build(in PlanInput) *Plan {
	folders := sortFolders(in.Folders)
	p := &Plan{Status: PlanOK, Quarantine: in.Quarantine, NoSeed: in.NoSeed, Exclude: slices.Clone(in.Exclude),
		FlagAliases: map[string]ids.Alias{}}
	sort.Strings(p.Exclude)
	for name, a := range in.Aliases {
		if a != "" {
			p.FlagAliases[name] = a
		}
	}
	folded := map[string]bool{}
	for _, f := range folders {
		folded[ids.FoldName(f.Name)] = true
	}
	hintOK := false
	if in.Hint != nil {
		var warn = map[HintWarning]bool{}
		hintOK = hintConsistent(in.Hint, folded)
		if !hintOK {
			warn[HintStale] = true
		}
		if in.Hint.Active != "" && in.Hint.Active != in.Hint.InDefault {
			warn[HintMidSwitch] = true
		}
		p.HintWarnings = sortedWarnings(warn)
	}
	active := -1
	for i, f := range folders {
		fp := FolderPlan{Name: f.Name, Active: f.Name == ids.DefaultFolder, Account: f.Account, IdentityErr: f.IdentityErr,
			Dev: f.Dev, Ino: f.Ino, Birth: f.Birth, ScanOrg: f.ScanOrg}
		if fp.Active {
			active = i
		}
		p.Folders = append(p.Folders, fp)
	}
	switch {
	case active < 0:
		p.Status = PlanNoActive
	case folders[active].Account == "":
		p.Status = PlanNoActiveIdentity
	}
	// First pass: everything decided by the folder alone.
	candidates := map[int]bool{}
	for i, f := range folders {
		fp := &p.Folders[i]
		switch {
		case slices.Contains(in.Exclude, f.Name):
			fp.skip(ReasonExcluded, "")
		case !fp.Active && !folderRE.MatchString(f.Name):
			fp.skip(ReasonUnsupportedName, "")
		case !fp.Active && reserved(f.Name, in.Reserved):
			fp.skip(ReasonReserved, "")
		case f.ScanErr:
			fp.skip(ReasonUnreadable, "")
		case f.Foreign:
			fp.skip(ReasonForeign, "")
		case f.Account == "":
			fp.skip(ReasonNoIdentity, f.IdentityErr)
		case f.Bound != "" && f.BoundHasState:
			fp.Status, fp.Alias = StatusRegistered, f.Bound
			candidates[i] = true
		case f.Bound != "":
			fp.Status, fp.Alias, fp.AliasSource = StatusFinish, f.Bound, SourceConfig
			candidates[i] = true
		default:
			candidates[i] = true
		}
	}
	// Duplicates: two candidate folders with one account. Claude wins;
	// parked duplicates are left out (both, when Claude is not one of them).
	for i := range p.Folders {
		fp := &p.Folders[i]
		if !candidates[i] || fp.Status != "" || fp.Active {
			continue
		}
		other := ""
		for j := range p.Folders {
			if j == i || !candidates[j] || p.Folders[j].Account != fp.Account {
				continue
			}
			if other == "" || p.Folders[j].Active {
				other = p.Folders[j].Name
			}
		}
		if other != "" {
			fp.skip(ReasonDuplicate, other)
		}
	}
	// Aliases.
	for i := range p.Folders {
		fp := &p.Folders[i]
		if !candidates[i] || fp.Status != "" {
			continue
		}
		if a, ok := in.Aliases[fp.Name]; ok && a != "" {
			fp.Alias, fp.AliasSource = a, SourceFlag
		} else if fp.Active {
			if hintOK {
				if a, err := DeriveAlias(ids.DefaultFolder + "-" + in.Hint.InDefault); err == nil {
					fp.Alias, fp.AliasSource = a, SourceHint
					p.HintAlias = a
				}
			}
			if fp.Alias == "" {
				fp.skip(ReasonNeedsAlias, "")
				continue
			}
		} else {
			a, err := DeriveAlias(fp.Name)
			if err != nil {
				fp.skip(ReasonNoAlias, fp.Name)
				continue
			}
			fp.Alias, fp.AliasSource = a, SourceFolder
		}
		fp.Status = StatusAdopt
		if fp.Active {
			if hintOK && fp.AliasSource == SourceHint {
				p.HintUsed = true
			}
			if hintOK {
				name := ids.DefaultFolder + "-" + in.Hint.InDefault
				if ids.ValidateFolder(name, in.Reserved...) == nil && !folded[ids.FoldName(name)] && name != ids.DefaultParkedFolder(fp.Alias) {
					fp.ConfigFolder = name
				}
			}
		} else if fp.Name != ids.DefaultParkedFolder(fp.Alias) {
			fp.ConfigFolder = fp.Name
		}
	}
	p.collisions(in.Existing)
	return p
}

func reserved(name string, extra []string) bool {
	return ids.ValidateFolder(name, extra...) != nil
}

func (fp *FolderPlan) skip(reason, arg string) {
	fp.Status, fp.Reason, fp.ReasonArg, fp.Alias, fp.AliasSource, fp.ConfigFolder = StatusSkip, reason, arg, "", "", ""
}

// parkedName is the name the profile's folder has while parked.
func (fp *FolderPlan) parkedName() string {
	if fp.ConfigFolder != "" {
		return fp.ConfigFolder
	}
	return ids.DefaultParkedFolder(fp.Alias)
}

// collisions leaves out new profiles whose alias or parked name clashes.
func (p *Plan) collisions(existing []ExistingProfile) {
	taken := map[ids.Alias]bool{}
	parkedExisting := map[string]bool{}
	for _, e := range existing {
		taken[e.Alias] = true
		parkedExisting[ids.FoldName(e.Parked)] = true
	}
	for i := range p.Folders {
		fp := &p.Folders[i]
		if fp.Status == StatusAdopt && taken[fp.Alias] {
			fp.skip(ReasonAliasTaken, string(fp.Alias))
		}
	}
	byAlias := map[ids.Alias][]int{}
	for i := range p.Folders {
		if p.Folders[i].Status == StatusAdopt {
			byAlias[p.Folders[i].Alias] = append(byAlias[p.Folders[i].Alias], i)
		}
	}
	for a, list := range byAlias {
		if len(list) > 1 {
			for _, i := range list {
				p.Folders[i].skip(ReasonAliasCollision, string(a))
			}
		}
	}
	for i := range p.Folders {
		fp := &p.Folders[i]
		if fp.Status != StatusAdopt {
			continue
		}
		name := fp.parkedName()
		clash := parkedExisting[ids.FoldName(name)]
		for j := range p.Folders {
			if j != i && ids.FoldName(p.Folders[j].Name) == ids.FoldName(name) {
				clash = true
			}
		}
		if clash {
			fp.skip(ReasonParkCollision, name)
		}
	}
}

// PlanHash returns "<folders>.<inputs>": two 32-hex halves (sha256
// truncated to 128 bits). The folders half describes only what is on disk,
// independent of every flag: each folder's id, account and candidate org
// (ScanOrg), and the leftovers found in every folder with an identity
// (path, kind, unique names). The inputs half is what the flags made of it:
// statuses, aliases, the parked name a new profile gets (park), which
// leftovers may move, --quarantine, and the effective seed decision
// (SeedAllowed).
func PlanHash(p *Plan) string {
	type hf struct {
		Name    string `json:"name"`
		Dev     uint64 `json:"dev"`
		Ino     uint64 `json:"ino"`
		Birth   int64  `json:"birth"`
		Account string `json:"account"`
		Org     string `json:"org"`
	}
	type hl struct {
		Folder string `json:"folder"`
		Rel    string `json:"rel"`
		Kind   string `json:"kind"`
		Unique int    `json:"unique"`
	}
	type hs struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Reason string `json:"reason"`
		Alias  string `json:"alias"`
		Park   string `json:"park,omitempty"`
	}
	type he struct {
		Folder string `json:"folder"`
		Rel    string `json:"rel"`
	}
	folders := struct {
		V         int  `json:"v"`
		Folders   []hf `json:"folders"`
		Leftovers []hl `json:"leftovers"`
	}{V: 2, Folders: []hf{}, Leftovers: []hl{}}
	inputs := struct {
		V          int  `json:"v"`
		Statuses   []hs `json:"statuses"`
		Eligible   []he `json:"eligible"`
		Quarantine bool `json:"quarantine"`
		Seed       bool `json:"seed"`
	}{V: 2, Statuses: []hs{}, Eligible: []he{}, Quarantine: p.Quarantine, Seed: p.SeedAllowed()}
	for _, f := range p.Folders {
		var birth int64
		if !f.Birth.IsZero() {
			birth = f.Birth.UnixNano()
		}
		folders.Folders = append(folders.Folders, hf{f.Name, f.Dev, f.Ino, birth, string(f.Account), string(f.ScanOrg)})
		for _, l := range f.Leftovers {
			folders.Leftovers = append(folders.Leftovers, hl{f.Name, l.Rel, string(l.Kind), l.Unique})
			if l.Eligible && f.Status != StatusSkip {
				inputs.Eligible = append(inputs.Eligible, he{f.Name, l.Rel})
			}
		}
		inputs.Statuses = append(inputs.Statuses, hs{f.Name, string(f.Status), f.Reason, string(f.Alias), f.ConfigFolder})
	}
	sort.Slice(folders.Folders, func(i, j int) bool { return folders.Folders[i].Name < folders.Folders[j].Name })
	sort.Slice(folders.Leftovers, func(i, j int) bool {
		a, b := folders.Leftovers[i], folders.Leftovers[j]
		if a.Folder != b.Folder {
			return a.Folder < b.Folder
		}
		return a.Rel < b.Rel
	})
	sort.Slice(inputs.Statuses, func(i, j int) bool { return inputs.Statuses[i].Name < inputs.Statuses[j].Name })
	sort.Slice(inputs.Eligible, func(i, j int) bool {
		a, b := inputs.Eligible[i], inputs.Eligible[j]
		if a.Folder != b.Folder {
			return a.Folder < b.Folder
		}
		return a.Rel < b.Rel
	})
	return half(folders) + "." + half(inputs)
}

func half(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic("legacy: plan hash encoding failed: " + err.Error())
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

// SplitHash returns the two halves of a plan hash; ok is false for
// anything but two 32-hex halves.
func SplitHash(h string) (folders, inputs string, ok bool) {
	a, b, found := strings.Cut(h, ".")
	if !found || !isHex32(a) || !isHex32(b) {
		return "", "", false
	}
	return a, b, true
}

func isHex32(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

// Hash comparison results of CompareHash.
const (
	HashSame          = ""
	HashFoldersDiffer = "folders" // the folders changed since the dry run
	HashInputsDiffer  = "inputs"  // only the flags differ from the dry run's
)

// CompareHash compares a plan hash given by the user with the one computed
// now. A malformed given hash counts as a folders difference.
func CompareHash(given, now string) string {
	gf, gi, ok := SplitHash(given)
	nf, ni, _ := SplitHash(now)
	switch {
	case !ok || gf != nf:
		return HashFoldersDiffer
	case gi != ni:
		return HashInputsDiffer
	}
	return HashSame
}

// ApplyCommand returns the command that applies this plan without a
// terminal, built from validated values only: every --legacy-alias flag as
// given (also for folders the plan leaves out, so the command reproduces
// their status), the hint's alias for Claude as an explicit --legacy-alias
// (so the state file's path is never printed), the excludes,
// --legacy-no-seed when the flags or the hint refuse seeding, and
// --legacy-plan. --quarantine is never part of it (it needs a terminal),
// and the printed hash is the plan's without it.
//
// The command cannot carry the hint's parked folder name for Claude (the
// label is never printed), so for a plan built with a hint the caller
// rebuilds the plan from the command's own flags and passes that plan:
// its hash is then exactly what the command computes.
func ApplyCommand(p *Plan) string {
	parts := []string{"paritydesk", "profile", "adopt", "--from-legacy", "--apply"}
	done := map[string]bool{}
	alias := func(name string, a ids.Alias) {
		parts = append(parts, "--legacy-alias", name+"="+string(a))
		done[name] = true
	}
	for _, f := range p.Folders {
		if a, ok := p.FlagAliases[f.Name]; ok {
			alias(f.Name, a)
		} else if f.Active && p.HintAlias != "" {
			alias(f.Name, p.HintAlias)
		}
	}
	rest := make([]string, 0, len(p.FlagAliases))
	for name := range p.FlagAliases {
		if !done[name] {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	for _, name := range rest {
		alias(name, p.FlagAliases[name])
	}
	for _, x := range p.Exclude {
		parts = append(parts, "--legacy-exclude", x)
	}
	if !p.SeedAllowed() {
		parts = append(parts, "--legacy-no-seed")
	}
	c := *p
	c.Quarantine = false
	parts = append(parts, "--legacy-plan", PlanHash(&c))
	return strings.Join(parts, " ")
}
