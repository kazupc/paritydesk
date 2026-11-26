package appsim

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func id(n int) string { return fmt.Sprintf("local_00000000-0000-4000-8000-%012d", n) }

func lin(parent int, kind string) *Lineage {
	return &Lineage{SessionID: id(parent), Kind: kind, Initiator: "user"}
}

// families are hand-built cases covering every link kind and every rule
// that removes a link.
func families() []struct {
	name       string
	sessions   []Session
	nested     bool
	filedApart map[string]bool
} {
	type fam = struct {
		name       string
		sessions   []Session
		nested     bool
		filedApart map[string]bool
	}
	return []fam{
		{name: "lineage kinds", nested: true, sessions: []Session{
			{ID: id(1)},
			{ID: id(2), Lineage: lin(1, KindSideSession)},
			{ID: id(3), Lineage: lin(1, KindHandOff)},
			{ID: id(4), Lineage: lin(1, KindSuggestedTask)},
			{ID: id(5), Lineage: lin(1, KindFork)},
			{ID: id(6), Lineage: &Lineage{SessionID: id(1), Kind: "other", Initiator: "user"}},
			{ID: id(7), Lineage: &Lineage{SessionID: id(1), Kind: KindFork, Initiator: "robot"}},
		}},
		{name: "flat layout keeps side sessions only", nested: false, sessions: []Session{
			{ID: id(1)},
			{ID: id(2), Lineage: lin(1, KindHandOff)},
			{ID: id(3), SpawnedFrom: &SpawnedFrom{SessionID: id(1), SideSession: true}},
			{ID: id(4), ForkedFromSessionID: id(1)},
		}},
		{name: "display parent with and without dS", nested: true, sessions: []Session{
			{ID: id(1)},
			{ID: id(9)},
			{ID: id(2), DisplayParentSessionID: id(1)},
			{ID: id(3), DisplayParentSessionID: id(1), Lineage: lin(9, KindHandOff)},
			{ID: id(4), DisplayParentSessionID: id(1), SpawnedFrom: &SpawnedFrom{SessionID: id(9), SideSession: true, SharedCheckout: true}},
			{ID: id(5), DisplayParentSessionID: id(1), Lineage: lin(9, KindFork)},
		}},
		{name: "lineage detached and side sessions", nested: true, sessions: []Session{
			{ID: id(1)},
			{ID: id(2), LineageDetached: true, Lineage: lin(1, KindHandOff)},
			{ID: id(3), SpawnedFrom: &SpawnedFrom{SessionID: id(1), SideSession: true, SharedCheckout: true}},
			{ID: id(4), SpawnedFrom: &SpawnedFrom{SessionID: id(1), SideSession: true, UserFork: true}, Lineage: lin(1, KindSideSession)},
			{ID: id(5), ForkedFromSessionID: id(3)},
		}},
		{name: "archived parents and children", nested: true, sessions: []Session{
			{ID: id(1)},
			{ID: id(2), IsArchived: true, Lineage: lin(1, KindFork)},
			{ID: id(3), Lineage: lin(2, KindFork)},
			{ID: id(4), IsArchived: true, Lineage: lin(1, KindHandOff)},
			{ID: id(5), Lineage: &Lineage{SessionID: id(2), Kind: KindFork, Initiator: "claude", AttachesThroughArchived: true}},
		}},
		{name: "project threads and filed apart", nested: true, filedApart: map[string]bool{id(4): true}, sessions: []Session{
			{ID: id(1)},
			{ID: id(2), ProjectThreadStarted: true, Lineage: lin(1, KindFork)},
			{ID: id(3), Lineage: lin(2, KindFork)},
			{ID: id(4), Lineage: lin(1, KindFork)},
			{ID: id(5), Lineage: lin(4, KindFork)},
		}},
		{name: "cycles", nested: true, sessions: []Session{
			{ID: id(1), DisplayParentSessionID: id(2)},
			{ID: id(2), DisplayParentSessionID: id(1)},
			{ID: id(3), ForkedFromSessionID: id(4)},
			{ID: id(4), ForkedFromSessionID: id(3)},
			{ID: id(5), ForkedFromSessionID: id(3)},
		}},
	}
}

func TestFamiliesGolden(t *testing.T) {
	var b strings.Builder
	for _, f := range families() {
		m := FamilyMap(f.sessions, f.nested, f.filedApart)
		var keys []string
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(&b, "# %s\n", f.name)
		for _, k := range keys {
			fmt.Fprintf(&b, "%s -> %s\n", k[len(k)-4:], m[k][len(m[k])-4:])
		}
	}
	path := filepath.Join("testdata", "families.golden")
	if *update {
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != b.String() {
		t.Fatalf("families differ from the golden; run with -update and review:\n%s", b.String())
	}
}

func TestSetPinned(t *testing.T) {
	a := &App{Nested: true, Pinned: map[string]bool{id(1): true, id(2): true, id(3): true},
		Hidden: map[string]bool{id(4): true},
		Sessions: []Session{{ID: id(1)}, {ID: id(2), Lineage: lin(1, KindHandOff)}, {ID: id(3), IsArchived: true, Lineage: lin(1, KindFork)},
			{ID: id(4)}, {ID: id(5), ScheduledTaskID: "t"}}}
	if got := a.SetPinned(id(1), true, false); got != TextAlreadyPinned {
		t.Fatal(got)
	}
	a.Pinned[id(4)] = true
	if got := a.SetPinned(id(4), false, false); got != TextHidden || !a.Pinned[id(4)] {
		t.Fatal(got)
	}
	if got := a.SetPinned(id(5), true, false); got != TextScheduled {
		t.Fatal(got)
	}
	if got := a.SetPinned(id(3), false, false); got != TextArchived {
		t.Fatal(got)
	}
	// Unpinning the root cascades over its pinned family, archived child
	// included.
	if got := a.SetPinned(id(1), false, false); got != TextUnpinned || a.Pinned[id(2)] || a.Pinned[id(3)] {
		t.Fatalf("%s %v", got, a.PinnedIDs())
	}
	a.Pinned[id(2)] = true
	a.SC = true
	a.Pinned[id(1)] = true
	a.SetPinned(id(1), false, false)
	if !a.Pinned[id(2)] {
		t.Fatal("SC() did not stop the cascade")
	}
	a.SC = false
	a.Pinned[id(1)] = true
	if got := a.SetPinned(id(1), false, true); got != TextTimeout || !a.Pinned[id(1)] {
		t.Fatal(got)
	}
	a.Flush()
	if a.Pinned[id(1)] || a.Pinned[id(2)] {
		t.Fatalf("late command not applied: %v", a.PinnedIDs())
	}
}

func TestFromRecord(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"lineage": map[string]any{"sessionId": id(1), "kind": "fork", "initiator": "user"}, "isArchived": true, "title": "x"})
	s := FromRecord(id(2), raw)
	if s.ID != id(2) || !s.IsArchived || s.Lineage == nil || s.Lineage.SessionID != id(1) {
		t.Fatalf("%+v", s)
	}
}
