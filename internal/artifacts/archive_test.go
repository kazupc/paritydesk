package artifacts

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/fsx/fsxtest"
)

func newArchive(t *testing.T) (*Archive, *fsxtest.FS) {
	t.Helper()
	fs := fsxtest.New(t)
	return NewArchive(filepath.Join(t.TempDir(), "support"), fs, testSpec(t)), fs
}

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestArchiveModesAndBlobs(t *testing.T) {
	a, _ := newArchive(t)
	content := []byte(page(2))
	s := shaHex(content)
	if err := a.PutBlob(s, content); err != nil {
		t.Fatal(err)
	}
	if mode(t, a.Root) != 0o700 || mode(t, filepath.Join(a.Root, "blobs")) != 0o700 ||
		mode(t, filepath.Join(a.Root, "blobs", s+".html.txt")) != 0o600 {
		t.Fatal("modes")
	}
	// EEXIST with equal content is fine; a different file is corrupt.
	if err := a.PutBlob(s, content); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.Root, "blobs", s+".html.txt"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.PutBlob(s, content); !errors.Is(err, ErrBlobCorrupt) {
		t.Fatalf("corrupt blob: %v", err)
	}
	if _, err := a.ReadBlob(s); !errors.Is(err, ErrBlobCorrupt) {
		t.Fatalf("read corrupt blob: %v", err)
	}
	if err := a.PutBlob(s, []byte("other")); !errors.Is(err, ErrBlobCorrupt) {
		t.Fatal("blob with a wrong name accepted")
	}
}

func TestArchiveRefusesSymlinks(t *testing.T) {
	a, _ := newArchive(t)
	if err := a.SaveChain(&Chain{V: 1, ID: "art-x"}); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(a.Root, "inventory")); err != nil {
		t.Fatal(err)
	}
	if err := a.SaveInventory(&Inventory{V: 1, Profile: "home"}); err == nil {
		t.Fatal("wrote through a symlinked directory")
	}
	if _, err := os.Stat(filepath.Join(outside, "home.json")); err == nil {
		t.Fatal("file outside the archive")
	}
	if err := os.Remove(filepath.Join(a.Root, "chain.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "x.json"), filepath.Join(a.Root, "chain.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.LoadChain(); !errors.Is(err, ErrSymlink) {
		t.Fatalf("read through a symlink: %v", err)
	}
}

func TestManifestCommitKeepsThreePrev(t *testing.T) {
	a, _ := newArchive(t)
	for i := 0; i < 6; i++ {
		if err := a.CommitManifest(NewManifest(), baseTime); err != nil {
			t.Fatal(err)
		}
	}
	m, err := a.LoadManifest()
	if err != nil || m.Seq != 6 {
		t.Fatalf("seq %v %v", m, err)
	}
	if prev := a.PrevManifests(); len(prev) != 3 || prev[0] != 5 || prev[2] != 3 {
		t.Fatalf("prev %v", prev)
	}
}

// TestCommitFault: a failure between the blob writes and the manifest
// commit leaves the old manifest valid and the new blob unreferenced;
// GC removes it once it is old.
func TestCommitFault(t *testing.T) {
	a, _ := newArchive(t)
	if err := a.CommitManifest(NewManifest(), baseTime); err != nil {
		t.Fatal(err)
	}
	content := []byte("orphan")
	s := shaHex(content)
	if err := a.PutBlob(s, content); err != nil {
		t.Fatal(err)
	}
	a.BeforeCommit = func() error { return errors.New("crash") }
	m := NewManifest()
	m.Pairs[PairIDFor(slugA)] = basePair()
	if err := a.CommitManifest(m, baseTime); err == nil {
		t.Fatal("fault ignored")
	}
	old, err := a.LoadManifest()
	if err != nil || len(old.Pairs) != 0 || Validate(old, testSpec(t), 10) != nil {
		t.Fatal("old manifest not kept")
	}
	if err := a.GC(old, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !a.HasBlob(s) {
		t.Fatal("a fresh orphan was removed")
	}
	if err := a.GC(old, nil, time.Now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if a.HasBlob(s) {
		t.Fatal("an old orphan was kept")
	}
}

func TestOutboxAndPartialGC(t *testing.T) {
	a, _ := newArchive(t)
	content := []byte(page(1))
	s := shaHex(content)
	_ = a.PutBlob(s, content)
	if _, err := a.Stage(s); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.Root, "outbox", "stray.html"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.PutPartial(Chunk{Slug: slugA, Version: verA, Offset: 1, NextOffset: 3, TotalLines: 4, Body: "a\nb"}); err != nil {
		t.Fatal(err)
	}
	if ps := a.Partials(); len(ps[slugA].Chunks) != 1 {
		t.Fatalf("%+v", ps)
	}
	m := NewManifest()
	live := &Chain{V: 1, State: ChainActive, Candidates: map[Slug]Candidate{slugA: {}}}
	if err := a.GC(m, live, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(a.Root, "outbox", "stray.html")); err == nil {
		t.Fatal("misnamed outbox file kept")
	}
	if len(a.Partials()) != 1 {
		t.Fatal("live partial removed")
	}
	if err := a.Unstage(s); err != nil {
		t.Fatal(err)
	}
	if err := a.GC(m, &Chain{State: ChainComplete}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(a.Partials()) != 0 {
		t.Fatal("partial of a finished chain kept")
	}
}

// TestTmpSymlinkRefused: a symlinked .tmp is refused; nothing is written
// where it points.
func TestTmpSymlinkRefused(t *testing.T) {
	h := newHarness(t)
	a := h.svc.Archive
	if err := a.SaveChain(&Chain{V: 1, ID: "art-20261126T100000Z-00000001", Profile: "home", State: ChainActive}); err != nil {
		t.Fatal(err)
	}
	elsewhere := t.TempDir()
	tmp := filepath.Join(a.Root, ".tmp")
	if err := os.RemoveAll(tmp); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, tmp); err != nil {
		t.Fatal(err)
	}
	if err := a.SaveChain(&Chain{V: 1, ID: "art-20261126T100000Z-00000002", Profile: "home", State: ChainActive}); err == nil {
		t.Fatal("wrote through a symlinked .tmp")
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Fatalf("files written through the symlink: %v", entries)
	}
}

// TestPartialLongVersion: a chunk of a version id at the 128-byte limit
// is kept (its folder name is a fixed-length key, never above NAME_MAX).
func TestPartialLongVersion(t *testing.T) {
	h := newHarness(t)
	a := h.svc.Archive
	long := VersionID(strings.Repeat("a", 128))
	for _, c := range []Chunk{
		{Slug: slugA, Version: long, Offset: 1, NextOffset: 3, TotalLines: 4, Body: "a\nb"},
		{Slug: slugA, Version: long, Offset: 3, TotalLines: 4, Body: "c\nd"},
	} {
		if err := a.PutPartial(c); err != nil {
			t.Fatalf("offset %d: %v", c.Offset, err)
		}
	}
	ps := a.Partials()[slugA]
	if ps.Version != long || len(ps.Chunks) != 2 || ps.Chunks[0].Offset != 1 {
		t.Fatalf("%+v", ps)
	}
	if err := a.DropPartial(slugA); err != nil || len(a.Partials()) != 0 {
		t.Fatalf("drop: %v %+v", err, a.Partials())
	}
}

func TestDueFile(t *testing.T) {
	a, _ := newArchive(t)
	if a.LoadDue() != nil {
		t.Fatal("due without a file")
	}
	if err := a.SaveDue(DueFile{Seq: 3, Profile: "home", Epoch: 2, Count: 1}); err != nil {
		t.Fatal(err)
	}
	if d := a.LoadDue(); d == nil || d.Count != 1 || d.Epoch != 2 {
		t.Fatalf("%+v", d)
	}
	_ = os.WriteFile(filepath.Join(a.Root, "due.json"), []byte(`{"v":1,"profile":"../x","count":1}`), 0o600)
	if a.LoadDue() != nil {
		t.Fatal("malformed due accepted")
	}
	big := make([]byte, 5000)
	_ = os.WriteFile(filepath.Join(a.Root, "due.json"), big, 0o600)
	if a.LoadDue() != nil {
		t.Fatal("oversized due accepted")
	}
}

func TestRemoveAll(t *testing.T) {
	a, fs := newArchive(t)
	_ = a.SaveChain(&Chain{V: 1})
	if err := RemoveAll(fs, filepath.Dir(a.Root)); err != nil {
		t.Fatal(err)
	}
	if a.Exists() {
		t.Fatal("archive kept")
	}
}
