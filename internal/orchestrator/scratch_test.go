package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/app"
	"github.com/kazupc/paritydesk/internal/apply"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/gen"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/rows"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/sources/scratch"
	"github.com/kazupc/paritydesk/internal/state"
	"github.com/kazupc/paritydesk/internal/testkit"
)

var (
	sLeaf  = "scratch-2026-01-02-0a0b0c"
	sWS    = string(acctA) + "/" + string(orgA) + "/" + sLeaf
	sMtime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
)

// scratchManifest is the test manifest plus 7.x's scratch row, the
// sessions row's scratch_ref role and the scratch root under synced.
func scratchManifest(t *testing.T) *compat.Manifest {
	t.Helper()
	ms, err := compat.Load("")
	if err != nil {
		t.Fatal(err)
	}
	m7, _, err := compat.Select(ms, "7.37095.0")
	if err != nil {
		t.Fatal(err)
	}
	sc, _ := m7.Row(model.RowScratch)
	m := testManifest()
	for i := range m.Rows {
		if m.Rows[i].Row == model.RowSessions {
			m.Rows[i].Fields["scratch_ref"] = []string{"cwd"}
		}
	}
	m.Rows = append(m.Rows, sc)
	m.Classification.Synced = append(m.Classification.Synced, "scratch-workspaces/**")
	m.TmpRecoveryGlobs = m7.TmpRecoveryGlobs
	return m
}

// newScratchEnv is the orchestrator env with row scratch wired to the
// real adapter, module, Compose (for scratch edits), Validate and apply.
// Profile a (Claude) has a session working in a scratch workspace.
func newScratchEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.compat.m = scratchManifest(t)
	e.gens.qdir = filepath.Join(t.TempDir(), "quarantine")
	cwd := filepath.Join(e.root, "Claude", "scratch-workspaces", filepath.FromSlash(sWS))
	e.writeRow(ids.DefaultFolder, model.RowSessions, map[string]any{
		string(fakeSession(1)): map[string]any{"title": "one", "cwd": cwd},
	})
	testkit.ScratchWorkspace(t, filepath.Join(e.root, ids.DefaultFolder), string(acctA), string(orgA), sLeaf,
		map[string]string{"notes.md": "hello", "src/app.py": "print(1)", ".git/HEAD": "ref", "node_modules/x.js": "dep"}, sMtime)
	e.tweak = func(d *Deps) {
		d.Adapters[model.RowScratch] = scratch.New(scratch.DefaultLimits(), nil, nil, nil)
		d.Rows[model.RowScratch] = rows.Registry()[model.RowScratch]
		d.Compose = func(edits []plan.Edit, src plan.Source) ([]plan.Op, error) {
			var scr, other []plan.Edit
			for _, ed := range edits {
				if ed.Row == model.RowScratch {
					scr = append(scr, ed)
				} else {
					other = append(other, ed)
				}
			}
			ops, err := fakeCompose(other, src)
			if err != nil {
				return nil, err
			}
			sops, err := plan.Compose(scr, src)
			return append(ops, sops...), err
		}
		d.Validate = func(in plan.ValidateInput) error {
			only := in
			p := *in.Plan
			p.Ops, p.Changes = nil, nil
			for _, op := range in.Plan.Ops {
				if op.Row == model.RowScratch {
					p.Ops = append(p.Ops, op)
				}
			}
			for _, c := range in.Plan.Changes {
				if c.Row == model.RowScratch {
					p.Changes = append(p.Changes, c)
				}
			}
			only.Plan, only.AllowPaths, only.DenyPaths = &p, nil, nil
			if err := plan.Validate(only); err != nil {
				return err
			}
			return e.valid.validate(in)
		}
		d.Applier = &scratchApplier{real: &apply.Applier{FS: e.fs, Holders: func(context.Context, string) (bool, string, error) { return false, "", nil }}, fake: e.applier}
	}
	return e
}

// scratchApplier applies row scratch's ops with the real applier and the
// fake rows' JSON writes with the fake one.
type scratchApplier struct {
	real *apply.Applier
	fake *fakeApplier
}

func split(p *plan.Plan) (scr, other *plan.Plan) {
	a, b := *p, *p
	a.Ops, b.Ops = nil, nil
	for _, op := range p.Ops {
		if op.Row == model.RowScratch || strings.HasPrefix(op.Path, "scratch-workspaces") {
			a.Ops = append(a.Ops, op)
		} else {
			b.Ops = append(b.Ops, op)
		}
	}
	return &a, &b
}

func (s *scratchApplier) Apply(ctx context.Context, in apply.Input) (apply.Result, error) {
	scr, other := split(in.Plan)
	res, err := s.fake.Apply(ctx, apply.Input{Plan: other, Folder: in.Folder, TmpDir: in.TmpDir, QuarantineDir: in.QuarantineDir})
	if err != nil || len(scr.Ops) == 0 {
		return res, err
	}
	in.Plan = scr
	r2, err := s.real.Apply(ctx, in)
	for k, v := range r2.PostHashes {
		res.PostHashes[k] = v
	}
	res.Touched = res.Touched || r2.Touched
	res.Applied += r2.Applied
	res.SoftSkipped = r2.SoftSkipped
	return res, err
}

func (s *scratchApplier) Check(ctx context.Context, p *plan.Plan, folder string) (bool, []string, error) {
	scr, other := split(p)
	ok1, bad1, err := s.fake.Check(ctx, other, folder)
	if err != nil {
		return false, nil, err
	}
	ok2, bad2, err := s.real.Check(ctx, scr, folder)
	return ok1 && ok2, append(bad1, bad2...), err
}

func wsFile(e *env, folder, rel string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(e.root, folder, "scratch-workspaces", filepath.FromSlash(sWS), rel))
	return string(b), err == nil
}

// scratchBase returns profile a's or b's scratch base, or nil.
func scratchBase(t *testing.T, e *env, a ids.Alias, rep model.Replica) *model.RowSnapshot {
	ref, ok := e.st.root(t).Bases[state.BaseKey{Alias: a, Replica: rep, Row: model.RowScratch}.String()]
	if !ok {
		return nil
	}
	s, err := e.st.GetSnapshot(ref)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// readScratch reads a folder's workspace like the adapter does.
func readScratch(t *testing.T, e *env, folder string) *model.RowSnapshot {
	sc, _ := e.compat.m.Row(model.RowScratch)
	s, err := scratch.New(scratch.DefaultLimits(), nil, nil, nil).ReadRefs(context.Background(),
		sources.ReadContext{Folder: filepath.Join(e.root, folder), Spec: sc, Manifest: e.compat.m}, []string{sWS})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestScratchSwitchCarriesTheMirror(t *testing.T) {
	e := newScratchEnv(t)
	res, err := e.orch().Switch(context.Background(), yes("b"))
	if err != nil {
		t.Fatalf("switch: %v", err)
	}
	// b (now in Claude) holds the mirror with the same bytes and mtimes.
	for rel, want := range map[string]string{"notes.md": "hello", "src/app.py": "print(1)", ".git/HEAD": "ref"} {
		if got, ok := wsFile(e, ids.DefaultFolder, rel); !ok || got != want {
			t.Errorf("%s = %q", rel, got)
		}
	}
	fi, err := os.Stat(filepath.Join(e.root, ids.DefaultFolder, "scratch-workspaces", sWS, "notes.md"))
	if err != nil || !fi.ModTime().Equal(sMtime) {
		t.Errorf("mtime %v %v", fi, err)
	}
	if _, ok := wsFile(e, ids.DefaultFolder, "node_modules/x.js"); ok {
		t.Error("a dependency folder was carried")
	}
	// The record still names the same path.
	b, _ := os.ReadFile(filepath.Join(e.root, ids.DefaultFolder, rowFile(model.RowSessions)))
	if !strings.Contains(string(b), "Claude/scratch-workspaces/"+sWS) {
		t.Errorf("record changed: %s", b)
	}
	// T's scratch base equals the real tree; root.json carries no scratch
	// post-hashes once the run is closed.
	base := scratchBase(t, e, "b", repB)
	if base == nil || !sameItems(base, readScratch(t, e, ids.DefaultFolder)) {
		t.Fatalf("scratch base of b: %v", base)
	}
	if res.Plan == nil || len(res.Plan.Ops) == 0 {
		t.Fatal("no plan")
	}
	e.checkInvariants("switch a->b")

	// b removes a file and changes one; switch back to a.
	ws := filepath.Join(e.root, ids.DefaultFolder, "scratch-workspaces", sWS)
	if err := os.Remove(filepath.Join(ws, "notes.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "src/app.py"), []byte("print(2)"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.app.running = true
	if _, err := e.orch().Switch(context.Background(), yes("a")); err != nil {
		t.Fatalf("switch back: %v", err)
	}
	if _, ok := wsFile(e, ids.DefaultFolder, "notes.md"); ok {
		t.Error("the removal was not carried")
	}
	if got, _ := wsFile(e, ids.DefaultFolder, "src/app.py"); got != "print(2)" {
		t.Errorf("app.py %q", got)
	}
	if _, err := os.Stat(filepath.Join(e.gens.qdir, "scratch-workspaces", sWS, "notes.md")); err != nil {
		t.Errorf("removed file not quarantined: %v", err)
	}
}

func TestScratchGates(t *testing.T) {
	ctx := context.Background()
	t.Run("sync skips", func(t *testing.T) {
		e := newScratchEnv(t)
		if _, err := e.orch().Switch(ctx, yes("b")); err != nil {
			t.Fatal(err)
		}
		e.app.running = true
		res, err := e.orch().Sync(ctx, SyncOptions{Yes: true})
		if err != nil {
			t.Fatal(err)
		}
		if res.Plan.Skipped[model.RowScratch] != reasonScratchSync {
			t.Errorf("skipped %v", res.Plan.Skipped)
		}
	})
	t.Run("managed edge skips", func(t *testing.T) {
		e := newScratchEnv(t)
		e.cfg.Profiles[1].ManagedByOrg = true
		res, err := e.orch().Preview(ctx, PreviewOptions{To: "b"})
		if err != nil {
			t.Fatal(err)
		}
		if res.Plan.Skipped[model.RowScratch] != reasonScratchManaged {
			t.Errorf("skipped %v", res.Plan.Skipped)
		}
		e.cfg.Scratch.CrossManaged = true
		res, _ = e.orch().Preview(ctx, PreviewOptions{To: "b"})
		if _, skipped := res.Plan.Skipped[model.RowScratch]; skipped {
			t.Error("cross_managed did not allow the row")
		}
	})
	t.Run("needs sessions", func(t *testing.T) {
		e := newScratchEnv(t)
		res, err := e.orch().Preview(ctx, PreviewOptions{To: "b", Skip: []model.Category{model.CatSessions}})
		if err != nil {
			t.Fatal(err)
		}
		if res.Plan.Skipped[model.RowScratch] != reasonScratchSessions {
			t.Errorf("skipped %v", res.Plan.Skipped)
		}
	})
	t.Run("sessions left out while planning", func(t *testing.T) {
		e := newScratchEnv(t)
		inner := e.tweak
		e.tweak = func(d *Deps) {
			inner(d)
			d.Adapters[model.RowSessions] = unavailableAdapter{d.Adapters[model.RowSessions]}
		}
		res, err := e.orch().Preview(ctx, PreviewOptions{To: "b"})
		if err != nil {
			t.Fatal(err)
		}
		if res.Plan.Skipped[model.RowScratch] != reasonScratchSessions || hasRowOps(res.Plan, model.RowScratch) {
			t.Errorf("skipped %v", res.Plan.Skipped)
		}
	})
	t.Run("only sessions carries scratch", func(t *testing.T) {
		e := newScratchEnv(t)
		res, err := e.orch().Preview(ctx, PreviewOptions{To: "b", Only: []model.Category{model.CatSessions}})
		if err != nil {
			t.Fatal(err)
		}
		if !hasRowOps(res.Plan, model.RowScratch) {
			t.Errorf("no scratch ops: %v", res.Plan.Skipped)
		}
	})
	t.Run("free space", func(t *testing.T) {
		e := newScratchEnv(t)
		e.fs.Free = minFreeBytes + 1 // enough for the generation, not for the copies
		res, err := e.orch().Switch(ctx, yes("b"))
		if err != nil {
			t.Fatal(err)
		}
		if res.Plan.Skipped[model.RowScratch] != reasonScratchSpace || hasRowOps(res.Plan, model.RowScratch) {
			t.Fatalf("skipped %v", res.Plan.Skipped)
		}
		root := e.st.root(t)
		if _, ok := root.Canonical[model.RowScratch]; ok {
			t.Error("canonical of the skipped row committed")
		}
		if scratchBase(t, e, "a", repA) != nil || scratchBase(t, e, "b", repB) != nil {
			t.Error("a base of the skipped row committed")
		}
		if _, ok := root.Bases[state.BaseKey{Alias: "b", Replica: repB, Row: model.RowSessions}.String()]; !ok {
			t.Error("the other rows lost their bases")
		}
	})
}

// unavailableAdapter is an adapter whose source cannot be read.
type unavailableAdapter struct{ sources.Adapter }

func (unavailableAdapter) Read(context.Context, sources.ReadContext) (*model.RowSnapshot, error) {
	return nil, sources.ErrSourceUnavailable
}

func hasRowOps(p *plan.Plan, r model.RowID) bool {
	for _, op := range p.Ops {
		if op.Row == r {
			return true
		}
	}
	return false
}

func TestScratchReadOnlyPlanWritesNothing(t *testing.T) {
	e := newScratchEnv(t)
	e.readOnly = true
	before := len(e.fs.Calls)
	res, err := e.orch().Preview(context.Background(), PreviewOptions{To: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasRowOps(res.Plan, model.RowScratch) {
		t.Fatal("no scratch ops planned")
	}
	for _, c := range e.fs.Calls[before:] {
		for _, w := range []string{"WriteFileAtomic", "Mkdir", "CloneFileAtomic", "Symlink", "MoveInto"} {
			if strings.HasPrefix(c, w+" ") {
				t.Errorf("write in read-only mode: %s", c)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(e.root, "state", statCacheRel)); err == nil {
		t.Error("the stat cache was saved")
	}
}

// quitHook runs a function when the app is quit (between P2 and P5).
type quitHook struct {
	*fakeApp
	onQuit func()
}

func (q quitHook) QuitAndWait(ctx context.Context, b, p string, sig compat.Signals, to time.Duration, onVeto func()) (app.QuitResult, error) {
	if q.onQuit != nil {
		q.onQuit()
	}
	return q.fakeApp.QuitAndWait(ctx, b, p, sig, to, onVeto)
}

func TestScratchRemovalAfterPreviewIsDeferred(t *testing.T) {
	e := newScratchEnv(t)
	ctx := context.Background()
	if _, err := e.orch().Switch(ctx, yes("b")); err != nil {
		t.Fatal(err)
	}
	e.app.running = true
	if _, err := e.orch().Switch(ctx, yes("a")); err != nil {
		t.Fatal(err)
	}
	// a removes a file after P2 of the next switch (a build cleaning up).
	e.app.running = true
	inner := e.tweak
	e.tweak = func(d *Deps) {
		inner(d)
		d.App = quitHook{fakeApp: e.app, onQuit: func() {
			_ = os.Remove(filepath.Join(e.root, ids.DefaultFolder, "scratch-workspaces", sWS, "notes.md"))
		}}
	}
	res, err := e.orch().Switch(ctx, yes("b"))
	if err != nil {
		t.Fatalf("switch with a removal after the preview: %v", err)
	}
	if _, ok := wsFile(e, ids.DefaultFolder, "notes.md"); !ok {
		t.Error("the new removal was carried without being previewed")
	}
	deferred := false
	for _, c := range res.Plan.Changes {
		if c.Note == scratch.NoteDeletionDefer {
			deferred = true
		}
		if c.Row == model.RowScratch && c.Kind == model.ChangeDeleted {
			t.Errorf("deletion in the final plan: %+v", c)
		}
	}
	if !deferred {
		t.Error("no deletion-deferred note")
	}
	if base := scratchBase(t, e, "b", repB); base == nil || !sameItems(base, readScratch(t, e, ids.DefaultFolder)) {
		t.Error("T's scratch base does not match its tree")
	}
}

// A detached run with a confirmed plan: a scratch file removed after the
// person's plan is deferred at P2 too, so the run does not stop with exit
// 3 (spec-scratch 3.3, checklist S-7).
func TestScratchRemovalAfterConfirmedPlanIsDeferred(t *testing.T) {
	e := newScratchEnv(t)
	ctx := context.Background()
	if _, err := e.orch().Switch(ctx, yes("b")); err != nil {
		t.Fatal(err)
	}
	e.app.running = true
	if _, err := e.orch().Switch(ctx, yes("a")); err != nil {
		t.Fatal(err)
	}
	pv, err := e.orch().Preview(ctx, PreviewOptions{To: "b", Save: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(e.root, ids.DefaultFolder, "scratch-workspaces", sWS, "notes.md")); err != nil {
		t.Fatal(err)
	}
	res, err := e.orch().Switch(ctx, SwitchOptions{To: "b", FromRunner: true, ConfirmedPlan: pv.PlanHash, LogOffset: 10})
	if err != nil {
		t.Fatalf("switch with a removal after the confirmed plan: %v", err)
	}
	if _, ok := wsFile(e, ids.DefaultFolder, "notes.md"); !ok {
		t.Error("the removal was carried without being in the confirmed plan")
	}
	deferred := false
	for _, c := range res.Plan.Changes {
		if c.Note == scratch.NoteDeletionDefer {
			deferred = true
		}
	}
	if !deferred {
		t.Error("no deletion-deferred note")
	}
}

func TestScratchCrashAfterHarvest(t *testing.T) {
	e := newScratchEnv(t)
	n := 0
	e.crash = func(p Phase) bool { n++; return p == P5Harvest && n > 0 }
	_, err := e.orch().Switch(context.Background(), yes("b"))
	if !errors.Is(err, ErrCrashInjected) {
		t.Fatalf("no crash: %v", err)
	}
	root := e.st.root(t)
	if root.Run == nil {
		t.Fatal("no open run")
	}
	for k := range root.Run.PostHashes {
		if strings.Contains(k, "scratch-workspaces") {
			t.Errorf("scratch post-hash inline: %s", k)
		}
	}
	if root.Run.PostHashesRef == "" {
		t.Error("no scratch post-hash object")
	}
	data, _ := intentData(root.Run, state.IntentPlanned)
	if data[intentScratchRefs] != sWS {
		t.Errorf("scratch_refs %q", data[intentScratchRefs])
	}
	e.crash = nil
	if _, err := e.orch().Recover(context.Background(), RecoverOptions{Yes: true}); err != nil {
		t.Fatal(err)
	}
	e.checkInvariants("recover after P5")
}

func TestScratchCrashMatrix(t *testing.T) {
	ctx := context.Background()
	for k := 1; k < 40; k++ {
		e := newScratchEnv(t)
		e.markBases()
		n := 0
		e.crash = func(Phase) bool { n++; return n == k }
		_, err := e.orch().Switch(ctx, yes("b"))
		if !errors.Is(err, ErrCrashInjected) {
			if err != nil {
				t.Fatalf("run without a crash failed: %v", err)
			}
			return
		}
		e.crash = nil
		if _, err := e.orch().Recover(ctx, RecoverOptions{Yes: true}); err != nil {
			t.Fatalf("crash %d: recover: %v", k, err)
		}
		e.checkInvariants("crash")
		// T's scratch base is absent or equals the real tree.
		root := e.st.root(t)
		for _, a := range []ids.Alias{"a", "b"} {
			ps := root.Profiles[a]
			base := scratchBase(t, e, a, ps.Replica)
			if base == nil {
				continue
			}
			folder := ids.DefaultFolder
			if root.Active != a {
				folder = "Claude-" + string(a)
			}
			if !sameItems(base, readScratch(t, e, folder)) {
				t.Errorf("crash %d: %s's scratch base differs from its tree", k, a)
			}
		}
	}
}

// noScratchTree reports an error unless folder has no scratch-workspaces
// directory left.
func noScratchTree(t *testing.T, e *env, folder string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(e.root, folder, "scratch-workspaces")); !errors.Is(err, fs.ErrNotExist) {
		var left []string
		_ = filepath.WalkDir(filepath.Join(e.root, folder, "scratch-workspaces"), func(p string, _ fs.DirEntry, _ error) error {
			left = append(left, strings.TrimPrefix(p, e.root))
			return nil
		})
		t.Errorf("%s keeps the created scratch tree: %v", folder, left)
	}
}

// A rollback removes the directories the switch created (spec-scratch
// 6.7): T's scratch tree is its pre-run tree, with no restore conflicts.
func TestScratchRollbackRemovesCreatedDirs(t *testing.T) {
	e := newScratchEnv(t)
	ctx := context.Background()
	if _, err := e.orch().Switch(ctx, yes("b")); err != nil {
		t.Fatal(err)
	}
	if _, ok := wsFile(e, ids.DefaultFolder, "notes.md"); !ok {
		t.Fatal("the workspace was not carried")
	}
	e.ui.interactive = true
	res, err := e.orch().Rollback(ctx, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Conflicts) != 0 {
		t.Errorf("restore conflicts: %v", res.Conflicts)
	}
	if e.gens.lastRestore.ScratchRoot != "scratch-workspaces" {
		t.Errorf("RestoreInput.ScratchRoot = %q", e.gens.lastRestore.ScratchRoot)
	}
	noScratchTree(t, e, ids.DefaultFolder)
}

// A failed apply restores the entering folder: the directories it
// created are removed and reported as restored, not as conflicts.
func TestScratchFailedApplyRemovesCreatedDirs(t *testing.T) {
	e := newScratchEnv(t)
	inner := e.tweak
	e.tweak = func(d *Deps) {
		inner(d)
		d.WriteMarker = func(fsx.Ops, string, model.Marker) error { return errors.New("injected marker failure") }
	}
	_, err := e.orch().Switch(context.Background(), yes("b"))
	if pderr.CodeOf(err) != pderr.ApplyRolledBack {
		t.Fatalf("switch: %v", err)
	}
	if e.gens.restores == 0 {
		t.Fatal("nothing was restored")
	}
	if e.gens.lastRestore.ScratchRoot != "scratch-workspaces" {
		t.Errorf("RestoreInput.ScratchRoot = %q", e.gens.lastRestore.ScratchRoot)
	}
	if len(e.gens.lastReport.Conflicts) != 0 {
		t.Errorf("restore conflicts: %+v", e.gens.lastReport.Conflicts)
	}
	noScratchTree(t, e, "Claude-b")
	e.checkInvariants("failed apply")
}

// ---- unit level ----

func TestPostHashKeysByKind(t *testing.T) {
	p := &plan.Plan{Ops: []plan.Op{
		{Kind: plan.OpMkdir, Path: "scratch-workspaces/x", PostHash: plan.DirHash(0o755)},
		{Kind: plan.OpSymlink, Path: "scratch-workspaces/l", PostHash: plan.LinkHash("t")},
		{Kind: plan.OpCopyFile, Path: "scratch-workspaces/f", PostHash: "h"},
		{Kind: plan.OpQuarantine, Path: "scratch-workspaces/q"},
		{Kind: plan.OpLSPut, Key: "k", PostHash: "v"},
	}}
	ph := postHashes(p)
	for _, k := range []string{"dir:scratch-workspaces/x", "link:scratch-workspaces/l", "path:scratch-workspaces/f", "path:scratch-workspaces/q", "ls:k"} {
		if _, ok := ph[k]; !ok {
			t.Errorf("missing %s in %v", k, ph)
		}
	}
	back := planFromPostHashes(ph, plan.Target{})
	kinds := map[string]plan.Kind{}
	for _, op := range back.Ops {
		kinds[postHashKey(op)] = op.Kind
	}
	if kinds["dir:scratch-workspaces/x"] != plan.OpMkdir || kinds["link:scratch-workspaces/l"] != plan.OpSymlink ||
		kinds["path:scratch-workspaces/q"] != plan.OpQuarantine || kinds["path:scratch-workspaces/f"] != plan.OpWriteFile {
		t.Errorf("round trip %v", kinds)
	}
	for k, v := range postHashes(back) {
		if ph[k] != v {
			t.Errorf("%s: %q != %q", k, v, ph[k])
		}
	}
}

func TestFillGenerationSkipsScratchConflicts(t *testing.T) {
	e := newEnv(t)
	r := e.orch().newRun(state.RunSwitch, SwitchOptions{})
	r.tPath = filepath.Join(e.root, ids.DefaultFolder)
	r.p5 = &planned{Work: map[model.RowID]*rowWork{}}
	w, _ := e.gens.Begin(1, "r", "a", repA)
	p := &plan.Plan{Conflicts: []model.Conflict{{Row: model.RowScratch, Key: sWS + "/f", Loser: "b", LoserHash: "x"}}}
	if err := r.fillGeneration(w, p); err != nil {
		t.Fatal(err)
	}
	for _, en := range e.gens.gens[1].m.Entries {
		if en.Kind == gen.EntryConflict {
			t.Error("a scratch conflict was saved")
		}
	}
}

func TestFoldScratchSkipsDesiredOnly(t *testing.T) {
	mk := func(k, sha string) model.Item {
		it, _ := scratch.FileItem(k, sha, 1, 0o644, 0)
		return it
	}
	a, b := sWS+"/a", sWS+"/d/b"
	tSnap := &model.RowSnapshot{Row: model.RowScratch, Items: map[string]model.Item{a: mk(a, strings.Repeat("1", 64))}}
	desired := &model.RowSnapshot{Row: model.RowScratch, Items: map[string]model.Item{a: mk(a, strings.Repeat("2", 64)), b: mk(b, strings.Repeat("3", 64))}}
	canon := desired.Clone()
	w := &rowWork{T: tSnap, Out: &rows.Output{Desired: desired, Canonical: canon,
		Changes: []model.Change{{Row: model.RowScratch, Kind: model.ChangeUpdated, Key: a}, {Row: model.RowScratch, Kind: model.ChangeAdded, Key: b}}}}
	res := &sources.OpsResult{Skipped: []model.Change{
		{Row: model.RowScratch, Kind: model.ChangeSkipped, Key: a, Note: scratch.NoteBytes},
		{Row: model.RowScratch, Kind: model.ChangeSkipped, Key: sWS + "/d/", Note: scratch.NoteCollision},
	}}
	(&Orchestrator{}).foldScratchSkips(w, res)
	if w.Out.Desired.Items[a].Hash != tSnap.Items[a].Hash {
		t.Error("a skipped file did not take T's item")
	}
	if _, ok := w.Out.Desired.Items[b]; ok {
		t.Error("a file below a skipped directory stayed in Desired")
	}
	if canon.Items[a].Hash != mk(a, strings.Repeat("2", 64)).Hash || len(canon.Items) != 2 {
		t.Error("canonical was changed")
	}
	if len(w.Out.Changes) != 0 {
		t.Errorf("changes of skipped keys kept: %+v", w.Out.Changes)
	}
}

func TestScratchRefsFromIntent(t *testing.T) {
	if got := scratchRefsFromIntent(map[string]string{}); got != nil {
		t.Error("refs without an intent key")
	}
	got := scratchRefsFromIntent(map[string]string{intentScratchRefs: ""})
	if refs, ok := got[model.RowScratch]; !ok || len(refs) != 0 {
		t.Errorf("empty carry set %v", got)
	}
	if got := scratchRefsFromIntent(map[string]string{intentScratchRefs: "bad"}); got != nil {
		t.Error("a malformed carry set was used")
	}
	b, _ := json.Marshal(scratchRefsFromIntent(map[string]string{intentScratchRefs: sWS}))
	if !strings.Contains(string(b), sLeaf) {
		t.Errorf("%s", b)
	}
}

func TestDoctorScratchChecks(t *testing.T) {
	e := newScratchEnv(t)
	ctx := context.Background()
	if _, err := e.orch().Switch(ctx, yes("b")); err != nil {
		t.Fatal(err)
	}
	checks, err := e.orch().Doctor(ctx, DoctorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Check{}
	var order []string
	for _, c := range checks {
		got[c.ID] = c
		order = append(order, c.ID)
	}
	for _, id := range []string{"scratch", "scratch-orphans", "scratch-kept"} {
		c, ok := got[id]
		if !ok || c.Status != "ok" {
			t.Errorf("%s: %+v", id, c)
		}
		if strings.Contains(c.Detail, string(acctA)) {
			t.Errorf("%s names a scope id: %q", id, c.Detail)
		}
	}
	if !strings.Contains(got["scratch"].Detail, "0 of this account, 1 carried") || !strings.Contains(got["scratch"].Detail, "Browser pane") {
		t.Errorf("scratch: %q", got["scratch"].Detail)
	}
	// The fake folders keep no session records: the mirror is an orphan.
	if !strings.Contains(got["scratch-orphans"].Detail, sLeaf) {
		t.Errorf("orphans: %q", got["scratch-orphans"].Detail)
	}
	joined := strings.Join(order, " ")
	if !strings.Contains(joined, "runner scratch scratch-orphans scratch-kept") {
		t.Errorf("order %s", joined)
	}
}

// doctor --json always lists the three scratch checks, also without a
// scratch row and with a scratch root that is not a directory.
func TestDoctorScratchChecksAlwaysPresent(t *testing.T) {
	ctx := context.Background()
	noRow := newEnv(t)
	notDir := newScratchEnv(t)
	sr := filepath.Join(notDir.root, ids.DefaultFolder, "scratch-workspaces")
	if err := os.RemoveAll(sr); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sr, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, e := range map[string]*env{"no scratch row": noRow, "root not a directory": notDir} {
		checks, err := e.orch().Doctor(ctx, DoctorOptions{})
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]Check{}
		for _, c := range checks {
			got[c.ID] = c
		}
		for _, id := range []string{"scratch", "scratch-orphans", "scratch-kept"} {
			if c, ok := got[id]; !ok || c.Detail == "" {
				t.Errorf("%s: %s: %+v", name, id, c)
			}
		}
		if got["scratch-orphans"].Status != "ok" {
			t.Errorf("%s: orphans %+v", name, got["scratch-orphans"])
		}
	}
}
