package render

import (
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/plan"
)

const (
	rAcct = "00000000-0000-4000-8000-00000000000a"
	rOrg  = "00000000-0000-4000-8000-00000000000b"
	rWS   = rAcct + "/" + rOrg + "/scratch-2026-01-02-0a0b0c"
)

func TestScratchSummaryCounts(t *testing.T) {
	if SummarizeScratch(&plan.Plan{}) != nil {
		t.Error("a plan without scratch has a summary")
	}
	p := &plan.Plan{
		Target: plan.Target{Replica: model.Replica{Account: rAcct, Org: rOrg}},
		Ops: []plan.Op{
			{Kind: plan.OpCopyFile, Row: model.RowScratch, Path: "scratch-workspaces/" + rWS + "/a", Copy: &plan.CopySource{Size: 10}},
			{Kind: plan.OpCopyFile, Row: model.RowScratch, Path: "scratch-workspaces/" + rWS + "/b", PreHash: "x", Copy: &plan.CopySource{Size: 5}},
			{Kind: plan.OpQuarantine, Row: model.RowScratch, Path: "scratch-workspaces/" + rWS + "/c"},
		},
		Changes: []model.Change{
			{Row: model.RowScratch, Kind: model.ChangeUpdated, Key: rWS + "/.git/"},
			{Row: model.RowScratch, Kind: model.ChangeSkipped, Key: rWS + "/node_modules/", Note: "held-back"},
			{Row: model.RowScratch, Kind: model.ChangeSkipped, Key: rWS + "/x/.git/", Note: "git-diverged"},
			{Row: model.RowScratch, Kind: model.ChangeSkipped, Key: rWS + "/d", Note: "deletion-deferred"},
			{Row: model.RowScratch, Kind: model.ChangeSkipped, Key: rWS, Note: "oversize-bytes"},
			{Row: model.RowScratch, Kind: model.ChangeSkipped, Key: rWS + "/e", Note: "source-changed"},
		},
		Conflicts: []model.Conflict{{Row: model.RowScratch, Key: rWS + "/a", Loser: "b"}},
	}
	s := SummarizeScratch(p)
	want := ScratchSummary{Workspaces: 1, FilesAdded: 1, FilesUpdated: 1, FilesQuarantined: 1, ConflictCopies: 1, GitUnits: 1,
		GitDiverged: 1, HeldBack: 1, Deferred: 1, Bytes: 15, SourceChanged: 1,
		NotCarried: []ScratchNotCarried{{Workspace: "scratch-2026-01-02-0a0b0c", Reason: "oversize-bytes"}}}
	if s == nil || s.Workspaces != want.Workspaces || s.FilesAdded != 1 || s.FilesUpdated != 1 || s.FilesQuarantined != 1 ||
		s.ConflictCopies != 1 || s.GitUnits != 1 || s.GitDiverged != 1 || s.HeldBack != 1 || s.Deferred != 1 || s.Bytes != 15 ||
		s.SourceChanged != 1 || len(s.NotCarried) != 1 || s.NotCarried[0] != want.NotCarried[0] {
		t.Fatalf("summary %+v", s)
	}
	// Every scratch op is in the entering replica's own scope here: no
	// preview notice.
	for _, l := range i18n.Strings(ScratchNotices(p, "a", "b")) {
		if strings.Contains(l, "Browser pane") {
			t.Error("preview notice for the account's own workspace")
		}
		if strings.Contains(l, rAcct) {
			t.Errorf("notice names a scope: %q", l)
		}
	}
	if !strings.Contains(ScratchKey(rWS+"/dir/f.txt"), "scratch-2026-01-02-0a0b0c/dir/f.txt") || strings.Contains(ScratchKey(rWS+"/f"), rAcct) {
		t.Errorf("ScratchKey %q", ScratchKey(rWS+"/dir/f.txt"))
	}
	if Bytes(512) != "512 B" || Bytes(1536) != "1.5 KiB" || Bytes(2<<30) != "2.0 GiB" {
		t.Errorf("Bytes: %s %s %s", Bytes(512), Bytes(1536), Bytes(2<<30))
	}
}

func TestPlanLinesScratchBeforeDestructive(t *testing.T) {
	p := &plan.Plan{From: "a", Target: plan.Target{Alias: "b"},
		Ops:     []plan.Op{{Kind: plan.OpQuarantine, Row: model.RowScratch, Path: "scratch-workspaces/" + rWS + "/c"}},
		Changes: []model.Change{{Row: model.RowScratch, Kind: model.ChangeDeleted, Key: rWS + "/c"}}}
	v := WithScratch(PlanView{From: "a", To: "b", Summary: p.Summarize()}, p)
	lines := PlanLines(v)
	if !strings.HasPrefix(lines[len(lines)-1], "DESTRUCTIVE") {
		t.Fatalf("last line %q", lines[len(lines)-1])
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "  scratch") || !strings.Contains(joined, "backup generation") {
		t.Errorf("lines:\n%s", joined)
	}
}

// The notices that name limits are plan lines (spec-scratch 13.1) when
// planning set the plan's ScratchNotes; held-back folders and read-only
// workspaces need no limits.
func TestScratchLimitNotices(t *testing.T) {
	ws2 := rAcct + "/" + rOrg + "/scratch-2026-01-03-0a0b0d"
	ws3 := rAcct + "/" + rOrg + "/scratch-2026-01-04-0a0b0e"
	skip := func(key, note string) model.Change {
		return model.Change{Row: model.RowScratch, Kind: model.ChangeSkipped, Key: key, Note: note}
	}
	p := &plan.Plan{
		Changes: []model.Change{
			skip(rWS, "oversize-files"), skip(rWS, "oversize-files"), // both sides
			skip(ws2, "budget"), skip(ws2, "budget"),
			skip(ws3, "read-only-dir"),
			skip(rWS+"/node_modules/", "held-back"), skip(rWS+"/.venv/", "held-back"),
		},
		ScratchNotes: &plan.ScratchNotes{MaxFilesPerWorkspace: 20000, MaxWorkspaceBytes: 2 << 30, MaxRunBytes: 8 << 30, MaxRunFiles: 50000},
	}
	lines := i18n.Strings(ScratchNotices(p, "home", "work"))
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"Scratch workspace scratch-2026-01-02-0a0b0c was not carried: it is over the limit of 20000 files or 2.0 GiB.",
		"1 scratch workspaces were not carried this time: together they exceed the per-switch limit (8.0 GiB or 50000 files).",
		"Scratch workspace scratch-2026-01-04-0a0b0e was not carried: one of its folders is read-only",
		"2 dependency, build or version-control folders in scratch workspaces were not carried (for example node_modules). Reinstall or rebuild them on work",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
	if strings.Count(joined, "scratch-2026-01-02-0a0b0c was not carried") != 1 {
		t.Errorf("oversize repeated:\n%s", joined)
	}
	if strings.Contains(joined, rAcct) {
		t.Errorf("a notice names a scope:\n%s", joined)
	}
	// Without ScratchNotes (a plan read from a file) the limit lines are
	// left out, the others stay.
	p.ScratchNotes = nil
	joined = strings.Join(i18n.Strings(ScratchNotices(p, "home", "work")), "\n")
	if strings.Contains(joined, "over the limit") || strings.Contains(joined, "per-switch limit") || !strings.Contains(joined, "dependency, build") {
		t.Errorf("without limits:\n%s", joined)
	}
	// Not enough free space: the row was left out with the check's numbers.
	p = &plan.Plan{Skipped: map[model.RowID]string{model.RowScratch: ReasonScratchSpace},
		ScratchNotes: &plan.ScratchNotes{NeedBytes: 3 << 30, FreeBytes: 1 << 30}}
	if got := strings.Join(i18n.Strings(ScratchNotices(p, "home", "work")), "\n"); !strings.Contains(got, "may need 3.0 GiB and only 1.0 GiB is free") {
		t.Errorf("no space: %q", got)
	}
}
