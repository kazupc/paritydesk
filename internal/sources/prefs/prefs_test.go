package prefs_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/sources/prefs"
	"github.com/kazupc/paritydesk/internal/testkit"
)

var rep = testkit.Replica(1)

func rc(t *testing.T, m *compat.Manifest, folder string, row model.RowID) sources.ReadContext {
	t.Helper()
	return sources.ReadContext{Folder: folder, Replica: rep, Spec: testkit.Spec(t, m, row, rep), Manifest: m}
}

func read(t *testing.T, a sources.Adapter, c sources.ReadContext) *model.RowSnapshot {
	t.Helper()
	s, err := a.Read(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func set(t *testing.T, s *model.RowSnapshot, key string, v any) *model.RowSnapshot {
	t.Helper()
	d := s.Clone()
	b, _ := json.Marshal(v)
	it, err := model.NewItem(key, b, b, model.Stamp{}, false)
	if err != nil {
		t.Fatal(err)
	}
	d.Items[key] = it
	return d
}

func TestAppPrefsAllowList(t *testing.T) {
	m := testkit.Manifest(t)
	folder := testkit.NewDataRoot(t).AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep,
		AppPrefs: map[string]any{"sidebarMode": "x", "keepAwakeEnabled": true, "ccAutoArchiveOnPrClose": true, "unknownPref": 1}})
	tests := []struct {
		allow []string
		want  string
	}{
		{nil, "keepAwakeEnabled,sidebarMode"},
		{[]string{"sidebarMode", "ccAutoArchiveOnPrClose", "unknownPref"}, "sidebarMode"},
	}
	for _, tc := range tests {
		c := rc(t, m, folder, model.RowAppPrefs)
		c.Spec.Fields = map[string][]string{sources.RoleAllow: tc.allow}
		if got := strings.Join(read(t, prefs.AppPrefs{}, c).Keys(), ","); got != tc.want {
			t.Errorf("allow %v: got %s, want %s", tc.allow, got, tc.want)
		}
	}
}

func TestAppPrefsEditOnlyTheirMember(t *testing.T) {
	m := testkit.Manifest(t)
	folder := testkit.NewDataRoot(t).AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep,
		AppPrefs: map[string]any{"sidebarMode": "x", "keepAwakeEnabled": true}, GroupOrder: []string{"a"}})
	c := rc(t, m, folder, model.RowAppPrefs)
	cur := read(t, prefs.AppPrefs{}, c)
	des := set(t, cur, "sidebarMode", "y")
	delete(des.Items, "keepAwakeEnabled")
	des = set(t, des, "ccAutoArchiveOnPrClose", false) // internal_state: never written
	res, err := prefs.AppPrefs{}.Ops(context.Background(), sources.OpsContext{ReadContext: c}, cur, des)
	if err != nil || len(res.Edits) != 2 {
		t.Fatalf("edits %+v %v", res.Edits, err)
	}
	cfg := filepath.Join(folder, sources.DesktopConfigFile)
	before, _ := os.ReadFile(cfg)
	testkit.Apply(t, folder, res.Edits)
	after, _ := os.ReadFile(cfg)
	strip := func(b []byte) string {
		for _, p := range []jsonx.Path{{"preferences", "sidebarMode"}, {"preferences", "keepAwakeEnabled"}} {
			if out, err := jsonx.Delete(b, p); err == nil {
				b = out
			} else if !errors.Is(err, jsonx.ErrNotFound) {
				t.Fatal(err)
			}
		}
		return string(b)
	}
	if strip(before) != strip(after) {
		t.Fatal("bytes outside the edited members changed")
	}
	if got := read(t, prefs.AppPrefs{}, c); got.Items["sidebarMode"].Hash != des.Items["sidebarMode"].Hash || len(got.Items) != 1 {
		t.Fatalf("after apply %v", got.Keys())
	}
}

func TestAppPrefsWithoutContainerSkip(t *testing.T) {
	m := testkit.Manifest(t)
	folder := testkit.NewDataRoot(t).AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep})
	c := rc(t, m, folder, model.RowAppPrefs)
	cur := read(t, prefs.AppPrefs{}, c)
	res, err := prefs.AppPrefs{}.Ops(context.Background(), sources.OpsContext{ReadContext: c}, cur, set(t, cur, "sidebarMode", "y"))
	if err != nil || len(res.Edits) != 0 || len(res.Skipped) != 1 {
		t.Fatalf("edits %d skipped %v err %v", len(res.Edits), res.Skipped, err)
	}
}

func TestMCPSecrets(t *testing.T) {
	m := testkit.Manifest(t)
	root := testkit.NewDataRoot(t)
	servers := map[string]any{"s": map[string]any{"command": "x", "env": map[string]any{"KEY": testkit.Canary}}}
	src := root.AddFolder(t, testkit.FolderSpec{Name: "Claude-s", Replica: rep, MCPServers: servers})
	dst := root.AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep, AppPrefs: map[string]any{}})
	srcRC := rc(t, m, src, model.RowMCPServers)
	des := read(t, prefs.MCPServers{}, srcRC)
	it := des.Items["s"]
	if !it.Secret || strings.Contains(string(it.Value), testkit.Canary) || !strings.Contains(string(it.Value), "sha256:") {
		t.Fatalf("item %v %s", it.Secret, it.Value)
	}
	c := rc(t, m, dst, model.RowMCPServers)
	res, err := prefs.MCPServers{}.Ops(context.Background(), sources.OpsContext{ReadContext: c, Source: &srcRC}, read(t, prefs.MCPServers{}, c), des)
	if err != nil || len(res.Edits) != 2 || !res.Edits[1].Secret {
		t.Fatalf("edits %+v %v", res.Edits, err)
	}
	testkit.Apply(t, dst, res.Edits)
	if got := read(t, prefs.MCPServers{}, c); got.Items["s"].Hash != it.Hash {
		t.Fatal("read after apply differs")
	}
	// The source moves on: the target keeps its value, the item is skipped.
	if err := os.WriteFile(filepath.Join(src, sources.DesktopConfigFile), []byte(`{"mcpServers":{"s":{"command":"x","env":{"KEY":"new"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	other := root.AddFolder(t, testkit.FolderSpec{Name: "Claude-o", Replica: rep, AppPrefs: map[string]any{}})
	oc := rc(t, m, other, model.RowMCPServers)
	res, err = prefs.MCPServers{}.Ops(context.Background(), sources.OpsContext{ReadContext: oc, Source: &srcRC}, read(t, prefs.MCPServers{}, oc), des)
	if err != nil || len(res.Edits) != 0 || len(res.Skipped) != 1 {
		t.Fatalf("edits %d skipped %v err %v", len(res.Edits), res.Skipped, err)
	}
}

func TestPermissionAcks(t *testing.T) {
	folder := testkit.NewDataRoot(t).AddFolder(t, testkit.FolderSpec{Name: "Claude", Replica: rep, AppPrefs: map[string]any{
		"bypassPermissionsAckByAccount": map[string]any{string(rep.Account): true, testkit.FakeUUID(9): false},
		"planPermissionAck":             false,
		"permissionNote":                testkit.Canary,
		"sidebarMode":                   "x",
	}})
	got, err := prefs.PermissionAcks(context.Background(), folder, rep)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"bypassPermissionsAckByAccount": true, "planPermissionAck": false}
	if len(got) != len(want) {
		t.Fatalf("acks %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %v", k, got[k])
		}
	}
	empty, err := prefs.PermissionAcks(context.Background(), t.TempDir(), rep)
	if err != nil || len(empty) != 0 {
		t.Fatalf("no config: %v %v", empty, err)
	}
}
