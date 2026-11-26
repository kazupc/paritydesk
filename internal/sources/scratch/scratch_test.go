package scratch_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/fsx/fsxtest"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/sources/scratch"
	"github.com/kazupc/paritydesk/internal/testkit"
)

var (
	acctA = testkit.FakeUUID(0xa)
	orgA  = testkit.FakeUUID(0xb)
	acctB = testkit.FakeUUID(0xc)
	orgB  = testkit.FakeUUID(0xd)
	leaf1 = "scratch-2026-01-02-0a0b0c"
	leaf2 = "scratch-2026-01-03-0d0e0f"
	ws1   = acctA + "/" + orgA + "/" + leaf1
	ws2   = acctA + "/" + orgA + "/" + leaf2
	mtime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	leafR = regexp.MustCompile(`^scratch-[0-9]{4}-[0-9]{2}-[0-9]{2}-[0-9a-f]{6}$`)
)

// manifest7 returns the embedded 7.x manifest (it has the scratch row).
func manifest7(t testing.TB) *compat.Manifest {
	t.Helper()
	ms, err := compat.Load("")
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := compat.Select(ms, "7.37095.0")
	if err != nil || m == nil {
		t.Fatalf("no 7.x manifest: %v", err)
	}
	return m
}

func rc(t testing.TB, folder string) sources.ReadContext {
	m := manifest7(t)
	spec, ok := m.Row(model.RowScratch)
	if !ok {
		t.Fatal("7.x has no scratch row")
	}
	return sources.ReadContext{Folder: folder, Replica: testkit.Replica(1), Spec: spec, Manifest: m}
}

func adapter() *scratch.Adapter {
	return scratch.New(scratch.DefaultLimits(), nil, []string{"target"}, nil)
}

func read(t *testing.T, a *scratch.Adapter, folder string, refs ...string) *model.RowSnapshot {
	t.Helper()
	s, err := a.ReadRefs(context.Background(), rc(t, folder), refs)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func value(t *testing.T, s *model.RowSnapshot, key string) scratch.Value {
	t.Helper()
	it, ok := s.Items[key]
	if !ok {
		t.Fatalf("no item %q in %v", key, s.Keys())
	}
	v, ok := scratch.Decode(it)
	if !ok {
		t.Fatalf("item %q has no scratch value", key)
	}
	return v
}

func hasNote(s *model.RowSnapshot, key, n string) bool {
	for _, c := range s.Notes {
		if c.Key == key && c.Note == n && c.Kind == model.ChangeSkipped {
			return true
		}
	}
	return false
}

// ---- Refs ----

func sessionsSnap(t *testing.T, records map[string]string) *model.RowSnapshot {
	t.Helper()
	s := sources.NewSnapshot(model.RowSessions)
	for k, v := range records {
		it, err := model.NewItem(k, json.RawMessage(v), []byte(v), model.Stamp{}, false)
		if err != nil {
			t.Fatal(err)
		}
		s.Items[k] = it
	}
	return s
}

var members = []string{"cwd", "originCwd", "gitAnchorsFolderRealpath", "gitAnchors", "folderPlace", "scratchFilesLeftIn", "scratchCarried", "shellFolders", "pendingFirstStart"}

func TestRefs(t *testing.T) {
	dataRoot := t.TempDir()
	prefix := filepath.Join(dataRoot, "Claude", "scratch-workspaces")
	p := func(rest string) string { return prefix + "/" + rest }
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	ws3 := acctB + "/" + orgB + "/scratch-2026-01-04-101112"
	ws4 := acctB + "/" + orgB + "/scratch-2026-01-05-131415"
	ws5 := acctA + "/" + orgB + "/scratch-2026-01-06-161718"
	recs := map[string]string{
		testkit.FakeSession(1): `{"cwd":` + q(p(ws1)) + `,"title":"x"}`,
		testkit.FakeSession(2): `{"folderPlace":{"origin":{"realPath":` + q(p(ws2)+"/sub/dir") + `}}}`,
		testkit.FakeSession(3): `{"scratchCarried":{"from":[` + q(p(ws3)) + `],"to":` + q(p(ws1)) + `},"shellFolders":[` + q(p(ws4)) + `]}`,
		testkit.FakeSession(4): `{"gitAnchors":[{"gitRoot":` + q(p(ws5)) + `}],"cwd":` + q(p(ws1)+"/a") + `}`,
		// non-UUID scope: counted; bad leaf: ignored; other prefix: counted;
		// ".." remainder: ignored; unlisted member: ignored.
		testkit.FakeSession(5):           `{"cwd":` + q(p("not-a-uuid/"+orgA+"/"+leaf1)) + `,"originCwd":` + q(p(acctA+"/"+orgA+"/scratch-bad")) + `}`,
		testkit.FakeSession(6):           `{"cwd":"/elsewhere/scratch-workspaces/x","originCwd":` + q(p(ws2)+"/../../x") + `}`,
		testkit.FakeSession(7):           `{"title":` + q(p(acctB+"/"+orgB+"/scratch-2026-02-02-aaaaaa")) + `}`,
		"deleted_" + testkit.FakeUUID(9): `1700000000000`,
	}
	snap := sessionsSnap(t, recs)
	before := map[string]string{}
	for k, it := range snap.Items {
		before[k] = string(it.Value)
	}
	refs, unrec, err := scratch.Refs(snap, members, scratch.Prefixes(dataRoot, "scratch-workspaces"), leafR)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(scratch.Keys(refs), " ")
	want := strings.Join([]string{ws1, ws2, ws5, ws3, ws4}, " ")
	if got != want {
		t.Errorf("refs\n got %s\nwant %s", got, want)
	}
	if unrec != 2 {
		t.Errorf("unrecognized %d, want 2", unrec)
	}
	if string(refs[2].Account) != acctA || string(refs[2].Org) != orgB || refs[2].Leaf != "scratch-2026-01-06-161718" {
		t.Errorf("ref %+v", refs[0])
	}
	for k, it := range snap.Items {
		if string(it.Value) != before[k] {
			t.Errorf("record %s modified", k)
		}
	}
}

func TestRefsEvaluatedPrefix(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "root")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	ev, _ := filepath.EvalSymlinks(link)
	prefixes := scratch.Prefixes(link, "scratch-workspaces")
	if len(prefixes) != 2 || prefixes[1] != filepath.Join(ev, "Claude", "scratch-workspaces") {
		t.Fatalf("prefixes %v", prefixes)
	}
	snap := sessionsSnap(t, map[string]string{testkit.FakeSession(1): `{"cwd":"` + prefixes[1] + "/" + ws1 + `"}`})
	refs, unrec, err := scratch.Refs(snap, members, prefixes, leafR)
	if err != nil || len(refs) != 1 || unrec != 0 {
		t.Fatalf("refs %v unrec %d err %v", refs, unrec, err)
	}
}

func TestJoinSplitRefs(t *testing.T) {
	keys := []string{ws1, ws2}
	got, err := scratch.SplitRefs(scratch.JoinRefs(keys))
	if err != nil || strings.Join(got, ",") != strings.Join(keys, ",") {
		t.Fatalf("round trip %v %v", got, err)
	}
	if got, err := scratch.SplitRefs(""); err != nil || len(got) != 0 || got == nil {
		t.Fatalf("empty set %v %v", got, err)
	}
	for _, bad := range []string{"x", ws1 + ",", acctA + "/" + orgA, "a/b/c", ws1 + "/extra"} {
		if _, err := scratch.SplitRefs(bad); err == nil {
			t.Errorf("SplitRefs(%q) accepted", bad)
		}
	}
}

// ---- Present ----

func TestPresent(t *testing.T) {
	folder := t.TempDir()
	testkit.ScratchWorkspace(t, folder, acctA, orgA, leaf1, map[string]string{"f": "x"}, mtime)
	if ok, err := scratch.Present(folder, "scratch-workspaces", ws1); !ok || err != nil {
		t.Fatalf("real dirs: %v %v", ok, err)
	}
	if ok, err := scratch.Present(folder, "scratch-workspaces", ws2); ok || err != nil {
		t.Fatalf("absent: %v %v", ok, err)
	}
	// A file at the leaf.
	if err := os.WriteFile(filepath.Join(folder, "scratch-workspaces", acctA, orgA, leaf2), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := scratch.Present(folder, "scratch-workspaces", ws2); ok || err != nil {
		t.Fatalf("file: %v %v", ok, err)
	}
	for i, comp := range []string{"root", "acct", "org", "leaf"} {
		f := t.TempDir()
		real := t.TempDir()
		parts := []string{"scratch-workspaces", acctB, orgB, leaf1}
		parent := filepath.Join(append([]string{f}, parts[:i]...)...)
		if err := os.MkdirAll(parent, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(append([]string{real}, parts[i+1:]...)...), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, filepath.Join(parent, parts[i])); err != nil {
			t.Fatal(err)
		}
		if ok, err := scratch.Present(f, "scratch-workspaces", acctB+"/"+orgB+"/"+leaf1); ok || !errors.Is(err, scratch.ErrLink) {
			t.Errorf("symlinked %s: %v %v", comp, ok, err)
		}
	}
}

// ---- ReadRefs ----

func TestReadRefsKindsAndValues(t *testing.T) {
	folder := t.TempDir()
	dir := testkit.ScratchWorkspace(t, folder, acctA, orgA, leaf1, map[string]string{
		"a.txt":                       "hello",
		"sub/":                        "",
		"sub/b.md":                    "bee",
		"link":                        "->a.txt",
		".git/HEAD":                   "ref: refs/heads/main\n",
		".git/objects/":               "",
		".git/index.lock":             "lock",
		".DS_Store":                   "x",
		"sub/.DS_Store":               "x",
		".claude/settings.local.json": "{}",
		".claude/.cc-writes/x":        "x",
		".claude/worktrees/w/f":       "x",
		".claude/keep.json":           "{}",
		"node_modules/pkg/index.js":   "x",
		"a/node_modules/x.js":         "x",
		".svn/entries":                "x",
		"target/out.bin":              "x",
		"local_x.json.tmp":            "x",
		"notes.tmp":                   "kept",
	}, mtime)
	if err := os.Chmod(filepath.Join(dir, "a.txt"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, `back\slash`), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := read(t, adapter(), folder, ws1)
	if v := value(t, s, ws1+"/a.txt"); v.K != "file" || v.Size != 5 || v.Mode != 0o640 || v.SHA256 != model.HashBytes([]byte("hello")) {
		t.Errorf("a.txt %+v", v)
	}
	if it := s.Items[ws1+"/a.txt"]; it.Stamp.AppTime != mtime.UnixMilli() || it.Raw != nil {
		t.Errorf("a.txt stamp %+v", it.Stamp)
	}
	if v := value(t, s, ws1+"/sub/"); v.K != "dir" || v.Mode != 0o755 {
		t.Errorf("sub %+v", v)
	}
	if v := value(t, s, ws1+"/link"); v.K != "link" || v.Target != "a.txt" {
		t.Errorf("link %+v", v)
	}
	if v := value(t, s, ws1+"/"); v.K != "root" || v.Over != "" {
		t.Errorf("root %+v", v)
	}
	unit := value(t, s, ws1+"/.git/")
	if unit.K != "git" || unit.Digest == "" || unit.N != 2 {
		t.Errorf("unit %+v", unit)
	}
	if _, ok := s.Items[ws1+"/.git/HEAD"]; !ok {
		t.Error("unit members are missing")
	}
	if _, ok := s.Items[ws1+"/notes.tmp"]; !ok {
		t.Error("a user file named like a temp file was skipped")
	}
	if _, ok := s.Items[ws1+"/.claude/keep.json"]; !ok {
		t.Error(".claude/keep.json was skipped")
	}
	for _, k := range []string{".git/index.lock", ".DS_Store", "sub/.DS_Store", ".claude/settings.local.json",
		".claude/.cc-writes/x", ".claude/.cc-writes/", ".claude/worktrees/w/f", "node_modules/pkg/index.js", "a/node_modules/x.js",
		".svn/entries", "target/out.bin", "local_x.json.tmp", `back\slash`, "pipe"} {
		if _, ok := s.Items[ws1+"/"+k]; ok {
			t.Errorf("%s was read", k)
		}
	}
	for _, n := range []struct{ key, note string }{
		{ws1 + "/node_modules/", scratch.NoteHeldBack}, {ws1 + "/a/node_modules/", scratch.NoteHeldBack},
		{ws1 + "/.svn/", scratch.NoteHeldBack}, {ws1 + "/target/", scratch.NoteHeldBack},
		{ws1 + "/pipe", scratch.NoteSpecial}, {ws1, scratch.NoteName}, {ws1 + "/local_x.json.tmp", scratch.NoteReserved},
	} {
		if !hasNote(s, n.key, n.note) {
			t.Errorf("no %s note for %s: %+v", n.note, n.key, s.Notes)
		}
	}
	// The same content in another folder hashes the same.
	other := t.TempDir()
	testkit.ScratchWorkspace(t, other, acctA, orgA, leaf1, map[string]string{"a.txt": "hello"}, mtime.Add(time.Hour))
	s2 := read(t, adapter(), other, ws1)
	if s2.Items[ws1+"/a.txt"].Hash == s.Items[ws1+"/a.txt"].Hash {
		t.Error("modes differ but the hashes are equal")
	}
	if _, err := adapter().Read(context.Background(), rc(t, folder)); !errors.Is(err, sources.ErrRefsRequired) {
		t.Errorf("Read: %v", err)
	}
}

func TestReadRefsSameContentSameHash(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	testkit.ScratchWorkspace(t, a, acctA, orgA, leaf1, map[string]string{"x": "1", ".git/HEAD": "h"}, mtime)
	testkit.ScratchWorkspace(t, b, acctA, orgA, leaf1, map[string]string{"x": "1", ".git/HEAD": "h"}, mtime.Add(time.Hour))
	sa, sb := read(t, adapter(), a, ws1), read(t, adapter(), b, ws1)
	for _, k := range []string{ws1 + "/x", ws1 + "/.git/"} {
		if sa.Items[k].Hash != sb.Items[k].Hash {
			t.Errorf("%s: same content, different hashes", k)
		}
	}
}

func TestReadRefsLimits(t *testing.T) {
	folder := t.TempDir()
	testkit.ScratchWorkspace(t, folder, acctA, orgA, leaf1, map[string]string{"a": "12345", "b": "67890"}, mtime)
	testkit.ScratchWorkspace(t, folder, acctA, orgA, leaf2, map[string]string{"c": "1"}, mtime)
	tests := []struct {
		name string
		lim  scratch.Limits
		ws   string
		over string
		note string
	}{
		{"files", scratch.Limits{MaxFilesPerWorkspace: 1, MaxWorkspaceBytes: 1 << 20, MaxRunBytes: 1 << 20, MaxRunFiles: 100}, ws1, "files", scratch.NoteOversizeFiles},
		{"bytes", scratch.Limits{MaxFilesPerWorkspace: 10, MaxWorkspaceBytes: 9, MaxRunBytes: 1 << 20, MaxRunFiles: 100}, ws1, "bytes", scratch.NoteOversizeBytes},
		{"budget by bytes", scratch.Limits{MaxFilesPerWorkspace: 10, MaxWorkspaceBytes: 100, MaxRunBytes: 10, MaxRunFiles: 100}, ws2, "budget", scratch.NoteBudget},
		{"budget by files", scratch.Limits{MaxFilesPerWorkspace: 10, MaxWorkspaceBytes: 100, MaxRunBytes: 100, MaxRunFiles: 2}, ws2, "budget", scratch.NoteBudget},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := scratch.New(tc.lim, nil, nil, nil)
			s := read(t, a, folder, ws1, ws2)
			if v := value(t, s, tc.ws+"/"); v.Over != tc.over {
				t.Errorf("marker %+v", v)
			}
			for k := range s.Items {
				if strings.HasPrefix(k, tc.ws+"/") && k != tc.ws+"/" {
					t.Errorf("%s kept item %s", tc.ws, k)
				}
			}
			if !hasNote(s, tc.ws, tc.note) {
				t.Errorf("no %s note: %+v", tc.note, s.Notes)
			}
		})
	}
}

// The listing stops at a limit: a workspace far over it is not listed to
// the end (a canary deeper in the tree is never reached, so a file there
// that cannot be listed does not turn the marker into "unreadable").
func TestReadRefsListingStopsAtLimit(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	folder := t.TempDir()
	files := map[string]string{"z/locked/x": "x"}
	for i := 0; i < 20; i++ {
		files[fmt.Sprintf("a/f%02d", i)] = "1"
	}
	dir := testkit.ScratchWorkspace(t, folder, acctA, orgA, leaf1, files, mtime)
	testkit.ScratchWorkspace(t, folder, acctA, orgA, leaf2, map[string]string{"c": "1"}, mtime)
	locked := filepath.Join(dir, "z", "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	for _, tc := range []struct {
		lim  scratch.Limits
		over string
	}{
		{scratch.Limits{MaxFilesPerWorkspace: 5, MaxWorkspaceBytes: 1 << 20, MaxRunBytes: 1 << 20, MaxRunFiles: 100}, "files"},
		{scratch.Limits{MaxFilesPerWorkspace: 100, MaxWorkspaceBytes: 1 << 20, MaxRunBytes: 1 << 20, MaxRunFiles: 5}, "budget"},
		{scratch.Limits{MaxFilesPerWorkspace: 100, MaxWorkspaceBytes: 5, MaxRunBytes: 1 << 20, MaxRunFiles: 100}, "bytes"},
	} {
		s := read(t, scratch.New(tc.lim, nil, nil, nil), folder, ws1, ws2)
		if v := value(t, s, ws1+"/"); v.Over != tc.over {
			t.Errorf("%s: marker %+v", tc.over, v)
		}
		// The next workspace is still read within the run's budget.
		if v := value(t, s, ws2+"/c"); v.K != "file" {
			t.Errorf("%s: next workspace %+v", tc.over, v)
		}
	}
}

// A folder without owner write permission (in the workspace or its root)
// keeps the workspace from being merged: copies into it would fail.
func TestReadRefsReadOnlyFolder(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root writes everywhere")
	}
	folder := t.TempDir()
	dir := testkit.ScratchWorkspace(t, folder, acctA, orgA, leaf1, map[string]string{"ro/f": "x", "ok": "y"}, mtime)
	if err := os.Chmod(filepath.Join(dir, "ro"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "ro"), 0o755) })
	s := read(t, adapter(), folder, ws1)
	if v := value(t, s, ws1+"/"); v.Over != scratch.OverReadOnly || !hasNote(s, ws1, scratch.NoteReadOnly) || len(s.Items) != 1 {
		t.Errorf("read-only folder: %+v %v", v, s.Keys())
	}
}

func TestReadRefsUnreadableAndLinks(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads everything")
	}
	folder := t.TempDir()
	dir := testkit.ScratchWorkspace(t, folder, acctA, orgA, leaf1, map[string]string{"secret": "x", "ok": "y"}, mtime)
	if err := os.Chmod(filepath.Join(dir, "secret"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "secret"), 0o644) })
	// A canary outside the workspace, linked from inside: never opened.
	canary := filepath.Join(t.TempDir(), "canary")
	if err := os.WriteFile(canary, []byte("CANARY-SCRATCH"), 0o600); err != nil {
		t.Fatal(err)
	}
	testkit.ScratchWorkspace(t, folder, acctA, orgA, leaf2, map[string]string{"in": "->" + canary}, mtime)
	s := read(t, adapter(), folder, ws1, ws2)
	if v := value(t, s, ws1+"/"); v.Over != "unreadable" || !hasNote(s, ws1, scratch.NoteUnreadable) {
		t.Errorf("unreadable %+v %+v", v, s.Notes)
	}
	b, _ := json.Marshal(s)
	if strings.Contains(string(b), "CANARY-SCRATCH") || strings.Contains(string(b), model.HashBytes([]byte("CANARY-SCRATCH"))) {
		t.Error("the canary behind a symlink was read")
	}
	if v := value(t, s, ws2+"/in"); v.K != "link" || v.Target != canary {
		t.Errorf("link %+v", v)
	}
}

func TestReadRefsRootLink(t *testing.T) {
	folder := t.TempDir()
	real := t.TempDir()
	if err := os.MkdirAll(filepath.Join(folder, "scratch-workspaces", acctA, orgA), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(folder, "scratch-workspaces", acctA, orgA, leaf1)); err != nil {
		t.Fatal(err)
	}
	s := read(t, adapter(), folder, ws1)
	if v := value(t, s, ws1+"/"); v.Over != "link" || !hasNote(s, ws1, scratch.NoteRootLink) || len(s.Items) != 1 {
		t.Errorf("root link: %+v %v", v, s.Keys())
	}
}

// ---- stat cache ----

func TestStatCache(t *testing.T) {
	folder := t.TempDir()
	dir := testkit.ScratchWorkspace(t, folder, acctA, orgA, leaf1, map[string]string{"f": "content"}, mtime)
	cachePath := filepath.Join(t.TempDir(), "cache", "scratch-stat.json")
	c := scratch.LoadStatCache(cachePath)
	a := scratch.New(scratch.DefaultLimits(), nil, nil, c)
	real := model.HashBytes([]byte("content"))
	if v := value(t, read(t, a, folder, ws1), ws1+"/f"); v.SHA256 != real {
		t.Fatalf("first read %+v", v)
	}
	if !c.Dirty() || c.Len() != 1 {
		t.Fatalf("cache dirty %v len %d", c.Dirty(), c.Len())
	}
	// A hit returns the cached hash without reading: poison it to see.
	fi, _ := os.Lstat(filepath.Join(dir, "f"))
	st, ok := scratch.StatOf(fi)
	if !ok {
		t.Skip("no stat identity on this platform")
	}
	fake := strings.Repeat("e", 64)
	fk := folderKey(t, folder)
	c.Put(fk, ws1+"/f", st, fake)
	if v := value(t, read(t, a, folder, ws1), ws1+"/f"); v.SHA256 != fake {
		t.Fatalf("hit did not use the cache: %+v", v)
	}
	// Size, mtime, ctime (chmod) and inode (replace) changes miss.
	changes := []func(){
		func() { _ = os.WriteFile(filepath.Join(dir, "f"), []byte("content2"), 0o644) },
		func() { _ = os.Chtimes(filepath.Join(dir, "f"), mtime.Add(time.Minute), mtime.Add(time.Minute)) },
		func() { _ = os.Chmod(filepath.Join(dir, "f"), 0o600) },
		func() {
			p := filepath.Join(dir, "f")
			b, _ := os.ReadFile(p)
			_ = os.Remove(p)
			_ = os.WriteFile(p, b, 0o600)
		},
	}
	for i, change := range changes {
		fi, _ := os.Lstat(filepath.Join(dir, "f"))
		st, _ := scratch.StatOf(fi)
		c.Put(fk, ws1+"/f", st, fake)
		time.Sleep(10 * time.Millisecond) // a new ctime
		change()
		b, _ := os.ReadFile(filepath.Join(dir, "f"))
		if v := value(t, read(t, a, folder, ws1), ws1+"/f"); v.SHA256 != model.HashBytes(b) {
			t.Errorf("change %d: stale hash used", i)
		}
	}
	// Save writes 0600 and keeps only the touched entries; a clean cache
	// does not write.
	tmp := filepath.Join(t.TempDir(), "tmp")
	if err := c.Save(fsxtest.New(t), cachePath, tmp); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(cachePath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("saved %v %v", fi, err)
	}
	if c2 := scratch.LoadStatCache(cachePath); c2.Len() != 0 {
		t.Errorf("loaded entries count as touched: %d", c2.Len())
	}
	if err := os.WriteFile(cachePath, []byte("{garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c3 := scratch.LoadStatCache(cachePath); c3.Len() != 0 || c3.Dirty() {
		t.Error("a corrupt cache is not empty")
	}
	clean := scratch.NewStatCache()
	if err := clean.Save(nil, filepath.Join(t.TempDir(), "x.json"), tmp); err != nil {
		t.Errorf("a clean cache tried to write: %v", err)
	}
}

func TestStatCacheSaveDropsUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.json")
	c := scratch.NewStatCache()
	c.Put("1:2", "a", scratch.FileStat{Dev: 1, Ino: 2, Size: 3}, strings.Repeat("a", 64))
	c.Put("1:2", "b", scratch.FileStat{Dev: 1, Ino: 3, Size: 3}, strings.Repeat("b", 64))
	if err := c.Save(fsxtest.New(t), path, filepath.Join(dir, "tmp")); err != nil {
		t.Fatal(err)
	}
	l := scratch.LoadStatCache(path)
	if _, ok := l.Lookup("1:2", "a", scratch.FileStat{Dev: 1, Ino: 2, Size: 3}); !ok {
		t.Fatal("entry a lost")
	}
	if !l.Dirty() {
		t.Fatal("dropping b must make the cache dirty")
	}
	if err := l.Save(fsxtest.New(t), path, filepath.Join(dir, "tmp")); err != nil {
		t.Fatal(err)
	}
	l2 := scratch.LoadStatCache(path)
	if _, ok := l2.Lookup("1:2", "b", scratch.FileStat{Dev: 1, Ino: 3, Size: 3}); ok {
		t.Fatal("an untouched entry survived a save")
	}
}

func folderKey(t *testing.T, folder string) string {
	fi, err := os.Lstat(folder)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := scratch.StatOf(fi)
	return strings.Join([]string{itoa(st.Dev), itoa(st.Ino)}, ":")
}

func itoa(n uint64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// ---- Ops ----

type opsEnv struct {
	s, t     string
	sSnap    *model.RowSnapshot
	tSnap    *model.RowSnapshot
	adapter  *scratch.Adapter
	readCtxT sources.ReadContext
	readCtxS sources.ReadContext
}

func newOpsEnv(t *testing.T, sEntries, tEntries map[string]string) *opsEnv {
	e := &opsEnv{s: t.TempDir(), t: t.TempDir(), adapter: adapter()}
	if sEntries != nil {
		testkit.ScratchWorkspace(t, e.s, acctA, orgA, leaf1, sEntries, mtime)
	}
	if tEntries != nil {
		testkit.ScratchWorkspace(t, e.t, acctA, orgA, leaf1, tEntries, mtime)
	}
	e.readCtxS, e.readCtxT = rc(t, e.s), rc(t, e.t)
	e.sSnap = read(t, e.adapter, e.s, ws1)
	e.tSnap = read(t, e.adapter, e.t, ws1)
	return e
}

func (e *opsEnv) ops(t *testing.T, desired *model.RowSnapshot) sources.OpsResult {
	t.Helper()
	res, err := e.adapter.Ops(context.Background(), sources.OpsContext{ReadContext: e.readCtxT, Source: &e.readCtxS, SourceSnapshot: e.sSnap}, e.tSnap, desired)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func editOf(res sources.OpsResult, item string) *plan.Edit {
	for i := range res.Edits {
		if res.Edits[i].Item == item {
			return &res.Edits[i]
		}
	}
	return nil
}

func TestOpsCopiesFromTheRightSide(t *testing.T) {
	e := newOpsEnv(t,
		map[string]string{"new.txt": "S-new", "same.txt": "T-has", "other.txt": "T-has", "swap.txt": "Y"},
		map[string]string{"other.txt": "T-has", "old.txt": "gone", "swap.txt": "X", "tonly.txt": "T-only", "keep.txt": "K"})
	d := e.sSnap.Clone()
	// Content only T's swap.txt holds, which a copy from S replaces later
	// in the plan (a conflict's T loser): the sibling is copied from T's
	// swap.txt before the copy from S runs.
	x := e.tSnap.Items[ws1+"/swap.txt"]
	d.Items[ws1+"/x-copy.txt"] = relabel(x, ws1+"/x-copy.txt")
	// Content only T's tonly.txt holds, while tonly.txt itself receives
	// T's keep.txt content from a copy from entering: never a source,
	// since copies from entering may run in any order among themselves.
	o := e.tSnap.Items[ws1+"/tonly.txt"]
	d.Items[ws1+"/keep.txt"] = e.tSnap.Items[ws1+"/keep.txt"]
	d.Items[ws1+"/tonly.txt"] = relabel(e.tSnap.Items[ws1+"/keep.txt"], ws1+"/tonly.txt")
	d.Items[ws1+"/o-copy.txt"] = relabel(o, ws1+"/o-copy.txt")
	res := e.ops(t, d)
	if ed := editOf(res, ws1+"/new.txt"); ed == nil || ed.Kind != plan.EditCopy || ed.Copy.Side != plan.SideLeaving || ed.BaseHash != "" {
		t.Errorf("new.txt %+v", ed)
	}
	// T's own content under another name: copied from T.
	if ed := editOf(res, ws1+"/same.txt"); ed == nil || ed.Copy.Side != plan.SideEntering || ed.Copy.Path != "scratch-workspaces/"+ws1+"/other.txt" {
		t.Errorf("same.txt %+v", ed)
	}
	if ed := editOf(res, ws1+"/tonly.txt"); ed == nil || ed.Copy.Side != plan.SideEntering || ed.Copy.Path != "scratch-workspaces/"+ws1+"/keep.txt" {
		t.Errorf("tonly.txt %+v", ed)
	}
	if ed := editOf(res, ws1+"/swap.txt"); ed == nil || ed.Copy.Side != plan.SideLeaving || ed.BaseHash != model.HashBytes([]byte("X")) {
		t.Errorf("swap.txt %+v", ed)
	}
	if ed := editOf(res, ws1+"/old.txt"); ed == nil || ed.Kind != plan.EditRemove || !ed.Destructive || ed.BaseHash != model.HashBytes([]byte("gone")) {
		t.Errorf("old.txt %+v", ed)
	}
	if ed := editOf(res, ws1+"/x-copy.txt"); ed == nil || ed.Copy.Side != plan.SideEntering || ed.Copy.Path != "scratch-workspaces/"+ws1+"/swap.txt" || ed.BaseHash != "" {
		t.Errorf("x-copy.txt (T's swap.txt before S's copy replaces it) %+v", ed)
	}
	if ed := editOf(res, ws1+"/o-copy.txt"); ed != nil {
		t.Errorf("o-copy.txt used a file a copy from entering writes as source: %+v", ed)
	}
	if !skippedWith(res, ws1+"/o-copy.txt", scratch.NoteBytes) {
		t.Errorf("o-copy.txt not skipped: %+v", res.Skipped)
	}
	// The sibling copy runs before the copy from S replaces its source.
	xi, si := -1, -1
	for i, ed := range res.Edits {
		switch ed.Item {
		case ws1 + "/x-copy.txt":
			xi = i
		case ws1 + "/swap.txt":
			si = i
		}
	}
	if xi < 0 || si < 0 || xi > si {
		t.Errorf("x-copy.txt at %d, swap.txt at %d", xi, si)
	}
	// Order: mkdirs, copies from T, symlinks, copies from S, removals.
	rank := map[plan.EditKind]int{plan.EditMkdir: 0, plan.EditSymlink: 2, plan.EditRemove: 4}
	last := -1
	for _, ed := range res.Edits {
		r := rank[ed.Kind]
		if ed.Kind == plan.EditCopy {
			r = 1
			if ed.Copy.Side == plan.SideLeaving {
				r = 3
			}
		}
		if r < last {
			t.Fatalf("edit order: %v", res.Edits)
		}
		last = r
	}
	if r2 := e.ops(t, e.tSnap.Clone()); len(r2.Edits) != 0 || len(r2.Skipped) != 0 {
		t.Errorf("Ops(cur, cur): %+v", r2)
	}
}

func relabel(it model.Item, key string) model.Item {
	it.Key = key
	return it
}

func skippedWith(res sources.OpsResult, key, n string) bool {
	for _, c := range res.Skipped {
		if c.Key == key && c.Note == n {
			return true
		}
	}
	return false
}

func TestOpsMkdirChainUsesSModes(t *testing.T) {
	e := newOpsEnv(t, map[string]string{"d/f": "x", "ln": "->d/f"}, nil)
	if err := os.Chmod(filepath.Join(e.s, "scratch-workspaces", acctA), 0o750); err != nil {
		t.Fatal(err)
	}
	res := e.ops(t, e.sSnap.Clone())
	var mk []string
	for _, ed := range res.Edits {
		if ed.Kind == plan.EditMkdir {
			mk = append(mk, ed.Loc.File)
			if ed.Loc.File == "scratch-workspaces/"+acctA && ed.Mode != 0o750 {
				t.Errorf("acct dir mode %o", ed.Mode)
			}
		}
	}
	want := []string{"scratch-workspaces", "scratch-workspaces/" + acctA, "scratch-workspaces/" + acctA + "/" + orgA,
		"scratch-workspaces/" + ws1, "scratch-workspaces/" + ws1 + "/d"}
	if strings.Join(mk, " ") != strings.Join(want, " ") {
		t.Errorf("mkdirs\n got %v\nwant %v", mk, want)
	}
	if ed := editOf(res, ws1+"/ln"); ed == nil || ed.Kind != plan.EditSymlink || ed.Link != "d/f" {
		t.Errorf("symlink %+v", ed)
	}
}

// A new ancestor whose mode in S has no owner write, or an existing T
// parent without it, would stop every copy below: the workspace is
// skipped (read-only-dir) instead of failing the whole switch.
func TestOpsReadOnlyAncestor(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root writes everywhere")
	}
	e := newOpsEnv(t, map[string]string{"f": "x"}, nil)
	org := filepath.Join(e.s, "scratch-workspaces", acctA, orgA)
	if err := os.Chmod(org, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(org, 0o755) })
	res := e.ops(t, e.sSnap.Clone())
	if !skippedWith(res, ws1+"/", scratch.NoteReadOnly) {
		t.Errorf("S's read-only org folder: %+v", res.Skipped)
	}
	for _, ed := range res.Edits {
		if ed.Kind == plan.EditMkdir && ed.Mode&0o200 == 0 {
			t.Errorf("read-only mkdir planned: %+v", ed)
		}
	}
	// T's existing org folder is read-only: the workspace cannot be made.
	_ = os.Chmod(org, 0o755)
	tOrg := filepath.Join(e.t, "scratch-workspaces", acctA, orgA)
	if err := os.MkdirAll(tOrg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(tOrg, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(tOrg, 0o755) })
	if res := e.ops(t, e.sSnap.Clone()); !skippedWith(res, ws1+"/", scratch.NoteReadOnly) || len(res.Edits) != 0 {
		t.Errorf("T's read-only org folder: %+v %+v", res.Skipped, res.Edits)
	}
}

func TestOpsNameCollision(t *testing.T) {
	nfd := "e\u0301.txt"
	nfc := "\u00e9.txt"
	e := newOpsEnv(t, map[string]string{"a.txt": "S", nfc: "S2", "Dir/x": "S3"}, map[string]string{"A.txt": "T", nfd: "T2", "dir/": ""})
	d := e.sSnap.Clone()
	res := e.ops(t, d)
	for _, k := range []string{ws1 + "/a.txt", ws1 + "/" + nfc, ws1 + "/Dir/"} {
		if !skippedWith(res, k, scratch.NoteCollision) {
			t.Errorf("%s not skipped as a collision: %+v", k, res.Skipped)
		}
	}
	if ed := editOf(res, ws1+"/Dir/x"); ed != nil {
		t.Errorf("a file below a collided directory was copied: %+v", ed)
	}
}

// ---- pure helpers ----

func TestHelpers(t *testing.T) {
	units := []struct {
		key, unit string
		ok        bool
	}{
		{ws1 + "/.git/HEAD", ws1 + "/.git/", true},
		{ws1 + "/.git/", "", false},
		{ws1 + "/.git", "", false},
		{ws1 + "/a/.GIT/objects/x", ws1 + "/a/.GIT/", true},
		{ws1 + "/.git/b/.git/c", ws1 + "/.git/", true},
		{ws1 + "/x", "", false},
		{ws1 + "/", "", false},
	}
	for _, u := range units {
		if got, ok := scratch.GitUnit(u.key); got != u.unit || ok != u.ok {
			t.Errorf("GitUnit(%q) = %q %v", u.key, got, ok)
		}
	}
	file, _ := scratch.FileItem(ws1+"/x", strings.Repeat("a", 64), 1, 0o644, 0)
	unit, _ := scratch.UnitItem(ws1+"/.git/", 0o755, nil)
	dir, _ := scratch.DirItem(ws1+"/d/", 0o755)
	for _, c := range []struct {
		key  string
		it   model.Item
		want bool
	}{{ws1 + "/x", file, true}, {ws1 + "/.git/x", file, false}, {ws1 + "/.git/", unit, true}, {ws1 + "/d/", dir, false}} {
		if got := scratch.Countable(c.key, c.it); got != c.want {
			t.Errorf("Countable(%s) = %v", c.key, got)
		}
	}
	if !scratch.HeldBackSegment("a/Node_Modules/b", []string{"node_modules"}) || scratch.HeldBackSegment("a/b", []string{"node_modules"}) {
		t.Error("HeldBackSegment")
	}
	if scratch.FoldName("Ab\u00c9") != scratch.FoldName("aB"+"e\u0301") {
		t.Error("FoldName does not fold case and composition alike")
	}
	if scratch.FoldName("a") == scratch.FoldName("b") {
		t.Error("FoldName merges different names")
	}
	names := []struct {
		rel   string
		taken []string
		want  string
		ok    bool
	}{
		{"dir/app.py", nil, "dir/app (conflict from home 2026-01-02).py", true},
		{".env", nil, ".env (conflict from home 2026-01-02)", true},
		{"a.tar.gz", nil, "a.tar (conflict from home 2026-01-02).gz", true},
		{"x.txt", []string{"x (conflict from home 2026-01-02).txt"}, "x (conflict from home 2026-01-02) 2.txt", true},
		{strings.Repeat("\u00e9", 200) + ".txt", nil, "", true},
	}
	for _, n := range names {
		taken := func(r string) bool {
			for _, x := range n.taken {
				if x == r {
					return true
				}
			}
			return false
		}
		got, ok := scratch.ConflictName(n.rel, "home", "2026-01-02", taken)
		if ok != n.ok || (n.want != "" && got != n.want) {
			t.Errorf("ConflictName(%q) = %q %v", n.rel, got, ok)
		}
		if len(filepathBase(got)) > 255 {
			t.Errorf("ConflictName(%q) longer than 255 bytes", n.rel)
		}
	}
	if _, ok := scratch.ConflictName("x.txt", "home", "2026-01-02", func(string) bool { return true }); ok {
		t.Error("ConflictName found a name although every one is taken")
	}
	for _, k := range []struct {
		key  string
		kind scratch.KeyKind
		ok   bool
	}{{ws1 + "/", scratch.KeyRoot, true}, {ws1 + "/a/", scratch.KeyDir, true}, {ws1 + "/a/b", scratch.KeyEntry, true},
		{ws1, 0, false}, {ws1 + "/../x", 0, false}, {"x/y/z/a", 0, false}, {ws1 + "/a//b", 0, false}, {ws1 + `/a\b`, 0, false}} {
		_, _, kind, err := scratch.SplitKey(k.key)
		if (err == nil) != k.ok || (k.ok && kind != k.kind) {
			t.Errorf("SplitKey(%q) = %v %v", k.key, kind, err)
		}
	}
	if scratch.DataPath("scratch-workspaces", ws1+"/a/") != "scratch-workspaces/"+ws1+"/a" {
		t.Error("DataPath")
	}
}

func filepathBase(p string) string { return filepath.Base(p) }
