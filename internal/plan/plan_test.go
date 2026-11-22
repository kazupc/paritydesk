package plan

import (
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/model"
)

func samplePlan() *Plan {
	return &Plan{
		Version: Version, RunID: "run-a", From: "a",
		Target:    Target{Alias: "b", Folder: "/x/Claude-b"},
		CreatedAt: time.Unix(1, 0),
		Ops: []Op{{Kind: OpWriteFile, Row: model.RowSessions, Path: "p.json", Content: []byte("one"),
			PreHash: "", PostHash: "h1", Items: []string{"local_1"}}},
		Changes: []model.Change{
			{Row: model.RowSessions, Kind: model.ChangeAdded, Key: "local_1"},
			{Row: model.RowSessions, Kind: model.ChangeDeleted, Key: "local_2"},
			{Row: model.RowPinnedOrder, Kind: model.ChangeCapped, Key: "order"},
		},
	}
}

func TestHashIgnoresRunTimesFolderAndContent(t *testing.T) {
	a, b := samplePlan(), samplePlan()
	b.RunID, b.CreatedAt, b.ExpiresAt, b.Target.Folder, b.Approximate = "run-b", time.Unix(9, 0), time.Unix(9, 0), "/y", true
	b.Ops[0].Content = []byte("two")
	if a.Hash() != b.Hash() {
		t.Fatal("hash depends on excluded fields")
	}
	tests := []struct {
		name   string
		mutate func(p *Plan)
	}{
		{"post hash", func(p *Plan) { p.Ops[0].PostHash = "h2" }},
		{"pre hash", func(p *Plan) { p.Ops[0].PreHash = "h0" }},
		{"change", func(p *Plan) { p.Changes[0].Kind = model.ChangeUpdated }},
		{"target", func(p *Plan) { p.Target.Alias = "c" }},
		{"needs confirmation", func(p *Plan) { p.NeedsConfirmation = true }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := samplePlan()
			tc.mutate(c)
			if c.Hash() == a.Hash() {
				t.Fatal("hash did not change")
			}
		})
	}
	var nilPlan *Plan
	if nilPlan.Hash() != "" {
		t.Fatal("nil plan hash")
	}
}

func TestSummarize(t *testing.T) {
	p := samplePlan()
	p.Conflicts = []model.Conflict{{Row: model.RowSessions, Key: "local_3"}}
	s := p.Summarize()
	got := s.Categories[model.CatSessions]
	if got.Added != 1 || got.Deleted != 1 || got.Conflicts != 1 {
		t.Fatalf("sessions %+v", got)
	}
	if s.Categories[model.CatPins].Updated != 1 {
		t.Fatalf("pins %+v", s.Categories[model.CatPins])
	}
	if s.Destructive[model.CatSessions] != 1 || len(s.Destructive) != 1 {
		t.Fatalf("destructive %+v", s.Destructive)
	}
}

func TestNewDestructiveAndDigest(t *testing.T) {
	preview, final := samplePlan(), samplePlan()
	if got := NewDestructive(preview, final); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
	final.Changes = append(final.Changes, model.Change{Row: model.RowTasks, Kind: model.ChangeDeleted, Key: "backlog/1"})
	got := NewDestructive(preview, final)
	if len(got) != 1 || got[0].Key != "backlog/1" {
		t.Fatalf("got %v", got)
	}
	if n := len(NewDestructive(nil, final)); n != 2 {
		t.Fatalf("nil preview: %d", n)
	}
	if preview.DestructiveDigest() == final.DestructiveDigest() || preview.DestructiveDigest() == "" {
		t.Fatal("digest")
	}
	reordered := samplePlan()
	reordered.Changes[0], reordered.Changes[1] = reordered.Changes[1], reordered.Changes[0]
	if reordered.DestructiveDigest() != preview.DestructiveDigest() {
		t.Fatal("digest depends on order")
	}
	if (&Plan{}).DestructiveDigest() != "" {
		t.Fatal("empty digest")
	}
}
