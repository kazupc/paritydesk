package apply_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/apply"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/fsx/fsxtest"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/rows"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/sources/scratch"
	"github.com/kazupc/paritydesk/internal/testkit"
)

// The scratch flow tests run the whole row on real directories: ReadRefs
// on both folders, the rows module, Ops, plan.Compose, plan.Validate and
// apply over fsxtest, then a re-read that must equal Desired.

var (
	fAcct = testkit.FakeUUID(0xa)
	fOrg  = testkit.FakeUUID(0xb)
	fLeaf = "scratch-2026-01-02-0a0b0c"
	fWS   = fAcct + "/" + fOrg + "/" + fLeaf
	fTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
)

type flow struct {
	t        *testing.T
	m        *compat.Manifest
	spec     compat.RowSpec
	ad       *scratch.Adapter
	sF, tF   string
	tmp, q   string
	fs       *fsxtest.FS
	sBase    *model.RowSnapshot
	tBase    *model.RowSnapshot
	canon    *model.RowSnapshot
	sV, tV   bool
	lastPlan *plan.Plan
}

func newFlow(t *testing.T) *flow {
	ms, err := compat.Load("")
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := compat.Select(ms, "7.37095.0")
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := m.Row(model.RowScratch)
	root := t.TempDir()
	f := &flow{t: t, m: m, spec: spec, ad: scratch.New(scratch.DefaultLimits(), nil, nil, nil),
		sF: filepath.Join(root, "Claude"), tF: filepath.Join(root, "Claude-b"),
		tmp: filepath.Join(root, ".paritydesk-tmp", "run"), q: filepath.Join(root, "gen", "quarantine"), fs: fsxtest.New(t)}
	for _, d := range []string{f.sF, f.tF} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *flow) rc(folder string) sources.ReadContext {
	return sources.ReadContext{Folder: folder, Spec: f.spec, Manifest: f.m}
}

func (f *flow) read(folder string) *model.RowSnapshot {
	f.t.Helper()
	s, err := f.ad.ReadRefs(context.Background(), f.rc(folder), []string{fWS})
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

// plan merges S into T and returns the module output and the composed,
// validated plan.
func (f *flow) plan(preview *plan.Plan) (rows.Output, *plan.Plan, error) {
	f.t.Helper()
	sSnap, tSnap := f.read(f.sF), f.read(f.tF)
	pol := rows.Policy{TombstoneMode: "newer-wins", LeavingExports: true, EnteringExports: true, EnteringImports: true,
		Scratch: &rows.ScratchPolicy{PropagateDeletions: true, Exclude: f.spec.Scratch.Exclude,
			HeldBack: append(append([]string(nil), f.spec.Scratch.DependencyDirs...), f.spec.Scratch.VCSDirs...),
			Leaf:     f.spec.Scratch.LeafRE(), ConflictDate: "2026-01-02", Refs: []string{fWS}, Referenced: []string{fWS}}}
	in := rows.Input{Row: model.RowScratch,
		Leaving:   rows.Side{Alias: "a", Snapshot: sSnap, Base: f.sBase, BaseValid: f.sV, LiveEpoch: 5},
		Entering:  rows.Side{Alias: "b", Snapshot: tSnap, Base: f.tBase, BaseValid: f.tV, LiveEpoch: 4},
		Canonical: f.canon, Policy: pol}
	out, err := rows.Registry()[model.RowScratch].Merge(in)
	if err != nil {
		f.t.Fatal(err)
	}
	src := f.rc(f.sF)
	res, err := f.ad.Ops(context.Background(), sources.OpsContext{ReadContext: f.rc(f.tF), Source: &src, SourceSnapshot: sSnap}, tSnap, out.Desired)
	if err != nil {
		f.t.Fatal(err)
	}
	ops, err := plan.Compose(res.Edits, func(plan.Location) ([]byte, bool, error) { return nil, false, errors.New("no read") })
	if err != nil {
		f.t.Fatal(err)
	}
	p := &plan.Plan{Version: plan.Version, Ops: ops, Changes: append(out.Changes, res.Skipped...), Conflicts: out.Conflicts}
	_ = preview
	err = plan.Validate(plan.ValidateInput{Plan: p, Before: map[model.Category]int{model.CatScratch: 1000},
		Limits:    plan.Limits{MaxDeleteFraction: 1, MaxDeleteMin: 1000},
		DenyPaths: f.m.Classification.NeverTouched, AllowPaths: f.m.SyncedFor(model.Replica{}),
		ScratchRoot: f.spec.Scratch.Root, ScratchLeaf: f.spec.Scratch.LeafRE(), MaxCopyBytes: 1 << 30,
		TmpRecoveryGlobs: f.m.TmpRecoveryGlobs, SecretsMayCross: true,
		BreakerExempt: func(c model.Change) bool { _, m := scratch.GitUnit(c.Key); return m }})
	f.lastPlan = p
	return out, p, err
}

func (f *flow) apply(p *plan.Plan, leaving string) (apply.Result, error) {
	a := &apply.Applier{FS: f.fs, Holders: func(context.Context, string) (bool, string, error) { return false, "", nil }}
	return a.Apply(context.Background(), apply.Input{Plan: p, Folder: f.tF, TmpDir: f.tmp, QuarantineDir: f.q, LeavingFolder: leaving})
}

// switchSToT runs one full switch and advances the bases as P5 and P10 do.
func (f *flow) switchSToT() (rows.Output, *plan.Plan, apply.Result) {
	f.t.Helper()
	out, p, err := f.plan(nil)
	if err != nil {
		f.t.Fatalf("validate: %v", err)
	}
	sSnap := f.read(f.sF)
	res, err := f.apply(p, f.sF)
	if err != nil {
		f.t.Fatalf("apply: %v", err)
	}
	reread := f.read(f.tF)
	if !sameScratch(reread, out.Desired) {
		f.t.Fatalf("T does not read back as Desired:\n got %v\nwant %v", reread.Keys(), out.Desired.Keys())
	}
	f.sBase, f.sV = sSnap, true
	f.tBase, f.tV = reread, true
	f.canon = out.Canonical
	return out, p, res
}

// swap exchanges the folders' roles (the next switch goes T -> S).
func (f *flow) swap() {
	f.sF, f.tF = f.tF, f.sF
	f.sBase, f.tBase = f.tBase, f.sBase
	f.sV, f.tV = f.tV, f.sV
}

func sameScratch(a, b *model.RowSnapshot) bool {
	if len(a.Items) != len(b.Items) {
		return false
	}
	for k, it := range a.Items {
		if o, ok := b.Items[k]; !ok || o.Hash != it.Hash {
			return false
		}
	}
	return true
}

func TestScratchFlowMirrorRoundTrip(t *testing.T) {
	f := newFlow(t)
	testkit.ScratchWorkspace(t, f.sF, fAcct, fOrg, fLeaf, map[string]string{
		"notes.md": "hello", "src/app.py": "print(1)", "ln": "->notes.md", ".git/HEAD": "ref", "node_modules/x.js": "dep",
	}, fTime)
	// A -> B: B gains the mirror with the same bytes, modes and mtimes.
	_, p, _ := f.switchSToT()
	if p.Empty() {
		t.Fatal("empty plan")
	}
	for _, rel := range []string{"notes.md", "src/app.py", ".git/HEAD"} {
		b, err := os.ReadFile(filepath.Join(f.tF, "scratch-workspaces", fWS, rel))
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		sb, _ := os.ReadFile(filepath.Join(f.sF, "scratch-workspaces", fWS, rel))
		if string(b) != string(sb) {
			t.Errorf("%s differs", rel)
		}
		fi, _ := os.Stat(filepath.Join(f.tF, "scratch-workspaces", fWS, rel))
		if !fi.ModTime().Equal(fTime) {
			t.Errorf("%s mtime %v", rel, fi.ModTime())
		}
	}
	if l, err := os.Readlink(filepath.Join(f.tF, "scratch-workspaces", fWS, "ln")); err != nil || l != "notes.md" {
		t.Errorf("link %q %v", l, err)
	}
	if _, err := os.Lstat(filepath.Join(f.tF, "scratch-workspaces", fWS, "node_modules")); err == nil {
		t.Error("a held-back folder was carried")
	}
	// B edits (add, modify, remove), switch B -> A.
	f.swap()
	ws := filepath.Join(f.sF, "scratch-workspaces", fWS)
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(ws, "new.txt"), []byte("n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "src/app.py"), []byte("print(2)"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(ws, "notes.md")); err != nil {
		t.Fatal(err)
	}
	_, p2, _ := f.switchSToT()
	quarantined := false
	for _, op := range p2.Ops {
		if op.Kind == plan.OpQuarantine && strings.HasSuffix(op.Path, "/notes.md") {
			quarantined = true
		}
	}
	if !quarantined {
		t.Errorf("the removal was not quarantined: %+v", p2.Ops)
	}
	if _, err := os.Stat(filepath.Join(f.q, "scratch-workspaces", fWS, "notes.md")); err != nil {
		t.Errorf("notes.md not in the quarantine: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(f.tF, "scratch-workspaces", fWS, "src/app.py")); string(b) != "print(2)" {
		t.Errorf("app.py %q", b)
	}
	// Nothing changed since: the next plan is empty.
	f.swap()
	if _, p3, err := f.plan(nil); err != nil || !p3.Empty() {
		t.Fatalf("steady state plan: %+v %v", p3.Ops, err)
	}
}

func TestScratchFlowSoftAndHardCopies(t *testing.T) {
	f := newFlow(t)
	testkit.ScratchWorkspace(t, f.sF, fAcct, fOrg, fLeaf, map[string]string{"a.txt": "one", ".git/HEAD": "ref"}, fTime)
	_, p, err := f.plan(nil)
	if err != nil {
		t.Fatal(err)
	}
	// The leaving file changes between P5 and P7: a soft copy is skipped.
	ws := filepath.Join(f.sF, "scratch-workspaces", fWS)
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := f.apply(p, f.sF)
	if err != nil || len(res.SoftSkipped) != 1 || !strings.HasSuffix(res.SoftSkipped[0], "/a.txt") {
		t.Fatalf("soft skip: %+v %v", res, err)
	}
	if _, err := os.Lstat(filepath.Join(f.tF, "scratch-workspaces", fWS, "a.txt")); err == nil {
		t.Error("the soft-skipped target was written")
	}
	// Inside a git unit the copy is hard: rollback (exit 40).
	f2 := newFlow(t)
	testkit.ScratchWorkspace(t, f2.sF, fAcct, fOrg, fLeaf, map[string]string{".git/HEAD": "ref"}, fTime)
	_, p2, err := f2.plan(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f2.sF, "scratch-workspaces", fWS, ".git/HEAD"), []byte("moved"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f2.apply(p2, f2.sF); pderr.CodeOf(err) != pderr.ApplyRolledBack && pderr.CodeOf(err) != pderr.Internal {
		t.Fatalf("git member source change: %v", err)
	}
	// A leaving copy without LeavingFolder fails before any write.
	f3 := newFlow(t)
	testkit.ScratchWorkspace(t, f3.sF, fAcct, fOrg, fLeaf, map[string]string{"a.txt": "one"}, fTime)
	_, p3, _ := f3.plan(nil)
	res3, err := f3.apply(p3, "")
	if pderr.CodeOf(err) != pderr.Internal || res3.Touched {
		t.Fatalf("no leaving folder: %v %+v", err, res3)
	}
	if _, err := os.Lstat(filepath.Join(f3.tF, "scratch-workspaces")); err == nil {
		t.Error("something was written")
	}
}

func TestScratchFlowCheckByKind(t *testing.T) {
	f := newFlow(t)
	testkit.ScratchWorkspace(t, f.sF, fAcct, fOrg, fLeaf, map[string]string{"d/f": "x", "ln": "->d/f"}, fTime)
	_, p, _ := f.switchSToT()
	a := &apply.Applier{FS: f.fs}
	ok, bad, err := a.Check(context.Background(), p, f.tF)
	if err != nil || !ok {
		t.Fatalf("check after apply: %v %v", bad, err)
	}
	var keys []string
	for _, op := range p.Ops {
		keys = append(keys, apply.PostHashKey(op))
	}
	joined := strings.Join(keys, " ")
	if !strings.Contains(joined, "dir:scratch-workspaces/"+fWS+"/d") || !strings.Contains(joined, "link:scratch-workspaces/"+fWS+"/ln") {
		t.Errorf("post-hash keys %v", keys)
	}
	// A file where a directory was planned fails the check.
	d := filepath.Join(f.tF, "scratch-workspaces", fWS, "d")
	if err := os.RemoveAll(d); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(d, []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, _, err := a.Check(context.Background(), p, f.tF); ok && err == nil {
		t.Error("a file passed as the planned directory")
	}
}

// tFilesHolding returns T's workspace files whose content is want.
func (f *flow) tFilesHolding(want string) []string {
	var out []string
	ws := filepath.Join(f.tF, "scratch-workspaces", filepath.FromSlash(fWS))
	_ = filepath.WalkDir(ws, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if b, _ := os.ReadFile(p); string(b) == want {
				out = append(out, strings.TrimPrefix(p, ws+string(filepath.Separator)))
			}
		}
		return nil
	})
	return out
}

// A conflict whose loser is T's version (spec-scratch 8): T's bytes are
// copied to the sibling before S's version replaces the file, with valid
// bases (S at the later epoch) and with invalid bases (S newer).
func TestScratchFlowEnteringLoserIsKept(t *testing.T) {
	for _, valid := range []bool{true, false} {
		f := newFlow(t)
		if valid {
			testkit.ScratchWorkspace(t, f.sF, fAcct, fOrg, fLeaf, map[string]string{"a.txt": "base"}, fTime)
			f.switchSToT()
			f.swap()
			f.swap() // S leaves again; both bases valid
			old := fTime.Add(time.Hour)
			if err := os.WriteFile(filepath.Join(f.tF, "scratch-workspaces", fWS, "a.txt"), []byte("t-edit"), 0o644); err != nil {
				t.Fatal(err)
			}
			_ = os.Chtimes(filepath.Join(f.tF, "scratch-workspaces", fWS, "a.txt"), old, old)
			if err := os.WriteFile(filepath.Join(f.sF, "scratch-workspaces", fWS, "a.txt"), []byte("s-edit"), 0o644); err != nil {
				t.Fatal(err)
			}
		} else {
			testkit.ScratchWorkspace(t, f.sF, fAcct, fOrg, fLeaf, map[string]string{"a.txt": "s-edit"}, fTime.Add(time.Hour))
			testkit.ScratchWorkspace(t, f.tF, fAcct, fOrg, fLeaf, map[string]string{"a.txt": "t-edit"}, fTime)
		}
		out, p, err := f.plan(nil)
		if err != nil {
			t.Fatalf("valid=%v: validate: %v", valid, err)
		}
		if len(out.Conflicts) != 1 || out.Conflicts[0].Loser != "b" {
			t.Fatalf("valid=%v: conflicts %+v", valid, out.Conflicts)
		}
		for _, c := range p.Changes {
			if c.Kind == model.ChangeSkipped && c.Note == scratch.NoteBytes {
				t.Errorf("valid=%v: %s skipped (bytes-unavailable)", valid, c.Key)
			}
		}
		if _, err := f.apply(p, f.sF); err != nil {
			t.Fatalf("valid=%v: apply: %v", valid, err)
		}
		if got := f.tFilesHolding("s-edit"); len(got) != 1 || got[0] != "a.txt" {
			t.Errorf("valid=%v: winner in %v", valid, got)
		}
		if got := f.tFilesHolding("t-edit"); len(got) != 1 || got[0] != "a (conflict from b 2026-01-02).txt" {
			t.Errorf("valid=%v: T's losing version in %v", valid, got)
		}
		if reread := f.read(f.tF); !sameScratch(reread, out.Desired) {
			t.Errorf("valid=%v: T does not read back as Desired:\n got %v\nwant %v", valid, reread.Keys(), out.Desired.Keys())
		}
	}
}

// A git unit taken whole from S whose hook is an absolute or escaping
// symlink: the link is not created (link-kept) and the plan validates.
func TestScratchFlowGitUnitEscapingLinks(t *testing.T) {
	f := newFlow(t)
	testkit.ScratchWorkspace(t, f.sF, fAcct, fOrg, fLeaf, map[string]string{
		".git/HEAD": "ref", ".git/hooks/pre-commit": "->/usr/local/bin/hook",
		".git/hooks/post-commit": "->../../../../x", ".git/hooks/ok": "->pre-commit"}, fTime)
	out, p, err := f.plan(nil)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	kept := 0
	for _, c := range p.Changes {
		if c.Kind == model.ChangeSkipped && c.Note == scratch.NoteLinkKept {
			kept++
		}
	}
	if kept != 2 {
		t.Errorf("link-kept notes: %d", kept)
	}
	for _, op := range p.Ops {
		if op.Kind == plan.OpSymlink && !strings.HasSuffix(op.Path, "/.git/hooks/ok") {
			t.Errorf("escaping link planned: %s -> %s", op.Path, op.Link)
		}
	}
	if _, err := f.apply(p, f.sF); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if reread := f.read(f.tF); !sameScratch(reread, out.Desired) {
		t.Errorf("T does not read back as Desired:\n got %v\nwant %v", reread.Keys(), out.Desired.Keys())
	}
}
