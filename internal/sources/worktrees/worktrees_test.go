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
	got, err := worktrees.Referenced(leaving)
	if err != nil || strings.Join(got, ",") != "/tmp/wt/a,/tmp/wt/b" {
		t.Fatalf("referenced %v %v", got, err)
	}
	lost, err := worktrees.Guard{}.LostRegistrations(leaving, sessionsSnap(t, "/tmp/wt/a"))
	if err != nil || strings.Join(lost, ",") != "/tmp/wt/b" {
		t.Fatalf("lost %v %v", lost, err)
	}
	if none, _ := worktrees.LostRegistrations(nil, nil); len(none) != 0 {
		t.Fatal("nil snapshots")
	}
}
