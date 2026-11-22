package worktrees_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/sources/worktrees"
	"github.com/kazupc/paritydesk/internal/testkit"
)

var rep = testkit.Replica(1)

func rc(t *testing.T, folder string) sources.ReadContext {
	m := testkit.Manifest(t)
	return sources.ReadContext{Folder: folder, Replica: rep, Spec: testkit.Spec(t, m, model.RowWorktrees, rep), Manifest: m}
}

func read(t *testing.T, folder string) *model.RowSnapshot {
	t.Helper()
	s, err := worktrees.Worktrees{}.Read(context.Background(), rc(t, folder))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestReadBothForms(t *testing.T) {
	tests := []struct {
		name, doc string
	}{
		{"object", `{"worktrees":{"/tmp/b":{"branch":"b"},"/tmp/a":{"branch":"a"}},"untrackedDirGc":{}}`},
		{"array", `{"worktrees":[{"path":"/tmp/a","branch":"a"},{"path":"/tmp/b","branch":"b"}]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			folder := t.TempDir()
			if err := os.WriteFile(filepath.Join(folder, worktrees.FileName), []byte(tc.doc), 0o600); err != nil {
				t.Fatal(err)
			}
			s := read(t, folder)
			if strings.Join(s.Keys(), ",") != "/tmp/a,/tmp/b" {
				t.Fatalf("keys %v", s.Keys())
			}
			des := s.Clone()
			delete(des.Items, "/tmp/b")
			res, err := worktrees.Worktrees{}.Ops(context.Background(), sources.OpsContext{ReadContext: rc(t, folder)}, s, des)
			if err != nil || len(res.Edits) == 0 {
				t.Fatalf("ops %v %v", res.Edits, err)
			}
			testkit.Apply(t, folder, res.Edits)
			if got := read(t, folder); strings.Join(got.Keys(), ",") != "/tmp/a" {
				t.Fatalf("after apply %v", got.Keys())
			}
			b, _ := os.ReadFile(filepath.Join(folder, worktrees.FileName))
			if tc.name == "object" && !strings.Contains(string(b), `"untrackedDirGc":{}`) {
				t.Fatal("GC state not preserved")
			}
		})
	}
}

func sessionsSnap(t *testing.T, paths ...string) *model.RowSnapshot {
	s := sources.NewSnapshot(model.RowSessions)
	for i, p := range paths {
		v, _ := json.Marshal(map[string]any{"title": "x", "worktreePath": p})
		it, err := model.NewItem(testkit.FakeSession(i+1), v, v, model.Stamp{}, false)
		if err != nil {
			t.Fatal(err)
		}
		s.Items[it.Key] = it
	}
	return s
}

func TestReferencedAndLost(t *testing.T) {
	leaving := sessionsSnap(t, "/tmp/wt/b", "/tmp/wt/a", "", "/tmp/wt/a")
	got, err := worktrees.Referenced(leaving, nil)
	if err != nil || strings.Join(got, ",") != "/tmp/wt/a,/tmp/wt/b" {
		t.Fatalf("referenced %v %v", got, err)
	}
	lost, err := worktrees.Guard{}.LostRegistrations(leaving, sessionsSnap(t, "/tmp/wt/a"), []string{"worktreePath"})
	if err != nil || strings.Join(lost, ",") != "/tmp/wt/b" {
		t.Fatalf("lost %v %v", lost, err)
	}
	if none, _ := worktrees.LostRegistrations(nil, nil, nil); len(none) != 0 {
		t.Fatal("nil snapshots")
	}
}

// TestObservedForm reads the registry as app 6.28899.2 writes it
// (schemaVersion 2, entries keyed by worktree name) and keeps every other
// member byte-identical when an entry changes.
func TestObservedForm(t *testing.T) {
	root := testkit.NewDataRoot(t)
	spec := testkit.Rich("Claude", 1)
	spec.Replica = rep
	folder := root.AddFolder(t, spec)
	s := read(t, folder)
	if strings.Join(s.Keys(), ",") != "one,three" {
		t.Fatalf("keys %v", s.Keys())
	}
	des := s.Clone()
	delete(des.Items, "three")
	res, err := worktrees.Worktrees{}.Ops(context.Background(), sources.OpsContext{ReadContext: rc(t, folder)}, s, des)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range res.Edits {
		if len(e.Path) != 2 || e.Path[0] != "worktrees" || e.Path[1] != "three" {
			t.Fatalf("edit outside the entry: %v", e.Path)
		}
	}
	testkit.Apply(t, folder, res.Edits)
	var doc map[string]json.RawMessage
	b, _ := os.ReadFile(filepath.Join(folder, worktrees.FileName))
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"schemaVersion", "originPins", "originUrls", "pendingTombstones", "untrackedDirGc"} {
		if _, ok := doc[k]; !ok {
			t.Errorf("member %s lost", k)
		}
	}
	if string(doc["schemaVersion"]) != "2" {
		t.Errorf("schemaVersion %s", doc["schemaVersion"])
	}
}

// TestRecordWorktreeAbsoluteOnly: only absolute paths name a worktree.
func TestRecordWorktreeAbsoluteOnly(t *testing.T) {
	for _, tc := range []struct {
		doc, want string
	}{
		{`{"worktreePath":"/tmp/wt/a"}`, "/tmp/wt/a"},
		{`{"worktreePath":"/tmp/wt/a/../b"}`, "/tmp/wt/b"},
		{`{"worktreePath":"wt/a"}`, ""},
		{`{"worktreePath":""}`, ""},
		{`{"worktreeName":"a"}`, ""},
	} {
		obj, err := sources.DecodeObject([]byte(tc.doc), "test")
		if err != nil {
			t.Fatal(err)
		}
		if got := sources.RecordWorktree(obj, []string{"worktreePath"}); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.doc, got, tc.want)
		}
	}
}

// TestLostEntries: registry entries compare by directory. A deleted entry
// and a same-name entry replaced by another directory are lost; an entry
// renamed but still registering the same directory is not.
func TestLostEntries(t *testing.T) {
	item := func(name, path string) model.Item {
		v := []byte(`{"name":"` + name + `","path":"` + path + `"}`)
		it, err := model.NewItem(name, v, v, model.Stamp{}, false)
		if err != nil {
			t.Fatal(err)
		}
		return it
	}
	mk := func(items ...model.Item) *model.RowSnapshot {
		s := &model.RowSnapshot{Row: model.RowWorktrees, Items: map[string]model.Item{}}
		for _, it := range items {
			s.Items[it.Key] = it
		}
		return s
	}
	cur := mk(item("gone", "/w/gone"), item("same", "/w/one"), item("renamed", "/w/r"), item("kept", "/w/k"))
	des := mk(item("same", "/w/two"), item("renamed-2", "/w/r"), item("kept", "/w/k"))
	got, err := worktrees.LostEntries(cur, des, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "/w/gone,/w/one" {
		t.Fatalf("lost %v", got)
	}
	if none, _ := (worktrees.Guard{}).LostEntries(nil, des, []string{"path"}); len(none) != 0 {
		t.Fatalf("nil current: %v", none)
	}
}
