package artifacts

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/pderr"
)

func fixtureDir(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func yes(string) error { return nil }

func TestDecodeLegacy(t *testing.T) {
	b, _ := os.ReadFile(filepath.Join("testdata", "legacy_v2", "manifest.json"))
	lm, err := DecodeLegacy(b)
	if err != nil || lm.Format != 2 || len(lm.Entries) != 5 {
		t.Fatalf("%v %+v", err, lm)
	}
	b1, _ := os.ReadFile(filepath.Join("testdata", "legacy_v1", "manifest.json"))
	lm1, err := DecodeLegacy(b1)
	if err != nil || lm1.Format != 1 || len(lm1.Entries) != 1 {
		t.Fatalf("v1: %v %+v", err, lm1)
	}
	e := lm1.Entries[0]
	// v1 conversion: the owner in step, the mirror with the same version
	// keeps the sha; times without a zone are unknown.
	if len(e.Copies) != 2 || e.Copies[0].SyncedVersion != "1700000000-0000e" || e.Copies[1].SyncedSHA != e.SHA256 ||
		e.Copies[1].SyncedVersion != "" || !e.SavedAt.IsZero() || e.ChangedAt.IsZero() {
		t.Fatalf("v1 entry %+v", e)
	}
	for _, bad := range []string{`[]`, `{"schema":3,"artifacts":{}}`, `{"schema":2}`, `not json`} {
		if _, err := DecodeLegacy([]byte(bad)); err == nil {
			t.Errorf("%s decoded", bad)
		}
	}
}

func TestImportLegacyV2(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	dir := fixtureDir(t, "legacy_v2")
	rep, err := h.svc.ImportLegacy(ctx, ImportInput{From: dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Applied || rep.Format != 2 || rep.Pairs.Found != 5 || rep.Pairs.Importable != 4 || rep.Pairs.LegacyWrapped != 1 ||
		rep.Pairs.WithoutContent != 2 || rep.Copies.DroppedUnmapped != 1 || rep.Copies.DroppedSlugUnknown != 1 || rep.History != 2 {
		t.Fatalf("%+v", rep)
	}
	if len(rep.Unmapped) != 1 || rep.Unmapped[0] != "0000\u2026" || rep.Pairs.TimesUnknown < 1 {
		t.Fatalf("unmapped %+v", rep)
	}
	if h.svc.Archive.Exists() {
		t.Fatal("dry run wrote")
	}
	// Labels never reach the output (canary).
	b, _ := json.Marshal(rep)
	if strings.Contains(string(b), "Canary") {
		t.Fatal("a label reached the output")
	}
	rep, err = h.svc.ImportLegacy(ctx, ImportInput{From: dir, Apply: true}, yes)
	if err != nil || !rep.Applied {
		t.Fatalf("%+v %v", rep, err)
	}
	m := h.manifest()
	if len(m.Pairs) != 4 || len(m.Imports) != 1 {
		t.Fatalf("pairs %d", len(m.Pairs))
	}
	mb, _ := Encode(m)
	if strings.Contains(string(mb), "Canary") {
		t.Fatal("a label reached the manifest")
	}
	one := m.Pairs[PairIDFor("00000000-0000-4000-8000-000000000101")]
	if one == nil || one.Content == nil || len(one.History) != 2 || one.Copies["home"].State != StateUnverified ||
		one.Copies["home"].Origin != OriginImported || one.Copies["work"] == nil {
		t.Fatalf("one %+v", one)
	}
	two := m.Pairs[PairIDFor("00000000-0000-4000-8000-000000000104")]
	if two == nil || !two.Flags.MultiFile || two.Flags.Skipped != "imported" {
		t.Fatalf("two %+v", two)
	}
	if w := m.Pairs[PairIDFor("00000000-0000-4000-8000-000000000105")]; w == nil || w.Content != nil {
		t.Fatalf("wrapped page imported with content: %+v", w)
	}
	// Idempotent.
	rep, err = h.svc.ImportLegacy(ctx, ImportInput{From: dir, Apply: true}, yes)
	if err != nil || rep.Pairs.Exists != 4 || rep.Pairs.Importable != 0 {
		t.Fatalf("second import %+v %v", rep, err)
	}
	// The first sync verifies an imported copy before any publish.
	srv := h.servers["home"]
	b1, _ := os.ReadFile(filepath.Join(dir, "pages", "one", "index.html"))
	srv.arts["00000000-0000-4000-8000-000000000101"] = &fakeArt{slug: "00000000-0000-4000-8000-000000000101", title: "Fictional notes",
		content: string(b1), version: 1, updated: baseTime, files: 1, read: ReadOwner}
	h.sync("apply")
	if c := h.manifest().Pairs[PairIDFor("00000000-0000-4000-8000-000000000101")].Copies["home"]; c.State != StateLive {
		t.Fatalf("imported copy not verified: %+v", c)
	}
}

// TestImportLegacyRereadsAtApply: --apply reads each page again (the plan
// keeps no bytes); a page that changed since the plan is not stored and
// its pair is imported without content.
func TestImportLegacyRereadsAtApply(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "old")
	if err := os.CopyFS(dir, os.DirFS(fixtureDir(t, "legacy_v2"))); err != nil {
		t.Fatal(err)
	}
	page := filepath.Join(dir, "pages", "one", "index.html")
	changeIt := func(string) error { return os.WriteFile(page, []byte("changed after the plan"), 0o600) }
	rep, err := h.svc.ImportLegacy(ctx, ImportInput{From: dir, Apply: true}, changeIt)
	if err != nil || !rep.Applied {
		t.Fatalf("%+v %v", rep, err)
	}
	one := h.manifest().Pairs[PairIDFor("00000000-0000-4000-8000-000000000101")]
	if one == nil || one.Content != nil || len(one.History) != 0 {
		t.Fatalf("a page changed after the plan was imported: %+v", one)
	}
	for sha := range referencedBlobs(h.manifest()) {
		if !h.svc.Archive.HasBlob(sha) {
			t.Fatalf("blob %s referenced but not stored", sha)
		}
	}
}

func TestImportLegacyMapping(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	dir := fixtureDir(t, "legacy_v2")
	// An account only in the history of a profile stays unmapped (M9).
	h.root.Profiles["work"].AccountHistory = []ids.AccountID{"00000000-0000-4000-8000-000000000005"}
	rep, _ := h.svc.ImportLegacy(ctx, ImportInput{From: dir}, nil)
	if rep.Copies.DroppedUnmapped != 1 {
		t.Fatalf("%+v", rep.Copies)
	}
	// --map maps it, but its replica is not the alias' current one.
	rep, _ = h.svc.ImportLegacy(ctx, ImportInput{From: dir, Map: map[ids.AccountID]ids.Alias{"00000000-0000-4000-8000-000000000005": "work"}}, nil)
	if rep.Copies.DroppedUnmapped != 0 || rep.Copies.DroppedReplicaUnknown != 1 {
		t.Fatalf("%+v", rep.Copies)
	}
	// A chain in progress blocks --apply.
	_ = h.svc.Archive.SaveChain(&Chain{V: 1, ID: "art-x", State: ChainActive})
	rep, err := h.svc.ImportLegacy(ctx, ImportInput{From: dir, Apply: true}, yes)
	if err != nil || rep.State != StateBlocked || rep.Applied {
		t.Fatalf("%+v %v", rep, err)
	}
}

func TestImportLegacyPaths(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for name, dir := range map[string]string{
		"relative":       "testdata/legacy_v2",
		"inside support": h.dir,
		"in a Claude folder": func() string {
			p := filepath.Join(t.TempDir(), "Claude-home", "x")
			_ = os.MkdirAll(p, 0o700)
			h.svc.DataRoot = filepath.Dir(filepath.Dir(p))
			return p
		}(),
		"missing": filepath.Join(t.TempDir(), "nope"),
	} {
		if _, err := h.svc.ImportLegacy(ctx, ImportInput{From: dir}, nil); pderr.CodeOf(err) != pderr.Usage {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A path whose parent is a symlink into a Claude folder, and a
	// folder named in another case than the app's (case-insensitive).
	data := t.TempDir()
	h.svc.DataRoot = data
	inside := filepath.Join(data, "Claude", "x")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(filepath.Join(data, "Claude"), link); err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{"through a symlinked parent": filepath.Join(link, "x"),
		"lower-case name": filepath.Join(data, "claude", "x")} {
		if name == "lower-case name" {
			if _, err := os.Stat(dir); err != nil {
				continue // a case-sensitive file system: no such folder
			}
		}
		if _, err := h.svc.ImportLegacy(ctx, ImportInput{From: dir}, nil); pderr.CodeOf(err) != pderr.Usage {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A symlinked manifest, a sha mismatch and an oversized manifest.
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(fixtureDir(t, "legacy_v2"), "manifest.json"), filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.ImportLegacy(ctx, ImportInput{From: dir}, nil); pderr.CodeOf(err) != pderr.Usage {
		t.Fatalf("symlink: %v", err)
	}
	dir = t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "pages", "one"), 0o700)
	b, _ := os.ReadFile(filepath.Join(fixtureDir(t, "legacy_v2"), "manifest.json"))
	_ = os.WriteFile(filepath.Join(dir, "manifest.json"), b, 0o600)
	_ = os.WriteFile(filepath.Join(dir, "pages", "one", "index.html"), []byte("changed"), 0o600)
	rep, err := h.svc.ImportLegacy(ctx, ImportInput{From: dir}, nil)
	if err != nil || rep.Pairs.WithoutContent != 4 {
		t.Fatalf("sha mismatch: %+v %v", rep.Pairs, err)
	}
	_ = os.WriteFile(filepath.Join(dir, "manifest.json"), make([]byte, MaxLegacyManifest+1), 0o600)
	if _, err := h.svc.ImportLegacy(ctx, ImportInput{From: dir}, nil); pderr.CodeOf(err) != pderr.Usage {
		t.Fatalf("oversized: %v", err)
	}
}

func TestLegacyWrappedDetection(t *testing.T) {
	for s, want := range map[string]bool{
		"<untrusted-document-0123abcd>x</untrusted-document-0123abcd>":         true,
		"\xef\xbb\xbf  <untrusted-document-0123abcd>x":                         true,
		"<!DOCTYPE html><p><untrusted-document-0123abcd></p>":                  false,
		"  <untrusted-document-0123abcd>\nx\n</untrusted-document-0123abcd>\n": true,
	} {
		if legacyWrapped([]byte(s)) != want {
			t.Errorf("%q", s)
		}
	}
	for p, want := range map[string]bool{"pages/one/index.html": true, "../x.html": false, "/abs/index.html": false, "pages/a/b/index.html": false} {
		if legacyPathOK(p, 2) != want {
			t.Errorf("%s", p)
		}
	}
}
