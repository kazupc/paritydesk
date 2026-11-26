package orchestrator

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/gen"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/redact"
	"github.com/kazupc/paritydesk/internal/render"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/sources/scratch"
	"github.com/kazupc/paritydesk/internal/state"
)

// doctorScratchWalkCap bounds the entries doctor's size walk visits.
const doctorScratchWalkCap = 200_000

// doctorOrphansShown is how many orphaned mirrors doctor names.
const doctorOrphansShown = 10

// scratchKeeper is implemented by gen.Store: the kept sets of
// quarantined scratch files.
type scratchKeeper interface {
	ScratchKept() ([]gen.KeptInfo, error)
	ScratchKeptPath() string
}

// scratchWorkspace is one workspace found in the active folder.
type scratchWorkspace struct {
	key   string
	size  int64
	exact bool // the size walk was not cut short
}

// scratchChecks are doctor's checks of row scratch (spec-scratch 12):
// scratch, scratch-orphans and scratch-kept. They only read the active
// folder; statuses are ok, warn or fail.
func (dr *doctor) scratchChecks(ctx context.Context, m *compat.Manifest, root *state.Root) {
	sc := scratchSpec(m)
	if sc == nil {
		dr.add("scratch", "ok", i18n.Tx(i18n.DoctorScratchUnsupported))
		dr.add("scratch-orphans", "ok", i18n.Tx(i18n.DoctorScratchOrphansNA))
		dr.scratchKept()
		return
	}
	claude := dr.o.claudePath()
	rootDir := filepath.Join(claude, sc.Root)
	if fi, err := os.Lstat(rootDir); err == nil && !fi.IsDir() {
		dr.add("scratch", "warn", i18n.Tx(i18n.DoctorScratchRootNotDir))
		dr.add("scratch-orphans", "ok", i18n.Tx(i18n.DoctorScratchOrphansNA))
		dr.scratchKept()
		return
	}
	own := model.Replica{}
	if ps := root.Profiles[root.Active]; ps != nil {
		own = ps.Replica
	}
	all, capped := listScratchWorkspaces(rootDir, sc)
	var mine, mirrors []scratchWorkspace
	var total int64
	for _, w := range all {
		total += w.size
		if own.Account != "" && strings.HasPrefix(w.key, own.Key()+"/") {
			mine = append(mine, w)
		} else {
			mirrors = append(mirrors, w)
		}
	}
	size := i18n.Raw(render.Bytes(total))
	if capped {
		size = i18n.Tx(i18n.DoctorScratchSizeMore, size)
	}
	detail := i18n.Tx(i18n.DoctorScratch, len(mine), len(mirrors), size)
	if len(mirrors) > 0 {
		detail = i18n.Tx(i18n.WordSentences, detail, i18n.Tx(i18n.DoctorScratchLimits))
	}
	dr.add("scratch", "ok", detail)
	dr.scratchOrphans(ctx, m, claude, sc, mirrors)
	dr.scratchKept()
}

// listScratchWorkspaces lists <root>/<acct>/<org>/<leaf> directories and
// their sizes, visiting at most doctorScratchWalkCap entries in all.
func listScratchWorkspaces(rootDir string, sc *compat.ScratchSpec) ([]scratchWorkspace, bool) {
	var out []scratchWorkspace
	budget := doctorScratchWalkCap
	capped := false
	accts, _ := os.ReadDir(rootDir)
	for _, a := range accts {
		if !a.IsDir() || !ids.IsUUID(a.Name()) {
			continue
		}
		orgs, _ := os.ReadDir(filepath.Join(rootDir, a.Name()))
		for _, o := range orgs {
			if !o.IsDir() || !ids.IsUUID(o.Name()) {
				continue
			}
			leaves, _ := os.ReadDir(filepath.Join(rootDir, a.Name(), o.Name()))
			for _, l := range leaves {
				if !l.IsDir() || sc.LeafRE() == nil || !sc.LeafRE().MatchString(l.Name()) {
					continue
				}
				w := scratchWorkspace{key: a.Name() + "/" + o.Name() + "/" + l.Name(), exact: true}
				w.size, budget = dirSizeCapped(filepath.Join(rootDir, a.Name(), o.Name(), l.Name()), budget)
				if budget <= 0 {
					w.exact, capped = false, true
				}
				out = append(out, w)
			}
		}
	}
	return out, capped
}

// errWalkCap stops a size walk at its budget.
var errWalkCap = errors.New("walk cap reached")

// dirSizeCapped sums regular file sizes under dir without following
// symlinks, visiting at most budget entries; it returns the size and the
// budget left.
func dirSizeCapped(dir string, budget int) (int64, int) {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if budget <= 0 {
			return errWalkCap
		}
		budget--
		if d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				total += fi.Size()
			}
		}
		return nil
	})
	return total, budget
}

// scratchOrphans reports mirrors that no session record of any replica in
// the active folder names. Nothing is removed.
func (dr *doctor) scratchOrphans(ctx context.Context, m *compat.Manifest, claude string, sc *compat.ScratchSpec, mirrors []scratchWorkspace) {
	if len(mirrors) == 0 {
		dr.add("scratch-orphans", "ok", i18n.Tx(i18n.DoctorScratchOrphansNone))
		return
	}
	named, err := scratchNamed(ctx, m, claude, sc, dr.o.d.DataRoot)
	if err != nil {
		dr.add("scratch-orphans", "warn", i18n.Tx(i18n.DoctorScratchOrphansUnread))
		return
	}
	var orphans []scratchWorkspace
	for _, w := range mirrors {
		if !named[w.key] {
			orphans = append(orphans, w)
		}
	}
	if len(orphans) == 0 {
		dr.add("scratch-orphans", "ok", i18n.Tx(i18n.DoctorScratchOrphansUsed))
		return
	}
	sort.Slice(orphans, func(i, j int) bool { return orphans[i].key < orphans[j].key })
	var parts i18n.CommaList
	for i, w := range orphans {
		if i == doctorOrphansShown {
			parts = append(parts, i18n.Tx(i18n.DoctorScratchOrphansMore, len(orphans)-doctorOrphansShown))
			break
		}
		parts = append(parts, i18n.Raw(render.ScratchKey(w.key)+" "+render.Bytes(w.size)))
	}
	dr.add("scratch-orphans", "ok", i18n.Tx(i18n.DoctorScratchOrphans, len(orphans), parts))
}

// scratchNamed returns the workspace keys any session record of any
// replica in folder names.
func scratchNamed(ctx context.Context, m *compat.Manifest, folder string, sc *compat.ScratchSpec, dataRoot string) (map[string]bool, error) {
	reps, err := sources.DiscoverReplicas(folder)
	if err != nil {
		return nil, err
	}
	sessSpec, _ := m.Row(model.RowSessions)
	out := map[string]bool{}
	for _, rep := range reps {
		recs, err := sources.ReadRecordFiles(ctx, folder, rep)
		if err != nil {
			return nil, err
		}
		snap := sources.NewSnapshot(model.RowSessions)
		for _, rec := range recs {
			snap.Items[string(rec.ID)] = model.Item{Key: string(rec.ID), Value: rec.Data}
		}
		refs, _, err := scratch.Refs(snap, sessSpec.Fields[sources.RoleScratchRef], scratch.Prefixes(dataRoot, sc.Root), sc.LeafRE())
		if err != nil {
			return nil, err
		}
		for _, r := range refs {
			out[r.Key] = true
		}
	}
	return out, nil
}

// scratchKept reports the kept sets of quarantined scratch files and the
// command that removes them.
func (dr *doctor) scratchKept() {
	k, ok := dr.o.d.Gens.(scratchKeeper)
	if !ok {
		dr.add("scratch-kept", "ok", i18n.Tx(i18n.DoctorScratchKeptNone))
		return
	}
	sets, err := k.ScratchKept()
	if err != nil {
		dr.add("scratch-kept", "warn", i18n.Tx(i18n.DoctorScratchKeptUnreadable))
		return
	}
	if len(sets) == 0 {
		dr.add("scratch-kept", "ok", i18n.Tx(i18n.DoctorScratchKeptNone))
		return
	}
	var total int64
	for _, s := range sets {
		total += s.Bytes
	}
	dr.add("scratch-kept", "ok", i18n.Tx(i18n.DoctorScratchKept, len(sets), render.Bytes(total), redact.Redactor{Home: dr.o.d.Home}.Path(k.ScratchKeptPath())))
}
