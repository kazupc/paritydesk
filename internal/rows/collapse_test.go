package rows

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/kazupc/paritydesk/internal/model"
)

const (
	colGroups   = "collapsedGroups"
	colFamilies = "collapsedFamilies"
)

// collapseFields are the manifest field lists of sidebar-collapse.
func collapseFields() map[string][]string {
	return map[string][]string{
		RoleSets:               {colGroups, colFamilies},
		RoleEnvRefPrefix:       {"project-preset::"},
		RoleProjectPrefix:      {"project-"},
		RoleFamilyPrefix:       {"code:", "open:code:"},
		RoleProjectFixed:       {"project-default::", "project-__scratch__", "project-__no_project__"},
		RoleProjectFixedPrefix: {"project-preset::", "project-kind::"},
	}
}

func collapsePolicy() Policy {
	p := openPolicy()
	p.Fields = collapseFields()
	p.Limits = map[string]int{limitMaxMembers: 4096, limitMaxMemberBytes: 2048}
	return p
}

// setIt builds a set item (members as given, so unsorted input is
// possible); stamp is the app time.
func setIt(t fatalHelper, key string, appTime int64, members ...string) model.Item {
	t.Helper()
	if members == nil {
		members = []string{}
	}
	b, _ := json.Marshal(members)
	return mk(t, key, string(b), appTime)
}

func groups(t fatalHelper, members ...string) *model.RowSnapshot {
	t.Helper()
	return snap(model.RowSidebarCollapse, setIt(t, colGroups, 0, members...))
}

// membersOf returns the members of item k in s (nil when absent).
func membersOf(t fatalHelper, s *model.RowSnapshot, k string) []string {
	t.Helper()
	if s == nil {
		return nil
	}
	it, ok := s.Items[k]
	if !ok {
		return nil
	}
	l, ok := stringList(it.Value)
	if !ok {
		t.Fatalf("item %s is not a list", k)
	}
	return l
}

func mergeCollapse(t *testing.T, in Input) Output {
	t.Helper()
	in.Row = model.RowSidebarCollapse
	out := mustMerge(t, model.RowSidebarCollapse, in)
	for _, c := range out.Changes {
		if c.Kind == model.ChangeDeleted {
			t.Fatalf("deletion: %+v", c)
		}
	}
	if out.NeedsConfirmation {
		t.Fatal("NeedsConfirmation set")
	}
	return out
}

func TestCollapseOneSideChanged(t *testing.T) {
	base := groups(t, "a")
	out := mergeCollapse(t, input(model.RowSidebarCollapse, groups(t, "a", "b"), base, groups(t, "a"), base, base, collapsePolicy()))
	if got := membersOf(t, out.Desired, colGroups); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("desired %v", got)
	}
	out = mergeCollapse(t, input(model.RowSidebarCollapse, groups(t, "a"), base, groups(t), base, base, collapsePolicy()))
	if got := membersOf(t, out.Desired, colGroups); len(got) != 0 {
		t.Fatalf("entering's removal lost: %v", got)
	}
}

func TestCollapseBothChangedValidBases(t *testing.T) {
	base := groups(t, "a", "r")
	// leaving removed r and added b; entering added c.
	out := mergeCollapse(t, input(model.RowSidebarCollapse, groups(t, "a", "b"), base, groups(t, "a", "r", "c"), base, base, collapsePolicy()))
	if got := membersOf(t, out.Desired, colGroups); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("desired %v", got)
	}
	if len(out.Conflicts) != 0 {
		t.Fatalf("conflicts %+v", out.Conflicts)
	}
	it := out.Desired.Items[colGroups]
	if it.Raw != nil || it.Stamp.Epoch != 7 {
		t.Fatalf("merged item raw %q stamp %+v: want the winner's stamp and no raw", it.Raw, it.Stamp)
	}
}

func TestCollapseInvalidBaseUnion(t *testing.T) {
	in := input(model.RowSidebarCollapse, groups(t, "x"), nil, groups(t, "y"), groups(t, "y"), nil, collapsePolicy())
	in.Leaving.BaseValid = false
	out := mergeCollapse(t, in)
	if got := membersOf(t, out.Desired, colGroups); !slices.Equal(got, []string{"x", "y"}) {
		t.Fatalf("desired %v", got)
	}
	if len(out.Conflicts) != 0 {
		t.Fatalf("conflicts %+v", out.Conflicts)
	}
	// Entering invalid, leaving unchanged against its valid base: still the
	// union (the engine decides the key for one side only).
	in = input(model.RowSidebarCollapse, groups(t, "x"), groups(t, "x"), groups(t, "y"), nil, groups(t, "x"), collapsePolicy())
	in.Entering.BaseValid = false
	out = mergeCollapse(t, in)
	if got := membersOf(t, out.Desired, colGroups); !slices.Equal(got, []string{"x", "y"}) {
		t.Fatalf("desired %v", got)
	}
}

func TestCollapseMissingItemRestored(t *testing.T) {
	base := snap(model.RowSidebarCollapse, setIt(t, colGroups, 0, "a"), setIt(t, colFamilies, 0, "code:x"))
	cur := groups(t, "a") // old store without collapsedFamilies
	out := mergeCollapse(t, input(model.RowSidebarCollapse, base.Clone(), base, cur, base, base, collapsePolicy()))
	if got := membersOf(t, out.Desired, colFamilies); !slices.Equal(got, []string{"code:x"}) {
		t.Fatalf("families %v", got)
	}
	if !hasChange(out, model.ChangeRestored, colFamilies) && !hasChange(out, model.ChangeAdded, colFamilies) {
		t.Fatalf("changes %+v", out.Changes)
	}
}

func TestCollapseStoreMissingKeepsTarget(t *testing.T) {
	pol := collapsePolicy()
	pol.EnteringStoreMissing = true
	empty := snap(model.RowSidebarCollapse)
	l := snap(model.RowSidebarCollapse, setIt(t, colGroups, 0, "a"), setIt(t, colFamilies, 0, "code:x"))
	out := mergeCollapse(t, input(model.RowSidebarCollapse, l, nil, empty, nil, nil, pol))
	if len(out.Desired.Items) != 0 {
		t.Fatalf("desired %v", out.Desired.Items)
	}
	n := 0
	for _, c := range out.Changes {
		if c.Kind != model.ChangeSkipped || c.Note != noteStoreMissing {
			t.Fatalf("change %+v", c)
		}
		n++
	}
	if n != 2 {
		t.Fatalf("skipped %d, want one per differing key", n)
	}
}

func TestCollapseLimitsKeepTarget(t *testing.T) {
	pol := collapsePolicy()
	pol.Limits = map[string]int{limitMaxMembers: 2, limitMaxMemberBytes: 8}
	base := groups(t, "a")
	for name, l := range map[string]*model.RowSnapshot{
		"members": groups(t, "a", "b", "c"),
		"bytes":   groups(t, "a", "123456789"),
	} {
		t.Run(name, func(t *testing.T) {
			out := mergeCollapse(t, input(model.RowSidebarCollapse, l, base, base.Clone(), base, base, pol))
			if got := membersOf(t, out.Desired, colGroups); !slices.Equal(got, []string{"a"}) {
				t.Fatalf("desired %v, want the target's set", got)
			}
			if !hasChange(out, model.ChangeSkipped, colGroups) || out.Changes[len(out.Changes)-1].Note != noteTooLarge {
				t.Fatalf("changes %+v", out.Changes)
			}
		})
	}
}

func TestCollapseRemap(t *testing.T) {
	pol := collapsePolicy()
	pol.EnvRemap = map[string]string{"env-b": "env-c"}
	l := groups(t, "project-preset::env-b", "project-/tmp/alpha", "recents")
	out := mergeCollapse(t, input(model.RowSidebarCollapse, l, nil, groups(t), nil, nil, pol))
	want := []string{"project-/tmp/alpha", "project-preset::env-c", "recents"}
	if got := membersOf(t, out.Desired, colGroups); !slices.Equal(got, want) {
		t.Fatalf("desired %v", got)
	}
	if got := membersOf(t, out.Canonical, colGroups); !slices.Equal(got, want) {
		t.Fatalf("canonical %v (want entering id space)", got)
	}
	// A leaving overlay item moves back into the leaving id space.
	pol.LeavingExports = false
	out = mergeCollapse(t, input(model.RowSidebarCollapse, l, groups(t), groups(t), groups(t), groups(t), pol))
	if got := membersOf(t, out.LeavingOverlay, colGroups); !slices.Contains(got, "project-preset::env-b") || slices.Contains(got, "project-preset::env-c") {
		t.Fatalf("leaving overlay %v", got)
	}
}

func TestCollapseExcludeGlob(t *testing.T) {
	pol := collapsePolicy()
	pol.Exclude = []string{"/tmp/client/**"}
	l := groups(t, "project-/tmp/client/x", "project-example/repo", "project-/tmp/alpha", "project-preset::env-a",
		"project-default::", "recents", "project-folder::local::/tmp/client/z")
	e := groups(t, "project-/tmp/client/y")
	out := mergeCollapse(t, input(model.RowSidebarCollapse, l, nil, e, nil, nil, pol))
	desired, canon := membersOf(t, out.Desired, colGroups), membersOf(t, out.Canonical, colGroups)
	for _, private := range []string{"project-/tmp/client/x", "project-example/repo", "project-folder::local::/tmp/client/z"} {
		if slices.Contains(desired, private) || slices.Contains(canon, private) {
			t.Errorf("%s left its profile: desired %v canonical %v", private, desired, canon)
		}
	}
	if !slices.Contains(desired, "project-/tmp/client/y") || slices.Contains(canon, "project-/tmp/client/y") {
		t.Errorf("entering's own private member: desired %v canonical %v", desired, canon)
	}
	for _, shared := range []string{"project-/tmp/alpha", "project-preset::env-a", "project-default::", "recents"} {
		if !slices.Contains(desired, shared) {
			t.Errorf("%s not carried: %v", shared, desired)
		}
	}
}

func TestCollapseExcludeSessionID(t *testing.T) {
	pol := collapsePolicy()
	pol.Exclude = []string{fakeSession(1)}
	l := snap(model.RowSidebarCollapse,
		setIt(t, colFamilies, 0, "code:"+fakeSession(1), "open:code:"+fakeSession(1), "code:"+fakeSession(2)),
		setIt(t, colGroups, 0, "project-/tmp/alpha", "project-example/repo"))
	out := mergeCollapse(t, input(model.RowSidebarCollapse, l, nil, snap(model.RowSidebarCollapse), nil, nil, pol))
	if got := membersOf(t, out.Desired, colFamilies); !slices.Equal(got, []string{"code:" + fakeSession(2)}) {
		t.Fatalf("families %v", got)
	}
	if got := membersOf(t, out.Desired, colGroups); !slices.Equal(got, []string{"project-/tmp/alpha", "project-example/repo"}) {
		t.Fatalf("groups %v", got)
	}
	// Any glob set: every family member stays home.
	pol.Exclude = []string{"/nowhere/**"}
	out = mergeCollapse(t, input(model.RowSidebarCollapse, l, nil, snap(model.RowSidebarCollapse), nil, nil, pol))
	if got := membersOf(t, out.Desired, colFamilies); len(got) != 0 {
		t.Fatalf("families with a glob set: %v", got)
	}
}

func TestMemberExcluder(t *testing.T) {
	f := collapseFields()
	none := memberExcluder(nil, f)
	glob := memberExcluder([]string{"/tmp/client/**"}, f)
	id := memberExcluder([]string{fakeSession(5)}, f)
	tests := []struct {
		member         string
		none, glob, id bool
	}{
		{"recents", false, false, false},
		{"custom-" + fakeUUID(3), false, false, false},
		{"project-/tmp/client/a", false, true, false},
		{"project-/tmp/other", false, false, false},
		{"project-owner/name", false, true, false},
		{"project-env-1:name", false, true, false},
		{"project-folder::ssh:host::/tmp/client/b", false, true, false},
		{"project-folder::ssh:host::/srv/x", false, false, false},
		{"project-default::", false, false, false},
		{"project-__scratch__", false, false, false},
		{"project-preset::env-a", false, false, false},
		{"project-kind::ssh:host", false, false, false},
		{"code:" + fakeSession(5), false, true, true},
		{"open:code:" + fakeSession(5), false, true, true},
		{"code:" + fakeSession(6), false, true, false},
	}
	for _, tc := range tests {
		if none(tc.member) != tc.none || glob(tc.member) != tc.glob || id(tc.member) != tc.id {
			t.Errorf("%s: none %v glob %v id %v", tc.member, none(tc.member), glob(tc.member), id(tc.member))
		}
	}
}

// TestCollapseExcludeAddedLater: a member carried before an exclude glob
// was added is dropped from canonical but stays in the folder that holds
// it.
func TestCollapseExcludeAddedLater(t *testing.T) {
	pol := collapsePolicy()
	pol.Exclude = []string{"/tmp/client/**"}
	shared := groups(t, "project-/tmp/client/x", "recents")
	out := mergeCollapse(t, input(model.RowSidebarCollapse, shared, shared, shared.Clone(), shared, shared, pol))
	if got := membersOf(t, out.Canonical, colGroups); !slices.Equal(got, []string{"recents"}) {
		t.Fatalf("canonical %v", got)
	}
	if got := membersOf(t, out.Desired, colGroups); !slices.Equal(got, []string{"project-/tmp/client/x", "recents"}) {
		t.Fatalf("desired %v", got)
	}
}

// The entering folder's own private members survive every path that keeps
// the target: a profile that does not import the row, a merged set over the
// limits, and a plain merge whose shared members are unchanged. Desired then
// equals the entering read exactly, so Ops writes nothing, and the plan
// lists no change that is not written.
func TestCollapsePrivateKeptWhenTargetKept(t *testing.T) {
	own := snap(model.RowSidebarCollapse,
		setIt(t, colGroups, 0, "project-/tmp/client/y", "recents"),
		setIt(t, colFamilies, 0, "code:"+fakeSession(1)))
	cases := map[string]struct {
		pol  func(*Policy)
		l    *model.RowSnapshot
		note string
	}{
		"no import": {
			pol: func(p *Policy) { p.EnteringImports = false },
			l:   groups(t, "a", "recents"),
		},
		"over max_members": {
			pol:  func(p *Policy) { p.Limits = map[string]int{limitMaxMembers: 3, limitMaxMemberBytes: 2048} },
			l:    snap(model.RowSidebarCollapse, setIt(t, colGroups, 0, "a", "b", "recents"), setIt(t, colFamilies, 0)),
			note: noteTooLarge,
		},
		"shared members unchanged": {
			pol: func(*Policy) {},
			l:   snap(model.RowSidebarCollapse, setIt(t, colGroups, 0, "recents"), setIt(t, colFamilies, 0)),
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			pol := collapsePolicy()
			pol.Exclude = []string{"/tmp/client/**"}
			c.pol(&pol)
			base := snap(model.RowSidebarCollapse, setIt(t, colGroups, 0, "recents"), setIt(t, colFamilies, 0))
			out := mergeCollapse(t, input(model.RowSidebarCollapse, c.l, base, own.Clone(), own.Clone(), base, pol))
			for _, k := range collapseKeys {
				if out.Desired.Items[k].Hash != own.Items[k].Hash {
					t.Errorf("%s: desired %v, want the entering folder's own %v", k, membersOf(t, out.Desired, k), membersOf(t, own, k))
				}
			}
			for _, ch := range out.Changes {
				if ch.Kind != model.ChangeSkipped || ch.Note != c.note || c.note == "" {
					t.Errorf("change %+v (want only %q skips)", ch, c.note)
				}
			}
			if c.note != "" && !hasChange(out, model.ChangeSkipped, colGroups) {
				t.Errorf("no skipped change for the kept key: %+v", out.Changes)
			}
			if got := membersOf(t, out.Canonical, colGroups); slices.Contains(got, "project-/tmp/client/y") {
				t.Errorf("private member reached canonical: %v", got)
			}
		})
	}
}

// A kept key whose candidate equals the entering folder's own set (private
// members included) gets no skipped note: nothing was held back.
func TestCollapseKeptNoteUsesOwnSet(t *testing.T) {
	pol := collapsePolicy()
	pol.Exclude = []string{"/tmp/client/**"}
	pol.Limits = map[string]int{limitMaxMembers: 1, limitMaxMemberBytes: 2048}
	own := groups(t, "project-/tmp/client/y", "recents")
	base := groups(t, "recents")
	out := mergeCollapse(t, input(model.RowSidebarCollapse, base.Clone(), base, own.Clone(), own.Clone(), base, pol))
	if len(out.Changes) != 0 {
		t.Fatalf("changes %+v, want none", out.Changes)
	}
	if out.Desired.Items[colGroups].Hash != own.Items[colGroups].Hash {
		t.Fatalf("desired %v", membersOf(t, out.Desired, colGroups))
	}
}
