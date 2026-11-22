package orchestrator

// Hand-written fakes for every Deps interface and function field, and a
// test environment over real directories in t.TempDir(). Rows are JSON
// files rows/<row>.json (an object of item key to value) so swaps, applies
// and restores are visible on disk.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/app"
	"github.com/kazupc/paritydesk/internal/apply"
	"github.com/kazupc/paritydesk/internal/clock"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/fsx/fsxtest"
	"github.com/kazupc/paritydesk/internal/gen"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/profile"
	"github.com/kazupc/paritydesk/internal/rows"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/sources/configjson"
	"github.com/kazupc/paritydesk/internal/state"
	"github.com/kazupc/paritydesk/internal/store/kv"
)

func fakeUUID(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012x", n) }

func fakeSession(n int) ids.SessionID { return ids.SessionID("local_" + fakeUUID(n)) }

var (
	acctA = ids.AccountID(fakeUUID(1))
	acctB = ids.AccountID(fakeUUID(2))
	acctC = ids.AccountID(fakeUUID(3))
	orgA  = ids.OrgID(fakeUUID(11))
	orgB  = ids.OrgID(fakeUUID(12))
	repA  = model.Replica{Account: acctA, Org: orgA}
	repB  = model.Replica{Account: acctB, Org: orgB}
)

func hashOf(b []byte, present bool) string {
	if !present {
		return ""
	}
	return model.HashBytes(b)
}

func rowFile(r model.RowID) string { return "rows/" + string(r) + ".json" }

// ---- state ----

type fakeState struct {
	clk       clock.Clock
	committed []byte
	objects   map[state.Ref][]byte
	commits   int
	lockCalls int
	lockErr   error
	log       []string
}

func (s *fakeState) Load() (*state.Root, error) {
	if s.committed == nil {
		return nil, nil
	}
	var r state.Root
	if err := json.Unmarshal(s.committed, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *fakeState) Commit(r *state.Root) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	s.committed = b
	s.commits++
	return nil
}

func (s *fakeState) Begin(r *state.Root, name state.IntentName, data map[string]string) error {
	if r.Run == nil {
		return errors.New("fake state: no open run")
	}
	r.Run.Intents = append(r.Run.Intents, state.Intent{Name: name, At: s.clk.Now(), Data: data})
	s.log = append(s.log, "begin:"+string(name))
	return s.Commit(r)
}

func (s *fakeState) Done(r *state.Root, name state.IntentName) error {
	for i := len(r.Run.Intents) - 1; i >= 0; i-- {
		if r.Run.Intents[i].Name == name {
			r.Run.Intents[i].Done = true
			s.log = append(s.log, "done:"+string(name))
			return s.Commit(r)
		}
	}
	return errors.New("fake state: no such intent")
}

func (s *fakeState) PutSnapshot(snap *model.RowSnapshot) (state.Ref, error) {
	b, err := json.Marshal(snap)
	if err != nil {
		return "", err
	}
	ref := state.Ref(model.HashBytes(b))
	s.objects[ref] = b
	return ref, nil
}

func (s *fakeState) GetSnapshot(ref state.Ref) (*model.RowSnapshot, error) {
	b, ok := s.objects[ref]
	if !ok {
		return nil, errors.New("fake state: no object")
	}
	var snap model.RowSnapshot
	return &snap, json.Unmarshal(b, &snap)
}

func (s *fakeState) GC(*state.Root) error { return nil }

func (s *fakeState) Lock(context.Context, time.Duration) (*state.Lock, error) {
	s.lockCalls++
	if s.lockErr != nil {
		return nil, s.lockErr
	}
	return &state.Lock{}, nil
}

func (s *fakeState) root(t *testing.T) *state.Root {
	t.Helper()
	r, err := s.Load()
	if err != nil || r == nil {
		t.Fatalf("no root: %v", err)
	}
	return r
}

// ---- generations ----

type fakeGen struct {
	m     gen.Manifest
	bytes map[string][]byte
}

type fakeGens struct {
	gens          map[uint64]*fakeGen
	restores      int
	levelRestores int
	prunes        int
}

type fakeGenWriter struct {
	g  *fakeGen
	gs *fakeGens
}

func (g *fakeGens) Begin(n uint64, runID string, a ids.Alias, rep model.Replica) (gen.Writer, error) {
	if _, ok := g.gens[n]; ok {
		return nil, errors.New("fake gens: generation exists")
	}
	fg := &fakeGen{m: gen.Manifest{Version: 1, N: n, RunID: runID, Alias: a, Replica: rep}, bytes: map[string][]byte{}}
	g.gens[n] = fg
	return &fakeGenWriter{g: fg, gs: g}, nil
}

func (w *fakeGenWriter) Dir() string           { return "" }
func (w *fakeGenWriter) QuarantineDir() string { return "" }

func (w *fakeGenWriter) SaveFile(row model.RowID, folder, rel, post string) error {
	b, present, err := readContained(folder, rel)
	if err != nil {
		return err
	}
	w.g.bytes[rel] = b
	w.g.m.Entries = append(w.g.m.Entries, gen.Entry{Kind: gen.EntryFile, Row: row, Path: rel, Present: present,
		PreHash: hashOf(b, present), PostHash: post})
	return nil
}

func (w *fakeGenWriter) SaveLSValue(row model.RowID, name string, raw []byte, present bool, post string) error {
	w.g.m.Entries = append(w.g.m.Entries, gen.Entry{Kind: gen.EntryLSValue, Row: row, Key: name, Present: present,
		PreHash: hashOf(raw, present), PostHash: post})
	return nil
}

func (w *fakeGenWriter) SaveConflict(row model.RowID, key string, raw []byte) error {
	w.g.m.Entries = append(w.g.m.Entries, gen.Entry{Kind: gen.EntryConflict, Row: row, Key: key})
	return nil
}

func (w *fakeGenWriter) CloneLevelDB(string) error { w.g.m.LevelDB = "leveldb"; return nil }

func (w *fakeGenWriter) Finish() (*gen.Manifest, error) {
	w.g.m.Complete = true
	m := w.g.m
	return &m, nil
}

func (w *fakeGenWriter) Abort() error {
	delete(w.gs.gens, w.g.m.N)
	return nil
}

func (g *fakeGens) List() ([]gen.Info, error) {
	var out []gen.Info
	for n, fg := range g.gens {
		out = append(out, gen.Info{N: n, Complete: fg.m.Complete, Entries: len(fg.m.Entries)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].N > out[j].N })
	return out, nil
}

func (g *fakeGens) Load(n uint64) (*gen.Manifest, error) {
	fg, ok := g.gens[n]
	if !ok {
		return nil, errors.New("fake gens: no generation")
	}
	m := fg.m
	return &m, nil
}

func (g *fakeGens) DeleteIncomplete(n uint64) (bool, error) {
	if fg, ok := g.gens[n]; ok && !fg.m.Complete {
		delete(g.gens, n)
		return true, nil
	}
	return false, nil
}

func (g *fakeGens) Prune(gen.PrunePolicy) error { g.prunes++; return nil }
func (g *fakeGens) Purge() error                { g.gens = map[uint64]*fakeGen{}; return nil }

// Restore is conditional and idempotent: post-hash -> restore, pre-hash ->
// already restored, anything else -> conflict (never overwritten).
func (g *fakeGens) Restore(_ context.Context, in gen.RestoreInput) (gen.RestoreReport, error) {
	g.restores++
	fg, ok := g.gens[in.N]
	if !ok {
		return gen.RestoreReport{}, errors.New("fake gens: no generation")
	}
	var rep gen.RestoreReport
	for _, e := range fg.m.Entries {
		if e.Kind != gen.EntryFile {
			continue
		}
		cur, present, err := readContained(in.Folder, e.Path)
		if err != nil {
			return rep, err
		}
		h := hashOf(cur, present)
		switch h {
		case e.PreHash:
			rep.Restored = append(rep.Restored, e)
		case e.PostHash:
			p := filepath.Join(in.Folder, e.Path)
			if e.Present {
				if err := os.WriteFile(p, fg.bytes[e.Path], 0o600); err != nil {
					return rep, err
				}
			} else if err := os.Remove(p); err != nil {
				return rep, err
			}
			rep.Restored = append(rep.Restored, e)
		default:
			rep.Conflicts = append(rep.Conflicts, e)
		}
	}
	return rep, nil
}

func (g *fakeGens) RestoreLevelDB(context.Context, uint64, string) error {
	g.levelRestores++
	return nil
}

// ---- folders ----

type identityFile struct {
	Account string `json:"account"`
	Org     string `json:"org"`
}

type fakeScanner struct {
	fs   fsx.Ops
	root string
}

func (s fakeScanner) Scan(context.Context) ([]profile.Folder, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	var out []profile.Folder
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() || (n != ids.DefaultFolder && !strings.HasPrefix(n, ids.DefaultFolder+"-")) {
			continue
		}
		p := filepath.Join(s.root, n)
		f := profile.Folder{Name: n, Path: p, Active: n == ids.DefaultFolder}
		if b, err := os.ReadFile(filepath.Join(p, "identity.json")); err == nil {
			var id identityFile
			if json.Unmarshal(b, &id) == nil && id.Account != "" {
				f.Identity = &configjson.Identity{Account: ids.AccountID(id.Account)}
				f.Replicas = []model.Replica{{Account: ids.AccountID(id.Account), Org: ids.OrgID(id.Org)}}
			}
		}
		if f.Identity == nil {
			f.IdentityErr = "no account recorded"
		}
		f.Marker, _ = readMarker(s.fs, p)
		f.ID, _ = s.fs.ID(p)
		out = append(out, f)
	}
	return out, nil
}

func readMarker(fs fsx.Ops, folder string) (*model.Marker, error) {
	b, found, err := fs.GetXattr(folder, fsx.MarkerXattr)
	if err != nil || !found {
		return nil, err
	}
	var m model.Marker
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func writeMarker(fs fsx.Ops, folder string, m model.Marker) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return fs.SetXattr(folder, fsx.MarkerXattr, b)
}

// fakeResolve binds profiles by the accounts recorded in state.
func fakeResolve(invalid map[ids.Alias]bool) func(*config.Config, *state.Root, []profile.Folder, profile.Hints) (*profile.Resolution, error) {
	return func(cfg *config.Config, root *state.Root, folders []profile.Folder, _ profile.Hints) (*profile.Resolution, error) {
		res := &profile.Resolution{Bindings: map[ids.Alias]*profile.Binding{}}
		for _, p := range cfg.Profiles {
			ps := root.Profiles[p.Alias]
			if ps == nil {
				continue
			}
			b := &profile.Binding{Profile: p, Replica: ps.Replica, BaseValid: !invalid[p.Alias]}
			for i := range folders {
				f := &folders[i]
				if f.Identity != nil && f.Identity.Account == ps.Replica.Account {
					b.Folder = f
				}
				if f.Name == p.ParkedFolder() && f.Identity != nil && f.Identity.Account != ps.Replica.Account &&
					aliasOf(root, f.Identity.Account) == "" {
					return nil, pderr.New(pderr.IdentityMismatch, "resolve", "folder of "+string(p.Alias)+" holds another account")
				}
			}
			res.Bindings[p.Alias] = b
			if b.Folder != nil && b.Folder.Name == ids.DefaultFolder {
				res.Active = b
			}
		}
		if res.Active != nil && root.Active != "" && res.Active.Profile.Alias != root.Active {
			res.OutsideSwitch = true
		}
		return res, nil
	}
}

// ---- app ----

type fakeApp struct {
	e           *env
	running     bool
	bundle      app.BundleIdentity
	staged      bool
	veto        bool
	ignoreQuit  bool
	lateExit    bool
	updRelaunch bool
	quitErr     error
	quits       int
	launches    int
	badLaunch   int
	opened      []ids.SessionID
	busySeq     [][]ids.SessionID
	busyCalls   int
	logSize     int64
	turnEnds    []int64
	turnFrom    []int64
	turnWaited  bool
	events      []app.LogEvent
	intruder    bool
	holders     bool
	notQuiet    bool
	dirty       map[string]bool
	autoErr     error
	locateErr   error
	// inUse marks folders a process holds open (a write holder).
	inUse map[string]bool
}

func (a *fakeApp) Locate(context.Context, string) (app.BundleIdentity, error) {
	return a.bundle, a.locateErr
}
func (a *fakeApp) UpdateStaged(context.Context, string) (bool, error) { return a.staged, nil }

func (a *fakeApp) Running(_ context.Context, path string) ([]app.Process, error) {
	if !a.running {
		return nil, nil
	}
	return []app.Process{{PID: 100, Exe: path + "/Contents/MacOS/Claude"}}, nil
}

func (a *fakeApp) CheckQuiescence(_ context.Context, in app.QuiescenceInput) (app.QuiescenceReport, error) {
	rep := app.QuiescenceReport{Quiet: !a.running && !a.notQuiet}
	for _, f := range in.Folders {
		if a.inUse[f] {
			rep.Quiet = false
			rep.WriteHolders = append(rep.WriteHolders, app.Holder{PID: 300, Path: f})
		}
	}
	if !in.After.IsZero() && a.intruder {
		rep.Quiet = false
		rep.StartedAfter = []app.Process{{PID: 200}}
		if a.holders {
			rep.WriteHolders = []app.Holder{{PID: 200, Path: in.Folders[0]}}
		}
	}
	return rep, nil
}

func (a *fakeApp) QuitAndWait(_ context.Context, _, _ string, _ compat.Signals, _ time.Duration, onVeto func()) (app.QuitResult, error) {
	a.quits++
	switch {
	case a.quitErr != nil:
		return app.QuitResult{}, a.quitErr
	case a.veto:
		if onVeto != nil {
			onVeto()
		}
		return app.QuitResult{Vetoed: true, Pending: true}, pderr.New(pderr.AppDidNotQuit, "quit", "timeout")
	case a.ignoreQuit:
		return app.QuitResult{}, pderr.New(pderr.AppDidNotQuit, "quit", "timeout")
	}
	a.running = false
	return app.QuitResult{Exited: true}, nil
}

func (a *fakeApp) WaitExited(context.Context, string, time.Duration) (bool, error) {
	if a.lateExit {
		a.running = false
		return true, nil
	}
	return false, nil
}

func (a *fakeApp) WaitTurnEnded(_ context.Context, from int64, _ compat.Signals, _ ids.SessionID, _ time.Duration) error {
	a.turnFrom = append(a.turnFrom, from)
	for _, off := range a.turnEnds {
		if off >= from {
			return nil
		}
	}
	a.turnWaited = true
	return errors.New("turn did not end")
}

func (a *fakeApp) BusySessions(context.Context, int64, compat.Signals) ([]ids.SessionID, error) {
	a.busyCalls++
	if len(a.busySeq) == 0 {
		return nil, nil
	}
	return a.busySeq[min(a.busyCalls-1, len(a.busySeq)-1)], nil
}

func (a *fakeApp) WaitUpdateInstalled(context.Context, string, time.Duration) error {
	a.staged = false
	a.bundle.Version, a.bundle.Build = "6.2", "62"
	if a.updRelaunch {
		a.running = true
	}
	return nil
}

func (a *fakeApp) ActiveOrg(context.Context, compat.Signals, ids.AccountID) (ids.OrgID, bool, error) {
	return "", false, app.ErrSignalUnknown
}

// Launch starts the app; like the real app it creates an empty Claude
// folder when none exists (the profile add case).
func (a *fakeApp) Launch(context.Context, string) error {
	a.launches++
	if !a.e.claudeRegistered() && !a.e.pendingAdd() {
		a.badLaunch++
	}
	claude := filepath.Join(a.e.root, ids.DefaultFolder)
	if _, err := os.Stat(claude); os.IsNotExist(err) {
		a.e.makeDir(claude)
	}
	a.running = true
	return nil
}

func (a *fakeApp) OpenSession(_ context.Context, id ids.SessionID) error {
	a.opened = append(a.opened, id)
	return nil
}

func (a *fakeApp) ProbeAutomation(context.Context, string) error { return a.autoErr }
func (a *fakeApp) Notify(context.Context, string, string) error  { return nil }
func (a *fakeApp) LogSize() (int64, error)                       { return a.logSize, nil }

func (a *fakeApp) LogEvents(context.Context, int64, compat.Signals, ids.SessionID) ([]app.LogEvent, int64, error) {
	return a.events, a.logSize, nil
}

func (a *fakeApp) GitDirty(_ context.Context, dir string) (bool, error) { return a.dirty[dir], nil }
func (a *fakeApp) LockHeld(string) (bool, error)                        { return true, nil }

// ---- compat ----

type fakeCompat struct {
	m       *compat.Manifest
	tested  bool
	probes  []compat.ProbeResult
	decide  func(compat.DecideInput) compat.Decision
	lastIn  compat.DecideInput
	probeOn []string
}

func (c *fakeCompat) Manifest(context.Context, string) (*compat.Manifest, bool, error) {
	return c.m, c.tested, nil
}

func (c *fakeCompat) Probes(_ context.Context, _ *compat.Manifest, env compat.ProbeEnv) ([]compat.ProbeResult, error) {
	c.probeOn = append(c.probeOn, filepath.Base(env.Folder))
	return c.probes, nil
}

func (c *fakeCompat) Decide(in compat.DecideInput) compat.Decision {
	c.lastIn = in
	if c.decide != nil {
		return c.decide(in)
	}
	switch {
	case in.Manifest == nil:
		return compat.Decision{Mode: compat.ModeRefuse}
	case !in.CompressionOK || in.NativeMultiAccount:
		return compat.Decision{Mode: compat.ModeSwapOnly}
	}
	for _, p := range in.Probes {
		if !p.OK && p.Critical {
			return compat.Decision{Mode: compat.ModeSwapOnly}
		}
	}
	return compat.Decision{Mode: compat.ModeFull}
}

func testManifest() *compat.Manifest {
	return &compat.Manifest{
		Name: "test",
		Rows: []compat.RowSpec{
			{Row: model.RowEnvironments, WritePath: compat.PathOff, Files: []string{rowFile(model.RowEnvironments)}},
			{Row: model.RowSessions, WritePath: compat.PathOff, Files: []string{rowFile(model.RowSessions)},
				Fields: map[string][]string{"busy": {"busy"}}},
			{Row: model.RowGroupOrder, WritePath: compat.PathOffVerify, Files: []string{rowFile(model.RowGroupOrder)},
				MirrorFile: "mirror.json"},
		},
		Classification: compat.Classification{NeverTouched: []string{"identity.json"}, Synced: []string{"rows/**"}},
		Footprints:     []compat.Footprint{{Tool: "other-switcher", FolderGlobs: []string{"Claude-Other*"}}},
		Signals:        compat.Signals{TurnEnded: "x", LoginRequired: "y"},
	}
}

// ---- sources, rows, plan, apply ----

type fileAdapter struct{ row model.RowID }

func (a fileAdapter) Row() model.RowID { return a.row }

func (a fileAdapter) Read(_ context.Context, rc sources.ReadContext) (*model.RowSnapshot, error) {
	b, present, err := readContained(rc.Folder, rowFile(a.row))
	if err != nil {
		return nil, err
	}
	return snapshotOf(a.row, b, present)
}

func snapshotOf(row model.RowID, b []byte, present bool) (*model.RowSnapshot, error) {
	s := &model.RowSnapshot{Row: row, Schema: 1, Items: map[string]model.Item{}}
	if !present {
		return s, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	for k, v := range m {
		it, err := model.NewItem(k, v, v, model.Stamp{}, false)
		if err != nil {
			return nil, err
		}
		s.Items[k] = it
	}
	return s, nil
}

func (a fileAdapter) Ops(_ context.Context, oc sources.OpsContext, cur, desired *model.RowSnapshot) (sources.OpsResult, error) {
	b, present, err := readContained(oc.Folder, rowFile(a.row))
	if err != nil {
		return sources.OpsResult{}, err
	}
	base := hashOf(b, present)
	loc := plan.Location{File: rowFile(a.row)}
	var res sources.OpsResult
	for _, k := range desired.Keys() {
		it := desired.Items[k]
		if c, ok := cur.Items[k]; ok && c.Hash == it.Hash {
			continue
		}
		res.Edits = append(res.Edits, plan.Edit{Loc: loc, Row: a.row, Item: k, Kind: plan.EditSet, Path: []string{k}, Value: it.Value, BaseHash: base})
	}
	for _, k := range cur.Keys() {
		if _, ok := desired.Items[k]; !ok {
			res.Edits = append(res.Edits, plan.Edit{Loc: loc, Row: a.row, Item: k, Kind: plan.EditDelete, Path: []string{k}, BaseHash: base, Destructive: true})
		}
	}
	return res, nil
}

// mirrorAdapter adds the MirrorReader of mirrored rows.
type mirrorAdapter struct{ fileAdapter }

func (m mirrorAdapter) MirrorItems(_ compat.RowSpec, mirror []byte) (*model.RowSnapshot, error) {
	return snapshotOf(m.row, mirror, mirror != nil)
}

// fakeModule is a small three-way merge: each side's changes against a
// valid base reach canonical (leaving wins); an invalid base is a union.
type fakeModule struct{ row model.RowID }

func (m fakeModule) Row() model.RowID { return m.row }

func (m fakeModule) Merge(in rows.Input) (rows.Output, error) {
	canon := map[string]model.Item{}
	if in.Canonical != nil {
		for k, v := range in.Canonical.Items {
			canon[k] = v
		}
	}
	needs := false
	apply := func(sd rows.Side) {
		if sd.Snapshot == nil {
			return
		}
		if !sd.BaseValid || sd.Base == nil {
			needs = true
			for k, it := range sd.Snapshot.Items {
				if _, ok := canon[k]; !ok {
					canon[k] = it
				}
			}
			return
		}
		for k, it := range sd.Snapshot.Items {
			if b, ok := sd.Base.Items[k]; !ok || b.Hash != it.Hash {
				canon[k] = it
			}
		}
		for k := range sd.Base.Items {
			if _, ok := sd.Snapshot.Items[k]; !ok {
				delete(canon, k)
			}
		}
	}
	apply(in.Entering)
	apply(in.Leaving)
	desired := &model.RowSnapshot{Row: m.row, Schema: 1, Items: canon}
	var changes []model.Change
	cur := in.Entering.Snapshot
	for _, k := range desired.Keys() {
		if c, ok := cur.Items[k]; !ok {
			changes = append(changes, model.Change{Row: m.row, Kind: model.ChangeAdded, Key: k})
		} else if c.Hash != canon[k].Hash {
			changes = append(changes, model.Change{Row: m.row, Kind: model.ChangeUpdated, Key: k})
		}
	}
	for _, k := range cur.Keys() {
		if _, ok := canon[k]; !ok {
			changes = append(changes, model.Change{Row: m.row, Kind: model.ChangeDeleted, Key: k})
		}
	}
	return rows.Output{Canonical: desired.Clone(), Desired: desired, Changes: changes, NeedsConfirmation: needs}, nil
}

// fakeCompose folds set/delete edits on top-level keys into one write per
// file.
func fakeCompose(edits []plan.Edit, src plan.Source) ([]plan.Op, error) {
	var order []string
	byFile := map[string][]plan.Edit{}
	for _, e := range edits {
		if _, ok := byFile[e.Loc.File]; !ok {
			order = append(order, e.Loc.File)
		}
		byFile[e.Loc.File] = append(byFile[e.Loc.File], e)
	}
	var ops []plan.Op
	for _, f := range order {
		orig, present, err := src(plan.Location{File: f})
		if err != nil {
			return nil, err
		}
		doc := map[string]json.RawMessage{}
		if present {
			if err := json.Unmarshal(orig, &doc); err != nil {
				return nil, err
			}
		}
		op := plan.Op{Kind: plan.OpWriteFile, Path: f, PreHash: hashOf(orig, present)}
		for _, e := range byFile[f] {
			if e.BaseHash != op.PreHash {
				return nil, errors.New("fake compose: base hash differs")
			}
			op.Row = e.Row
			op.Items = append(op.Items, e.Item)
			op.Destructive = op.Destructive || e.Destructive
			if e.Kind == plan.EditDelete {
				delete(doc, e.Path[0])
			} else {
				doc[e.Path[0]] = e.Value
			}
		}
		b, err := json.Marshal(doc)
		if err != nil {
			return nil, err
		}
		if present && string(b) == string(orig) {
			continue
		}
		op.Content, op.PostHash = b, model.HashBytes(b)
		ops = append(ops, op)
	}
	return ops, nil
}

// fakeValidate refuses a dirty worktree risk without --accept-worktree-risk
// (23) and returns err otherwise.
type fakeValidate struct {
	err  error
	last plan.ValidateInput
}

func (v *fakeValidate) validate(in plan.ValidateInput) error {
	v.last = in
	for _, w := range in.Worktrees {
		if w.Dirty && !in.AcceptWorktreeRisk {
			return pderr.New(pderr.WorktreeRisk, "validate", "worktree risk")
		}
	}
	return v.err
}

type fakeApplier struct {
	failAfter int // fail after this many ops (-1: never)
	applies   int
	// before, if set, runs at the start of Apply (e.g. to reopen the app).
	before func()
}

func (a *fakeApplier) Apply(_ context.Context, in apply.Input) (apply.Result, error) {
	a.applies++
	if a.before != nil {
		a.before()
	}
	res := apply.Result{PostHashes: map[string]string{}}
	for i, op := range in.Plan.Ops {
		if a.failAfter >= 0 && i >= a.failAfter {
			return res, errors.New("fake apply: disk full")
		}
		p := filepath.Join(in.Folder, op.Path)
		res.Touched = true
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return res, err
		}
		if err := os.WriteFile(p, op.Content, 0o600); err != nil {
			return res, err
		}
		res.Applied++
		res.PostHashes["path:"+op.Path] = op.PostHash
	}
	return res, nil
}

func (a *fakeApplier) Check(_ context.Context, p *plan.Plan, folder string) (bool, []string, error) {
	var bad []string
	for _, op := range p.Ops {
		b, present, err := readContained(folder, op.Path)
		if err != nil {
			return false, nil, err
		}
		if hashOf(b, present) != op.PostHash {
			bad = append(bad, op.Path)
		}
	}
	return len(bad) == 0, bad, nil
}

type fakeSessions struct {
	infos []sources.SessionInfo
	cli   map[string]ids.SessionID
}

func (s *fakeSessions) ListSessions(context.Context, string, model.Replica, compat.RowSpec) ([]sources.SessionInfo, error) {
	return s.infos, nil
}

func (s *fakeSessions) ResolveByCLISession(_ context.Context, _ string, _ model.Replica, cli string) (ids.SessionID, error) {
	if id, ok := s.cli[cli]; ok {
		return id, nil
	}
	return "", errors.New("not found")
}

func (s *fakeSessions) DiscoverReplicas(string) ([]model.Replica, error) { return nil, nil }

type fakeWorktrees struct{ lost, lostEntries []string }

func (w *fakeWorktrees) LostRegistrations(_, _ *model.RowSnapshot, _ []string) ([]string, error) {
	return w.lost, nil
}

func (w *fakeWorktrees) LostEntries(_, _ *model.RowSnapshot, _ []string) ([]string, error) {
	return w.lostEntries, nil
}

type fakeDetacher struct {
	inside    bool
	submitted [][]string
	child     func(args []string)
}

func (d *fakeDetacher) InsideApp(context.Context) (bool, error) { return d.inside, nil }

func (d *fakeDetacher) Submit(_ context.Context, args []string) (string, error) {
	d.submitted = append(d.submitted, args)
	if d.child != nil {
		d.child(args)
	}
	return "child-run-0001", nil
}

type fakePlans struct {
	plans map[string][]byte
	saves int
}

func (p *fakePlans) Save(pl *plan.Plan) (string, error) {
	b, err := json.Marshal(pl)
	if err != nil {
		return "", err
	}
	h := pl.Hash()
	p.plans[h] = b
	p.saves++
	return h, nil
}

func (p *fakePlans) Load(hash string, now time.Time) (*plan.Plan, error) {
	b, ok := p.plans[hash]
	if !ok {
		return nil, pderr.New(pderr.Declined, "plans", "missing")
	}
	var pl plan.Plan
	if err := json.Unmarshal(b, &pl); err != nil {
		return nil, err
	}
	if now.After(pl.ExpiresAt) {
		return nil, pderr.New(pderr.Declined, "plans", "expired")
	}
	return &pl, nil
}

type fakeUI struct {
	interactive bool
	answers     map[ConfirmKind]bool
	asked       []ConfirmKind
	lines       [][]string
	events      []Event
	onEvent     func(Event)
}

func (u *fakeUI) Progress(ev Event) {
	u.events = append(u.events, ev)
	if u.onEvent != nil {
		u.onEvent(ev)
	}
}

func (u *fakeUI) Confirm(_ context.Context, c Confirmation) (bool, error) {
	u.asked = append(u.asked, c.Kind)
	u.lines = append(u.lines, c.Lines)
	if !u.interactive {
		return false, errors.New("not interactive")
	}
	if v, ok := u.answers[c.Kind]; ok {
		return v, nil
	}
	return true, nil
}

func (u *fakeUI) Interactive() bool { return u.interactive }

// phases lists the phases that started, in order.
func (u *fakeUI) phases() []Phase {
	var out []Phase
	for _, e := range u.events {
		if e.Status == "start" && (len(out) == 0 || out[len(out)-1] != e.Phase) {
			out = append(out, e.Phase)
		}
	}
	return out
}

// countingKV wraps an opener and counts calls.
type countingKV struct {
	kv.Opener
	calls int
}

func (c *countingKV) OpenSnapshot(ctx context.Context, dir string) (kv.Reader, error) {
	c.calls++
	return c.Opener.OpenSnapshot(ctx, dir)
}

func (c *countingKV) OpenWritable(ctx context.Context, dir string) (kv.Writer, error) {
	c.calls++
	return c.Opener.OpenWritable(ctx, dir)
}

func (c *countingKV) CheckFormat(ctx context.Context, dir string) error {
	c.calls++
	return c.Opener.CheckFormat(ctx, dir)
}

// ---- environment ----

type env struct {
	t        *testing.T
	root     string
	fs       *fsxtest.FS
	clk      *clock.Fake
	st       *fakeState
	gens     *fakeGens
	app      *fakeApp
	compat   *fakeCompat
	ui       *fakeUI
	det      *fakeDetacher
	plans    *fakePlans
	sess     *fakeSessions
	wt       *fakeWorktrees
	applier  *fakeApplier
	valid    *fakeValidate
	kv       *countingKV
	mem      *kv.MemOpener
	cfg      *config.Config
	invalid  map[ids.Alias]bool
	crash    func(Phase) bool
	readOnly bool
	scans    int
	// basesBefore are the bases when markBases was last called.
	basesBefore map[string]state.Ref
}

// markBases records the current bases for the I5 check.
func (e *env) markBases() {
	e.basesBefore = map[string]state.Ref{}
	for k, v := range e.st.root(e.t).Bases {
		e.basesBefore[k] = v
	}
}

// newEnv builds two profiles: a (active, in Claude) and b (parked in
// Claude-b), each with its own sessions and environments.
func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, root: filepath.Join(t.TempDir(), "data"), clk: clock.NewFake(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))}
	e.fs = fsxtest.New(t)
	e.st = &fakeState{clk: e.clk, objects: map[state.Ref][]byte{}}
	e.gens = &fakeGens{gens: map[uint64]*fakeGen{}}
	e.app = &fakeApp{e: e, running: true, logSize: 1000, dirty: map[string]bool{}, inUse: map[string]bool{},
		bundle: app.BundleIdentity{BundleID: config.DefaultBundleID, Path: "/Apps/Claude.app", Version: "6.1", Build: "61"}}
	e.compat = &fakeCompat{m: testManifest(), tested: true}
	e.ui = &fakeUI{answers: map[ConfirmKind]bool{}}
	e.det = &fakeDetacher{}
	e.plans = &fakePlans{plans: map[string][]byte{}}
	e.sess = &fakeSessions{cli: map[string]ids.SessionID{}}
	e.wt = &fakeWorktrees{}
	e.applier = &fakeApplier{failAfter: -1}
	e.valid = &fakeValidate{}
	e.mem = kv.NewMemOpener()
	e.kv = &countingKV{Opener: e.mem}
	e.invalid = map[ids.Alias]bool{}
	e.cfg = config.Default()
	e.cfg.Profiles = []config.Profile{{Alias: "a"}, {Alias: "b"}}
	e.cfg.Sync.AppPrefs = nil
	e.makeFolder(ids.DefaultFolder, repA, map[model.RowID]map[string]any{
		model.RowSessions:     {string(fakeSession(1)): map[string]any{"title": "one"}},
		model.RowEnvironments: {"preset/p1": map[string]any{"name": "env one"}},
	})
	e.makeFolder("Claude-b", repB, map[model.RowID]map[string]any{
		model.RowSessions:     {string(fakeSession(2)): map[string]any{"title": "two"}},
		model.RowEnvironments: {"preset/p2": map[string]any{"name": "env two"}},
	})
	root := state.NewRoot(e.root)
	root.Profiles["a"] = &state.ProfileState{Alias: "a", Replica: repA}
	root.Profiles["b"] = &state.ProfileState{Alias: "b", Replica: repB}
	root.Active, root.Previous = "a", "b"
	if err := e.st.Commit(root); err != nil {
		t.Fatal(err)
	}
	e.st.commits = 0
	return e
}

func (e *env) makeFolder(name string, rep model.Replica, data map[model.RowID]map[string]any) {
	e.t.Helper()
	p := filepath.Join(e.root, name)
	if err := os.MkdirAll(filepath.Join(p, "rows"), 0o700); err != nil {
		e.t.Fatal(err)
	}
	id, _ := json.Marshal(identityFile{Account: string(rep.Account), Org: string(rep.Org)})
	if rep.Account == "" {
		id = []byte("{}")
	}
	if err := os.WriteFile(filepath.Join(p, "identity.json"), id, 0o600); err != nil {
		e.t.Fatal(err)
	}
	for row, items := range data {
		e.writeRow(name, row, items)
	}
}

func (e *env) writeRow(folder string, row model.RowID, items map[string]any) {
	e.t.Helper()
	b, err := json.Marshal(items)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.root, folder, rowFile(row)), b, 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) makeDir(p string) {
	e.t.Helper()
	if err := os.MkdirAll(p, 0o700); err != nil {
		e.t.Fatal(err)
	}
}

// keys returns the item keys of a row file, sorted.
func (e *env) keys(folder string, row model.RowID) []string {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.root, folder, rowFile(row)))
	if err != nil {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// account returns the account in a folder ("" when missing).
func (e *env) account(folder string) ids.AccountID {
	b, err := os.ReadFile(filepath.Join(e.root, folder, "identity.json"))
	if err != nil {
		return ""
	}
	var id identityFile
	_ = json.Unmarshal(b, &id)
	return ids.AccountID(id.Account)
}

func (e *env) claudeRegistered() bool {
	r, err := e.st.Load()
	if err != nil || r == nil {
		return false
	}
	return aliasOf(r, e.account(ids.DefaultFolder)) != ""
}

func (e *env) pendingAdd() bool {
	r, err := e.st.Load()
	return err == nil && r != nil && r.PendingAdd != nil
}

func (e *env) deps() Deps {
	d := Deps{
		Config: e.cfg, Home: filepath.Join(e.root, "home"), DataRoot: e.root, Clock: e.clk, FS: e.fs,
		Paths: config.Paths{StateDir: filepath.Join(e.root, "state"), SupportDir: filepath.Join(e.root, "support")},
		State: e.st, Gens: e.gens, Folders: countingScanner{fakeScanner{fs: e.fs, root: e.root}, &e.scans},
		Resolve: fakeResolve(e.invalid), ReadMarker: readMarker, WriteMarker: writeMarker,
		App: e.app, Compat: e.compat, KV: e.kv,
		Adapters: sources.Registry{
			model.RowSessions:     fileAdapter{model.RowSessions},
			model.RowEnvironments: fileAdapter{model.RowEnvironments},
			model.RowGroupOrder:   mirrorAdapter{fileAdapter{model.RowGroupOrder}},
		},
		Rows: map[model.RowID]rows.Module{
			model.RowSessions:     fakeModule{model.RowSessions},
			model.RowEnvironments: fakeModule{model.RowEnvironments},
			model.RowGroupOrder:   fakeModule{model.RowGroupOrder},
		},
		PostMerge: func(map[model.RowID]*rows.Output) error { return nil },
		Compose:   fakeCompose,
		Validate:  e.valid.validate,
		PermissionAcks: func(context.Context, string, model.Replica) (map[string]bool, error) {
			return nil, nil
		},
		Applier: e.applier, Sessions: e.sess, Worktrees: e.wt, Detach: e.det, Plans: e.plans, UI: e.ui,
		Crash: e.crash, ReadOnly: e.readOnly,
	}
	if e.readOnly {
		d.FS = fsx.ReadOnly(e.fs)
		d.KV = kv.ReadOnly(e.kv)
	}
	return d
}

type countingScanner struct {
	fakeScanner
	n *int
}

func (c countingScanner) Scan(ctx context.Context) ([]profile.Folder, error) {
	*c.n++
	return c.fakeScanner.Scan(ctx)
}

func (e *env) orch() *Orchestrator { return New(e.deps()) }

// yes are the options of a non-interactive switch that confirms a first
// sync.
func yes(to ids.Alias) SwitchOptions {
	return SwitchOptions{To: to, Yes: true, ConfirmFirstSync: true, LogOffset: -1}
}

func codeOf(t *testing.T, err error) pderr.Code {
	t.Helper()
	if errors.Is(err, ErrCrashInjected) {
		t.Fatalf("unexpected crash: %v", err)
	}
	return pderr.CodeOf(err)
}

// checkInvariants asserts I1 (Claude holds one registered account, or a
// pending add), I4 (every launch happened with a registered Claude) and
// I5 (every base that changed since markBases equals its folder's current
// content: bases advance only to harvested or applied state).
func (e *env) checkInvariants(label string) {
	e.t.Helper()
	root := e.st.root(e.t)
	if root.Run != nil {
		e.t.Errorf("%s: run still open: %+v", label, root.Run.Intents)
	}
	if !e.claudeRegistered() && root.PendingAdd == nil {
		e.t.Errorf("%s: I1: Claude holds %q", label, e.account(ids.DefaultFolder))
	}
	if e.app.badLaunch > 0 {
		e.t.Errorf("%s: I4: %d launch(es) without a registered Claude", label, e.app.badLaunch)
	}
	folderOf := map[ids.AccountID]string{}
	entries, _ := os.ReadDir(e.root)
	for _, en := range entries {
		if a := e.account(en.Name()); a != "" {
			if prev, dup := folderOf[a]; dup {
				e.t.Errorf("%s: account in two folders: %s and %s", label, prev, en.Name())
			}
			folderOf[a] = en.Name()
		}
	}
	for k, ref := range root.Bases {
		if e.basesBefore[k] == ref {
			continue // unchanged by this run
		}
		parts := strings.Split(k, "/")
		row := model.RowID(parts[len(parts)-1])
		ps := root.Profiles[ids.Alias(parts[0])]
		if ps == nil {
			continue
		}
		base, err := e.st.GetSnapshot(ref)
		if err != nil {
			e.t.Fatal(err)
		}
		b, present, _ := readContained(filepath.Join(e.root, folderOf[ps.Replica.Account]), rowFile(row))
		cur, _ := snapshotOf(row, b, present)
		if !sameItems(base, cur) {
			e.t.Errorf("%s: I5: base %s differs from the folder", label, k)
		}
	}
}
