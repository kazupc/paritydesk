package presets_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/sources/presets"
	"github.com/kazupc/paritydesk/internal/testkit"
)

var rep = testkit.Replica(1)

func rc(t *testing.T, m *compat.Manifest, folder string) sources.ReadContext {
	t.Helper()
	return sources.ReadContext{Folder: folder, Replica: rep, LS: testkit.LS(t, folder),
		Spec: testkit.Spec(t, m, model.RowEnvironments, rep), Manifest: m}
}

func read(t *testing.T, c sources.ReadContext) *model.RowSnapshot {
	t.Helper()
	s, err := presets.Environments{}.Read(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func preset(id, model string, token string) map[string]any {
	p := map[string]any{"id": id, "name": id, "target": map[string]any{"path": "/tmp/" + id}, "settings": map[string]any{"model": model}}
	if token != "" {
		p["settings"].(map[string]any)["env"] = map[string]any{"TOKEN": token}
	}
	return p
}

func TestReadHashesSecrets(t *testing.T) {
	m := testkit.Manifest(t)
	folder := testkit.NewDataRoot(t).AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep,
		Presets:             []map[string]any{preset("b", "m", testkit.Canary), preset("a", "m", "")},
		StartedEnvironments: map[string]string{testkit.FakeSession(1): "b"}})
	s := read(t, rc(t, m, folder))
	if strings.Join(s.Order, ",") != "preset/b,preset/a" {
		t.Fatalf("order %v", s.Order)
	}
	b := s.Items["preset/b"]
	if !b.Secret || strings.Contains(string(b.Value), testkit.Canary) || !strings.Contains(string(b.Value), `"sha256:`) {
		t.Fatalf("secret item %+v value %s", b.Secret, b.Value)
	}
	if !strings.Contains(string(b.Raw), testkit.Canary) {
		t.Fatal("raw bytes must keep the secret for copies")
	}
	if a := s.Items["preset/a"]; a.Secret {
		t.Fatal("preset without env marked secret")
	}
	if _, ok := s.Items[presets.PlaceDefaults]; !ok {
		t.Error("placeDefaults missing")
	}
	if v := s.Items[presets.StartedPrefix+testkit.FakeSession(1)].Value; string(v) != `"b"` {
		t.Errorf("started %s", v)
	}
}

func TestReadFallsBackToFile(t *testing.T) {
	m := testkit.Manifest(t)
	folder := testkit.NewDataRoot(t).AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep,
		Presets: []map[string]any{preset("a", "m", "")}})
	if err := os.RemoveAll(filepath.Join(folder, testkit.LevelDBRel)); err != nil {
		t.Fatal(err)
	}
	s := read(t, rc(t, m, folder))
	if _, ok := s.Items["preset/a"]; !ok || len(s.Items) != 1 {
		t.Fatalf("items %v", s.Keys())
	}
}

func syncOps(t *testing.T, m *compat.Manifest, src, dst string, mutate func()) (sources.OpsResult, *model.RowSnapshot) {
	t.Helper()
	srcRC := rc(t, m, src)
	desired := read(t, srcRC)
	if mutate != nil {
		mutate()
		srcRC = rc(t, m, src)
	}
	c := rc(t, m, dst)
	res, err := presets.Environments{}.Ops(context.Background(), sources.OpsContext{ReadContext: c, NowMillis: 1, Source: &srcRC}, read(t, c), desired)
	if err != nil {
		t.Fatal(err)
	}
	return res, desired
}

func TestSecretsCopiedFromSourceRaw(t *testing.T) {
	m := testkit.Manifest(t)
	root := testkit.NewDataRoot(t)
	src := root.AddFolder(t, testkit.FolderSpec{Name: "Claude-s", Replica: rep, Presets: []map[string]any{preset("a", "m1", testkit.Canary)}})
	dst := root.AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep})
	res, desired := syncOps(t, m, src, dst, nil)
	if len(res.Edits) != 2 || len(res.Skipped) != 0 {
		t.Fatalf("edits %d skipped %v", len(res.Edits), res.Skipped)
	}
	for _, e := range res.Edits {
		if !e.Secret {
			t.Errorf("edit on %+v not marked secret", e.Loc)
		}
		if e.Loc.File == presets.FileName && e.Mode != 0o600 {
			t.Errorf("presets file mode %v", e.Mode)
		}
		b, _ := json.Marshal(e)
		if strings.Contains(string(b), testkit.Canary) {
			t.Error("edit JSON form carries the secret")
		}
	}
	testkit.Apply(t, dst, res.Edits)
	fi, err := os.Stat(filepath.Join(dst, presets.FileName))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("presets file %v %v", fi, err)
	}
	if !strings.Contains(testkit.ReadLS(t, dst)["persisted.ccd-session-presets."+string(rep.Account)], testkit.Canary) {
		t.Fatal("secret not copied into Local Storage")
	}
	if got := read(t, rc(t, m, dst)); !sources.SameItems(got, desired, false) {
		t.Fatalf("read after apply %v", got.Keys())
	}
}

func TestChangedSourceSecretIsSkipped(t *testing.T) {
	m := testkit.Manifest(t)
	root := testkit.NewDataRoot(t)
	src := root.AddFolder(t, testkit.FolderSpec{Name: "Claude-s", Replica: rep, Presets: []map[string]any{preset("a", "m1", testkit.Canary)}})
	dst := root.AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep, Presets: []map[string]any{preset("a", "m2", "old")}})
	res, _ := syncOps(t, m, src, dst, func() {
		rotated := []any{preset("a", "m1", "rotated")}
		b, _ := json.Marshal(map[string]any{"state": map[string]any{"presets": rotated}, "version": testkit.PresetsVersion})
		testkit.WriteLS(t, src, map[string]string{"persisted.ccd-session-presets." + string(rep.Account): string(b)})
		f, _ := json.Marshal(map[string]any{"byAccount": map[string]any{string(rep.Account): map[string]any{"presets": rotated}}})
		if err := os.WriteFile(filepath.Join(src, presets.FileName), f, 0o600); err != nil {
			t.Fatal(err)
		}
	})
	if len(res.Edits) != 0 {
		t.Fatalf("edits for a skipped secret: %+v", res.Edits)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Key != "preset/a" || res.Skipped[0].Kind != model.ChangeSkipped ||
		strings.Contains(res.Skipped[0].Note, "rotated") {
		t.Fatalf("skipped %+v", res.Skipped)
	}
}

func TestLimits(t *testing.T) {
	m := testkit.Manifest(t)
	root := testkit.NewDataRoot(t)
	src := root.AddFolder(t, testkit.FolderSpec{Name: "Claude-s", Replica: rep, Presets: []map[string]any{preset("a", "m", ""), preset("b", "m", "")}})
	dst := root.AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep})
	srcRC := rc(t, m, src)
	c := rc(t, m, dst)
	c.Spec.Limits = map[string]int{"max_presets": 1}
	_, err := presets.Environments{}.Ops(context.Background(), sources.OpsContext{ReadContext: c, Source: &srcRC}, read(t, c), read(t, srcRC))
	if err == nil {
		t.Fatal("limit not enforced")
	}
}

func TestStartedEntriesEditedByPath(t *testing.T) {
	m := testkit.Manifest(t)
	folder := testkit.NewDataRoot(t).AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep,
		StartedEnvironments: map[string]string{testkit.FakeSession(1): "a", testkit.FakeSession(2): "b"}})
	c := rc(t, m, folder)
	cur := read(t, c)
	des := cur.Clone()
	delete(des.Items, presets.StartedPrefix+testkit.FakeSession(2))
	res, err := presets.Environments{}.Ops(context.Background(), sources.OpsContext{ReadContext: c}, cur, des)
	if err != nil || len(res.Edits) != 1 {
		t.Fatalf("edits %+v %v", res.Edits, err)
	}
	e := res.Edits[0]
	if strings.Join(e.Path, "/") != "state/bySessionId/"+testkit.FakeSession(2) || !e.Destructive {
		t.Fatalf("edit %+v", e)
	}
}

// TestMirrorFirstAndStaleLS: the file byAccount[<acct>] mirrors the Local
// Storage item, so it is read first; a differing Local Storage copy is
// listed in Stale per preset key, and Local Storage is used only when the
// file has no entry for the account.
func TestMirrorFirstAndStaleLS(t *testing.T) {
	m := testkit.Manifest(t)
	folder := testkit.NewDataRoot(t).AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep,
		Presets: []map[string]any{preset("a", "m1", ""), preset("b", "m1", "")}})
	ls, _ := json.Marshal(map[string]any{"version": testkit.PresetsVersion, "state": map[string]any{
		"presets": []any{preset("a", "stale", ""), preset("b", "m1", ""), preset("c", "m1", "")}, "fileMirrored": true}})
	testkit.WriteLS(t, folder, map[string]string{"persisted.ccd-session-presets." + string(rep.Account): string(ls)})
	s := read(t, rc(t, m, folder))
	if strings.Join(s.Order, ",") != "preset/a,preset/b" {
		t.Fatalf("order %v", s.Order)
	}
	if !strings.Contains(string(s.Items["preset/a"].Value), `"m1"`) {
		t.Fatalf("value is not the mirror's: %s", s.Items["preset/a"].Value)
	}
	if strings.Join(s.Stale, ",") != "preset/a,preset/c" {
		t.Fatalf("stale %v", s.Stale)
	}
	if err := os.Remove(filepath.Join(folder, presets.FileName)); err != nil {
		t.Fatal(err)
	}
	s = read(t, rc(t, m, folder))
	if strings.Join(s.Order, ",") != "preset/a,preset/b,preset/c" || len(s.Stale) != 0 {
		t.Fatalf("without the file: order %v stale %v", s.Order, s.Stale)
	}
}

// TestEditsBothCopies: a changed preset is written to the Local Storage
// item and the file together, and both read back the same.
func TestEditsBothCopies(t *testing.T) {
	m := testkit.Manifest(t)
	root := testkit.NewDataRoot(t)
	src := root.AddFolder(t, testkit.FolderSpec{Name: "Claude-s", Replica: rep, Presets: []map[string]any{preset("a", "m2", "")}})
	dst := root.AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep, Presets: []map[string]any{preset("a", "m1", "")}})
	res, desired := syncOps(t, m, src, dst, nil)
	locs := map[string]bool{}
	for _, e := range res.Edits {
		locs[e.Loc.File+e.Loc.LSKey] = true
	}
	if len(locs) != 2 || !locs[presets.FileName] || !locs["persisted.ccd-session-presets."+string(rep.Account)] {
		t.Fatalf("edited locations %v", locs)
	}
	testkit.Apply(t, dst, res.Edits)
	got := read(t, rc(t, m, dst))
	if !sources.SameItems(got, desired, false) || len(got.Stale) != 0 {
		t.Fatalf("after apply: %v stale %v", got.Keys(), got.Stale)
	}
}

// TestPerOrgItemNeverTouched: the per-org presets item is observed but not
// understood; it is never read, and edits never name it.
func TestPerOrgItemNeverTouched(t *testing.T) {
	m := testkit.Manifest(t)
	root := testkit.NewDataRoot(t)
	src := root.AddFolder(t, testkit.FolderSpec{Name: "Claude-s", Replica: rep, Presets: []map[string]any{preset("a", "m2", "")}})
	dst := root.AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep, Presets: []map[string]any{preset("b", "m1", "")}})
	perOrg := "persisted.ccd-session-presets." + string(rep.Account) + "." + string(rep.Org)
	before, ok := testkit.ReadLS(t, dst)[perOrg]
	if !ok {
		t.Fatal("fixture lacks the per-org item")
	}
	if _, found := read(t, rc(t, m, dst)).Items["preset/per-org-only"]; found {
		t.Fatal("the per-org item was read")
	}
	res, _ := syncOps(t, m, src, dst, nil)
	for _, e := range res.Edits {
		if e.Loc.LSKey == perOrg {
			t.Fatalf("edit names the per-org item: %+v", e.Loc)
		}
	}
	testkit.Apply(t, dst, res.Edits)
	if after := testkit.ReadLS(t, dst)[perOrg]; after != before {
		t.Fatal("the per-org item changed")
	}
}

// TestVersionsMatchManifest ties the versions the adapter writes to the
// manifest's probes.
func TestVersionsMatchManifest(t *testing.T) {
	m := testkit.Manifest(t)
	if spec := testkit.Spec(t, m, model.RowEnvironments, rep); spec.PersistVersion != testkit.PresetsVersion {
		t.Fatalf("persist_version %d", spec.PersistVersion)
	}
	for _, p := range m.Probes {
		if p.ID == "started-environments-version" && p.Version != presets.StartedVersion {
			t.Fatalf("started version %d, adapter writes %d", p.Version, presets.StartedVersion)
		}
	}
	root := testkit.NewDataRoot(t)
	src := root.AddFolder(t, testkit.FolderSpec{Name: "Claude-s", Replica: rep, Presets: []map[string]any{preset("a", "m", "")},
		StartedEnvironments: map[string]string{testkit.FakeSession(1): "a"}})
	dst := root.AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep})
	res, _ := syncOps(t, m, src, dst, nil)
	testkit.Apply(t, dst, res.Edits)
	ls := testkit.ReadLS(t, dst)
	if !strings.Contains(ls["persisted.ccd-session-presets."+string(rep.Account)], `"version":4`) ||
		!strings.Contains(ls["persisted.ccd-started-environments"], `"version":1`) {
		t.Fatalf("created items %v", ls)
	}
}

// TestDisagreeingCopiesNotWritten: when the target's Local Storage presets
// differ from its file mirror (an edit the file has not caught up with,
// or mirroring off), a preset change from the source is not written, so
// neither copy overwrites the other. Without fileMirrored the Local
// Storage copy is the one read.
func TestDisagreeingCopiesNotWritten(t *testing.T) {
	for _, mirrored := range []bool{false, true} {
		m := testkit.Manifest(t)
		root := testkit.NewDataRoot(t)
		src := root.AddFolder(t, testkit.FolderSpec{Name: "Claude-s", Replica: rep,
			Presets: []map[string]any{preset("a", "m1", ""), preset("b", "m2", "")}})
		dst := root.AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep,
			Presets: []map[string]any{preset("a", "m1", ""), preset("b", "m1", "")}})
		lsKey := "persisted.ccd-session-presets." + string(rep.Account)
		ls, _ := json.Marshal(map[string]any{"version": testkit.PresetsVersion, "state": map[string]any{
			"presets": []any{preset("a", "USEREDIT", ""), preset("b", "m1", "")}, "fileMirrored": mirrored}})
		testkit.WriteLS(t, dst, map[string]string{lsKey: string(ls)})
		s := read(t, rc(t, m, dst))
		if strings.Join(s.Stale, ",") != "preset/a" {
			t.Fatalf("mirrored=%v: stale %v", mirrored, s.Stale)
		}
		if got := strings.Contains(string(s.Items["preset/a"].Value), "USEREDIT"); got == mirrored {
			t.Fatalf("mirrored=%v: read the wrong copy: %s", mirrored, s.Items["preset/a"].Value)
		}
		res, _ := syncOps(t, m, src, dst, nil)
		for _, e := range res.Edits {
			// placeDefaults (Local Storage only) may still be written.
			if e.Loc.File == presets.FileName || (e.Loc.LSKey == lsKey && e.Item != presets.PlaceDefaults) {
				t.Fatalf("mirrored=%v: presets written while the copies disagree: %+v", mirrored, e)
			}
		}
		if len(res.Skipped) == 0 || res.Skipped[0].Note != presets.NoteCopiesDisagree {
			t.Fatalf("mirrored=%v: skipped %+v", mirrored, res.Skipped)
		}
		testkit.Apply(t, dst, res.Edits)
		if !strings.Contains(testkit.ReadLS(t, dst)[lsKey], "USEREDIT") {
			t.Fatalf("mirrored=%v: the Local Storage edit was lost", mirrored)
		}
	}
}
