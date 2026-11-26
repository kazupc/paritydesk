package orchestrator

// Orchestrator integration tests of row scratch (spec-scratch 14.5) that
// drive whole switches over real workspace trees: soft copies, the
// deletion breaker, conflicts, git units, rollback, pruning to
// scratch-kept, the size of root.json and the crash matrix.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/gen"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/render"
	"github.com/kazupc/paritydesk/internal/sources/scratch"
	"github.com/kazupc/paritydesk/internal/state"
)

// wsDir is the workspace directory of sWS in folder.
func wsDir(e *env, folder string) string {
	return filepath.Join(e.root, folder, "scratch-workspaces", filepath.FromSlash(sWS))
}

// folderOfAlias is the folder that holds profile a now.
func folderOfAlias(t *testing.T, e *env, a ids.Alias) string {
	t.Helper()
	if e.st.root(t).Active == a {
		return ids.DefaultFolder
	}
	return "Claude-" + string(a)
}

// putWS writes a workspace file (creating its folders) with mtime.
func putWS(t *testing.T, e *env, folder, rel, content string, mtime time.Time) {
	t.Helper()
	p := filepath.Join(wsDir(e, folder), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// rmWS removes a workspace path (and what is below it).
func rmWS(t *testing.T, e *env, folder, rel string) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(wsDir(e, folder), filepath.FromSlash(rel))); err != nil {
		t.Fatal(err)
	}
}

// wsTree lists a workspace: files as content, symlinks as "->target",
// directories as "/" (held-back node_modules left out). A missing
// workspace is an empty tree.
func wsTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) && p == dir {
			return filepath.SkipAll
		}
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		switch {
		case rel == ".":
			return nil
		case rel == "node_modules":
			return filepath.SkipDir
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			out[rel] = "->" + target
			return err
		case d.IsDir():
			out[rel] = "/"
			return nil
		}
		b, err := os.ReadFile(p)
		out[rel] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// subTree returns the entries of tr at or below prefix.
func subTree(tr map[string]string, prefix string) map[string]string {
	out := map[string]string{}
	for k, v := range tr {
		if k == prefix || strings.HasPrefix(k, prefix+"/") {
			out[k] = v
		}
	}
	return out
}

func sameTree(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

func treeDiff(a, b map[string]string) string {
	var d []string
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			d = append(d, fmt.Sprintf("%s: %q vs %q", k, v, w))
		}
	}
	for k, w := range b {
		if _, ok := a[k]; !ok {
			d = append(d, fmt.Sprintf("%s: absent vs %q", k, w))
		}
	}
	return strings.Join(d, "; ")
}

// switchOK switches to a with the app running (so P2 previews the plan)
// and fails the test on an error.
func switchOK(t *testing.T, e *env, a ids.Alias) *Result {
	t.Helper()
	e.app.running = true
	res, err := e.orch().Switch(context.Background(), yes(a))
	if err != nil {
		t.Fatalf("switch to %s: %v", a, err)
	}
	return res
}

// bothCarry returns a scratch env where a and b both hold the workspace
// with valid bases: a -> b, then b -> a (a is active again).
func bothCarry(t *testing.T, setup func(e *env)) *env {
	t.Helper()
	e := newScratchEnv(t)
	if setup != nil {
		setup(e)
	}
	switchOK(t, e, "b")
	switchOK(t, e, "a")
	if scratchBase(t, e, "a", repA) == nil || scratchBase(t, e, "b", repB) == nil {
		t.Fatal("setup: a scratch base is missing")
	}
	return e
}

// checkScratchBase asserts that a's scratch base exists and equals the
// tree of the folder that holds a.
func checkScratchBase(t *testing.T, e *env, a ids.Alias, rep model.Replica) {
	t.Helper()
	base := scratchBase(t, e, a, rep)
	if base == nil {
		t.Fatalf("%s has no scratch base", a)
	}
	if !sameItems(base, readScratch(t, e, folderOfAlias(t, e, a))) {
		t.Errorf("%s's scratch base differs from its tree", a)
	}
}

func scratchChanges(p *plan.Plan, kind model.ChangeKind) []model.Change {
	var out []model.Change
	for _, c := range p.Changes {
		if c.Row == model.RowScratch && c.Kind == kind {
			out = append(out, c)
		}
	}
	return out
}

func hasScratchNote(p *plan.Plan, note string) bool {
	for _, c := range p.Changes {
		if c.Row == model.RowScratch && c.Kind == model.ChangeSkipped && c.Note == note {
			return true
		}
	}
	return false
}

// useRealGens replaces the fake generations with a gen.Store over a
// temporary directory, so restores and pruning run the real code.
func useRealGens(t *testing.T, e *env) *gen.Store {
	t.Helper()
	store := gen.Open(filepath.Join(e.root, "state", "generations"), e.fs, e.clk)
	inner := e.tweak
	e.tweak = func(d *Deps) {
		inner(d)
		d.Gens = store
	}
	return store
}

// ---- soft copies (3.3) ----

// A leaving-side file rewritten between P5 and P7 is a soft skip: the
// switch succeeds, the plan result counts it, T's scratch base stays
// valid, and later switches carry the newer content.
func TestScratchSoftCopyEndToEnd(t *testing.T) {
	e := newScratchEnv(t)
	newer := sMtime.Add(time.Hour)
	fired := false
	e.crash = func(p Phase) bool {
		if p == P6Backup && !fired {
			fired = true
			putWS(t, e, ids.DefaultFolder, "notes.md", "rewritten", newer)
		}
		return false
	}
	res := switchOK(t, e, "b")
	e.crash = nil
	if !fired {
		t.Fatal("the hook did not run")
	}
	sum := render.SummarizeScratch(res.Plan)
	if sum == nil || sum.SourceChanged != 1 {
		t.Fatalf("summary %+v", sum)
	}
	if !hasScratchNote(res.Plan, scratch.NoteSourceChanged) {
		t.Error("no source-changed note in the result")
	}
	if _, ok := wsFile(e, ids.DefaultFolder, "notes.md"); ok {
		t.Error("the soft-skipped copy was written")
	}
	if got, _ := wsFile(e, ids.DefaultFolder, "src/app.py"); got != "print(1)" {
		t.Errorf("the other copies were not applied: %q", got)
	}
	checkScratchBase(t, e, "b", repB)
	e.checkInvariants("soft skip")

	// The next switch (into a) keeps a's newer file; the one after
	// carries it to b.
	switchOK(t, e, "a")
	if got, _ := wsFile(e, ids.DefaultFolder, "notes.md"); got != "rewritten" {
		t.Fatalf("a's newer file was reverted: %q", got)
	}
	checkScratchBase(t, e, "a", repA)
	switchOK(t, e, "b")
	if got, _ := wsFile(e, ids.DefaultFolder, "notes.md"); got != "rewritten" {
		t.Errorf("the newer file was not carried to b: %q", got)
	}
	checkScratchBase(t, e, "b", repB)
	e.checkInvariants("after the soft skip")
}

// ---- breaker (9.1) ----

func TestScratchBreaker(t *testing.T) {
	ctx := context.Background()
	many := func(e *env) {
		for i := 0; i < 100; i++ {
			putWS(t, e, ids.DefaultFolder, fmt.Sprintf("data/f%03d.txt", i), fmt.Sprintf("file %d", i), sMtime)
		}
	}
	remove60 := func(t *testing.T, e *env) {
		for i := 0; i < 60; i++ {
			rmWS(t, e, ids.DefaultFolder, fmt.Sprintf("data/f%03d.txt", i))
		}
	}
	count := func(e *env) int {
		n := 0
		for k := range wsTree(t, wsDir(e, ids.DefaultFolder)) {
			if strings.HasPrefix(k, "data/f") {
				n++
			}
		}
		return n
	}
	t.Run("60 of 100 trips", func(t *testing.T) {
		e := bothCarry(t, many)
		remove60(t, e)
		e.app.running = true
		_, err := e.orch().Switch(ctx, yes("b"))
		if c := codeOf(t, err); c != pderr.DeletionBreaker {
			t.Fatalf("code %v: %v", c, err)
		}
		if n := len(wsTree(t, filepath.Join(wsDir(e, "Claude-b"), "data"))); n != 100 {
			t.Errorf("b holds %d files after a refused plan", n)
		}
		e.checkInvariants("breaker")
	})
	t.Run("allow-deletions 60 passes", func(t *testing.T) {
		e := bothCarry(t, many)
		remove60(t, e)
		e.app.running = true
		opts := yes("b")
		opts.AllowDeletions = 60
		res, err := e.orch().Switch(ctx, opts)
		if err != nil {
			t.Fatalf("switch: %v", err)
		}
		if n := len(scratchChanges(res.Plan, model.ChangeDeleted)); n != 60 {
			t.Errorf("%d deletions", n)
		}
		if n := count(e); n != 40 {
			t.Errorf("b holds %d files", n)
		}
		checkScratchBase(t, e, "b", repB)
	})
	t.Run("500 member removals in a unit replacement", func(t *testing.T) {
		e := bothCarry(t, func(e *env) {
			for i := 0; i < 500; i++ {
				putWS(t, e, ids.DefaultFolder, fmt.Sprintf(".git/objects/%02d/o%03d", i%10, i), fmt.Sprintf("obj %d", i), sMtime)
			}
		})
		rmWS(t, e, ids.DefaultFolder, ".git/objects")
		putWS(t, e, ids.DefaultFolder, ".git/objects/pack/p1", "packed", sMtime.Add(time.Hour))
		putWS(t, e, ids.DefaultFolder, ".git/HEAD", "ref2", sMtime.Add(time.Hour))
		want := subTree(wsTree(t, wsDir(e, ids.DefaultFolder)), ".git")
		res := switchOK(t, e, "b")
		members := 0
		for _, c := range scratchChanges(res.Plan, model.ChangeDeleted) {
			if _, member := scratch.GitUnit(c.Key); member {
				members++
			}
		}
		if members < 500 {
			t.Errorf("%d member removals planned", members)
		}
		got := wsTree(t, wsDir(e, ids.DefaultFolder))
		// Directories T had inside the unit are never removed.
		gitFiles := map[string]string{}
		for k, v := range subTree(got, ".git") {
			if v != "/" {
				gitFiles[k] = v
			}
		}
		wantFiles := map[string]string{}
		for k, v := range want {
			if v != "/" {
				wantFiles[k] = v
			}
		}
		if !sameTree(gitFiles, wantFiles) {
			t.Errorf("b's .git differs from a's: %s", treeDiff(gitFiles, wantFiles))
		}
		checkScratchBase(t, e, "b", repB)
	})
}

// ---- conflicts and git units (8, 7.4) ----

func conflictSibling(t *testing.T, e *env, folder, stem string) (string, string) {
	t.Helper()
	for k, v := range wsTree(t, wsDir(e, folder)) {
		if strings.HasPrefix(k, stem+" (conflict from ") {
			return k, v
		}
	}
	return "", ""
}

func TestScratchConflictOnBothSides(t *testing.T) {
	t.Run("valid bases: the live, newer side wins", func(t *testing.T) {
		e := bothCarry(t, nil)
		putWS(t, e, "Claude-b", "notes.md", "from b", sMtime.Add(time.Hour))
		putWS(t, e, ids.DefaultFolder, "notes.md", "from a", sMtime.Add(2*time.Hour))
		res := switchOK(t, e, "b")
		if got, _ := wsFile(e, ids.DefaultFolder, "notes.md"); got != "from a" {
			t.Errorf("notes.md = %q", got)
		}
		name, v := conflictSibling(t, e, ids.DefaultFolder, "notes")
		if v != "from b" || !strings.HasSuffix(name, ").md") || !strings.Contains(name, "from b ") {
			t.Errorf("sibling %q = %q", name, v)
		}
		if len(res.Plan.Conflicts) != 1 {
			t.Errorf("conflicts %+v", res.Plan.Conflicts)
		}
		checkScratchBase(t, e, "b", repB)
		// The next switch back carries the sibling; then nothing is left.
		switchOK(t, e, "a")
		if n, v := conflictSibling(t, e, ids.DefaultFolder, "notes"); v != "from b" {
			t.Errorf("sibling not carried back: %q %q", n, v)
		}
		third := switchOK(t, e, "b")
		if hasRowOps(third.Plan, model.RowScratch) {
			t.Errorf("third switch not empty: %+v", third.Plan.Ops)
		}
	})
	t.Run("invalid base: the newer file wins", func(t *testing.T) {
		e := bothCarry(t, nil)
		putWS(t, e, "Claude-b", "notes.md", "from b", sMtime.Add(2*time.Hour))
		putWS(t, e, ids.DefaultFolder, "notes.md", "from a", sMtime.Add(time.Hour))
		e.invalid["b"] = true
		res := switchOK(t, e, "b")
		delete(e.invalid, "b")
		if got, _ := wsFile(e, ids.DefaultFolder, "notes.md"); got != "from b" {
			t.Errorf("notes.md = %q", got)
		}
		name, v := conflictSibling(t, e, ids.DefaultFolder, "notes")
		if v != "from a" || !strings.Contains(name, "from a ") {
			t.Errorf("sibling %q = %q", name, v)
		}
		if len(scratchChanges(res.Plan, model.ChangeDeleted)) != 0 {
			t.Error("a deletion under an invalid base")
		}
		checkScratchBase(t, e, "b", repB)
	})
}

func gitFilesOf(t *testing.T, e *env, folder string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for k, v := range subTree(wsTree(t, wsDir(e, folder)), ".git") {
		if v != "/" {
			out[k] = v
		}
	}
	return out
}

func TestScratchGitUnitsEndToEnd(t *testing.T) {
	later := sMtime.Add(time.Hour)
	withRepo := func(e *env) {
		putWS(t, e, ids.DefaultFolder, ".git/objects/aa/1", "one", sMtime)
		putWS(t, e, ids.DefaultFolder, ".git/refs/heads/main", "c1", sMtime)
	}
	t.Run("commit on one side carries its .git whole", func(t *testing.T) {
		e := newScratchEnv(t)
		withRepo(e)
		switchOK(t, e, "b")
		// A commit on b (active): new object, moved ref, a pruned object.
		putWS(t, e, ids.DefaultFolder, ".git/objects/bb/2", "two", later)
		putWS(t, e, ids.DefaultFolder, ".git/refs/heads/main", "c2", later)
		rmWS(t, e, ids.DefaultFolder, ".git/objects/aa/1")
		want := gitFilesOf(t, e, ids.DefaultFolder)
		res := switchOK(t, e, "a")
		if got := gitFilesOf(t, e, ids.DefaultFolder); !sameTree(got, want) {
			t.Errorf("a's .git differs from b's: %s", treeDiff(got, want))
		}
		if hasScratchNote(res.Plan, scratch.NoteGitConflict) || hasScratchNote(res.Plan, scratch.NoteGitDiverged) {
			t.Error("a one-sided change was reported as diverged")
		}
		checkScratchBase(t, e, "a", repA)
	})
	t.Run("commits on both keep each side's own", func(t *testing.T) {
		e := bothCarry(t, withRepo)
		putWS(t, e, ids.DefaultFolder, ".git/refs/heads/main", "on a", later)
		putWS(t, e, "Claude-b", ".git/refs/heads/main", "on b", later)
		putWS(t, e, "Claude-b", ".git/objects/cc/3", "three", later)
		aGit, bGit := gitFilesOf(t, e, ids.DefaultFolder), gitFilesOf(t, e, "Claude-b")
		res := switchOK(t, e, "b")
		if !hasScratchNote(res.Plan, scratch.NoteGitConflict) {
			t.Errorf("no git-conflict note: %+v", res.Plan.Changes)
		}
		for _, c := range res.Plan.Conflicts {
			if c.Row == model.RowScratch {
				t.Errorf("a unit conflict reached the plan: %+v", c)
			}
		}
		check := func(label string) {
			t.Helper()
			if got := gitFilesOf(t, e, folderOfAlias(t, e, "a")); !sameTree(got, aGit) {
				t.Errorf("%s: a's .git changed: %s", label, treeDiff(got, aGit))
			}
			if got := gitFilesOf(t, e, folderOfAlias(t, e, "b")); !sameTree(got, bGit) {
				t.Errorf("%s: b's .git changed: %s", label, treeDiff(got, bGit))
			}
		}
		check("first switch")
		for i, to := range []ids.Alias{"a", "b"} {
			res := switchOK(t, e, to)
			if !hasScratchNote(res.Plan, scratch.NoteGitDiverged) {
				t.Errorf("switch %d: no git-diverged note", i+2)
			}
			check(fmt.Sprintf("switch %d", i+2))
		}
		// Renaming .git away on a: the next switch into a creates b's.
		if folderOfAlias(t, e, "a") != "Claude-a" {
			t.Fatal("a is not parked")
		}
		if err := os.Rename(filepath.Join(wsDir(e, "Claude-a"), ".git"), filepath.Join(wsDir(e, "Claude-a"), "old-git")); err != nil {
			t.Fatal(err)
		}
		res = switchOK(t, e, "a")
		if got := gitFilesOf(t, e, ids.DefaultFolder); !sameTree(got, bGit) {
			t.Errorf("a did not get b's .git: %s", treeDiff(got, bGit))
		}
		if hasScratchNote(res.Plan, scratch.NoteGitDiverged) {
			t.Error("still diverged after the rename")
		}
		if got, _ := wsFile(e, ids.DefaultFolder, "old-git/refs/heads/main"); got != "on a" {
			t.Errorf("a's own repository was not kept: %q", got)
		}
		checkScratchBase(t, e, "a", repA)
		again := switchOK(t, e, "b")
		if hasScratchNote(again.Plan, scratch.NoteGitDiverged) || hasScratchNote(again.Plan, scratch.NoteGitConflict) {
			t.Error("diverged again after the units agreed")
		}
	})
}

// ---- rollback (6.7, 14.5) ----

// rollback of a generation with scratch entries: the quarantined file is
// put back, the created file removed, the profile's scratch base dropped,
// and the next switch is a union (nothing removed).
func TestScratchRollbackPutsQuarantinedBack(t *testing.T) {
	e := newScratchEnv(t)
	useRealGens(t, e)
	ctx := context.Background()
	switchOK(t, e, "b")
	rmWS(t, e, ids.DefaultFolder, "notes.md")
	putWS(t, e, ids.DefaultFolder, "lib/new.txt", "new", sMtime.Add(time.Hour))
	res := switchOK(t, e, "a")
	if len(scratchChanges(res.Plan, model.ChangeDeleted)) != 1 {
		t.Fatalf("setup: changes %+v", res.Plan.Changes)
	}
	if _, ok := wsFile(e, ids.DefaultFolder, "notes.md"); ok {
		t.Fatal("setup: the removal was not carried")
	}
	e.ui.interactive = true
	rb, err := e.orch().Rollback(ctx, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rb.Conflicts) != 0 {
		t.Errorf("restore conflicts: %v", rb.Conflicts)
	}
	if got, ok := wsFile(e, ids.DefaultFolder, "notes.md"); !ok || got != "hello" {
		t.Errorf("quarantined file not put back: %q %v", got, ok)
	}
	if _, ok := wsFile(e, ids.DefaultFolder, "lib/new.txt"); ok {
		t.Error("the created file is still there")
	}
	if _, err := os.Lstat(filepath.Join(wsDir(e, ids.DefaultFolder), "lib")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the created directory is still there: %v", err)
	}
	if scratchBase(t, e, "a", repA) != nil {
		t.Error("the rolled-back profile keeps a scratch base")
	}
	if _, ok := e.st.root(t).Bases[state.BaseKey{Alias: "a", Replica: repA, Row: model.RowSessions}.String()]; !ok {
		t.Error("the rollback dropped another row's base")
	}
	next := switchOK(t, e, "b")
	for _, op := range next.Plan.Ops {
		if op.Row == model.RowScratch && (op.Kind == plan.OpQuarantine || op.Destructive) {
			t.Errorf("removal after the rollback: %+v", op)
		}
	}
	if n := len(scratchChanges(next.Plan, model.ChangeDeleted)); n != 0 {
		t.Errorf("%d deletions after the rollback", n)
	}
	if got, _ := wsFile(e, ids.DefaultFolder, "notes.md"); got != "hello" {
		t.Errorf("the union did not carry notes.md back to b: %q", got)
	}
	if got, _ := wsFile(e, ids.DefaultFolder, "lib/new.txt"); got != "new" {
		t.Errorf("b lost its own file: %q", got)
	}
	e.checkInvariants("rollback then switch")
}

// ---- pruning (9.4) ----

// Pruning past backups.keep moves a generation's quarantined scratch
// files into scratch-kept; removing the kept sets (backups prune
// --scratch --yes) then deletes them.
func TestScratchPruneKeepsQuarantined(t *testing.T) {
	e := newScratchEnv(t)
	store := useRealGens(t, e)
	e.cfg.Backups.Keep = 1
	switchOK(t, e, "b")
	rmWS(t, e, ids.DefaultFolder, "notes.md")
	switchOK(t, e, "a") // quarantines a's notes.md
	sets, err := store.ScratchKept()
	if err != nil || len(sets) != 0 {
		t.Fatalf("kept before the generation was pruned: %+v %v", sets, err)
	}
	q := 0
	infos, _ := store.List()
	for _, in := range infos {
		q += int(in.ScratchBytes)
	}
	if q == 0 {
		t.Fatal("setup: no scratch bytes in the generations")
	}
	putWS(t, e, ids.DefaultFolder, "more.txt", "more", sMtime.Add(time.Hour))
	switchOK(t, e, "b") // a third generation: the quarantining one is pruned
	sets, err = store.ScratchKept()
	if err != nil || len(sets) != 1 || sets[0].Files != 1 || !sets[0].Listed {
		t.Fatalf("kept sets %+v %v", sets, err)
	}
	kept := filepath.Join(store.ScratchKeptPath(), fmt.Sprint(sets[0].N), "files", "scratch-workspaces", filepath.FromSlash(sWS), "notes.md")
	if b, err := os.ReadFile(kept); err != nil || string(b) != "hello" {
		t.Fatalf("kept file %q %v", b, err)
	}
	if infos, _ := store.List(); len(infos) != 1 {
		t.Errorf("generations left: %+v", infos)
	}
	n, bytes, err := store.RemoveScratchKept(time.Time{})
	if err != nil || n != 1 || bytes != int64(len("hello")) {
		t.Fatalf("removed %d (%d bytes): %v", n, bytes, err)
	}
	if _, err := os.Lstat(kept); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("kept file still there: %v", err)
	}
	if sets, _ := store.ScratchKept(); len(sets) != 0 {
		t.Errorf("kept sets left: %+v", sets)
	}
}

// ---- root.json size (6.9) ----

// A switch carrying 5,000 files keeps root.json's post-hashes free of
// scratch keys; they live in one state object.
func TestScratchManyFilesKeepRootSmall(t *testing.T) {
	e := newScratchEnv(t)
	const n = 5000
	for i := 0; i < n; i++ {
		putWS(t, e, ids.DefaultFolder, fmt.Sprintf("gen/d%02d/f%04d.txt", i%50, i), fmt.Sprint(i), sMtime)
	}
	crashed := false
	e.crash = func(p Phase) bool {
		if p == P9Park && !crashed {
			crashed = true
			return true
		}
		return false
	}
	e.app.running = true
	_, err := e.orch().Switch(context.Background(), yes("b"))
	if !errors.Is(err, ErrCrashInjected) {
		t.Fatalf("no crash: %v", err)
	}
	root := e.st.root(t)
	for k := range root.Run.PostHashes {
		if strings.Contains(k, "scratch-workspaces") {
			t.Fatalf("scratch post-hash inline: %s", k)
		}
	}
	var scr map[string]string
	if err := e.st.GetObject(root.Run.PostHashesRef, scratchPostHashesKind, &scr); err != nil || len(scr) < n {
		t.Fatalf("scratch post-hash object: %d keys, %v", len(scr), err)
	}
	if len(e.st.committed) > 64<<10 {
		t.Errorf("root.json is %d bytes", len(e.st.committed))
	}
	e.crash = nil
	if _, err := e.orch().Recover(context.Background(), RecoverOptions{Yes: true}); err != nil {
		t.Fatal(err)
	}
	e.checkInvariants("recover")
	got := 0
	for k := range wsTree(t, wsDir(e, ids.DefaultFolder)) {
		if strings.HasSuffix(k, ".txt") {
			got++
		}
	}
	if got != n {
		t.Errorf("b holds %d files", got)
	}
	checkScratchBase(t, e, "b", repB)
	if root := e.st.root(t); root.Run != nil {
		t.Error("run left open")
	}
}

// ---- crash matrix (14.5) ----

// richSwitch prepares a switch a -> b whose scratch plan has mkdirs,
// copies (new and updated), a symlink and a quarantine.
func richSwitch(t *testing.T) *env {
	t.Helper()
	e := bothCarry(t, nil)
	useRealGens(t, e)
	later := sMtime.Add(time.Hour)
	putWS(t, e, ids.DefaultFolder, "lib/new/deep.txt", "deep", later)
	putWS(t, e, ids.DefaultFolder, "notes.md", "notes v2", later)
	if err := os.Symlink("notes.md", filepath.Join(wsDir(e, ids.DefaultFolder), "link")); err != nil {
		t.Fatal(err)
	}
	rmWS(t, e, ids.DefaultFolder, "src/app.py")
	return e
}

func TestScratchCrashMatrixKinds(t *testing.T) {
	ctx := context.Background()
	// The plan of the rich switch, from a run without a crash.
	ref := richSwitch(t)
	planned := wsTree(t, wsDir(ref, ids.DefaultFolder))
	res := switchOK(t, ref, "b")
	kinds := map[plan.Kind]bool{}
	for _, op := range res.Plan.Ops {
		if op.Row == model.RowScratch {
			kinds[op.Kind] = true
		}
	}
	for _, k := range []plan.Kind{plan.OpMkdir, plan.OpCopyFile, plan.OpSymlink, plan.OpQuarantine} {
		if !kinds[k] {
			t.Fatalf("the plan has no %s op: %v", k, kinds)
		}
	}
	if got := wsTree(t, wsDir(ref, ids.DefaultFolder)); !sameTree(got, planned) {
		t.Fatalf("planned tree: %s", treeDiff(got, planned))
	}

	completedWithMismatch := 0
	for k := 1; k < 60; k++ {
		for _, mutate := range []bool{false, true} {
			e := richSwitch(t)
			pre := wsTree(t, wsDir(e, "Claude-b"))
			e.markBases()
			n := 0
			e.crash = func(Phase) bool { n++; return n == k }
			e.app.running = true
			_, err := e.orch().Switch(ctx, yes("b"))
			if !errors.Is(err, ErrCrashInjected) {
				if err != nil {
					t.Fatalf("run without a crash failed: %v", err)
				}
				if completedWithMismatch == 0 {
					t.Error("no crash point completed the run with a scratch-only mismatch")
				}
				return
			}
			e.crash = nil
			tFolder := ""
			for _, f := range []string{ids.DefaultFolder, "Claude-b"} {
				if e.account(f) == acctB {
					tFolder = f
				}
			}
			if mutate {
				// A file the plan writes changes before recover: only a
				// scratch path mismatches its post-hash.
				putWS(t, e, tFolder, "notes.md", "edited before recover", sMtime.Add(3*time.Hour))
			}
			if _, err := e.orch().Recover(ctx, RecoverOptions{Yes: true}); err != nil {
				t.Fatalf("crash %d: recover: %v", k, err)
			}
			label := fmt.Sprintf("crash %d (mutated %v)", k, mutate)
			e.checkInvariants(label)
			root := e.st.root(t)
			// T's scratch base is absent or equals its tree (without the
			// edit before recover); a base this run set equals its tree.
			for _, a := range []ids.Alias{"a", "b"} {
				ps := root.Profiles[a]
				k := state.BaseKey{Alias: a, Replica: ps.Replica, Row: model.RowScratch}.String()
				if a == "a" || mutate {
					if ref, ok := root.Bases[k]; !ok || e.basesBefore[k] == ref {
						continue
					}
				}
				if base := scratchBase(t, e, a, ps.Replica); base != nil && !sameItems(base, readScratch(t, e, folderOfAlias(t, e, a))) {
					t.Errorf("%s: %s's scratch base differs from its tree", label, a)
				}
			}
			bTree := wsTree(t, wsDir(e, folderOfAlias(t, e, "b")))
			if !mutate {
				if !sameTree(bTree, pre) && !sameTree(bTree, planned) {
					t.Errorf("%s: b's tree is neither its pre-run nor its planned tree: pre %s; planned %s",
						label, treeDiff(bTree, pre), treeDiff(bTree, planned))
				}
				continue
			}
			if root.Active != "b" {
				continue
			}
			// The switch was completed: the other rows' bases of b survive.
			for _, row := range []model.RowID{model.RowSessions, model.RowEnvironments} {
				if _, ok := root.Bases[state.BaseKey{Alias: "b", Replica: repB, Row: row}.String()]; !ok {
					t.Errorf("%s: b lost its %s base", label, row)
				}
			}
			if bTree["notes.md"] == "edited before recover" && scratchBase(t, e, "b", repB) == nil {
				completedWithMismatch++
			}
		}
	}
	t.Fatal("the switch never finished without a crash")
}
