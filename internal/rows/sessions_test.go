package rows

import (
	"fmt"
	"testing"

	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/testkit"
)

// sessionFields is a manifest-like field list used only by tests.
func sessionFields() map[string][]string {
	return map[string][]string{
		RolePerField:     {"title", "isArchived", "envScopeId", "startedFromEnvironmentId", "model"},
		RoleMaxTime:      {"lastActivityAt"},
		RoleRunState:     {"priorErrorMark"},
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
	newRec := rec(t, 4, `"priorErrorMark":5,"bridgeSessionIds":["l"]`, 50)
	targetKnown := rec(t, 5, `"priorErrorMark":9,"bridgeSessionIds":["e"],"title":"x"`, 10)
	leavingKnown := rec(t, 5, `"priorErrorMark":1,"bridgeSessionIds":["l"],"title":"y"`, 70)
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
		{"run state cleared on new copy", fakeSession(4), "priorErrorMark", nil},
		{"account bound carried on new copy", fakeSession(4), "bridgeSessionIds", []any{"l"}},
		{"run state kept from target", fakeSession(5), "priorErrorMark", float64(9)},
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

// manifestFields returns the sessions row's field lists from the embedded
// manifest, so these tests exercise the observed field names.
func manifestFields(t *testing.T) map[string][]string {
	t.Helper()
	s, ok := testkit.Manifest(t).Row(model.RowSessions)
	if !ok {
		t.Fatal("no sessions row")
	}
	return s.Fields
}

func manifestPolicy(t *testing.T) Policy {
	p := openPolicy()
	p.Fields = manifestFields(t)
	return p
}

// TestSessionsSecurityGrantsNeverCarried: the per-session security grants
// never cross accounts. A copy new to the target carries none of them; a
// record the target knows keeps the target's own grants, including their
// absence. Account-bound fields keep the target's values on known records.
func TestSessionsSecurityGrantsNeverCarried(t *testing.T) {
	grants := `"chromePermissionMode":"skip_all_permission_checks","cuAllowedApps":["app.one"],` +
		`"cuGrantFlags":{"clipboardRead":true,"clipboardWrite":true,"systemKeyCombos":true},` +
		`"sessionPermissionUpdates":[{"type":"addDirectories","destination":"session","directories":["/tmp/x"]}],` +
		`"alwaysAllowedReasons":["r1"],"remoteControlUserEnabled":true,"artifactsMcpVouchedCalls":["c1"]`
	newRec := rec(t, 20, `"title":"n",`+grants+`,"publishedArtifacts":[{"url":"u"}]`, 50)
	targetKnown := rec(t, 21, `"title":"x","chromePermissionMode":"ask","cuAllowedApps":[],"publishedArtifacts":[{"url":"mine"}],"remoteControlUserEnabled":false`, 10)
	leavingKnown := rec(t, 21, `"title":"y",`+grants+`,"publishedArtifacts":[{"url":"theirs"}]`, 70)
	in := input(model.RowSessions,
		snap(model.RowSessions, newRec, leavingKnown), snap(model.RowSessions, targetKnown),
		snap(model.RowSessions, targetKnown), snap(model.RowSessions, targetKnown),
		snap(model.RowSessions, targetKnown), manifestPolicy(t))
	out := mustMerge(t, model.RowSessions, in)
	grantFields := manifestFields(t)[RoleGrant]
	// The remote-control opt-in and vouched calls are opt-ins of one
	// account, so they are grants: a copy new to another account has
	// neither.
	if len(grantFields) != 7 {
		t.Fatalf("grant fields %v", grantFields)
	}
	n := out.Desired.Items[fakeSession(20)]
	for _, f := range grantFields {
		if v := field(t, n, f); v != nil {
			t.Errorf("new copy carries %s = %v", f, v)
		}
	}
	if field(t, n, "title") != "n" || fmt.Sprint(field(t, n, "publishedArtifacts")) == "" {
		t.Errorf("new copy lost other fields: %s", n.Value)
	}
	k := out.Desired.Items[fakeSession(21)]
	want := map[string]any{"chromePermissionMode": "ask", "cuAllowedApps": "[]", "cuGrantFlags": nil,
		"sessionPermissionUpdates": nil, "alwaysAllowedReasons": nil, "title": "y",
		"publishedArtifacts": "[map[url:mine]]", "remoteControlUserEnabled": false}
	for f, w := range want {
		got := field(t, k, f)
		if fmt.Sprint(got) != fmt.Sprint(w) && !(w == nil && got == nil) {
			t.Errorf("known record %s = %v, want %v", f, got, w)
		}
	}
	if field(t, out.Canonical.Items[fakeSession(21)], "chromePermissionMode") != "skip_all_permission_checks" {
		t.Error("canonical must keep the leaving side's record unchanged")
	}
}

// TestSessionsParentRoles: every observed parent field (nested ones
// included) keeps a family whole, a parent that no input holds leaves the
// child as it is, an excluded parent stays private, and rewind edges do
// not count as parents.
func TestSessionsParentRoles(t *testing.T) {
	parentKey := fakeSession(30)
	parent := rec(t, 30, `"title":"p","cwd":"/work/p"`, 100)
	for _, tc := range []struct {
		name, link string
		revived    bool
	}{
		{"forkedFromSessionId", fmt.Sprintf(`"forkedFromSessionId":%q`, parentKey), true},
		{"spawnedFrom.sessionId", fmt.Sprintf(`"spawnedFrom":{"sessionId":%q,"taskId":"t"}`, parentKey), true},
		{"startChain.firstSessionId", fmt.Sprintf(`"startChain":{"depth":1,"firstSessionId":%q}`, parentKey), true},
		{"rewind edge is not a parent", fmt.Sprintf(`"rewindEdges":[{"id":"e","parent":%q,"child":"c"}]`, parentKey), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			child := rec(t, 31, tc.link+`,"title":"c","cwd":"/work/c"`, 100)
			childEdited := rec(t, 31, tc.link+`,"title":"c2","cwd":"/work/c"`, 600)
			lCur := withTombs(snap(model.RowSessions), model.Tombstone{Key: parentKey, At: 500}, model.Tombstone{Key: child.Key, At: 500})
			both := snap(model.RowSessions, parent, child)
			in := input(model.RowSessions, lCur, both, snap(model.RowSessions, parent, childEdited), both, both, manifestPolicy(t))
			out := mustMerge(t, model.RowSessions, in)
			if _, ok := out.Desired.Items[child.Key]; !ok {
				t.Fatal("edited child not revived")
			}
			if _, ok := out.Desired.Items[parentKey]; ok != tc.revived {
				t.Fatalf("parent present = %v, want %v", ok, tc.revived)
			}
		})
	}
	t.Run("parent absent everywhere", func(t *testing.T) {
		child := rec(t, 32, fmt.Sprintf(`"forkedFromSessionId":%q,"title":"c"`, fakeSession(39)), 100)
		in := input(model.RowSessions, snap(model.RowSessions, child), snap(model.RowSessions),
			snap(model.RowSessions), snap(model.RowSessions), snap(model.RowSessions), manifestPolicy(t))
		out := mustMerge(t, model.RowSessions, in)
		got, ok := out.Desired.Items[child.Key]
		if !ok || field(t, got, "forkedFromSessionId") != fakeSession(39) {
			t.Fatalf("child not carried as is: %v", out.Desired.Keys())
		}
		if len(out.Desired.Items) != 1 {
			t.Fatalf("desired %v", out.Desired.Keys())
		}
	})
	t.Run("parent absent on the target", func(t *testing.T) {
		child := rec(t, 33, fmt.Sprintf(`"spawnedFrom":{"sessionId":%q}`, parentKey), 100)
		in := input(model.RowSessions, snap(model.RowSessions, parent, child), snap(model.RowSessions),
			snap(model.RowSessions), snap(model.RowSessions), snap(model.RowSessions), manifestPolicy(t))
		out := mustMerge(t, model.RowSessions, in)
		for _, k := range []string{child.Key, parentKey} {
			if !hasChange(out, model.ChangeAdded, k) {
				t.Errorf("%s not added: %+v", k, out.Changes)
			}
		}
	})
	t.Run("excluded parent stays private", func(t *testing.T) {
		child := rec(t, 34, fmt.Sprintf(`"forkedFromSessionId":%q,"cwd":"/work/c"`, parentKey), 100)
		pol := manifestPolicy(t)
		pol.Exclude = []string{"/work/p/**"}
		in := input(model.RowSessions, snap(model.RowSessions, parent, child), snap(model.RowSessions),
			snap(model.RowSessions), snap(model.RowSessions), snap(model.RowSessions), pol)
		out := mustMerge(t, model.RowSessions, in)
		if _, ok := out.Desired.Items[child.Key]; !ok {
			t.Fatal("child not carried")
		}
		if _, ok := out.Canonical.Items[parentKey]; ok {
			t.Fatal("an excluded parent reached canonical")
		}
		if _, ok := out.Desired.Items[parentKey]; ok {
			t.Fatal("an excluded parent reached the target")
		}
		if _, ok := out.LeavingOverlay.Items[parentKey]; !ok {
			t.Fatal("the excluded parent left its own overlay")
		}
	})
}

// TestSessionsWorktreeGroup: a session's worktree fields move as one unit
// in a field merge, so a record never pairs one side's worktree name with
// the other side's path or branch.
func TestSessionsWorktreeGroup(t *testing.T) {
	wt := func(name string) string {
		return fmt.Sprintf(`"worktreeName":%q,"worktreePath":"/tmp/wt/%s","branch":"wt/%s","sourceBranch":"main"`, name, name, name)
	}
	base := rec(t, 40, `"title":"old",`+wt("a"), 100)
	renamed := rec(t, 40, `"title":"new",`+wt("a"), 150)
	moved := rec(t, 40, `"title":"old",`+wt("b"), 120)
	movedC := rec(t, 40, `"title":"new",`+wt("c"), 160)
	for _, tc := range []struct {
		name     string
		leaving  model.Item
		entering model.Item
		title    string
		wt       string
		conflict bool
	}{
		{"only one side moved the worktree", renamed, moved, "new", "b", false},
		{"both sides moved it", movedC, moved, "new", "c", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := input(model.RowSessions,
				snap(model.RowSessions, tc.leaving), snap(model.RowSessions, base),
				snap(model.RowSessions, tc.entering), snap(model.RowSessions, base),
				snap(model.RowSessions, base), manifestPolicy(t))
			out := mustMerge(t, model.RowSessions, in)
			got := out.Desired.Items[fakeSession(40)]
			if field(t, got, "title") != tc.title {
				t.Fatalf("title %v", field(t, got, "title"))
			}
			for f, want := range map[string]string{"worktreeName": tc.wt, "worktreePath": "/tmp/wt/" + tc.wt, "branch": "wt/" + tc.wt} {
				if field(t, got, f) != want {
					t.Errorf("%s = %v, want %s", f, field(t, got, f), want)
				}
			}
			if (len(out.Conflicts) > 0) != tc.conflict {
				t.Errorf("conflicts %+v, want %v", out.Conflicts, tc.conflict)
			}
		})
	}
}

func resumePolicy() Policy {
	p := sessPolicy()
	p.Fields["resume_mark"] = []string{"interruptedByQuitAt", "cutTurn"}
	p.Fields[RoleRunState] = append(p.Fields[RoleRunState], "isStarred")
	return p
}

// Spike S15: the app resumes a turn by itself when a session carries a
// quit-resume mark, so a mark never crosses accounts and the target's own
// mark goes once the other account moved the session on.
func TestSessionsResumeMarks(t *testing.T) {
	marks := `"interruptedByQuitAt":1000,"cutTurn":{"at":1},"isStarred":true`
	t.Run("leaving side moved on", func(t *testing.T) {
		newRec := rec(t, 4, marks, 50)
		targetOwn := rec(t, 5, `"title":"x",`+marks, 10)
		leavingNewer := rec(t, 5, `"title":"y","interruptedByQuitAt":2000`, 70)
		in := input(model.RowSessions,
			snap(model.RowSessions, newRec, leavingNewer), snap(model.RowSessions, targetOwn),
			snap(model.RowSessions, targetOwn), snap(model.RowSessions, targetOwn),
			snap(model.RowSessions, targetOwn), resumePolicy())
		out := mustMerge(t, model.RowSessions, in)
		for _, f := range []string{"interruptedByQuitAt", "cutTurn", "isStarred"} {
			if got := field(t, out.Desired.Items[fakeSession(4)], f); got != nil {
				t.Errorf("new copy carries %s = %v", f, got)
			}
		}
		known := out.Desired.Items[fakeSession(5)]
		if field(t, known, "title") != "y" {
			t.Fatalf("leaving edit not carried: %s", known.Value)
		}
		for _, f := range []string{"interruptedByQuitAt", "cutTurn"} {
			if got := field(t, known, f); got != nil {
				t.Errorf("resume mark %s = %v survived a newer record", f, got)
			}
		}
		if field(t, known, "isStarred") != true {
			t.Errorf("isStarred not kept from the target: %s", known.Value)
		}
	})
	t.Run("target record is the newest", func(t *testing.T) {
		old := rec(t, 5, `"title":"x"`, 10)
		targetNewer := rec(t, 5, `"title":"z",`+marks, 80)
		in := input(model.RowSessions,
			snap(model.RowSessions, old), snap(model.RowSessions, old),
			snap(model.RowSessions, targetNewer), snap(model.RowSessions, old),
			snap(model.RowSessions, old), resumePolicy())
		out := mustMerge(t, model.RowSessions, in)
		known := out.Desired.Items[fakeSession(5)]
		if field(t, known, "interruptedByQuitAt") != float64(1000) || field(t, known, "cutTurn") == nil {
			t.Fatalf("target's own current mark dropped: %s", known.Value)
		}
	})
}

// Fields each replica keeps for itself (here isStarred) never make a
// conflict: neither side's value is lost.
func TestSessionsKeptFieldsNoConflict(t *testing.T) {
	t.Run("canonical differs only in a kept field", func(t *testing.T) {
		canon := rec(t, 5, `"title":"x","isStarred":true`, 10)
		written := rec(t, 5, `"title":"x"`, 10)
		edited := rec(t, 5, `"title":"y"`, 20)
		in := input(model.RowSessions,
			snap(model.RowSessions, edited), snap(model.RowSessions, written),
			snap(model.RowSessions, canon), snap(model.RowSessions, canon),
			snap(model.RowSessions, canon), resumePolicy())
		out := mustMerge(t, model.RowSessions, in)
		if len(out.Conflicts) != 0 {
			t.Fatalf("conflicts %+v", out.Conflicts)
		}
		if field(t, out.Desired.Items[fakeSession(5)], "title") != "y" {
			t.Fatal("edit not carried")
		}
	})
	t.Run("both sides changed a kept field", func(t *testing.T) {
		base := rec(t, 5, `"title":"x"`, 10)
		leaving := rec(t, 5, `"title":"x","isStarred":true`, 15)
		entering := rec(t, 5, `"title":"z","isStarred":false`, 30)
		in := input(model.RowSessions,
			snap(model.RowSessions, leaving), snap(model.RowSessions, base),
			snap(model.RowSessions, entering), snap(model.RowSessions, base),
			snap(model.RowSessions, base), resumePolicy())
		out := mustMerge(t, model.RowSessions, in)
		if len(out.Conflicts) != 0 {
			t.Fatalf("conflicts %+v", out.Conflicts)
		}
	})
	t.Run("a real lost edit is still a conflict", func(t *testing.T) {
		base := rec(t, 5, `"title":"x"`, 10)
		leaving := rec(t, 5, `"title":"x","note":"a"`, 15)
		entering := rec(t, 5, `"title":"z","note":"b"`, 30)
		in := input(model.RowSessions,
			snap(model.RowSessions, leaving), snap(model.RowSessions, base),
			snap(model.RowSessions, entering), snap(model.RowSessions, base),
			snap(model.RowSessions, base), resumePolicy())
		out := mustMerge(t, model.RowSessions, in)
		if len(out.Conflicts) == 0 {
			t.Fatal("a field both sides changed outside the merged lists made no conflict")
		}
	})
}
