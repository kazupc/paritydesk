package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/rows"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/state"
	"github.com/kazupc/paritydesk/internal/store/kv"
)

// sideRef names one profile's replica and folder for a merge.
type sideRef struct {
	Alias     ids.Alias
	Replica   model.Replica
	Folder    string
	LS        kv.Reader
	BaseValid bool
	Managed   bool
}

// planInput is everything buildPlan needs.
type planInput struct {
	S, T        sideRef
	Manifest    *compat.Manifest
	Root        *state.Root
	Rows        []model.RowID
	Skipped     map[model.RowID]string
	RunID       string
	Approximate bool
	// Seeded: the entering profile's bases were seeded by profile adopt
	// --from-legacy and this is its first sync.
	Seeded bool
	// Preview is P2's plan when this is P5 (nil at P2).
	Preview *plan.Plan
}

// rowWork keeps one row's merge inputs and outputs for later phases.
type rowWork struct {
	Spec         compat.RowSpec // unexpanded
	S, T         *model.RowSnapshot
	Out          *rows.Output
	SKey, TKey   state.BaseKey
	Stamps       map[string]int64
	TBaseInvalid bool
	// Refs are the keys a sources.RefReader row was read with (nil for
	// every other row).
	Refs []string
}

// planned is a computed plan plus its row work.
type planned struct {
	Plan *plan.Plan
	Rows []model.RowID
	Work map[model.RowID]*rowWork
	// Skipped are the rows left out while planning (a source that could
	// not be read), with their reasons; composePlan copies them into
	// Plan.Skipped.
	Skipped map[model.RowID]string
	// AuxSessions are the sessions-row snapshots of both sides when the
	// sessions row is not planned but another row needs its facts.
	AuxSessions struct{ S, T *model.RowSnapshot }
	// SoftSkipped are the data-folder-relative paths of row scratch's soft
	// copies apply left as they were (P7, spec-scratch 3.3).
	SoftSkipped []string
	// Pins is what reading row pins found (nil when the row was not
	// planned).
	Pins *pinsInfo
}

// selectRows returns the rows to plan, in model.AllRows order, and the
// reasons for every row left out.
func (o *Orchestrator) selectRows(m *compat.Manifest, dec compat.Decision, only, skip []model.Category) ([]model.RowID, map[model.RowID]string) {
	skipped := map[model.RowID]string{}
	var out []model.RowID
	for _, r := range model.AllRows {
		cat := model.CategoryOf(r)
		spec, ok := compat.RowSpec{}, false
		if m != nil {
			spec, ok = m.Row(r)
		}
		switch {
		case !o.d.Config.RowEnabled(r):
			skipped[r] = "disabled in the config"
		case len(only) > 0 && !slices.Contains(only, cat) && (model.ImpliedBy(cat) == "" || !slices.Contains(only, model.ImpliedBy(cat))):
			skipped[r] = "not selected (--only)"
		case slices.Contains(skip, cat):
			skipped[r] = "skipped (--skip)"
		case slices.Contains(dec.SkippedRows, r):
			skipped[r] = "skipped by the compatibility check"
		case !ok:
			skipped[r] = "not covered by the compatibility manifest"
		case spec.WritePath == compat.PathOn && !o.onlineRowSelectable(m, r):
			skipped[r] = reasonOnlineOff
		case spec.WritePath != compat.PathOff && spec.WritePath != compat.PathOffVerify && spec.WritePath != compat.PathOn:
			skipped[r] = "not carried in this version"
		case o.d.Adapters[r] == nil || o.d.Rows[r] == nil:
			skipped[r] = "no adapter"
		case r == model.RowSidebarSeed && !slices.Contains(out, model.RowSidebarView):
			skipped[r] = reasonNeedsSidebarView
		default:
			out = append(out, r)
			continue
		}
	}
	return out, skipped
}

// buildPlan reads both sides, merges every row, and composes the plan for
// the entering folder.
func (o *Orchestrator) buildPlan(ctx context.Context, in planInput) (*planned, error) {
	pl := &planned{Work: map[model.RowID]*rowWork{}}
	outs := map[model.RowID]*rows.Output{}
	acks := o.permissionAcks(ctx, in.Manifest, in.T)
	for _, r := range in.Rows {
		if err := ctx.Err(); err != nil {
			return nil, pderr.Wrap(pderr.Declined, "interrupted", err)
		}
		var refs []string
		var notes []model.Change
		if r == model.RowScratch {
			reason := o.scratchGate(in)
			if sess := outs[model.RowSessions]; reason == "" && (sess == nil || sess.Desired == nil) {
				// The sessions row was left out while planning (a source
				// that could not be read): its refs are unknown, and an
				// empty carry set would prune the row's canonical state.
				reason = reasonScratchSessions
			}
			if reason != "" {
				if pl.Skipped == nil {
					pl.Skipped = map[model.RowID]string{}
				}
				pl.Skipped[r] = reason
				continue
			}
			var err error
			if refs, _, notes, err = o.scratchCarry(in, outs); err != nil {
				return nil, err
			}
			if refs == nil {
				refs = []string{}
			}
		}
		var w *rowWork
		var err error
		if r == model.RowPins {
			w, err = o.mergePins(ctx, in, pl, acks)
		} else {
			w, err = o.mergeRow(ctx, in, r, acks, outs, refs)
		}
		if err == nil && r == model.RowScratch {
			o.scratchReadNotes(in, w, notes)
		}
		var skip *errRowSkipped
		if errors.As(err, &skip) {
			// Only this row is left out: it is not merged, written or
			// committed, and every other row is planned as usual. No
			// generic warning is printed here: the unit that owns the
			// skip reason emits its own (e.g. spec-pins 6.4 item 3).
			if pl.Skipped == nil {
				pl.Skipped = map[model.RowID]string{}
			}
			pl.Skipped[r] = skip.Reason
			continue
		}
		if err != nil {
			return nil, err
		}
		pl.Rows = append(pl.Rows, r)
		pl.Work[r] = w
		outs[r] = w.Out
	}
	if o.d.PostMerge != nil {
		if err := o.d.PostMerge(outs); err != nil {
			return nil, pderr.Wrap(pderr.Internal, "merge", err)
		}
	}
	p, err := o.composePlan(ctx, in, pl)
	if err != nil {
		return nil, err
	}
	pl.Plan = p
	return pl, nil
}

// permissionAcks returns the entering folder's acknowledgements keyed by
// permission mode: prefs are read by name (Deps.PermissionAcks) and mapped
// to modes through the manifest's permission_acks table.
func (o *Orchestrator) permissionAcks(ctx context.Context, m *compat.Manifest, t sideRef) map[string]bool {
	if o.d.PermissionAcks == nil {
		return nil
	}
	prefs, err := o.d.PermissionAcks(ctx, t.Folder, t.Replica)
	if err != nil {
		o.progress(P2Preview, "warn", "permission acknowledgements of the entering folder could not be read")
		return nil
	}
	var table map[string][]string
	if m != nil {
		table = m.PermissionAcks
	}
	return modeAcks(prefs, table)
}

// modeAcks maps preference-keyed acknowledgements to permission modes: a
// mode is acknowledged when any preference the table lists for it is true.
func modeAcks(prefs map[string]bool, table map[string][]string) map[string]bool {
	out := map[string]bool{}
	for mode, names := range table {
		for _, n := range names {
			if prefs[n] {
				out[mode] = true
			}
		}
	}
	return out
}

// mergeRow reads one row on both sides and merges it. prior holds the
// outputs of the rows merged before r in this plan (the environment remap
// comes from there); refs are the keys a sources.RefReader row is read
// with (nil for every other row). A source that cannot be read this time
// (sources.ErrSourceUnavailable) returns *errRowSkipped, so the caller
// leaves only this row out.
func (o *Orchestrator) mergeRow(ctx context.Context, in planInput, r model.RowID, acks map[string]bool, prior map[model.RowID]*rows.Output, refs []string) (*rowWork, error) {
	spec, _ := o.rowSpec(in.Manifest, r)
	ad := o.d.Adapters[r]
	if r == model.RowScratch {
		o.scratchAdapter() // configures the registry's adapter from [scratch]
	}
	sRC := sources.ReadContext{Folder: in.S.Folder, Replica: in.S.Replica, LS: in.S.LS, Spec: spec.Expand(in.S.Replica), Manifest: in.Manifest}
	tRC := sources.ReadContext{Folder: in.T.Folder, Replica: in.T.Replica, LS: in.T.LS, Spec: spec.Expand(in.T.Replica), Manifest: in.Manifest}
	sSnap, err := readAdapter(ctx, ad, sRC, refs)
	if err != nil {
		return nil, readError(r, "leaving", err)
	}
	tSnap, err := readAdapter(ctx, ad, tRC, refs)
	if err != nil {
		return nil, readError(r, "entering", err)
	}
	for _, snap := range []*model.RowSnapshot{sSnap, tSnap} {
		if snap != nil && len(snap.Stale) > 0 {
			// A stale Local Storage copy that the mirror overrides needs
			// nothing from the person: it goes to the run log only.
			status := StatusInfo
			if r == model.RowEnvironments {
				status = "warn"
			}
			o.progress(P2Preview, status, staleWarning(r, len(snap.Stale)))
		}
	}
	w := &rowWork{
		Spec: spec, S: sSnap, T: tSnap, Refs: refs,
		SKey: state.BaseKey{Alias: in.S.Alias, Replica: in.S.Replica, Row: r},
		TKey: state.BaseKey{Alias: in.T.Alias, Replica: in.T.Replica, Row: r},
	}
	leaving := o.side(in.Root, in.S, w.SKey, sSnap)
	entering := o.side(in.Root, in.T, w.TKey, tSnap)
	cat := model.CategoryOf(r)
	mi := rows.Input{
		Row: r, Leaving: leaving, Entering: entering,
		Canonical:       o.object(in.Root.Canonical[r]),
		LeavingOverlay:  o.object(in.Root.Overlays[w.SKey.String()]),
		EnteringOverlay: o.object(in.Root.Overlays[w.TKey.String()]),
		Policy:          o.policy(in, cat, spec, acks, envRemapOf(prior)),
	}
	if r != model.RowSessions && r != model.RowSidebarCollapse {
		mi.Policy.EnvRemap = nil
	}
	if err := o.sidebarPolicy(ctx, r, ad, tRC, spec, prior, &mi.Policy); err != nil {
		return nil, err
	}
	if r == model.RowScratch {
		var err error
		if mi.Policy.Scratch, err = o.scratchPolicy(in, refs, prior); err != nil {
			return nil, err
		}
	}
	out, err := o.d.Rows[r].Merge(mi)
	if err != nil {
		return nil, pderr.Wrap(pderr.Internal, fmt.Sprintf("merge %s", r), err)
	}
	w.Out = &out
	return w, nil
}

// rowSpec returns row r's manifest spec with the field lists that come
// from the config rather than the manifest: the app-prefs allow-list
// (sync.app_prefs, read by the adapter as sources.RoleAllow). The
// manifest's own maps are never modified.
func (o *Orchestrator) rowSpec(m *compat.Manifest, r model.RowID) (compat.RowSpec, bool) {
	if m == nil {
		return compat.RowSpec{}, false
	}
	spec, ok := m.Row(r)
	if !ok || r != model.RowAppPrefs || o.d.Config == nil {
		return spec, ok
	}
	fields := make(map[string][]string, len(spec.Fields)+1)
	maps.Copy(fields, spec.Fields)
	fields[sources.RoleAllow] = slices.Clone(o.d.Config.Sync.AppPrefs)
	spec.Fields = fields
	return spec, true
}

// side builds a merge side with its base, contested items and stamps.
func (o *Orchestrator) side(r *state.Root, s sideRef, k state.BaseKey, snap *model.RowSnapshot) rows.Side {
	sd := rows.Side{Alias: s.Alias, Replica: s.Replica, Snapshot: snap, Contested: contestedFor(r, k), ToolStamps: r.ToolStamps[k.String()]}
	if ps := r.Profiles[s.Alias]; ps != nil {
		sd.LiveEpoch = ps.LiveEpoch
	}
	if ref, ok := r.Bases[k.String()]; ok && s.BaseValid {
		base, err := o.d.State.GetSnapshot(ref)
		if err != nil {
			o.progressNaming(P2Preview, "warn", fmt.Sprintf("%s: the base of %s is unreadable and is treated as invalid", k.Row, s.Alias), s.Alias)
		} else {
			sd.Base, sd.BaseValid = base, true
		}
	}
	return sd
}

// object loads a stored snapshot, or nil when missing or unreadable.
func (o *Orchestrator) object(ref state.Ref) *model.RowSnapshot {
	if ref == "" {
		return nil
	}
	s, err := o.d.State.GetSnapshot(ref)
	if err != nil {
		return nil
	}
	return s
}

// expandExcludes expands a leading "~" or "~/" in sync.exclude patterns,
// as data_root is expanded, so "~/clients/**" matches absolute project
// paths.
func expandExcludes(home string, pats []string) []string {
	if home == "" || len(pats) == 0 {
		return pats
	}
	out := make([]string, len(pats))
	for i, p := range pats {
		out[i] = config.ExpandHome(home, p)
	}
	return out
}

// policy builds the merge policy for one row.
func (o *Orchestrator) policy(in planInput, cat model.Category, spec compat.RowSpec, acks map[string]bool, envRemap map[string]string) rows.Policy {
	sync := o.d.Config.Sync
	p := rows.Policy{
		TombstoneMode:   sync.Tombstones,
		Exclude:         expandExcludes(o.d.Home, sync.Exclude),
		LeavingExports:  config.CategoryListed(sync.Export, in.S.Alias, cat),
		EnteringExports: config.CategoryListed(sync.Export, in.T.Alias, cat),
		EnteringImports: config.CategoryListed(sync.Import, in.T.Alias, cat),
		LeavingManaged:  in.S.Managed,
		EnteringManaged: in.T.Managed,
		PermissionAcks:  acks,
		EnvRemap:        envRemap,
		Fields:          spec.Fields,
		Limits:          spec.Limits,
	}
	p.SecretsMayCross, p.SecretsAllow = o.secrets(in.S.Managed, in.T.Managed)
	return p
}

// secrets applies sync.secrets: secrets may cross only between profiles
// that are both unmanaged ("same-boundary"), plus the explicit exceptions.
func (o *Orchestrator) secrets(sManaged, tManaged bool) (bool, map[string]bool) {
	sync := o.d.Config.Sync
	may := sync.Secrets == "same-boundary" && !sManaged && !tManaged
	var allow map[string]bool
	if len(sync.SecretsAllow) > 0 {
		allow = map[string]bool{}
		for _, k := range sync.SecretsAllow {
			allow[k] = true
		}
	}
	return may, allow
}

// composePlan turns every row's Desired into edits and composes the plan.
func (o *Orchestrator) composePlan(ctx context.Context, in planInput, pl *planned) (*plan.Plan, error) {
	now := o.now()
	p := &plan.Plan{
		Version: plan.Version, RunID: in.RunID, From: in.S.Alias,
		Target:      plan.Target{Alias: in.T.Alias, Replica: in.T.Replica, Folder: in.T.Folder},
		Approximate: in.Approximate, CreatedAt: now,
		Skipped: mergeSkipped(in.Skipped, pl.Skipped),
	}
	var edits []plan.Edit
	var dropped []model.RowID
	for _, r := range pl.Rows {
		w := pl.Work[r]
		spec, _ := o.rowSpec(in.Manifest, r)
		tRC := sources.ReadContext{Folder: in.T.Folder, Replica: in.T.Replica, LS: in.T.LS, Spec: spec.Expand(in.T.Replica), Manifest: in.Manifest}
		sRC := sources.ReadContext{Folder: in.S.Folder, Replica: in.S.Replica, LS: in.S.LS, Spec: spec.Expand(in.S.Replica), Manifest: in.Manifest}
		res, err := o.d.Adapters[r].Ops(ctx, sources.OpsContext{ReadContext: tRC, NowMillis: now.UnixMilli(), Source: &sRC,
			SourceSnapshot: w.S}, w.T, w.Out.Desired)
		if err != nil {
			return nil, pderr.Wrap(pderr.Internal, fmt.Sprintf("ops %s", r), err)
		}
		if r == model.RowScratch && o.scratchCompose(in, w, &res, p) {
			// Not enough free space: the row leaves the harvested set, as
			// selectRows leaves a row out; nothing of it is committed.
			if pl.Skipped == nil {
				pl.Skipped = map[model.RowID]string{}
			}
			pl.Skipped[r] = reasonScratchSpace
			dropped = append(dropped, r)
			continue
		}
		edits = append(edits, res.Edits...)
		w.Stamps = res.WrittenStamps
		if w.Spec.WritePath == compat.PathOn {
			// Applied in the app with the user's answer (the online queue):
			// not part of the plan's changes, hash, DESTRUCTIVE line or
			// deletion breaker (spec-pins 7.1, 12.3).
			continue
		}
		p.Changes = append(p.Changes, w.Out.Changes...)
		p.Changes = append(p.Changes, res.Skipped...)
		p.Conflicts = append(p.Conflicts, w.Out.Conflicts...)
		p.NeedsConfirmation = p.NeedsConfirmation || w.Out.NeedsConfirmation
	}
	for _, r := range dropped {
		pl.Rows = slices.DeleteFunc(pl.Rows, func(x model.RowID) bool { return x == r })
		delete(pl.Work, r)
	}
	if len(dropped) > 0 {
		p.Skipped = mergeSkipped(in.Skipped, pl.Skipped)
	}
	if len(edits) > 0 {
		ops, err := o.d.Compose(edits, folderSource(in.T.Folder, in.T.LS))
		if err != nil {
			return nil, pderr.Wrap(pderr.Internal, "compose", err)
		}
		p.Ops = ops
	}
	return p, nil
}

// beforeCounts counts the entering replica's items per category.
func beforeCounts(pl *planned) map[model.Category]int {
	m := map[model.Category]int{}
	for r, w := range pl.Work {
		switch {
		case w.T == nil, w.Spec.WritePath == compat.PathOn:
		case r == model.RowScratch:
			m[model.CategoryOf(r)] += scratchCountable(w.T)
		default:
			m[model.CategoryOf(r)] += len(w.T.Items)
		}
	}
	return m
}

// emptyPlan is the plan of a swap-only run.
func (o *Orchestrator) emptyPlan(runID string, s, t sideRef, skipped map[model.RowID]string) *plan.Plan {
	now := o.now()
	return &plan.Plan{
		Version: plan.Version, RunID: runID, From: s.Alias,
		Target:    plan.Target{Alias: t.Alias, Replica: t.Replica, Folder: t.Folder},
		CreatedAt: now, Skipped: skipped,
	}
}

// staleWarning describes values on which a row's two stored copies
// disagree. For presets neither copy wins: the presets are not written
// while the copies disagree.
func staleWarning(r model.RowID, n int) string {
	if r == model.RowEnvironments {
		return fmt.Sprintf("%s: %d preset(s) differ between Local Storage and ccd-session-presets.json; presets are not written until they agree", r, n)
	}
	return fmt.Sprintf("%s: %d stale Local Storage value(s) ignored; the config mirror wins", r, n)
}
