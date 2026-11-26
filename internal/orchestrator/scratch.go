package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kazupc/paritydesk/internal/apply"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/render"
	"github.com/kazupc/paritydesk/internal/rows"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/sources/scratch"
	"github.com/kazupc/paritydesk/internal/state"
)

// Row scratch's Plan.Skipped reasons (plain strings, spec-scratch 13.2).
const (
	reasonScratchSync     = "carried only by switch"
	reasonScratchSessions = "needs the sessions row"
	reasonScratchManaged  = render.ReasonScratchManaged
	reasonScratchSpace    = render.ReasonScratchSpace
)

// scratchSpaceMargin is the free space kept on top of the copy bytes.
const scratchSpaceMargin = 500 << 20

// scratchPostHashesKind is the state object kind of row scratch's
// post-hashes.
const scratchPostHashesKind = "scratch-post-hashes"

// intentScratchRefs is the planned intent's data key of the carry set.
const intentScratchRefs = "scratch_refs"

// statCacheRel is the stat cache file below the state directory.
const statCacheRel = "cache/scratch-stat.json"

// scratchGate returns why row scratch is not planned in this run, or "":
// a sync (both sides are the same profile), a run without the sessions
// row, or a managed edge while scratch.cross_managed is off (spec-scratch
// 6.9). It runs on the rows selectRows chose, before any read.
func (o *Orchestrator) scratchGate(in planInput) string {
	switch {
	case in.S.Alias == in.T.Alias:
		return reasonScratchSync
	case !slices.Contains(in.Rows, model.RowSessions):
		return reasonScratchSessions
	case (in.S.Managed || in.T.Managed) && !o.d.Config.Scratch.CrossManaged:
		return reasonScratchManaged
	}
	return ""
}

// scratchSpec returns the manifest's [row.scratch] (nil when the
// manifest has no scratch row).
func scratchSpec(m *compat.Manifest) *compat.ScratchSpec {
	if m == nil {
		return nil
	}
	spec, ok := m.Row(model.RowScratch)
	if !ok {
		return nil
	}
	return spec.Scratch
}

// scratchRootOf returns the manifest's scratch root ("" without one).
func scratchRootOf(m *compat.Manifest) string {
	if sc := scratchSpec(m); sc != nil {
		return sc.Root
	}
	return ""
}

// scratchAdapter returns the registry's scratch adapter configured from
// [scratch] (limits, excludes, held-back folders) with the stat cache
// loaded once per process; nil when the registry has none.
func (o *Orchestrator) scratchAdapter() *scratch.Adapter {
	ad, ok := o.d.Adapters[model.RowScratch].(*scratch.Adapter)
	if !ok || ad == nil {
		return nil
	}
	if c := o.d.Config; c != nil {
		ad.Limits = scratchLimits(c.Scratch)
		ad.Exclude = slices.Clone(c.Scratch.Exclude)
		ad.HeldBack = slices.Clone(c.Scratch.CacheDirs)
	}
	if ad.Cache == nil && o.d.Paths.StateDir != "" {
		ad.Cache = scratch.LoadStatCache(o.statCachePath())
	}
	return ad
}

// scratchLimits converts [scratch] into adapter limits (sizes were
// validated with the config).
func scratchLimits(s config.Scratch) scratch.Limits {
	l := scratch.DefaultLimits()
	if s.MaxFilesPerWorkspace > 0 {
		l.MaxFilesPerWorkspace = s.MaxFilesPerWorkspace
	}
	if s.MaxFilesPerRun > 0 {
		l.MaxRunFiles = s.MaxFilesPerRun
	}
	if n, err := config.ParseSize(s.MaxWorkspaceBytes); err == nil && n > 0 {
		l.MaxWorkspaceBytes = n
	}
	if n, err := config.ParseSize(s.MaxRunBytes); err == nil && n > 0 {
		l.MaxRunBytes = n
	}
	return l
}

func (o *Orchestrator) statCachePath() string {
	return filepath.Join(o.d.Paths.StateDir, filepath.FromSlash(statCacheRel))
}

// saveScratchCache writes the stat cache after P10 (never in read-only
// mode). A failure is a warning only.
func (r *run) saveScratchCache() {
	o := r.o
	ad, ok := o.d.Adapters[model.RowScratch].(*scratch.Adapter)
	if !ok || ad == nil || ad.Cache == nil || o.d.ReadOnly || o.d.Paths.StateDir == "" {
		return
	}
	p := o.statCachePath()
	if err := ad.Cache.Save(o.d.FS, p, filepath.Join(filepath.Dir(p), ".tmp")); err != nil {
		r.warn(P10Commit, i18n.Tx(i18n.ScratchStatCacheUnsaved))
	}
}

// scratchCarry finds the workspaces the sessions row's Desired names:
// the carry set (present in S), every referenced key, and the notes for
// workspaces neither folder has and for paths outside this data root.
func (o *Orchestrator) scratchCarry(in planInput, prior map[model.RowID]*rows.Output) (carry, referenced []string, notes []model.Change, err error) {
	sc := scratchSpec(in.Manifest)
	sess := prior[model.RowSessions]
	if sc == nil || sess == nil || sess.Desired == nil {
		return nil, nil, nil, nil
	}
	sessSpec, _ := in.Manifest.Row(model.RowSessions)
	refs, unrec, err := scratch.Refs(sess.Desired, sessSpec.Fields[sources.RoleScratchRef],
		scratch.Prefixes(o.d.DataRoot, sc.Root), sc.LeafRE())
	if err != nil {
		return nil, nil, nil, pderr.Wrap(pderr.Internal, "scratch refs", err)
	}
	for _, ref := range refs {
		referenced = append(referenced, ref.Key)
		inS, sErr := scratch.Present(in.S.Folder, sc.Root, ref.Key)
		if inS || sErr != nil {
			// A symlink or an unreadable path is reported by ReadRefs.
			carry = append(carry, ref.Key)
			continue
		}
		if inT, tErr := scratch.Present(in.T.Folder, sc.Root, ref.Key); !inT && tErr == nil {
			notes = append(notes, sources.Skip(model.RowScratch, ref.Key, scratch.NoteMissing))
		}
	}
	for range unrec {
		notes = append(notes, sources.Skip(model.RowScratch, "", scratch.NoteUnrecognized))
	}
	return carry, referenced, notes, nil
}

// scratchPolicy builds rows.Policy.Scratch for one merge.
func (o *Orchestrator) scratchPolicy(in planInput, refs []string, prior map[model.RowID]*rows.Output) (*rows.ScratchPolicy, error) {
	sc := scratchSpec(in.Manifest)
	if sc == nil {
		return nil, errors.New("orchestrator: the manifest has no [row.scratch]")
	}
	_, referenced, _, err := o.scratchCarry(in, prior)
	if err != nil {
		return nil, err
	}
	cfg := o.d.Config.Scratch
	return &rows.ScratchPolicy{
		PropagateDeletions: cfg.PropagateDeletions,
		Exclude:            append(slices.Clone(sc.Exclude), cfg.Exclude...),
		HeldBack:           append(append(slices.Clone(sc.DependencyDirs), sc.VCSDirs...), cfg.CacheDirs...),
		ReservedGlobs:      slices.Clone(in.Manifest.TmpRecoveryGlobs),
		Leaf:               sc.LeafRE(),
		ConflictDate:       o.now().Local().Format("2006-01-02"),
		Refs:               slices.Clone(refs),
		Referenced:         referenced,
	}, nil
}

// scratchReadNotes appends both snapshots' read notes to the row's
// changes (one per key and note) and, while previewing, reports the
// limit-bearing ones as progress warnings with the configured limits.
func (o *Orchestrator) scratchReadNotes(in planInput, w *rowWork, extra []model.Change) {
	seen := map[string]bool{}
	for _, c := range w.Out.Changes {
		if c.Kind == model.ChangeSkipped {
			seen[c.Key+"\x00"+c.Note] = true
		}
	}
	var heldBack, budget int
	for _, snap := range []*model.RowSnapshot{w.S, w.T} {
		if snap == nil {
			continue
		}
		for _, c := range snap.Notes {
			id := c.Key + "\x00" + c.Note
			if seen[id] {
				continue
			}
			seen[id] = true
			w.Out.Changes = append(w.Out.Changes, c)
		}
	}
	w.Out.Changes = append(w.Out.Changes, extra...)
	if !in.Approximate {
		return
	}
	lim := scratchLimits(o.d.Config.Scratch)
	for _, c := range w.Out.Changes {
		if c.Row != model.RowScratch || c.Kind != model.ChangeSkipped {
			continue
		}
		switch c.Note {
		case scratch.NoteOversizeFiles, scratch.NoteOversizeBytes:
			o.progress(P2Preview, "warn", i18n.Tx(i18n.ScratchOversize, render.ScratchKey(c.Key),
				lim.MaxFilesPerWorkspace, render.Bytes(lim.MaxWorkspaceBytes)))
		case scratch.NoteBudget:
			budget++
		case scratch.NoteHeldBack:
			heldBack++
		}
	}
	if budget > 0 {
		o.progress(P2Preview, "warn", i18n.Tx(i18n.ScratchBudget, budget, render.Bytes(lim.MaxRunBytes), lim.MaxRunFiles))
	}
	if heldBack > 0 {
		o.progress(P2Preview, "warn", i18n.Tx(i18n.ScratchHeldBack, heldBack, string(in.T.Alias)))
	}
}

// scratchCompose post-processes row scratch's Ops result inside
// composePlan (spec-scratch 6.9): per-key skips change Desired only,
// removals the preview did not show are deferred, and the row leaves the
// harvested set when the disk may be too full. skip is true in that last
// case: the caller drops the row's edits, changes and work.
func (o *Orchestrator) scratchCompose(in planInput, w *rowWork, res *sources.OpsResult, p *plan.Plan) (skip bool) {
	lim := scratchLimits(o.d.Config.Scratch)
	notes := &plan.ScratchNotes{MaxFilesPerWorkspace: lim.MaxFilesPerWorkspace, MaxWorkspaceBytes: lim.MaxWorkspaceBytes,
		MaxRunBytes: lim.MaxRunBytes, MaxRunFiles: lim.MaxRunFiles}
	p.ScratchNotes = notes
	o.foldScratchSkips(w, res)
	if in.Preview != nil {
		o.deferScratchRemovals(in.Preview, w, res)
	}
	var need uint64 = scratchSpaceMargin
	for _, e := range res.Edits {
		if e.Copy != nil && e.Copy.Size > 0 {
			need += uint64(e.Copy.Size)
		}
	}
	if need == scratchSpaceMargin {
		return false
	}
	free, err := o.d.FS.FreeBytes(o.d.DataRoot)
	if err == nil && free >= need {
		return false
	}
	phase := P5Harvest
	if in.Approximate {
		phase = P2Preview
	}
	notes.NeedBytes, notes.FreeBytes = int64(need), int64(free)
	o.progress(phase, "warn", i18n.Tx(i18n.ScratchNoSpace, render.Bytes(int64(need)), render.Bytes(int64(free))))
	return true
}

// confirmedPreview returns the confirmed plan of a detached run (nil
// without one, or when it cannot be loaded: confirmP2 reports that). P2
// defers scratch removals against it as P5 defers against P2, so a
// scratch file removed after the person's plan is never a new destructive
// change that stops the run (spec-scratch 3.3).
func (r *run) confirmedPreview() *plan.Plan {
	if r.opts.ConfirmedPlan == "" || r.d().Plans == nil {
		return nil
	}
	p, err := r.d().Plans.Load(r.opts.ConfirmedPlan, r.o.now())
	if err != nil {
		return nil
	}
	return p
}

// tItems returns the T snapshot's items (never nil).
func tItems(w *rowWork) map[string]model.Item {
	if w.T == nil || w.T.Items == nil {
		return map[string]model.Item{}
	}
	return w.T.Items
}

// keepTargetKeys sets Desired of every key that affected reports back to
// T's item (or drops it), and drops the module's changes and the edits
// of those keys. Canonical, overlays and bases are not touched.
func keepTargetKeys(w *rowWork, res *sources.OpsResult, affected func(string) bool) {
	d := w.Out.Desired
	if d == nil {
		return
	}
	for k := range d.Items {
		if affected(k) {
			delete(d.Items, k)
		}
	}
	for k, it := range tItems(w) {
		if affected(k) {
			d.Items[k] = it
		}
	}
	w.Out.Changes = slices.DeleteFunc(w.Out.Changes, func(c model.Change) bool {
		return c.Kind != model.ChangeSkipped && affected(c.Key)
	})
	res.Edits = slices.DeleteFunc(res.Edits, func(e plan.Edit) bool { return e.Item != "" && affected(e.Item) })
}

// foldScratchSkips applies Ops's per-key skips to Desired only: a file
// key takes T's item, a directory key (or a workspace's root marker) its
// whole subtree.
func (o *Orchestrator) foldScratchSkips(w *rowWork, res *sources.OpsResult) {
	var exact, prefixes []string
	for _, c := range res.Skipped {
		switch {
		case c.Key == "":
		case strings.HasSuffix(c.Key, "/"):
			prefixes = append(prefixes, c.Key)
		default:
			exact = append(exact, c.Key)
		}
	}
	if len(exact)+len(prefixes) == 0 {
		return
	}
	keepTargetKeys(w, res, func(k string) bool {
		return slices.Contains(exact, k) || slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(k, p) })
	})
}

// deferScratchRemovals drops every row-scratch removal whose key the
// preview did not show as destructive (spec-scratch 3.3): Desired keeps
// T's item, and a git unit with any deferred member is deferred whole.
func (o *Orchestrator) deferScratchRemovals(preview *plan.Plan, w *rowWork, res *sources.OpsResult) {
	shown := map[string]bool{}
	for _, c := range preview.Changes {
		if c.Row == model.RowScratch && c.Destructive() {
			shown[c.Key] = true
		}
	}
	var files, units []string
	for _, e := range res.Edits {
		if e.Row != model.RowScratch || e.Kind != plan.EditRemove || shown[e.Item] {
			continue
		}
		if u, member := scratch.GitUnit(e.Item); member {
			if !slices.Contains(units, u) {
				units = append(units, u)
			}
			continue
		}
		files = append(files, e.Item)
	}
	if len(files)+len(units) == 0 {
		return
	}
	keepTargetKeys(w, res, func(k string) bool {
		return slices.Contains(files, k) || slices.ContainsFunc(units, func(u string) bool { return strings.HasPrefix(k, u) })
	})
	for _, k := range append(files, units...) {
		w.Out.Changes = append(w.Out.Changes, sources.Skip(model.RowScratch, k, scratch.NoteDeletionDefer))
	}
}

// scratchCountable counts the scratch items the breaker's denominator
// uses: files outside git units and whole units.
func scratchCountable(s *model.RowSnapshot) int {
	if s == nil {
		return 0
	}
	n := 0
	for k, it := range s.Items {
		if scratch.Countable(k, it) {
			n++
		}
	}
	return n
}

// scratchValidate adds row scratch's validation input: the root and leaf,
// the copy bound, the [scratch] breaker limits, and the exemption of git
// unit members (a unit removal counts as one deletion).
func (r *run) scratchValidate(in *plan.ValidateInput) {
	sc := scratchSpec(r.manifest)
	if sc == nil {
		return
	}
	cfg := r.d().Config.Scratch
	in.ScratchRoot, in.ScratchLeaf = sc.Root, sc.LeafRE()
	in.MaxCopyBytes = 2 * scratchLimits(cfg).MaxRunBytes
	if in.Limits.PerCategory == nil {
		in.Limits.PerCategory = map[model.Category]plan.CategoryLimits{}
	}
	in.Limits.PerCategory[model.CatScratch] = plan.CategoryLimits{MaxDeleteFraction: cfg.MaxDeleteFraction, MaxDeleteMin: cfg.MaxDeleteMin}
	in.BreakerExempt = func(c model.Change) bool {
		_, member := scratch.GitUnit(c.Key)
		return c.Row == model.RowScratch && member
	}
}

// scratchIntentData records the carry set in the planned intent, so
// recover re-reads exactly those workspaces.
func (r *run) scratchIntentData(data map[string]string) {
	if w := r.p5.Work[model.RowScratch]; w != nil {
		data[intentScratchRefs] = scratch.JoinRefs(w.Refs)
	}
}

// isScratchPath reports whether a data-folder-relative path lies under
// the scratch root.
func isScratchPath(root, p string) bool {
	return root != "" && (p == root || strings.HasPrefix(p, root+"/"))
}

// postHashPath returns the path of a post-hash key ("" for ls: keys).
func postHashPath(k string) string {
	for _, pre := range []string{"path:", "dir:", "link:"} {
		if rest, ok := strings.CutPrefix(k, pre); ok {
			return rest
		}
	}
	return ""
}

// recordPostHashes stores the plan's post-hashes: other rows inline in
// the run, row scratch's in a state object, so root.json does not grow
// with workspace size.
func (r *run) recordPostHashes(root *state.Root, p *plan.Plan) error {
	all := postHashes(p)
	root.Run.PostHashesRef = ""
	sc := scratchSpec(r.manifest)
	if sc == nil || len(all) == 0 {
		root.Run.PostHashes = all
		return nil
	}
	inline, scr := map[string]string{}, map[string]string{}
	for k, v := range all {
		if isScratchPath(sc.Root, postHashPath(k)) {
			scr[k] = v
		} else {
			inline[k] = v
		}
	}
	if len(inline) == 0 {
		inline = nil
	}
	root.Run.PostHashes = inline
	if len(scr) == 0 {
		return nil
	}
	ref, err := r.d().State.PutObject(scratchPostHashesKind, scr)
	if err != nil {
		return pderr.Wrap(pderr.Internal, "state", err)
	}
	root.Run.PostHashesRef = ref
	return nil
}

// allPostHashes merges a run's inline post-hashes with row scratch's
// object. ok is false when the object cannot be read.
func (o *Orchestrator) allPostHashes(run *state.Run) (map[string]string, bool) {
	out := map[string]string{}
	for k, v := range run.PostHashes {
		out[k] = v
	}
	if run.PostHashesRef == "" {
		return out, true
	}
	var scr map[string]string
	if err := o.d.State.GetObject(run.PostHashesRef, scratchPostHashesKind, &scr); err != nil {
		return out, false
	}
	for k, v := range scr {
		out[k] = v
	}
	return out, true
}

// scratchRereadRefs returns the refs of rereadRows for a finished run:
// row scratch's carry set when it was planned.
func (r *run) scratchRereadRefs() map[model.RowID][]string {
	if w := r.p5.Work[model.RowScratch]; w != nil {
		return map[model.RowID][]string{model.RowScratch: w.Refs}
	}
	return nil
}

// scratchExpected returns what T must read back as for row scratch: a
// copy of Desired with every soft-skipped key set back to T's item.
func (r *run) scratchExpected(w *rowWork) *model.RowSnapshot {
	sc := scratchSpec(r.manifest)
	if sc == nil || len(r.p5.SoftSkipped) == 0 || w.Out.Desired == nil {
		return w.Out.Desired
	}
	d := w.Out.Desired.Clone()
	for _, p := range r.p5.SoftSkipped {
		k, ok := strings.CutPrefix(p, sc.Root+"/")
		if !ok {
			continue
		}
		if it, has := tItems(w)[k]; has {
			d.Items[k] = it
		} else {
			delete(d.Items, k)
		}
	}
	return d
}

// noteSoftSkipped records apply's soft skips (spec-scratch 3.3): the
// re-read expects T's old content there, and the run result carries one
// source-changed change per path. The P5 plan itself is not changed (its
// hash is the marker's base).
func (r *run) noteSoftSkipped(res apply.Result) {
	if len(res.SoftSkipped) == 0 {
		return
	}
	r.p5.SoftSkipped = slices.Clone(res.SoftSkipped)
	sc := scratchSpec(r.manifest)
	shown := *r.p5.Plan
	shown.Changes = slices.Clone(r.p5.Plan.Changes)
	for _, p := range res.SoftSkipped {
		k := p
		if sc != nil {
			k = strings.TrimPrefix(p, sc.Root+"/")
		}
		shown.Changes = append(shown.Changes, sources.Skip(model.RowScratch, k, scratch.NoteSourceChanged))
	}
	r.res.Plan = &shown
	r.warn(P7Apply, i18n.Tx(i18n.ScratchSourceChanged, len(res.SoftSkipped), string(r.T.Profile.Alias)))
}

// scratchRefsFromIntent returns the refs of rereadRows for recover: the
// carry set the planned intent recorded (no entry when it recorded none,
// so the row is not read and its base is dropped).
func scratchRefsFromIntent(data map[string]string) map[model.RowID][]string {
	s, ok := data[intentScratchRefs]
	if !ok {
		return nil
	}
	keys, err := scratch.SplitRefs(s)
	if err != nil {
		return nil
	}
	return map[model.RowID][]string{model.RowScratch: keys}
}

// onlyScratchMismatches reports whether every mismatched post-hash key
// lies under the scratch root.
func onlyScratchMismatches(m *compat.Manifest, mismatched []string) bool {
	sc := scratchSpec(m)
	if sc == nil || len(mismatched) == 0 {
		return false
	}
	for _, k := range mismatched {
		p := postHashPath(k)
		if p == "" && !strings.HasPrefix(k, "ls:") {
			p = k // a bare path
		}
		if !isScratchPath(sc.Root, p) {
			return false
		}
	}
	return true
}

// recheckScratch is recover's post-hash check (spec-scratch 6.9): it
// merges the inline post-hashes with row scratch's object. A mismatch
// only under the scratch root, or an unreadable object, drops only T's
// scratch base (scratchOK false); any other mismatch keeps today's rule
// (ok false: every T base is dropped).
func (rc *recovery) recheckScratch(ctx context.Context, target plan.Target, folder string) (ok, scratchOK bool) {
	run := rc.root.Run
	ph, objOK := rc.o.allPostHashes(run)
	scratchOK = objOK
	if len(ph) == 0 {
		return true, scratchOK
	}
	match, mismatched, err := rc.o.d.Applier.Check(ctx, planFromPostHashes(ph, target), folder)
	switch {
	case err != nil:
		return false, false
	case match:
		return true, scratchOK
	case onlyScratchMismatches(rc.manifest, mismatched):
		return true, false
	}
	return false, false
}
