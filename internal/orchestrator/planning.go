package orchestrator

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

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
}

// rowWork keeps one row's merge inputs and outputs for later phases.
type rowWork struct {
	Spec         compat.RowSpec // unexpanded
	S, T         *model.RowSnapshot
	Out          *rows.Output
	SKey, TKey   state.BaseKey
	Stamps       map[string]int64
	TBaseInvalid bool
}

// planned is a computed plan plus its row work.
type planned struct {
	Plan *plan.Plan
	Rows []model.RowID
	Work map[model.RowID]*rowWork
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
		case len(only) > 0 && !slices.Contains(only, cat):
			skipped[r] = "not selected (--only)"
		case slices.Contains(skip, cat):
			skipped[r] = "skipped (--skip)"
		case slices.Contains(dec.SkippedRows, r):
			skipped[r] = "skipped by the compatibility check"
		case !ok:
			skipped[r] = "not covered by the compatibility manifest"
		case spec.WritePath != compat.PathOff && spec.WritePath != compat.PathOffVerify:
			skipped[r] = "not carried in this version"
		case o.d.Adapters[r] == nil || o.d.Rows[r] == nil:
			skipped[r] = "no adapter"
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
	pl := &planned{Rows: in.Rows, Work: map[model.RowID]*rowWork{}}
	outs := map[model.RowID]*rows.Output{}
	var envRemap map[string]string
	acks := o.permissionAcks(ctx, in.T)
	for _, r := range in.Rows {
		if err := ctx.Err(); err != nil {
			return nil, pderr.Wrap(pderr.Declined, "interrupted", err)
		}
		w, err := o.mergeRow(ctx, in, r, acks, envRemap)
		if err != nil {
			return nil, err
		}
		pl.Work[r] = w
		outs[r] = w.Out
		if r == model.RowEnvironments && w.Out.EnvRemap != nil {
			envRemap = w.Out.EnvRemap
		}
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

func (o *Orchestrator) permissionAcks(ctx context.Context, t sideRef) map[string]bool {
	if o.d.PermissionAcks == nil {
		return nil
	}
	acks, err := o.d.PermissionAcks(ctx, t.Folder, t.Replica)
	if err != nil {
		o.progress(P2Preview, "warn", "permission acknowledgements of the entering folder could not be read")
		return nil
	}
	return acks
}

// mergeRow reads one row on both sides and merges it.
func (o *Orchestrator) mergeRow(ctx context.Context, in planInput, r model.RowID, acks map[string]bool, envRemap map[string]string) (*rowWork, error) {
	spec, _ := o.rowSpec(in.Manifest, r)
	ad := o.d.Adapters[r]
	sRC := sources.ReadContext{Folder: in.S.Folder, Replica: in.S.Replica, LS: in.S.LS, Spec: spec.Expand(in.S.Replica), Manifest: in.Manifest}
	tRC := sources.ReadContext{Folder: in.T.Folder, Replica: in.T.Replica, LS: in.T.LS, Spec: spec.Expand(in.T.Replica), Manifest: in.Manifest}
	sSnap, err := ad.Read(ctx, sRC)
	if err != nil {
		return nil, pderr.Wrap(pderr.Internal, fmt.Sprintf("read %s (leaving)", r), err)
	}
	tSnap, err := ad.Read(ctx, tRC)
	if err != nil {
		return nil, pderr.Wrap(pderr.Internal, fmt.Sprintf("read %s (entering)", r), err)
	}
	for _, snap := range []*model.RowSnapshot{sSnap, tSnap} {
		if snap != nil && len(snap.Stale) > 0 {
			o.progress(P2Preview, "warn", fmt.Sprintf("%s: %d stale Local Storage value(s) ignored; the config mirror wins", r, len(snap.Stale)))
		}
	}
	w := &rowWork{
		Spec: spec, S: sSnap, T: tSnap,
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
		Policy:          o.policy(in, cat, spec, acks, envRemap),
	}
	if r != model.RowSessions {
		mi.Policy.EnvRemap = nil
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
			o.progress(P2Preview, "warn", fmt.Sprintf("%s: the base of %s is unreadable and is treated as invalid", k.Row, s.Alias))
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
		LeavingExports:  categoryListed(sync.Export, in.S.Alias, cat),
		EnteringExports: categoryListed(sync.Export, in.T.Alias, cat),
		EnteringImports: categoryListed(sync.Import, in.T.Alias, cat),
		LeavingManaged:  in.S.Managed,
		EnteringManaged: in.T.Managed,
		PermissionAcks:  acks,
		EnvRemap:        envRemap,
		Fields:          spec.Fields,
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

// categoryListed reads sync.export / sync.import: an alias without an
// entry exports and imports everything; otherwise only the listed
// categories ("all" or "*" list everything).
func categoryListed(m map[string][]string, a ids.Alias, cat model.Category) bool {
	list, ok := m[string(a)]
	if !ok {
		return true
	}
	for _, c := range list {
		if c == string(cat) || c == "all" || c == "*" {
			return true
		}
	}
	return false
}

// composePlan turns every row's Desired into edits and composes the plan.
func (o *Orchestrator) composePlan(ctx context.Context, in planInput, pl *planned) (*plan.Plan, error) {
	now := o.now()
	p := &plan.Plan{
		Version: plan.Version, RunID: in.RunID, From: in.S.Alias,
		Target:      plan.Target{Alias: in.T.Alias, Replica: in.T.Replica, Folder: in.T.Folder},
		Approximate: in.Approximate, CreatedAt: now, ExpiresAt: now.Add(ConfirmedPlanTTL),
		Skipped: in.Skipped,
	}
	var edits []plan.Edit
	for _, r := range pl.Rows {
		w := pl.Work[r]
		spec, _ := o.rowSpec(in.Manifest, r)
		tRC := sources.ReadContext{Folder: in.T.Folder, Replica: in.T.Replica, LS: in.T.LS, Spec: spec.Expand(in.T.Replica), Manifest: in.Manifest}
		sRC := sources.ReadContext{Folder: in.S.Folder, Replica: in.S.Replica, LS: in.S.LS, Spec: spec.Expand(in.S.Replica), Manifest: in.Manifest}
		res, err := o.d.Adapters[r].Ops(ctx, sources.OpsContext{ReadContext: tRC, NowMillis: now.UnixMilli(), Source: &sRC}, w.T, w.Out.Desired)
		if err != nil {
			return nil, pderr.Wrap(pderr.Internal, fmt.Sprintf("ops %s", r), err)
		}
		edits = append(edits, res.Edits...)
		w.Stamps = res.WrittenStamps
		p.Changes = append(p.Changes, w.Out.Changes...)
		p.Changes = append(p.Changes, res.Skipped...)
		p.Conflicts = append(p.Conflicts, w.Out.Conflicts...)
		p.NeedsConfirmation = p.NeedsConfirmation || w.Out.NeedsConfirmation
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
		if w.T != nil {
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
		CreatedAt: now, ExpiresAt: now.Add(ConfirmedPlanTTL), Skipped: skipped,
	}
}

// expiry returns when a confirmation asked now stops being valid.
func (o *Orchestrator) expiry() time.Time { return o.now().Add(ConfirmedPlanTTL) }
