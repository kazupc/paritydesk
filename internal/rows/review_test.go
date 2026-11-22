package rows

import (
	"fmt"
	"testing"

	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/testkit"
)

func rowPolicy(t *testing.T, row model.RowID) Policy {
	t.Helper()
	s, ok := testkit.Manifest(t).Row(row)
	if !ok {
		t.Fatalf("no %s row", row)
	}
	p := openPolicy()
	p.Fields = s.Fields
	return p
}

// TestTasksHonourExclude: a task whose project path matches an exclude
// glob, or whose originating session is an excluded session id, never
// leaves its profile; other tasks are carried.
func TestTasksHonourExclude(t *testing.T) {
	row := model.RowTasks
	pol := rowPolicy(t, row)
	pol.Exclude = []string{"/Users/test/clients/**", fakeSession(7)}
	byPath := mk(t, "backlog/a", `{"id":"a","title":"client work","cwd":"/Users/test/clients/x","filedAt":1}`, 1)
	bySession := mk(t, "backlog/b", fmt.Sprintf(`{"id":"b","title":"from a private session","originSessionId":%q,"filedAt":1}`, fakeSession(7)), 1)
	open := mk(t, "backlog/c", `{"id":"c","title":"home","cwd":"/Users/test/home","filedAt":1}`, 1)
	noPath := mk(t, "scheduled/s", `{"id":"s","cron":"0 9 * * *"}`, 0)
	empty := snap(row)
	out := mustMerge(t, row, input(row, snap(row, byPath, bySession, open, noPath), empty, empty, empty, empty, pol))
	for _, k := range []string{byPath.Key, bySession.Key} {
		if _, ok := out.Desired.Items[k]; ok {
			t.Errorf("excluded task %s reached the entering profile", k)
		}
		if _, ok := out.Canonical.Items[k]; ok {
			t.Errorf("excluded task %s reached canonical", k)
		}
	}
	for _, k := range []string{open.Key, noPath.Key} {
		if _, ok := out.Desired.Items[k]; !ok {
			t.Errorf("task %s not carried", k)
		}
	}
}

// TestWorktreesKeepTargetDirectoryAndGrants: a same-name entry that
// registers another directory never replaces the target's entry, and an
// entry new to the target carries none of the source's trust grants or
// leases.
func TestWorktreesKeepTargetDirectoryAndGrants(t *testing.T) {
	row := model.RowWorktrees
	pol := rowPolicy(t, row)
	theirs := mk(t, "feature-x", `{"name":"feature-x","path":"/Users/test/one/.wt/feature-x","autoTrusted":true}`, 0)
	mine := mk(t, "feature-x", `{"name":"feature-x","path":"/Users/test/two/.wt/feature-x","autoTrusted":false}`, 0)
	fresh := mk(t, "fresh", `{"name":"fresh","path":"/Users/test/one/.wt/fresh","autoTrusted":true,`+
		`"creationGrant":{"by":"a"},"leasedBy":"run-1","leaseCleared":false,"branch":"wt/fresh"}`, 0)
	in := input(row, snap(row, theirs, fresh), snap(row), snap(row, mine), snap(row), snap(row), pol)
	in.Leaving.BaseValid, in.Entering.BaseValid = false, false
	out := mustMerge(t, row, in)
	if got := field(t, out.Desired.Items["feature-x"], "path"); got != "/Users/test/two/.wt/feature-x" {
		t.Fatalf("the target's registration was replaced: path %v", got)
	}
	if !hasChange(out, model.ChangeSkipped, "feature-x") {
		t.Errorf("the kept entry is not reported: %+v", out.Changes)
	}
	f := out.Desired.Items["fresh"]
	for _, fld := range []string{"autoTrusted", "creationGrant", "leasedBy", "leaseCleared"} {
		if v := field(t, f, fld); v != nil {
			t.Errorf("new entry carries %s = %v", fld, v)
		}
	}
	if field(t, f, "branch") != "wt/fresh" || field(t, f, "path") != "/Users/test/one/.wt/fresh" {
		t.Errorf("new entry lost other fields: %s", f.Value)
	}
}

// TestSessionsWorktreeGroupMovesCwd: one side moved the session into a
// worktree (cwd and worktree fields), the other side renamed it later. The
// merged record takes the worktree and its cwd together.
func TestSessionsWorktreeGroupMovesCwd(t *testing.T) {
	row := model.RowSessions
	base := rec(t, 40, `"title":"t","cwd":"/Users/test/repo"`, 100)
	moved := rec(t, 40, `"title":"t","cwd":"/Users/test/repo/.wt/a","worktreeName":"a","worktreePath":"/Users/test/repo/.wt/a","branch":"b"`, 200)
	renamed := rec(t, 40, `"title":"renamed","cwd":"/Users/test/repo"`, 300)
	b := snap(row, base)
	out := mustMerge(t, row, input(row, snap(row, renamed), b, snap(row, moved), b, b, manifestPolicy(t)))
	d := out.Desired.Items[base.Key]
	if field(t, d, "cwd") != "/Users/test/repo/.wt/a" || field(t, d, "worktreePath") != "/Users/test/repo/.wt/a" {
		t.Fatalf("worktree and cwd split: %s", d.Value)
	}
	if field(t, d, "title") != "renamed" {
		t.Fatalf("the newer rename was lost: %s", d.Value)
	}
}

// TestSessionsModeUsedByTargetNotCapped: a session new to the target in a
// mode the target's own records already use keeps it; a mode the target
// neither uses nor acknowledges is capped to "default".
func TestSessionsModeUsedByTargetNotCapped(t *testing.T) {
	row := model.RowSessions
	own := rec(t, 50, `"title":"mine","permissionMode":"auto"`, 10)
	autoNew := rec(t, 51, `"title":"a","permissionMode":"auto"`, 20)
	bypassNew := rec(t, 52, `"title":"b","permissionMode":"bypassPermissions"`, 20)
	e := snap(row, own)
	out := mustMerge(t, row, input(row, snap(row, own, autoNew, bypassNew), e, e, e, e, manifestPolicy(t)))
	if got := field(t, out.Desired.Items[autoNew.Key], "permissionMode"); got != "auto" {
		t.Errorf("auto mode used by the target was capped to %v", got)
	}
	if got := field(t, out.Desired.Items[bypassNew.Key], "permissionMode"); got != "default" {
		t.Errorf("an unacknowledged mode was carried: %v", got)
	}
}

// TestSessionsNewerParentDeletionSticks: the user deleted the first
// session of a chain after its last activity, and a later session of the
// chain is still there. The deletion stands; the parent is revived only
// when the child itself is revived, and then with its own note.
func TestSessionsNewerParentDeletionSticks(t *testing.T) {
	row := model.RowSessions
	root := rec(t, 60, `"title":"root"`, 100)
	link := fmt.Sprintf(`"startChain":{"depth":1,"firstSessionId":%q}`, root.Key)
	child := rec(t, 61, link+`,"title":"child"`, 100)
	both := snap(row, root, child)
	lCur := withTombs(snap(row, child), model.Tombstone{Key: root.Key, At: 500})
	out := mustMerge(t, row, input(row, lCur, both, both, both, both, manifestPolicy(t)))
	if _, ok := out.Desired.Items[root.Key]; ok {
		t.Fatalf("a newer deletion of the chain root was undone: %+v", out.Changes)
	}
	if hasChange(out, model.ChangeRevived, root.Key) {
		t.Fatal("reported as revived")
	}

	// The child is revived by a newer edit: its parent comes back too.
	childEdited := rec(t, 61, link+`,"title":"child2"`, 600)
	lCur = withTombs(snap(row), model.Tombstone{Key: root.Key, At: 500}, model.Tombstone{Key: child.Key, At: 500})
	out = mustMerge(t, row, input(row, lCur, both, snap(row, root, childEdited), both, both, manifestPolicy(t)))
	if _, ok := out.Desired.Items[root.Key]; !ok {
		t.Fatal("parent of a revived child not revived")
	}
	for _, c := range out.Changes {
		if c.Key == root.Key && c.Kind == model.ChangeRevived && c.Note != noteParentKept {
			t.Errorf("parent revival note %q", c.Note)
		}
	}
}
