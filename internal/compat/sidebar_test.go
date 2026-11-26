package compat_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/model"
)

// checkSidebarOffline checks the two sidebar-offline rows of an embedded
// manifest: sidebar-collapse is off-verify and contested on both majors;
// sidebar-seed is off-verify and contested on 7.x and "none" on 6.x (its
// key names were not verified there).
func checkSidebarOffline(t *testing.T, m *compat.Manifest, s compat.RowSpec) {
	t.Helper()
	switch {
	case s.Row == model.RowSidebarCollapse:
		if s.WritePath != compat.PathOffVerify || !s.Contested || !reflect.DeepEqual(s.LSKeys, []string{"dframe-store"}) ||
			s.MirrorFile != "" || len(s.MirrorPaths) != 0 || s.PersistVersion != 1 {
			t.Errorf("%s %s: %+v", m.Name, s.Row, s)
		}
		if !reflect.DeepEqual(s.Fields["sets"], []string{"collapsedGroups", "collapsedFamilies"}) ||
			s.Limits["max_members"] != 4096 || s.Limits["max_member_bytes"] != 2048 {
			t.Errorf("%s %s: fields %v limits %v", m.Name, s.Row, s.Fields, s.Limits)
		}
		if contains(s.Fields["sets"], "openedGroups") {
			t.Errorf("%s: openedGroups must not be carried", m.Name)
		}
	case m.Name == "6.x":
		if s.WritePath != compat.PathNone || len(s.LSKeys) != 0 || len(s.Probes) != 0 {
			t.Errorf("6.x %s: %+v", s.Row, s)
		}
	default:
		if s.WritePath != compat.PathOffVerify || !s.Contested || !s.PerOrg || len(s.LSKeys) != 2 || len(s.MirrorPaths) != 2 {
			t.Errorf("%s %s: %+v", m.Name, s.Row, s)
		}
		for _, p := range s.MirrorPaths {
			if p[0] != "preferences" || p[1] != "epitaxyPrefs" {
				t.Errorf("%s: mirror path %v", s.Row, p)
			}
		}
		if !reflect.DeepEqual(s.Fields["seed_only"], []string{"code-sessions-sort-ascending"}) {
			t.Errorf("%s: seed_only %v", s.Row, s.Fields["seed_only"])
		}
	}
}

// TestSidebarViewFieldMerge: both majors list the section members merged
// on their own and the sort pair that moves as one group.
func TestSidebarViewFieldMerge(t *testing.T) {
	ms, err := compat.Load("")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		v, _ := m.Row(model.RowSidebarView)
		if !reflect.DeepEqual(v.Fields["per_field"], []string{"collapsed", "name", "shown", "show", "groupBy", "order", "members"}) ||
			!reflect.DeepEqual(v.Fields["group"], []string{"sortBy", "ascending"}) {
			t.Errorf("%s: sidebar-view fields %v", m.Name, v.Fields)
		}
	}
}

func text7x(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("manifests", "7.x.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestSidebarRowValidation: each rule of the sidebar field roles fails
// on a broken copy of the 7.x manifest.
func TestSidebarRowValidation(t *testing.T) {
	base := text7x(t)
	if _, err := compat.Decode([]byte(base), "7.x"); err != nil {
		t.Fatalf("7.x: %v", err)
	}
	tests := []struct {
		name, old, new, want string
	}{
		{"seed_from shorter", `seed_from = ["ascending", "show.emptyGroups"]`, `seed_from = ["ascending"]`,
			"seed_keys, seed_from, mirror_paths and ls_keys differ in length"},
		{"ls_keys shorter", `ls_keys = ["LSS-persisted.code-sessions-sort-ascending.{acct}", "LSS-persisted.code-sessions-show-empty-projects.{acct}"]`,
			`ls_keys = ["LSS-persisted.code-sessions-sort-ascending.{acct}"]`,
			"seed_keys, seed_from, mirror_paths and ls_keys differ in length"},
		{"seed_only unknown", `seed_only = ["code-sessions-sort-ascending"]`, `seed_only = ["code-sessions-other"]`,
			"seed_only names a key not in seed_keys"},
		{"two seed sections", `seed_section = ["sessions"]`, `seed_section = ["sessions", "pinned"]`, "seed_section"},
		{"sets with two keys", `ls_keys = ["dframe-store"]`, `ls_keys = ["dframe-store", "dframe-other"]`,
			"sets need exactly one ls_key"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if strings.Count(base, tc.old) != 1 {
				t.Fatalf("fixture text %q not found once", tc.old)
			}
			_, err := compat.Decode([]byte(strings.Replace(base, tc.old, tc.new, 1)), "broken")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v, want %q", err, tc.want)
			}
		})
	}
}

// TestSidebarRowsExpand: the seed row's keys and mirror paths name the
// replica's account; the collapse row's store key is not per account.
func TestSidebarRowsExpand(t *testing.T) {
	m, err := compat.Decode([]byte(text7x(t)), "7.x")
	if err != nil {
		t.Fatal(err)
	}
	rep := testReplica()
	seed, _ := m.Row(model.RowSidebarSeed)
	exp := seed.Expand(rep)
	for i, k := range exp.LSKeys {
		if !strings.HasSuffix(k, "."+string(rep.Account)) || !strings.HasSuffix(exp.MirrorPaths[i][2], "."+string(rep.Account)) {
			t.Errorf("seed key %d not expanded: %s %v", i, k, exp.MirrorPaths[i])
		}
	}
	col, _ := m.Row(model.RowSidebarCollapse)
	if got := col.Expand(rep).LSKeys; !reflect.DeepEqual(got, []string{"dframe-store"}) {
		t.Errorf("collapse keys %v", got)
	}
}
