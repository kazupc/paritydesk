package model

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestV02RowsAndCategories(t *testing.T) {
	want := []RowID{RowEnvironments, RowSessions, RowTasks, RowWaitingInput, RowWorktrees, RowScratch,
		RowGroupOrder, RowSidebarView, RowPinnedOrder, RowSidebarCollapse, RowSidebarSeed,
		RowAppPrefs, RowWindowState, RowMCPServers, RowPins}
	if !slices.Equal(AllRows, want) {
		t.Fatalf("AllRows = %v", AllRows)
	}
	cats := []Category{CatSessions, CatEnvironments, CatOrder, CatSidebar, CatPins, CatPrefs, CatWorktrees, CatScratch, CatWindow, CatMCP}
	if !slices.Equal(AllCategories, cats) {
		t.Fatalf("AllCategories = %v", AllCategories)
	}
	for r, c := range map[RowID]Category{
		RowScratch: CatScratch, RowSidebarCollapse: CatSidebar, RowSidebarSeed: CatSidebar, RowPins: CatPins,
		RowSidebarView: CatSidebar, RowPinnedOrder: CatPins, RowSessions: CatSessions,
	} {
		if got := CategoryOf(r); got != c {
			t.Errorf("CategoryOf(%s) = %q, want %q", r, got, c)
		}
	}
	for _, r := range AllRows {
		if c := CategoryOf(r); !slices.Contains(AllCategories, c) {
			t.Errorf("row %s has category %q outside AllCategories", r, c)
		}
	}
	seen := map[RowID]bool{}
	for _, r := range AllRows {
		if seen[r] {
			t.Errorf("row %s listed twice", r)
		}
		seen[r] = true
	}
	// Scratch comes after sessions (its refs come from session records),
	// the sidebar rows after sidebar-view and environments.
	idx := func(r RowID) int { return slices.Index(AllRows, r) }
	if idx(RowScratch) < idx(RowSessions) || idx(RowSidebarSeed) < idx(RowSidebarView) || idx(RowSidebarCollapse) < idx(RowEnvironments) {
		t.Error("v0.2 rows out of order")
	}
}

func TestBoundaryCategories(t *testing.T) {
	if !slices.Equal(BoundaryCategories[:len(AllCategories)], AllCategories) || len(BoundaryCategories) != len(AllCategories)+1 ||
		BoundaryCategories[len(AllCategories)] != CatArtifacts {
		t.Fatalf("BoundaryCategories = %v", BoundaryCategories)
	}
	if slices.Contains(AllCategories, CatArtifacts) {
		t.Fatal("artifacts must not be an --only/--skip category")
	}
	// BoundaryCategories is its own slice: growing it never writes into
	// AllCategories' backing array.
	if &BoundaryCategories[0] == &AllCategories[0] {
		t.Fatal("BoundaryCategories shares AllCategories' array")
	}
}

func TestImpliedBy(t *testing.T) {
	if ImpliedBy(CatScratch) != CatSessions {
		t.Fatal("scratch must follow sessions")
	}
	for _, c := range BoundaryCategories {
		if c != CatScratch && ImpliedBy(c) != "" {
			t.Errorf("ImpliedBy(%s) = %q", c, ImpliedBy(c))
		}
	}
	if ImpliedBy("") != "" || ImpliedBy("unknown") != "" {
		t.Error("unknown categories imply nothing")
	}
}

func TestOnlineCountsJSON(t *testing.T) {
	b, err := json.Marshal(OnlineCounts{PinsAdd: 1, PinsRemove: 2, PinsKeep: 3, PinsBlocked: 4, Sidebar: 5, Other: 6, Done: 7, Skipped: 8, Failed: 9})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"pins_add":1,"pins_remove":2,"pins_keep":3,"pins_blocked":4,"sidebar":5,"other":6,"done":7,"skipped":8,"failed":9}`
	if string(b) != want {
		t.Fatalf("got %s", b)
	}
}

func TestSnapshotNotesCloneAndNotPersisted(t *testing.T) {
	s := &RowSnapshot{Row: RowScratch, Schema: 1, Notes: []Change{{Row: RowScratch, Kind: ChangeSkipped, Key: "k", Note: "budget"}}}
	c := s.Clone()
	if len(c.Notes) != 1 || c.Notes[0] != s.Notes[0] {
		t.Fatalf("clone notes %v", c.Notes)
	}
	c.Notes[0].Note = "changed"
	if s.Notes[0].Note != "budget" {
		t.Fatal("clone shares the notes array")
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]json.RawMessage
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if _, ok := back["notes"]; ok {
		t.Fatal("notes persisted")
	}
	if s.Hash() != (&RowSnapshot{Row: RowScratch, Schema: 1}).Hash() {
		t.Fatal("notes change the snapshot hash")
	}
}
