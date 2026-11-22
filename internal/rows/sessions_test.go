package rows

import (
	"fmt"
	"testing"

	"github.com/kazupc/paritydesk/internal/model"
)

// sessionFields is a manifest-like field list used only by tests.
func sessionFields() map[string][]string {
	return map[string][]string{
		RolePerField:     {"title", "isArchived", "envScopeId", "startedFromEnvironmentId", "model"},
		RoleMaxTime:      {"lastActivityAt"},
		RoleRunState:     {"interruptedByQuitAt"},
		RoleAccountBound: {"bridgeSessionIds"},
		RoleParent:       {"parentSessionId"},
		RolePermission:   {"permissionMode"},
		RoleEnvRef:       {"envScopeId", "startedFromEnvironmentId"},
		RoleProjectPath:  {"cwd"},
	}
}

func rec(t fatalHelper, n int, body string, activity int64) model.Item {
	t.Helper()
	sep := ""
	if body != "" {
		sep = ","
	}
	v := fmt.Sprintf(`{"sessionId":%q,"lastActivityAt":%d%s%s}`, fakeSession(n), activity, sep, body)
	return mk(t, fakeSession(n), v, activity)
}

func sessPolicy() Policy {
	p := openPolicy()
	p.Fields = sessionFields()
	return p
}

func TestSessionsFieldMergeD8(t *testing.T) {
	base := rec(t, 1, `"title":"old","isArchived":false`, 100)
	renamed := rec(t, 1, `"title":"new","isArchived":false`, 150)
	archived := rec(t, 1, `"title":"old","isArchived":true`, 120)
	tests := []struct {
		name     string
		fields   map[string][]string
		title    string
		archived bool
		activity float64
		conflict bool
	}{
		{"per-field merge keeps both edits", sessionFields(), "new", true, 150, false},
		{"empty field map merges whole records", map[string][]string{}, "new", false, 150, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pol := openPolicy()
			pol.Fields = tc.fields
			in := input(model.RowSessions,
				snap(model.RowSessions, renamed), snap(model.RowSessions, base),
				snap(model.RowSessions, archived), snap(model.RowSessions, base),
				snap(model.RowSessions, base), pol)
			out := mustMerge(t, model.RowSessions, in)
			got := out.Desired.Items[fakeSession(1)]
			if field(t, got, "title") != tc.title || field(t, got, "isArchived") != tc.archived || field(t, got, "lastActivityAt") != tc.activity {
				t.Fatalf("desired = %s", got.Value)
			}
			if (len(out.Conflicts) > 0) != tc.conflict {
				t.Fatalf("conflicts = %+v, want conflict %v", out.Conflicts, tc.conflict)
			}
			if got.Hash != out.Canonical.Items[fakeSession(1)].Hash {
				t.Fatal("desired differs from canonical")
			}
		})
	}
}

func TestSessionsPermissionCap(t *testing.T) {
	newRec := rec(t, 2, `"permissionMode":"bypassPermissions"`, 50)
	known := rec(t, 3, `"permissionMode":"acceptEdits","title":"a"`, 10)
	knownEdited := rec(t, 3, `"permissionMode":"bypassPermissions","title":"b"`, 60)
	tests := []struct {
		name    string
		acks    map[string]bool
		key     string
		want    string
		capped  bool
		noField bool
	}{
		{"new record without ack gets default", nil, fakeSession(2), "default", true, false},
		{"new record with ack keeps mode", map[string]bool{"bypassPermissions": true}, fakeSession(2), "bypassPermissions", false, false},
		{"known record without ack keeps target", nil, fakeSession(3), "acceptEdits", true, false},
		{"known record with ack takes mode", map[string]bool{"bypassPermissions": true}, fakeSession(3), "bypassPermissions", false, false},
		{"no permission field role: no cap", nil, fakeSession(2), "bypassPermissions", false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pol := sessPolicy()
			pol.PermissionAcks = tc.acks
			if tc.noField {
				delete(pol.Fields, RolePermission)
			}
			in := input(model.RowSessions,
				snap(model.RowSessions, newRec, knownEdited), snap(model.RowSessions, known),
				snap(model.RowSessions, known), snap(model.RowSessions, known),
				snap(model.RowSessions, known), pol)
			out := mustMerge(t, model.RowSessions, in)
			if got := field(t, out.Desired.Items[tc.key], "permissionMode"); got != tc.want {
				t.Fatalf("permissionMode = %v, want %v", got, tc.want)
			}
			if hasChange(out, model.ChangeCapped, tc.key) != tc.capped {
				t.Fatalf("capped change = %v, want %v (%+v)", !tc.capped, tc.capped, out.Changes)
			}
			if field(t, out.Canonical.Items[tc.key], "permissionMode") != "bypassPermissions" {
				t.Fatal("canonical lost the true mode")
			}
		})
	}
}

func TestSessionsRunStateAndAccountBound(t *testing.T) {
	newRec := rec(t, 4, `"interruptedByQuitAt":5,"bridgeSessionIds":["l"]`, 50)
	targetKnown := rec(t, 5, `"interruptedByQuitAt":9,"bridgeSessionIds":["e"],"title":"x"`, 10)
	leavingKnown := rec(t, 5, `"interruptedByQuitAt":1,"bridgeSessionIds":["l"],"title":"y"`, 70)
	pol := sessPolicy()
	in := input(model.RowSessions,
		snap(model.RowSessions, newRec, leavingKnown), snap(model.RowSessions, targetKnown),
		snap(model.RowSessions, targetKnown), snap(model.RowSessions, targetKnown),
		snap(model.RowSessions, targetKnown), pol)
	out := mustMerge(t, model.RowSessions, in)
	tests := []struct {
		name, key, field string
		want             any
	}{
		{"run state cleared on new copy", fakeSession(4), "interruptedByQuitAt", nil},
		{"account bound carried on new copy", fakeSession(4), "bridgeSessionIds", []any{"l"}},
		{"run state kept from target", fakeSession(5), "interruptedByQuitAt", float64(9)},
		{"account bound kept from target", fakeSession(5), "bridgeSessionIds", []any{"e"}},
		{"other edits carried", fakeSession(5), "title", "y"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := field(t, out.Desired.Items[tc.key], tc.field)
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Fatalf("%s = %v, want %v", tc.field, got, tc.want)
			}
		})
	}
}

func TestSessionsEnvRemap(t *testing.T) {
	envL, envE := fakeUUID(100), fakeUUID(200)
	newRec := rec(t, 6, fmt.Sprintf(`"envScopeId":%q,"title":%q`, envL, envL), 50)
	pol := sessPolicy()
	pol.EnvRemap = map[string]string{envL: envE}
	in := input(model.RowSessions, snap(model.RowSessions, newRec), snap(model.RowSessions),
		snap(model.RowSessions), snap(model.RowSessions), snap(model.RowSessions), pol)
	out := mustMerge(t, model.RowSessions, in)
	got := out.Desired.Items[fakeSession(6)]
	if field(t, got, "envScopeId") != envE {
		t.Fatalf("envScopeId not remapped: %s", got.Value)
	}
	if field(t, got, "title") != envL {
		t.Fatal("a field outside env_ref was rewritten")
	}
	if !hasChange(out, model.ChangeRemapped, fakeSession(6)) {
		t.Fatalf("no remapped change: %+v", out.Changes)
	}
	if in.Leaving.Snapshot.Items[fakeSession(6)].Hash != newRec.Hash {
		t.Fatal("input snapshot mutated")
	}
}

func TestSessionsTombstones(t *testing.T) {
	id := fakeSession(7)
	tests := []struct {
		name     string
		lCur     *model.RowSnapshot
		eAct     int64
		mode     string
		present  bool
		kind     model.ChangeKind
		tombKept bool
	}{
		{"newer tombstone deletes", withTombs(snap(model.RowSessions), model.Tombstone{Key: id, At: 200}), 100, "newer-wins", false, model.ChangeDeleted, true},
		{"older tombstone revives", withTombs(snap(model.RowSessions), model.Tombstone{Key: id, At: 200}), 300, "newer-wins", true, model.ChangeRevived, false},
		{"delete-wins deletes", withTombs(snap(model.RowSessions), model.Tombstone{Key: id, At: 1}), 300, "delete-wins", false, model.ChangeDeleted, true},
		{"missing without tombstone is restored", snap(model.RowSessions), 100, "newer-wins", true, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := rec(t, 7, `"title":"t"`, tc.eAct)
			pol := sessPolicy()
			pol.TombstoneMode = tc.mode
			in := input(model.RowSessions, tc.lCur, snap(model.RowSessions, e),
				snap(model.RowSessions, e), snap(model.RowSessions, e), snap(model.RowSessions, e), pol)
			out := mustMerge(t, model.RowSessions, in)
			_, present := out.Desired.Items[id]
			if present != tc.present {
				t.Fatalf("present = %v, want %v", present, tc.present)
			}
			if tc.kind != "" && !hasChange(out, tc.kind, id) {
				t.Fatalf("missing %s change: %+v", tc.kind, out.Changes)
			}
			if tc.kind == "" && len(out.Changes) != 0 {
				t.Fatalf("unexpected changes: %+v", out.Changes)
			}
			if _, ok := out.Desired.Tombstones[id]; ok != tc.tombKept {
				t.Fatalf("desired tombstone = %v, want %v", ok, tc.tombKept)
			}
		})
	}
}

func TestSessionsFamilyRevivesParent(t *testing.T) {
	parent := rec(t, 8, `"title":"p"`, 100)
	child := rec(t, 9, fmt.Sprintf(`"parentSessionId":%q,"title":"c"`, fakeSession(8)), 100)
	childEdited := rec(t, 9, fmt.Sprintf(`"parentSessionId":%q,"title":"c2"`, fakeSession(8)), 600)
	lCur := withTombs(snap(model.RowSessions), model.Tombstone{Key: parent.Key, At: 500}, model.Tombstone{Key: child.Key, At: 500})
	both := snap(model.RowSessions, parent, child)
	for _, tc := range []struct {
		name   string
		fields map[string][]string
		parent bool
	}{
		{"parent field revives parent", sessionFields(), true},
		{"no parent role leaves parent deleted", map[string][]string{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pol := openPolicy()
			pol.Fields = tc.fields
			in := input(model.RowSessions, lCur, both, snap(model.RowSessions, parent, childEdited), both, both, pol)
			out := mustMerge(t, model.RowSessions, in)
			if _, ok := out.Desired.Items[child.Key]; !ok {
				t.Fatal("edited child not revived")
			}
			if _, ok := out.Desired.Items[parent.Key]; ok != tc.parent {
				t.Fatalf("parent present = %v, want %v", ok, tc.parent)
			}
		})
	}
}

func TestSessionsExclude(t *testing.T) {
	byID := rec(t, 10, `"cwd":"/work/a"`, 10)
	byGlob := rec(t, 11, `"cwd":"/private/x/y"`, 10)
	shared := rec(t, 12, `"cwd":"/work/b"`, 10)
	pol := sessPolicy()
	pol.Exclude = []string{fakeSession(10), "/private/**"}
	in := input(model.RowSessions, snap(model.RowSessions, byID, byGlob, shared), snap(model.RowSessions),
		snap(model.RowSessions), snap(model.RowSessions), snap(model.RowSessions), pol)
	out := mustMerge(t, model.RowSessions, in)
	for _, k := range []string{byID.Key, byGlob.Key} {
		if _, ok := out.Canonical.Items[k]; ok {
			t.Fatalf("excluded %s reached canonical", k)
		}
		if _, ok := out.Desired.Items[k]; ok {
			t.Fatalf("excluded %s reached the target", k)
		}
		if _, ok := out.LeavingOverlay.Items[k]; !ok {
			t.Fatalf("excluded %s missing from the leaving overlay", k)
		}
	}
	if _, ok := out.Desired.Items[shared.Key]; !ok {
		t.Fatal("shared session not carried")
	}
}

// TestSessionExcluderFailsClosed checks the exclusion rules record by
// record: ids, the "/**" form, a record or manifest without a project
// path while a glob is set (excluded), and a missing record (id only).
func TestSessionExcluderFailsClosed(t *testing.T) {
	item := func(doc string) *model.Item { it := mk(t, "k", doc, 0); return &it }
	id := fakeSession(1)
	tests := []struct {
		name    string
		exclude []string
		field   string
		key     string
		it      *model.Item
		want    bool
	}{
		{"no patterns", nil, "cwd", id, item(`{"cwd":"/a"}`), false},
		{"id", []string{id}, "cwd", id, item(`{"cwd":"/a"}`), true},
		{"id of another record", []string{fakeSession(2)}, "cwd", id, item(`{}`), false},
		{"glob below prefix", []string{"/clients/**"}, "cwd", id, item(`{"cwd":"/clients/acme/x"}`), true},
		{"glob prefix itself", []string{"/clients/**"}, "cwd", id, item(`{"cwd":"/clients"}`), true},
		{"glob elsewhere", []string{"/clients/**"}, "cwd", id, item(`{"cwd":"/work/a"}`), false},
		{"glob, field missing", []string{"/clients/**"}, "cwd", id, item(`{"title":"x"}`), true},
		{"glob, field not a string", []string{"/clients/**"}, "cwd", id, item(`{"cwd":3}`), true},
		{"glob, no field in the manifest", []string{"/clients/**"}, "", id, item(`{"cwd":"/work/a"}`), true},
		{"glob, missing record", []string{"/clients/**"}, "cwd", id, nil, false},
		{"id, missing record", []string{id}, "cwd", id, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sessionExcluder(tc.exclude, tc.field)(tc.key, tc.it); got != tc.want {
				t.Fatalf("excluded = %v, want %v", got, tc.want)
			}
		})
	}
}
