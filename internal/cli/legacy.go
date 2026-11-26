package cli

import (
	"context"
	"flag"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/legacy"
	"github.com/kazupc/paritydesk/internal/orchestrator"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/render"
)

// legacyOnlyFlags are accepted only with --from-legacy.
var legacyOnlyFlags = []string{flagLegacyApply, flagQuarantine, flagLegacyState, flagLegacyAlias, flagLegacyExclude,
	flagLegacyManaged, flagLegacyPlan, flagLegacyNoSeed}

// legacyFolderRE is a folder name --legacy-alias and --legacy-exclude
// accept: Claude, or a parked folder name.
var legacyFolderRE = regexp.MustCompile(`^Claude(-[A-Za-z0-9._-]{1,48})?$`)

// flagSet reports whether a flag was given on the command line.
func flagSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

// legacyView is the --json document of profile adopt --from-legacy.
type legacyView struct {
	OK bool `json:"ok"`
	*orchestrator.LegacyResult
}

func newLegacyView(res *orchestrator.LegacyResult, ok bool) legacyView {
	c := *res
	if c.Folders == nil {
		c.Folders = []orchestrator.LegacyFolder{}
	}
	return legacyView{OK: ok, LegacyResult: &c}
}

// legacyOptions reads and validates the flags of the legacy mode. Every
// check here runs before any I/O.
func legacyOptions(e *env, fs *flag.FlagSet) (orchestrator.LegacyOptions, error) {
	o := orchestrator.LegacyOptions{
		Apply:      getBool(fs, flagLegacyApply),
		Quarantine: getBool(fs, flagQuarantine),
		PlanHash:   getString(fs, flagLegacyPlan),
		NoSeed:     getBool(fs, flagLegacyNoSeed),
		Aliases:    map[string]ids.Alias{},
	}
	if o.Apply && o.Quarantine && !e.terminal() {
		return o, pderr.New(pderr.Usage, "profile adopt", i18n.T(i18n.ErrorLegacyQuarantineTerminal))
	}
	if st := getString(fs, flagLegacyState); st != "" {
		home, err := e.sys.home()
		if err != nil {
			return o, err
		}
		st = config.ExpandHome(home, st)
		if !filepath.IsAbs(st) {
			return o, usageErrorf("--%s must be an absolute path", flagLegacyState)
		}
		o.HintPath = filepath.Clean(st)
	}
	for _, v := range getStrings(fs, flagLegacyAlias) {
		folder, alias, ok := strings.Cut(v, "=")
		if !ok || !legacyFolderRE.MatchString(folder) {
			return o, usageErrorf("--%s takes <folder>=<alias>, where <folder> is Claude or a Claude-<name> folder", flagLegacyAlias)
		}
		a, err := parseAlias(alias)
		if err != nil {
			return o, err
		}
		if _, dup := o.Aliases[folder]; dup {
			return o, usageErrorf("--%s names %s twice", flagLegacyAlias, folder)
		}
		o.Aliases[folder] = a
	}
	for _, x := range getStrings(fs, flagLegacyExclude) {
		if !legacyFolderRE.MatchString(x) {
			return o, usageErrorf("--%s takes Claude or a Claude-<name> folder", flagLegacyExclude)
		}
		if !slices.Contains(o.Exclude, x) {
			o.Exclude = append(o.Exclude, x)
		}
	}
	if o.PlanHash != "" {
		if _, _, ok := legacy.SplitHash(o.PlanHash); !ok {
			return o, usageErrorf("--%s must be the plan hash the dry run printed", flagLegacyPlan)
		}
	}
	managed, unmanaged, list := getBool(fs, "managed"), getBool(fs, "unmanaged"), getString(fs, flagLegacyManaged)
	switch {
	case managed && unmanaged:
		return o, usageErrorf("--managed and --unmanaged exclude each other")
	case list != "" && (managed || unmanaged):
		return o, usageErrorf("--%s excludes --managed and --unmanaged", flagLegacyManaged)
	}
	if list != "" {
		for _, a := range strings.Split(list, ",") {
			if _, err := parseAlias(strings.TrimSpace(a)); err != nil {
				return o, err
			}
		}
	}
	if o.Apply && !e.interactive() {
		if o.PlanHash == "" {
			return o, pderr.New(pderr.Declined, "profile adopt", i18n.T(i18n.ErrorLegacyPlanRequired))
		}
		if !managed && !unmanaged && list == "" {
			return o, usageErrorf("--managed or --unmanaged is required when there is no terminal to ask (or --%s)", flagLegacyManaged)
		}
	}
	return o, nil
}

// legacyManaged decides managed per new alias: the flags, else one
// question per alias in folder order.
func legacyManaged(ctx context.Context, e *env, fs *flag.FlagSet, newAliases []ids.Alias) (map[ids.Alias]bool, error) {
	out := map[ids.Alias]bool{}
	managed, unmanaged, list := getBool(fs, "managed"), getBool(fs, "unmanaged"), getString(fs, flagLegacyManaged)
	switch {
	case list != "":
		for _, a := range newAliases {
			out[a] = false
		}
		for _, raw := range strings.Split(list, ",") {
			a := ids.Alias(strings.TrimSpace(raw))
			if !slices.Contains(newAliases, a) {
				return nil, usageErrorf("--%s: %s is not a profile this run creates", flagLegacyManaged, a)
			}
			out[a] = true
		}
		return out, nil
	case managed || unmanaged:
		for _, a := range newAliases {
			out[a] = managed
		}
		return out, nil
	case !e.interactive():
		return nil, usageErrorf("--managed or --unmanaged is required when there is no terminal to ask (or --%s)", flagLegacyManaged)
	}
	u := e.newUI(false, false)
	for _, a := range newAliases {
		ok, err := u.ask(ctx, i18n.T(i18n.AskManaged, string(a)))
		if err != nil {
			return nil, err
		}
		out[a] = ok
	}
	return out, nil
}

// runProfileAdoptLegacy is spec-legacy-adopt 2.2.
func runProfileAdoptLegacy(ctx context.Context, e *env, fs *flag.FlagSet) error {
	opts, err := legacyOptions(e, fs)
	if err != nil {
		return err
	}
	svc, err := e.service(ctx, e.newUI(false, false))
	if err != nil {
		return err
	}
	apply := opts.Apply
	dry := opts
	dry.Apply, dry.Managed, dry.PlanHash = false, nil, ""
	res, err := svc.AdoptLegacy(ctx, dry)
	if err != nil {
		return err
	}
	if res == nil {
		return orInternal(nil, "profile adopt")
	}
	// The active folder needs an alias: ask when a person is there.
	if c := activeFolder(res); c != nil && c.Reason == legacy.ReasonNeedsAlias && e.interactive() {
		u := e.newUI(false, false)
		_, _ = u.p.Out.Write([]byte(i18n.T(i18n.AskLegacyAliasActive)))
		line, err := u.readLine(ctx)
		if err != nil {
			return err
		}
		a, err := parseAlias(strings.TrimSpace(line))
		if err != nil {
			return err
		}
		opts.Aliases[ids.DefaultFolder] = a
		dry.Aliases = opts.Aliases
		if res, err = svc.AdoptLegacy(ctx, dry); err != nil {
			return err
		}
	}
	if !apply {
		return e.emit(newLegacyView(res, true), func(p *render.Printer) { printLegacy(p, res) }, nil)
	}
	if c := activeFolder(res); c != nil && c.Reason == legacy.ReasonNeedsAlias && res.Status == legacy.PlanOK {
		return usageErrorf("pass --%s Claude=<alias> when there is no terminal to ask", flagLegacyAlias)
	}
	if e.interactive() && !e.g.JSON {
		// Step 3: the plan the person is asked about. Without a terminal
		// the consent is --legacy-plan.
		printLegacy(e.printer(), res)
	}
	if opts.PlanHash == "" {
		opts.PlanHash = res.PlanHash
	} else if e.interactive() {
		// Checked before the questions; the apply checks it again under
		// the lock. Only the inputs half differing means other flags.
		switch legacy.CompareHash(opts.PlanHash, res.PlanHash) {
		case legacy.HashFoldersDiffer:
			return pderr.New(pderr.Declined, "profile adopt", i18n.T(i18n.ErrorLegacyPlanChanged))
		case legacy.HashInputsDiffer:
			return pderr.New(pderr.Declined, "profile adopt", i18n.T(i18n.ErrorLegacyPlanFlags))
		}
	}
	var newAliases []ids.Alias
	for _, f := range res.Folders {
		if f.Status == string(legacy.StatusAdopt) {
			newAliases = append(newAliases, f.Alias)
		}
	}
	if opts.Managed, err = legacyManaged(ctx, e, fs, newAliases); err != nil {
		return err
	}
	if e.interactive() {
		q := i18n.T(i18n.AskLegacyAdopt)
		if opts.Quarantine && res.Moves > 0 {
			q = i18n.T(i18n.AskLegacyAdoptQuarantine, res.Moves)
		}
		ok, err := e.newUI(false, false).ask(ctx, q)
		if err != nil {
			return err
		}
		if !ok {
			return pderr.New(pderr.Declined, "confirm", i18n.T(i18n.Declined))
		}
	}
	out, err := svc.AdoptLegacy(ctx, opts)
	if out == nil {
		return orInternal(err, "profile adopt")
	}
	return e.emit(newLegacyView(out, err == nil), func(p *render.Printer) { printLegacy(p, out) }, err)
}

func activeFolder(res *orchestrator.LegacyResult) *orchestrator.LegacyFolder {
	for i := range res.Folders {
		if res.Folders[i].Active {
			return &res.Folders[i]
		}
	}
	return nil
}

// legacyReasonText is the text of a folder's skip reason.
func legacyReasonText(f orchestrator.LegacyFolder) string {
	switch f.Reason {
	case legacy.ReasonExcluded:
		return i18n.T(i18n.LegacyReasonExcluded)
	case legacy.ReasonUnsupportedName:
		return i18n.T(i18n.LegacyReasonUnsupportedName)
	case legacy.ReasonReserved:
		return i18n.T(i18n.LegacyReasonReserved)
	case legacy.ReasonUnreadable:
		return i18n.T(i18n.LegacyReasonUnreadable)
	case legacy.ReasonForeign:
		return i18n.T(i18n.LegacyReasonForeign)
	case legacy.ReasonNoIdentity:
		return i18n.T(i18n.LegacyReasonNoIdentity, f.ReasonArg)
	case legacy.ReasonDuplicate:
		return i18n.T(i18n.LegacyReasonDuplicate, f.ReasonArg)
	case legacy.ReasonNoAlias:
		return i18n.T(i18n.LegacyReasonNoAlias, f.ReasonArg)
	case legacy.ReasonNeedsAlias:
		return i18n.T(i18n.LegacyReasonNeedsAlias)
	case legacy.ReasonAliasTaken:
		return i18n.T(i18n.LegacyReasonAliasTaken, f.ReasonArg)
	case legacy.ReasonAliasCollision:
		return i18n.T(i18n.LegacyReasonAliasCollision, f.ReasonArg)
	case legacy.ReasonParkCollision:
		return i18n.T(i18n.LegacyReasonParkCollision, f.ReasonArg)
	}
	return f.Reason
}

var legacyWhy = map[string]i18n.Msg{
	legacy.WhyUnrecognized:   i18n.LegacyWhyUnrecognized,
	legacy.WhyHoldsData:      i18n.LegacyWhyHoldsData,
	legacy.WhyUniqueSessions: i18n.LegacyWhyUniqueSessions,
	legacy.WhySymlink:        i18n.LegacyWhySymlink,
	legacy.WhyTooLarge:       i18n.LegacyWhyTooLarge,
	legacy.WhyScratch:        i18n.LegacyWhyScratch,
	legacy.WhyOrgUnknown:     i18n.LegacyWhyOrgUnknown,
	legacy.WhyOtherVolume:    i18n.LegacyWhyOtherVolume,
	legacy.WhyAppRunning:     i18n.LegacyWhyAppRunning,
	legacy.WhyInUse:          i18n.LegacyWhyInUse,
	legacy.WhyFolderChanged:  i18n.LegacyWhyFolderChanged,
	legacy.WhyDenied:         i18n.LegacyWhyDenied,
	legacy.WhyMoveFailed:     i18n.LegacyWhyMoveFailed,
}

func legacyWhyText(l orchestrator.LegacyLeftover) string {
	if l.Why == legacy.WhyShortID {
		if l.ReasonArg != "" {
			return i18n.T(i18n.LegacyWhyShortID, l.ReasonArg)
		}
		return i18n.T(i18n.LegacyWhyShortIDUnknown)
	}
	if id, ok := legacyWhy[l.Why]; ok {
		return i18n.T(id)
	}
	return l.Why
}

func legacyKindText(l orchestrator.LegacyLeftover) string {
	switch legacy.LeftoverKind(l.Kind) {
	case legacy.KindOtherAccount:
		owner := string(l.Owner)
		if owner == "" {
			owner = l.OwnerFolder
		}
		return i18n.T(i18n.LegacyKindOtherAccount, owner)
	case legacy.KindUnrecognized:
		return i18n.T(i18n.LegacyKindUnrecognized)
	case legacy.KindMixedOrgStub:
		return i18n.T(i18n.LegacyKindMixedOrgStub)
	case legacy.KindMixedOrg:
		return i18n.T(i18n.LegacyKindMixedOrg)
	}
	return l.Kind
}

func legacyActionText(l orchestrator.LegacyLeftover) string {
	switch l.Action {
	case orchestrator.ActionEligible:
		return i18n.T(i18n.LegacyActionEligible)
	case orchestrator.ActionDeferred:
		return i18n.T(i18n.LegacyActionDeferred, legacyWhyText(l))
	case orchestrator.ActionMoved:
		return i18n.T(i18n.LegacyActionMoved, l.Generation)
	case orchestrator.ActionFailed:
		return i18n.T(i18n.LegacyActionFailed)
	}
	return i18n.T(i18n.LegacyActionReportOnly, legacyWhyText(l))
}

var legacySeedReason = map[string]i18n.Msg{
	orchestrator.SeedNoSeedFlag:       i18n.LegacySeedReasonNoSeedFlag,
	orchestrator.SeedNoManifest:       i18n.LegacySeedReasonNoManifest,
	orchestrator.SeedTooFew:           i18n.LegacySeedReasonTooFew,
	orchestrator.SeedHasBases:         i18n.LegacySeedReasonHasBases,
	orchestrator.SeedHasCanonical:     i18n.LegacySeedReasonHasCanonical,
	orchestrator.SeedRunOpen:          i18n.LegacySeedReasonRunOpen,
	orchestrator.SeedHintMidSwitch:    i18n.LegacySeedReasonHintMidSwitch,
	orchestrator.SeedDuplicateAccount: i18n.LegacySeedReasonDuplicateAccount,
	orchestrator.SeedProbeFailed:      i18n.LegacySeedReasonProbeFailed,
	orchestrator.SeedOrgUnknown:       i18n.LegacySeedReasonOrgUnknown,
	orchestrator.SeedUnreadable:       i18n.LegacySeedReasonUnreadable,
	orchestrator.SeedNoAdapter:        i18n.LegacySeedReasonNoAdapter,
}

func legacySeedReasonText(r string) string {
	if id, ok := legacySeedReason[r]; ok {
		return i18n.T(id)
	}
	return r
}

// legacyWarningLines turns the result's warning ids into text.
func legacyWarningLines(res *orchestrator.LegacyResult) []string {
	var out []string
	if h := res.Hint; h != nil {
		if h.Used {
			out = append(out, i18n.T(i18n.LegacyHintUsed, string(res.HintAlias)))
		}
		dropped := false
		for _, w := range h.Warnings {
			switch legacy.HintWarning(w) {
			case legacy.HintStale:
				out = append(out, i18n.T(i18n.LegacyHintStale))
			case legacy.HintMidSwitch:
				out = append(out, i18n.T(i18n.LegacyHintMidSwitch))
			default:
				if !dropped {
					out = append(out, i18n.T(i18n.LegacyHintDropped))
					dropped = true
				}
			}
		}
	}
	for _, w := range res.Warnings {
		switch {
		case w == orchestrator.LegacyWarnRunOpen:
			out = append(out, i18n.T(i18n.LegacyWarnRunOpen))
		case w == orchestrator.LegacyWarnPendingAdd:
			out = append(out, i18n.T(i18n.LegacyWarnPendingAdd))
		case w == orchestrator.LegacyWarnNoActive:
			out = append(out, i18n.T(i18n.LegacyWarnNoActive))
		case w == orchestrator.LegacyWarnNoActiveIdentity:
			out = append(out, i18n.T(i18n.LegacyWarnNoActiveIdentity))
		case strings.HasPrefix(w, orchestrator.LegacyWarnForeign):
			out = append(out, i18n.T(i18n.LegacyWarnForeign, strings.TrimPrefix(w, orchestrator.LegacyWarnForeign)))
		case strings.HasPrefix(w, orchestrator.LegacyWarnMarker):
			out = append(out, i18n.T(i18n.LegacyWarnMarker, strings.TrimPrefix(w, orchestrator.LegacyWarnMarker)))
		case strings.HasPrefix(w, orchestrator.LegacyWarnFolderChanged):
			out = append(out, i18n.T(i18n.LegacyWarnFolderChanged, strings.TrimPrefix(w, orchestrator.LegacyWarnFolderChanged)))
		case strings.HasPrefix(w, orchestrator.LegacyWarnLeftovers):
			out = append(out, i18n.T(i18n.LegacyWarnLeftoversUnreadable, strings.TrimPrefix(w, orchestrator.LegacyWarnLeftovers)))
		case w == orchestrator.LegacyWarnPrintedDiffers:
			out = append(out, i18n.T(i18n.LegacyWarnPrintedDiffers))
		}
	}
	return out
}

// legacyLines is the text of a result (spec-legacy-adopt 6), the
// DESTRUCTIVE line last.
func legacyLines(res *orchestrator.LegacyResult) []string {
	var out []string
	if res.DryRun {
		out = append(out, i18n.T(i18n.LegacyHeaderDry))
	} else {
		out = append(out, i18n.T(i18n.LegacyHeaderApply))
	}
	ok := res.Status == legacy.PlanOK
	adopted, eligible := 0, 0
	var names []string
	for _, f := range res.Folders {
		org := f.Org
		if org == "" {
			org = i18n.T(i18n.LegacyOrgUnknown)
		}
		switch legacy.Status(f.Status) {
		case legacy.StatusAdopt:
			id := i18n.LegacyFolderAdopt
			if f.Active {
				id = i18n.LegacyFolderActive
			}
			out = append(out, i18n.T(id, f.Folder, string(f.Alias), f.Account, org))
			adopted++
			names = append(names, string(f.Alias))
		case legacy.StatusFinish:
			out = append(out, i18n.T(i18n.LegacyFolderFinish, f.Folder, string(f.Alias)))
			adopted++
			names = append(names, string(f.Alias))
		case legacy.StatusRegistered:
			out = append(out, i18n.T(i18n.LegacyFolderRegistered, f.Folder, string(f.Alias)))
		default:
			out = append(out, i18n.T(i18n.LegacyFolderSkip, f.Folder, legacyReasonText(f)))
		}
	}
	if ok {
		var left []string
		for _, f := range res.Folders {
			for _, l := range f.Leftovers {
				left = append(left, i18n.T(i18n.LegacyLeftoverItem, f.Folder, l.Path, legacyKindText(l), l.Files, render.Bytes(l.Bytes), legacyActionText(l)))
				if l.Unique > 0 {
					left = append(left, i18n.T(i18n.LegacyLeftoverUnique, l.Unique, l.OwnerFolder))
				}
				if l.Action == orchestrator.ActionEligible {
					eligible++
				}
			}
		}
		if len(left) > 0 {
			out = append(out, i18n.T(i18n.LegacyLeftoverHeader))
			out = append(out, left...)
		}
		out = append(out, legacySeedLines(res.Seed)...)
	}
	out = append(out, legacyWarningLines(res)...)
	switch {
	case !ok:
	case res.DryRun:
		if adopted > 0 || eligible > 0 {
			out = append(out, i18n.T(i18n.LegacyDryRun))
			out = append(out, i18n.T(i18n.LegacyPlanHash, res.PlanHash, res.ApplyCommand))
		}
		if eligible > 0 {
			if !res.Quarantine {
				out = append(out, i18n.T(i18n.LegacyLeftoverOffer, eligible))
			}
			out = append(out, i18n.T(i18n.LegacyPlanQuarantineTerminal))
		}
	default:
		if adopted > 0 {
			out = append(out, i18n.T(i18n.LegacyAdopted, adopted, strings.Join(names, ", ")))
		}
		for _, g := range res.Generations {
			out = append(out, i18n.T(i18n.LegacyQuarantined, g.Moved, g.Planned, g.Folder, g.N, g.N))
		}
	}
	nothing := ok && adopted == 0 && len(res.Generations) == 0
	if nothing {
		out = append(out, i18n.T(i18n.LegacyNothing))
	}
	// Next steps after an apply, and with "nothing to adopt" (the folders
	// may already be profiles: the earlier script still has to go).
	if ok && (!res.DryRun || nothing) {
		out = append(out, i18n.T(i18n.LegacyNextHeader), i18n.T(i18n.LegacyNextRetire), i18n.T(i18n.LegacyNextSkill),
			i18n.T(i18n.LegacyNextBackups), i18n.T(i18n.LegacyNextPlugin), i18n.T(i18n.LegacyNextPlan))
	}
	if res.DryRun {
		n := 0
		if res.Quarantine {
			n = res.Moves
		}
		return append(out, i18n.T(i18n.LegacyDestructiveDry, n))
	}
	return append(out, i18n.T(i18n.LegacyDestructive, res.Moves))
}

// legacySeedLines is the seed block.
func legacySeedLines(s orchestrator.LegacySeed) []string {
	if !s.Seeded {
		return []string{i18n.T(i18n.LegacySeedNone, legacySeedReasonText(s.Reason))}
	}
	out := []string{i18n.T(i18n.LegacySeedHeader)}
	for _, r := range s.Rows {
		switch {
		case r.Seeded:
			out = append(out, i18n.T(i18n.LegacySeedRow, string(r.Row), r.Agreed, r.Differing))
		case r.Reason != orchestrator.SeedNoAdapter:
			out = append(out, i18n.T(i18n.LegacySeedRowSkipped, string(r.Row), legacySeedReasonText(r.Reason)))
		}
	}
	return append(out, i18n.T(i18n.LegacySeedFirstSwitch))
}

// printLegacy prints a result through the printer's sanitizer.
func printLegacy(p *render.Printer, res *orchestrator.LegacyResult) {
	p.Lines(legacyLines(res))
}
