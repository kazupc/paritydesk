package testkit

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/store/kv"
)

// TestedVersion is the app version the embedded manifest lists as tested.
const TestedVersion = "6.28899.2"

// Replica returns the n-th fake replica: account FakeUUID(100n+1), org
// FakeUUID(100n+2).
func Replica(n int) model.Replica {
	return model.Replica{Account: ids.AccountID(FakeUUID(100*n + 1)), Org: ids.OrgID(FakeUUID(100*n + 2))}
}

// Manifest loads the embedded manifests and selects the tested version's.
func Manifest(t testing.TB) *compat.Manifest {
	t.Helper()
	ms, err := compat.Load("")
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := compat.Select(ms, TestedVersion)
	if err != nil || m == nil {
		t.Fatalf("testkit: no manifest for %s: %v", TestedVersion, err)
	}
	return m
}

// Spec returns m's spec of row, expanded for r.
func Spec(t testing.TB, m *compat.Manifest, row model.RowID, r model.Replica) compat.RowSpec {
	t.Helper()
	s, ok := m.Row(row)
	if !ok {
		t.Fatalf("testkit: manifest has no row %s", row)
	}
	return s.Expand(r)
}

// LS opens a snapshot of the folder's Local Storage through Opener, or
// returns nil when the folder has none. It is closed when the test ends.
func LS(t testing.TB, folder string) kv.Reader {
	t.Helper()
	dir := filepath.Join(folder, filepath.FromSlash(LevelDBRel))
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	r, err := Opener(t).OpenSnapshot(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// Rich returns a folder spec that fills every v0.1 row with the shapes
// observed on app 6.28899.2 (ADR 0004). Variants 1 and 2 share some
// session and preset ids but differ in content and order, so one can be
// synced onto the other. Token-bearing fields (config.json, env values,
// buddy-tokens.json) hold Canary.
func Rich(name string, variant int) FolderSpec {
	v := int64(variant)
	alt := variant%2 == 0
	pick := func(a, b string) string {
		if alt {
			return b
		}
		return a
	}
	wtThree := pick("three", "three-b")
	return FolderSpec{
		Name:      name,
		Replica:   Replica(variant),
		WithToken: true,
		Sessions: []Session{
			{N: 1, Title: pick("first", "first renamed"), LastActivityAt: 1000 + v, PermissionMode: "default",
				EnvScopeID: "env-a", Worktree: "/tmp/wt/one", CLISessionID: FakeUUID(901),
				Extra: map[string]any{"futureField": map[string]any{"nested": []int{1, 2}}}},
			{N: 2, Title: "second", Archived: true, LastActivityAt: 2000},
			{N: 3, Title: "third", LastActivityAt: 3000 + 10*v, Worktree: "/tmp/wt/" + wtThree,
				Extra: map[string]any{"forkedFromSessionId": FakeSession(1),
					"spawnedFrom": map[string]any{"sessionId": FakeSession(1), "taskId": "task-1", "sideSession": false},
					"startChain":  map[string]any{"depth": 1, "firstSessionId": FakeSession(1)}}},
			{N: 10 + variant, Title: "only here", LastActivityAt: 4000},
		},
		Tombstones: map[int]int64{20 + variant: 5000 + v},
		Presets: []map[string]any{
			{"id": "env-a", "name": "A", "createdAt": 50, "lastUsedAt": 100 + v,
				"target":   map[string]any{"kind": "local", "cwd": "/tmp/project"},
				"settings": map[string]any{"effort": "high", "modelId": pick("m1", "m2"), "fastMode": false, "worktree": false, "env": map[string]any{"TOKEN": Canary}}},
			{"id": "env-" + pick("b", "c"), "name": "B", "createdAt": 60, "lastUsedAt": 60,
				"target":   map[string]any{"kind": "local", "cwd": "/tmp/other"},
				"settings": map[string]any{"effort": "low", "modelId": "m1", "fastMode": true, "worktree": true}},
		},
		GroupOrder:  []string{"/tmp/project", pick("/tmp/other", "/tmp/third"), pick("/tmp/third", "/tmp/other")},
		PinnedOrder: []string{FakeSession(1 + variant%2), FakeSession(2 - variant%2)},
		SidebarView: map[string]any{
			"migratedFrom": pick("legacy", "legacy-b"),
			"prefsSeeded":  true,
			"sections": []any{
				section("sessions", "sessions", 0, pick("recency", "alpha")),
				section("s-"+pick("x", "y"), "custom", 1, "recency"),
			},
		},
		AppPrefs: map[string]any{"sidebarMode": pick("expanded", "compact"), "keepAwakeEnabled": alt,
			"ccAutoArchiveOnPrClose": true},
		MCPServers: map[string]any{
			"files":            map[string]any{"command": "node", "args": []string{"server.js"}, "env": map[string]any{"API_KEY": Canary}},
			pick("one", "two"): map[string]any{"command": "true"},
		},
		Worktrees: map[string]any{
			"one":   worktree("one", "/tmp/wt/one", 1),
			wtThree: worktree(wtThree, "/tmp/wt/"+wtThree, 2+v),
		},
		WindowState: []byte(`{"x":10,"y":20,"width":` + pick("800", "1024") + `,"height":600,"isMaximized":false,` +
			`"isFullScreen":false,"displayBounds":{"x":0,"y":0,"width":1440,"height":900}}`),
		Backlog: []map[string]any{
			backlogItem("t1", pick("task", "task edited"), 10),
			backlogItem("t"+pick("2", "3"), "more", 20),
		},
		ScheduledTasks: []map[string]any{{"id": "sched-1", "cron": pick("0 9 * * *", "0 10 * * *")}},
		WaitingInput: map[string][]byte{
			"q-" + pick("1", "2") + ".json": []byte(`{"q":1}`),
			"images/a.png":                  []byte(pick("png-a", "png-b")),
		},
		StartedEnvironments: map[string]string{FakeSession(1): "env-a", FakeSession(3): "env-" + pick("b", "c")},
		Decoys: []Decoy{{Replica: DecoyReplica(variant), Sessions: []Session{
			{N: 1, Title: "decoy", LastActivityAt: 9000},
			{N: 70 + variant, Title: "decoy only", LastActivityAt: 9100},
		}, Tombstones: map[int]int64{2: 9200}}},
		Extras: map[string][]byte{
			"buddy-tokens.json":         []byte(`{"token":"` + Canary + `"}`),
			"TransportSecurity":         []byte(`{}`),
			"cowork-memory-resets.json": []byte(`{}`),
		},
	}
}

// DecoyReplica returns another account's replica that variant n's folder
// holds as a decoy: account FakeUUID(100n+51), org FakeUUID(100n+52).
func DecoyReplica(n int) model.Replica {
	return model.Replica{Account: ids.AccountID(FakeUUID(100*n + 51)), Org: ids.OrgID(FakeUUID(100*n + 52))}
}

func section(id, kind string, order int, sortBy string) map[string]any {
	return map[string]any{"id": id, "kind": kind, "name": id, "order": order, "shown": true, "collapsed": false,
		"groupBy": "project", "sortBy": sortBy, "ascending": false, "v": 1,
		"show": map[string]any{"emptyGroups": false, "metadata": true, "sideChats": true, "slack": false}}
}

func worktree(name, path string, created int64) map[string]any {
	return map[string]any{"name": name, "path": path, "branch": "wt/" + name, "sourceBranch": "main",
		"baseRepo": "/tmp/project", "originTopLevel": "/tmp/project", "anchors": []any{}, "autoTrusted": true,
		"createdAt": created}
}

func backlogItem(id, title string, at int64) map[string]any {
	return map[string]any{"id": id, "title": title, "tldr": "short", "prompt": "do it", "cwd": "/tmp/project",
		"source": "session", "originSessionId": FakeSession(1), "originTaskId": "task-1", "suggestedAt": at, "filedAt": at + 1}
}
