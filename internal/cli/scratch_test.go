package cli

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/clock"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/fsx/fsxtest"
	"github.com/kazupc/paritydesk/internal/gen"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/orchestrator"
	"github.com/kazupc/paritydesk/internal/plan"
)

// Fake scratch ids (spec-scratch 14): account and org of the leaving
// profile (a, b), of the entering one (c, d).
const (
	scrAcctA = "00000000-0000-4000-8000-00000000000a"
	scrOrgA  = "00000000-0000-4000-8000-00000000000b"
	scrAcctB = "00000000-0000-4000-8000-00000000000c"
	scrOrgB  = "00000000-0000-4000-8000-00000000000d"
)

func scrHash(c byte) string { return strings.Repeat(string(c), 64) }

// scratchPlan is a plan with every kind of scratch outcome: adds,
// updates, a conflict copy, a quarantine, a git unit replaced, a
// diverged unit, an oversize and a missing workspace, held-back folders.
func scratchPlan() *plan.Plan {
	ws := scrAcctA + "/" + scrOrgA + "/scratch-2026-01-02-0a0b0c"
	ws2 := scrAcctA + "/" + scrOrgA + "/scratch-2026-01-03-0d0e0f"
	ws3 := scrAcctA + "/" + scrOrgA + "/scratch-2026-01-04-101112"
	p := func(rel string) string { return "scratch-workspaces/" + ws + "/" + rel }
	cp := func(rel, pre, post string, side string, size int64) plan.Op {
		return plan.Op{Kind: plan.OpCopyFile, Row: model.RowScratch, Path: p(rel), Mode: 0o644, PreHash: pre, PostHash: post,
			Items: []string{ws + "/" + rel}, Copy: &plan.CopySource{Side: side, Path: p(rel), Hash: post, Size: size, MTimeNs: fixedTime.UnixNano()}}
	}
	skip := func(key, note string) model.Change {
		return model.Change{Row: model.RowScratch, Kind: model.ChangeSkipped, Key: key, Note: note}
	}
	return &plan.Plan{
		Version: 1, RunID: "run-0001", From: "work",
		Target:      plan.Target{Alias: "home", Folder: "Claude-home", Replica: model.Replica{Account: scrAcctB, Org: scrOrgB}},
		Approximate: true, CreatedAt: fixedTime,
		Ops: []plan.Op{
			{Kind: plan.OpMkdir, Row: model.RowScratch, Path: "scratch-workspaces/" + scrAcctA, Mode: 0o700, PostHash: plan.DirHash(0o700)},
			cp("notes.md", "", scrHash('1'), plan.SideLeaving, 120),
			cp("app.py", scrHash('2'), scrHash('3'), plan.SideLeaving, 2048),
			cp("app (conflict from home 2026-01-02).py", "", scrHash('2'), plan.SideEntering, 1024),
			cp(".git/HEAD", scrHash('4'), scrHash('5'), plan.SideLeaving, 23),
			{Kind: plan.OpQuarantine, Row: model.RowScratch, Path: p("old.txt"), PreHash: scrHash('6'), Destructive: true,
				Items: []string{ws + "/old.txt"}},
		},
		Changes: []model.Change{
			{Row: model.RowScratch, Kind: model.ChangeAdded, Key: ws + "/notes.md"},
			{Row: model.RowScratch, Kind: model.ChangeUpdated, Key: ws + "/app.py"},
			{Row: model.RowScratch, Kind: model.ChangeAdded, Key: ws + "/app (conflict from home 2026-01-02).py"},
			{Row: model.RowScratch, Kind: model.ChangeUpdated, Key: ws + "/.git/"},
			{Row: model.RowScratch, Kind: model.ChangeUpdated, Key: ws + "/.git/HEAD"},
			{Row: model.RowScratch, Kind: model.ChangeDeleted, Key: ws + "/old.txt"},
			skip(ws+"/node_modules/", "held-back"),
			skip(ws+"/sub/.git/", "git-diverged"),
			skip(ws2, "oversize-files"),
			skip(ws3, "missing"),
		},
		Conflicts: []model.Conflict{{Row: model.RowScratch, Key: ws + "/app.py", Winner: "work", Loser: "home", LoserHash: scrHash('2'), Reason: "epoch"}},
	}
}

func scratchResult() *orchestrator.Result {
	p := scratchPlan()
	// Planning sets the limits the notices name (memory only).
	p.ScratchNotes = &plan.ScratchNotes{MaxFilesPerWorkspace: 20000, MaxWorkspaceBytes: 2 << 30, MaxRunBytes: 8 << 30, MaxRunFiles: 50000}
	return &orchestrator.Result{RunID: "run-0001", From: p.From, To: p.Target.Alias, Mode: compat.Mode("full"),
		Plan: p, PlanHash: p.Hash(), DestructiveDigest: p.DestructiveDigest()}
}

func init() {
	jsonCases = append(jsonCases, struct {
		name  string
		args  []string
		setup func(h *harness)
		code  int
	}{name: "plan_scratch", args: []string{"plan", "home"}, setup: func(h *harness) { h.svc.result = scratchResult() }})
}

func TestPlanScratchTextGolden(t *testing.T) {
	h := newHarness(t)
	h.svc.result = scratchResult()
	code, out, errOut := h.run("", "plan", "home")
	if code != 0 {
		t.Fatalf("code %d: %s", code, errOut)
	}
	golden(t, "text/plan_scratch", h.normalize(out))
	_, js, _ := h.run("", "--json", "plan", "home")
	if !strings.Contains(js, `"DESTRUCTIVE: 1 deletions (scratch 1)"]`) {
		t.Errorf("the DESTRUCTIVE line is not the last plan line: %s", js)
	}
	for _, want := range []string{"scratch", "conflict from <account> <date>", "backup generation", "Browser pane previews"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan text lacks %q:\n%s", want, out)
		}
	}
	for _, id := range []string{scrAcctA, scrOrgA} {
		if strings.Contains(out, id) {
			t.Errorf("plan text names a scope id:\n%s", out)
		}
	}
}

func TestOnlyScratchNeedsSessions(t *testing.T) {
	h := newHarness(t)
	code, _, errOut := h.run("", "plan", "--only", "scratch", "home")
	if code != 2 || !strings.Contains(errOut, "scratch workspaces are carried with sessions") {
		t.Fatalf("--only scratch: code %d, stderr %q", code, errOut)
	}
	code, _, errOut = h.run("", "plan", "--only", "sessions,scratch", "home")
	if code != 0 {
		t.Fatalf("--only sessions,scratch: code %d, %s", code, errOut)
	}
	code, _, errOut = h.run("", "plan", "--only", "sessions", "--skip", "scratch", "home")
	if code != 0 || len(h.svc.previewOpts.Skip) != 1 || h.svc.previewOpts.Skip[0] != model.CatScratch {
		t.Fatalf("--only sessions --skip scratch: code %d, %+v, %s", code, h.svc.previewOpts, errOut)
	}
}

// keptGens is a generation store that keeps scratch sets.
type keptGens struct {
	*fakeGens
	sets    []gen.KeptInfo
	removed int
	cutoff  time.Time
}

func (g *keptGens) ScratchKept() ([]gen.KeptInfo, error) { return g.sets, nil }

func (g *keptGens) RemoveScratchKept(cutoff time.Time) (int, int64, error) {
	g.cutoff = cutoff
	n, b := 0, int64(0)
	for _, s := range g.sets {
		if cutoff.IsZero() || s.Created.Before(cutoff) {
			n++
			b += s.Bytes
		}
	}
	g.removed += n
	return n, b, nil
}

func keptHarness(t *testing.T) (*harness, *keptGens) {
	h := newHarness(t)
	g := &keptGens{fakeGens: h.gens, sets: []gen.KeptInfo{
		{N: 3, Created: fixedTime.Add(-40 * 24 * time.Hour), Files: 2, Bytes: 4096, Listed: true},
		{N: 9, Created: fixedTime.Add(-2 * 24 * time.Hour), Files: 1, Bytes: 10, Listed: true},
	}}
	h.sys.gens = func(context.Context, *env) (generationStore, error) { return g, nil }
	return h, g
}

func TestBackupsPruneScratch(t *testing.T) {
	t.Run("declined", func(t *testing.T) {
		h, g := keptHarness(t)
		h.tty = true
		code, out, errOut := h.run("n\n", "backups", "prune", "--scratch")
		if code != 3 || g.removed != 0 || !strings.Contains(out+errOut, "Permanently delete 2 kept sets") {
			t.Fatalf("code %d removed %d out %q %q", code, g.removed, out, errOut)
		}
	})
	t.Run("accepted", func(t *testing.T) {
		h, g := keptHarness(t)
		h.tty = true
		code, out, _ := h.run("y\n", "backups", "prune", "--scratch")
		if code != 0 || g.removed != 2 || !strings.Contains(out, "Removed 2 kept sets") {
			t.Fatalf("code %d removed %d out %q", code, g.removed, out)
		}
	})
	t.Run("yes skips the question, older-than filters", func(t *testing.T) {
		h, g := keptHarness(t)
		code, out, _ := h.run("", "--json", "backups", "prune", "--scratch", "--older-than", "30", "--yes")
		if code != 0 || g.removed != 1 || !strings.Contains(out, `"scratch_kept_removed":1`) || !strings.Contains(out, `"bytes":4096`) {
			t.Fatalf("code %d removed %d out %q", code, g.removed, out)
		}
		if want := fixedTime.Add(-30 * 24 * time.Hour); !g.cutoff.Equal(want) {
			t.Errorf("cutoff %v, want %v", g.cutoff, want)
		}
	})
	t.Run("older-than needs scratch", func(t *testing.T) {
		h, _ := keptHarness(t)
		if code, _, _ := h.run("", "backups", "prune", "--older-than", "3"); code != 2 {
			t.Fatalf("code %d", code)
		}
	})
	t.Run("refused while a run is open", func(t *testing.T) {
		h, g := keptHarness(t)
		writeFile(h, "Library/Application Support/paritydesk/state/root.json", `{"v":2,"run":{"id":"r","kind":"switch"}}`)
		code, _, _ := h.run("", "backups", "prune", "--scratch", "--yes")
		if code == 0 || g.removed != 0 {
			t.Fatalf("pruned while a run is open: code %d removed %d", code, g.removed)
		}
	})
}

// End to end over a real generation store (spec-scratch 9.4, 14.5):
// `backups prune` past backups.keep moves a quarantined scratch file into
// scratch-kept, and `backups prune --scratch --yes` removes it.
func TestBackupsPruneScratchRealStore(t *testing.T) {
	h := newHarness(t)
	fsys := fsxtest.New(t)
	store := gen.Open(filepath.Join(h.home, "generations"), fsys, clock.NewFake(fixedTime))
	h.sys.gens = func(context.Context, *env) (generationStore, error) { return store, nil }
	folder := filepath.Join(h.home, "data", "Claude")
	rel := "scratch-workspaces/" + scrAcctA + "/" + scrOrgA + "/scratch-2026-01-02-0a0b0c/old.txt"
	if err := os.MkdirAll(filepath.Join(folder, filepath.Dir(rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, rel), []byte("removed by a switch"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := model.Replica{Account: scrAcctB, Org: scrOrgB}
	// Generation 1 quarantines the file the way apply does.
	w, err := store.Begin(1, "run-0001", "home", rep)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.SaveFile(model.RowScratch, folder, rel, ""); err != nil {
		t.Fatal(err)
	}
	if err := fsys.MoveInto(folder, rel, w.QuarantineDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	// Generation 2 saves another row's file.
	if err := os.WriteFile(filepath.Join(folder, "other.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if w, err = store.Begin(2, "run-0002", "home", rep); err != nil {
		t.Fatal(err)
	}
	if err := w.SaveFile(model.RowSessions, folder, "other.json", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	writeFile(h, ".config/paritydesk/config.toml", "version = 1\n[backups]\nkeep = 1\n")
	if code, _, errOut := h.run("", "backups", "prune"); code != 0 {
		t.Fatalf("backups prune: code %d: %s", code, errOut)
	}
	sets, err := store.ScratchKept()
	if err != nil || len(sets) != 1 || sets[0].N != 1 || sets[0].Files != 1 {
		t.Fatalf("kept sets %+v %v", sets, err)
	}
	kept := filepath.Join(store.ScratchKeptPath(), "1", "files", filepath.FromSlash(rel))
	if b, err := os.ReadFile(kept); err != nil || string(b) != "removed by a switch" {
		t.Fatalf("kept file %q %v", b, err)
	}
	code, out, errOut := h.run("", "--json", "backups", "prune", "--scratch", "--yes")
	if code != 0 || !strings.Contains(out, `"scratch_kept_removed":1`) {
		t.Fatalf("prune --scratch: code %d out %q err %q", code, out, errOut)
	}
	if _, err := os.Lstat(kept); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("kept file still there: %v", err)
	}
	if sets, _ := store.ScratchKept(); len(sets) != 0 {
		t.Errorf("kept sets left: %+v", sets)
	}
}
