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
		b, _ := json.Marshal(map[string]any{"state": map[string]any{"presets": []any{preset("a", "m1", "rotated")}}, "version": 0})
		testkit.WriteLS(t, src, map[string]string{"persisted.ccd-session-presets." + string(rep.Account): string(b)})
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
