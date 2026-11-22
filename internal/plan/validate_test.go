package plan

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/localstorage"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
)

const secretCanary = "PARITYDESK-CANARY-TOKEN-7f3c"

func writeOp(row model.RowID, path string, content string, items ...string) Op {
	return Op{Kind: OpWriteFile, Row: row, Path: path, Content: []byte(content),
		PostHash: hashOf([]byte(content)), Items: items}
}

func lsPut(row model.RowID, key, text string) Op {
	c := localstorage.EncodeString(text)
	return Op{Kind: OpLSPut, Row: row, Key: key, Content: c, PostHash: hashOf(c)}
}

func quarantine(row model.RowID, path string, items ...string) Op {
	return Op{Kind: OpQuarantine, Row: row, Path: path, PreHash: hashOf([]byte(path)), Items: items}
}

func deleted(row model.RowID, keys ...string) []model.Change {
	var cs []model.Change
	for _, k := range keys {
		cs = append(cs, model.Change{Row: row, Kind: model.ChangeDeleted, Key: k})
	}
	return cs
}

// validInput is a plan that passes every check.
func validInput() ValidateInput {
	return ValidateInput{
		Plan: &Plan{
			Version: Version,
			Ops: []Op{
				writeOp(model.RowGroupOrder, "claude_desktop_config.json", `{"a":1}`, "order"),
				lsPut(model.RowPinnedOrder, "dframe-store", `{"state":{}}`),
				quarantine(model.RowSessions, "claude-code-sessions/a/b/local_1.json", "local_1"),
			},
			Changes: deleted(model.RowSessions, "local_1"),
		},
		Before: map[model.Category]int{model.CatSessions: 10},
		Limits: Limits{MaxDeleteFraction: 0.2, MaxDeleteMin: 2},
	}
}

func withOp(op Op) func(*ValidateInput) {
	return func(in *ValidateInput) { in.Plan.Ops = append(in.Plan.Ops, op) }
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ValidateInput)
		want   pderr.Code // OK = accepted
	}{
		{"valid plan", func(*ValidateInput) {}, pderr.OK},
		{"nil plan", func(in *ValidateInput) { in.Plan = nil }, pderr.Internal},

		// The fixed floor applies with empty manifest lists.
		{"floor config.json", withOp(writeOp(model.RowAppPrefs, "config.json", "{}")), pderr.Internal},
		{"floor Cookies", withOp(writeOp(model.RowAppPrefs, "Cookies", "x")), pderr.Internal},
		{"floor cookies lower case", withOp(writeOp(model.RowAppPrefs, "cookies", "x")), pderr.Internal},
		{"floor Local Storage as a file", withOp(writeOp(model.RowAppPrefs, "Local Storage/leveldb/000003.log", "x")), pderr.Internal},
		{"floor quarantine of Network", withOp(Op{Kind: OpQuarantine, Row: model.RowAppPrefs, Path: "Network/Cookies", PreHash: hashOf(nil), Reason: ReasonAppTmpCandidate}), pderr.Internal},
		{"floor oauth-like LS key", withOp(lsPut(model.RowAppPrefs, "oauthState", "{}")), pderr.Internal},
		{"floor token LS key", withOp(lsPut(model.RowAppPrefs, "my-AccessToken", "{}")), pderr.Internal},

		// Manifest lists.
		{"deny path glob", func(in *ValidateInput) {
			in.DenyPaths = []string{"vm_bundles/**"}
			withOp(writeOp(model.RowAppPrefs, "vm_bundles/x/y.json", "{}"))(in)
		}, pderr.Internal},
		{"deny LS key", func(in *ValidateInput) {
			in.DenyLSKeys = []string{"private-*"}
			withOp(lsPut(model.RowAppPrefs, "private-thing", "{}"))(in)
		}, pderr.Internal},
		{"allow paths miss", func(in *ValidateInput) {
			in.AllowPaths = []string{"claude-code-sessions/**"}
		}, pderr.Internal},
		{"allow paths hit", func(in *ValidateInput) {
			in.AllowPaths = []string{"claude-code-sessions/**", "claude_desktop_config.json"}
		}, pderr.OK},
		{"allow LS keys miss", func(in *ValidateInput) { in.AllowLSKeys = []string{"other"} }, pderr.Internal},
		{"allow LS keys hit", func(in *ValidateInput) { in.AllowLSKeys = []string{"other", "dframe-store"} }, pderr.OK},

		// Containment.
		{"dot dot", withOp(writeOp(model.RowAppPrefs, "a/../config.json", "{}")), pderr.Internal},
		{"leading dot dot", withOp(writeOp(model.RowAppPrefs, "../x.json", "{}")), pderr.Internal},
		{"absolute", withOp(writeOp(model.RowAppPrefs, "/etc/x.json", "{}")), pderr.Internal},
		{"NUL", withOp(writeOp(model.RowAppPrefs, "a\x00b.json", "{}")), pderr.Internal},
		{"empty path", withOp(writeOp(model.RowAppPrefs, "", "{}")), pderr.Internal},
		{"double slash", withOp(writeOp(model.RowAppPrefs, "a//b.json", "{}")), pderr.Internal},
		{"dot segment", withOp(writeOp(model.RowAppPrefs, "./b.json", "{}")), pderr.Internal},

		// Temporary names.
		{"tmp recovery glob", func(in *ValidateInput) {
			in.TmpRecoveryGlobs = []string{"local_*.json.tmp"}
			withOp(writeOp(model.RowSessions, "claude-code-sessions/a/b/local_2.json.tmp", "{}"))(in)
		}, pderr.Internal},
		{"tmp recovery glob with dir", func(in *ValidateInput) {
			in.TmpRecoveryGlobs = []string{"claude-code-sessions/**/*.pending"}
			withOp(writeOp(model.RowSessions, "claude-code-sessions/a/b/local_2.pending", "{}"))(in)
		}, pderr.Internal},
		{"temp name tmp", withOp(writeOp(model.RowSessions, "x/y.json.tmp", "{}")), pderr.Internal},
		{"temp name tilde", withOp(writeOp(model.RowSessions, "x/y.json~", "{}")), pderr.Internal},
		{"temp name swap", withOp(writeOp(model.RowSessions, "x/.y.json.swp", "{}")), pderr.Internal},

		// META key and LS content.
		{"META key", withOp(lsPut(model.RowAppPrefs, "META:"+localstorage.Origin, "{}")), pderr.Internal},
		{"META prefix", withOp(lsPut(model.RowAppPrefs, "meta:x", "{}")), pderr.Internal},
		{"empty LS key", withOp(lsPut(model.RowAppPrefs, "", "{}")), pderr.Internal},
		{"LS content not encoded", withOp(Op{Kind: OpLSPut, Row: model.RowAppPrefs, Key: "k", Content: []byte{0x07}, PostHash: hashOf([]byte{0x07})}), pderr.Internal},
		{"LS content empty", withOp(Op{Kind: OpLSPut, Row: model.RowAppPrefs, Key: "k", PostHash: hashOf(nil)}), pderr.Internal},
		{"LS op naming a file", withOp(Op{Kind: OpLSDelete, Row: model.RowAppPrefs, Key: "k", Path: "x", PreHash: hashOf(nil)}), pderr.Internal},
		{"file op naming a key", withOp(Op{Kind: OpWriteFile, Row: model.RowAppPrefs, Key: "k", Path: "x.json", PostHash: hashOf(nil)}), pderr.Internal},
		{"unknown kind", withOp(Op{Kind: "chmod", Row: model.RowAppPrefs, Path: "x.json"}), pderr.Internal},

		// Limits.
		{"max ops exceeded", func(in *ValidateInput) { in.MaxOps = 2 }, pderr.Internal},
		{"max ops at limit", func(in *ValidateInput) { in.MaxOps = 3 }, pderr.OK},
		{"max bytes exceeded", func(in *ValidateInput) { in.MaxBytes = 5 }, pderr.Internal},
		{"max bytes at limit", func(in *ValidateInput) { in.MaxBytes = int64(7 + len(localstorage.EncodeString(`{"state":{}}`))) }, pderr.OK},

		// Hashes.
		{"malformed pre-hash", func(in *ValidateInput) { in.Plan.Ops[0].PreHash = "abc" }, pderr.Internal},
		{"upper-case pre-hash", func(in *ValidateInput) { in.Plan.Ops[0].PreHash = strings.ToUpper(hashOf(nil)) }, pderr.Internal},
		{"post-hash not of content", func(in *ValidateInput) { in.Plan.Ops[0].PostHash = hashOf([]byte("other")) }, pderr.Internal},
		{"empty post-hash for a write", func(in *ValidateInput) { in.Plan.Ops[0].PostHash = "" }, pderr.Internal},
		{"removal without pre-hash", func(in *ValidateInput) { in.Plan.Ops[2].PreHash = "" }, pderr.Internal},
		{"removal with post-hash", func(in *ValidateInput) { in.Plan.Ops[2].PostHash = hashOf(nil) }, pderr.Internal},

		// Destructive mapping.
		{"destructive without deleted change", func(in *ValidateInput) { in.Plan.Changes = nil }, pderr.Internal},
		{"destructive mapped to another row", func(in *ValidateInput) { in.Plan.Changes = deleted(model.RowTasks, "local_1") }, pderr.Internal},
		{"destructive mapped to an updated change", func(in *ValidateInput) {
			in.Plan.Changes = []model.Change{{Row: model.RowSessions, Kind: model.ChangeUpdated, Key: "local_1"}}
		}, pderr.Internal},
		{"destructive write without change", withOp(Op{Kind: OpWriteFile, Row: model.RowTasks, Path: "t.json", Content: []byte("{}"),
			PostHash: hashOf([]byte("{}")), Destructive: true, Items: []string{"backlog/1"}}), pderr.Internal},
		{"destructive mapped through Rows", func(in *ValidateInput) {
			op := Op{Kind: OpWriteFile, Row: model.RowGroupOrder, Rows: []model.RowID{model.RowGroupOrder, model.RowMCPServers},
				Path: "c2.json", Content: []byte("{}"), PostHash: hashOf([]byte("{}")), Destructive: true, Items: []string{"order", "srv"}}
			in.Plan.Ops = append(in.Plan.Ops, op)
			in.Plan.Changes = append(in.Plan.Changes, deleted(model.RowMCPServers, "srv")...)
			in.Before[model.CatMCP] = 10
		}, pderr.OK},
		{"LS delete without change", withOp(Op{Kind: OpLSDelete, Row: model.RowSidebarView, Key: "k", PreHash: hashOf(nil), Items: []string{"field:x"}}), pderr.Internal},
		{"app tmp candidate accepted", withOp(Op{Kind: OpQuarantine, Row: model.RowSessions, Path: "claude-code-sessions/a/b/local_9.json.tmp",
			PreHash: hashOf(nil), Reason: ReasonAppTmpCandidate}), pderr.OK},
		{"app tmp candidate accepted with tmp globs", func(in *ValidateInput) {
			in.TmpRecoveryGlobs = []string{"local_*.json.tmp"}
			withOp(Op{Kind: OpQuarantine, Row: model.RowSessions, Path: "claude-code-sessions/a/b/local_9.json.tmp",
				PreHash: hashOf(nil), Reason: ReasonAppTmpCandidate})(in)
		}, pderr.OK},
		{"revived tombstone accepted", withOp(Op{Kind: OpQuarantine, Row: model.RowSessions, Path: "claude-code-sessions/a/b/deleted_9",
			PreHash: hashOf(nil), Items: []string{"local_9"}, Reason: ReasonRevivedTombstone}), pderr.OK},
		{"unknown reason", withOp(Op{Kind: OpQuarantine, Row: model.RowSessions, Path: "x.json",
			PreHash: hashOf(nil), Reason: "because"}), pderr.Internal},

		// Secrets.
		{"secret across managed edge", func(in *ValidateInput) {
			in.Plan.Ops[0].Secret = true
		}, pderr.Internal},
		{"secret allowed to cross", func(in *ValidateInput) {
			in.Plan.Ops[0].Secret, in.SecretsMayCross = true, true
		}, pderr.OK},
		{"secret allowed per key", func(in *ValidateInput) {
			in.Plan.Ops[0].Secret, in.Plan.Ops[0].Items = true, []string{"preset/p1"}
			in.SecretsAllowed = map[string]bool{"preset/p1": true}
		}, pderr.OK},
		{"secret partly allowed", func(in *ValidateInput) {
			in.Plan.Ops[0].Secret, in.Plan.Ops[0].Items = true, []string{"preset/p1", "preset/p2"}
			in.SecretsAllowed = map[string]bool{"preset/p1": true}
		}, pderr.Internal},
		{"secret without items", func(in *ValidateInput) {
			in.Plan.Ops[0].Secret, in.Plan.Ops[0].Items = true, nil
			in.SecretsAllowed = map[string]bool{"preset/p1": true}
		}, pderr.Internal},

		// Worktree guard.
		{"dirty worktree refused", func(in *ValidateInput) {
			in.Worktrees = []WorktreeRisk{{Path: "/w/clean"}, {Path: "/w/dirty", Dirty: true}}
		}, pderr.WorktreeRisk},
		{"dirty worktree accepted", func(in *ValidateInput) {
			in.Worktrees, in.AcceptWorktreeRisk = []WorktreeRisk{{Path: "/w/dirty", Dirty: true}}, true
		}, pderr.OK},
		{"clean worktree", func(in *ValidateInput) { in.Worktrees = []WorktreeRisk{{Path: "/w/clean"}} }, pderr.OK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := validInput()
			tc.mutate(&in)
			assertCode(t, Validate(in), tc.want)
		})
	}
}

func assertCode(t *testing.T, err error, want pderr.Code) {
	t.Helper()
	if want == pderr.OK {
		if err != nil {
			t.Fatalf("refused: %v", err)
		}
		return
	}
	var pe *pderr.Error
	if !errors.As(err, &pe) {
		t.Fatalf("want *pderr.Error code %d, got %T %v", want, err, err)
	}
	if pe.Code != want {
		t.Fatalf("code %d, want %d (%v)", pe.Code, want, err)
	}
	t.Logf("refused as expected: %v", err)
}

// breakerInput deletes n sessions out of before with limits 20% / min 2.
func breakerInput(n, before, allow int) ValidateInput {
	in := ValidateInput{
		Plan:   &Plan{Version: Version},
		Before: map[model.Category]int{model.CatSessions: before},
		Limits: Limits{MaxDeleteFraction: 0.2, MaxDeleteMin: 2, AllowDeletions: allow},
	}
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("local_%d", i)
		in.Plan.Ops = append(in.Plan.Ops, quarantine(model.RowSessions, "claude-code-sessions/a/b/"+key+".json", key))
		in.Plan.Changes = append(in.Plan.Changes, deleted(model.RowSessions, key)...)
	}
	return in
}

func TestValidateDeletionBreaker(t *testing.T) {
	tests := []struct {
		name               string
		n, before, allowed int
		want               pderr.Code
	}{
		{"no deletions", 0, 0, 0, pderr.OK},
		{"min limit exactly", 2, 5, 0, pderr.OK},
		{"min limit plus one", 3, 5, 0, pderr.DeletionBreaker},
		{"fraction limit exactly", 4, 20, 0, pderr.OK},
		{"fraction limit plus one", 5, 20, 0, pderr.DeletionBreaker},
		{"fraction rounds up", 5, 21, 0, pderr.OK},
		{"becomes empty", 2, 2, 0, pderr.DeletionBreaker},
		{"becomes empty from one", 1, 1, 0, pderr.DeletionBreaker},
		{"allow deletions covers", 5, 20, 5, pderr.OK},
		{"allow deletions too low", 5, 20, 4, pderr.DeletionBreaker},
		{"allow deletions covers emptying", 2, 2, 2, pderr.OK},
		{"unknown before uses min", 2, 0, 0, pderr.OK},
		{"unknown before above min", 3, 0, 0, pderr.DeletionBreaker},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertCode(t, Validate(breakerInput(tc.n, tc.before, tc.allowed)), tc.want)
		})
	}
}

// TestValidateBreakerCountsAdditions checks that the emptying rule looks
// at what the category holds afterwards: a deletion the same plan
// replaces (a rename, or new items) does not empty it, and the message of
// an emptying trip names the emptying, not the numeric limit.
func TestValidateBreakerCountsAdditions(t *testing.T) {
	tests := []struct {
		name      string
		n, before int
		added     []model.ChangeKind
		want      pderr.Code
		wantMsg   string
	}{
		{"one of one, three added", 1, 1, []model.ChangeKind{model.ChangeAdded, model.ChangeAdded, model.ChangeAdded}, pderr.OK, ""},
		{"rename of the only item", 1, 1, []model.ChangeKind{model.ChangeAdded}, pderr.OK, ""},
		{"one of one, one revived", 1, 1, []model.ChangeKind{model.ChangeRevived}, pderr.OK, ""},
		{"one of one, one updated", 1, 1, []model.ChangeKind{model.ChangeUpdated}, pderr.DeletionBreaker, "empty"},
		{"one of one, nothing added", 1, 1, nil, pderr.DeletionBreaker, "would leave sessions empty"},
		{"over the limit despite additions", 3, 5, []model.ChangeKind{model.ChangeAdded}, pderr.DeletionBreaker, "exceed the limit"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := breakerInput(tc.n, tc.before, 0)
			for i, k := range tc.added {
				in.Plan.Changes = append(in.Plan.Changes, model.Change{Row: model.RowSessions, Kind: k, Key: fmt.Sprintf("local_new_%d", i)})
			}
			err := Validate(in)
			assertCode(t, err, tc.want)
			if tc.wantMsg != "" && !strings.Contains(err.Error(), tc.wantMsg) {
				t.Fatalf("message %q lacks %q", err, tc.wantMsg)
			}
		})
	}
}

// TestValidateOrderDeletionMaps checks that a destructive delete of an
// ordered list maps to the order item's deletion (rows keeps removals of
// the order item as deletions, not moves).
func TestValidateOrderDeletionMaps(t *testing.T) {
	in := validInput()
	op := Op{Kind: OpLSDelete, Row: model.RowGroupOrder, Key: "LSS-persisted.code-projects-order.x", Items: []string{"order"},
		PreHash: hashOf([]byte("x")), Destructive: true}
	in.Plan.Ops = append(in.Plan.Ops, op)
	in.Before[model.CategoryOf(model.RowGroupOrder)] = 2
	in.Plan.Changes = append(in.Plan.Changes, model.Change{Row: model.RowGroupOrder, Kind: model.ChangeMoved, Key: "order"})
	assertCode(t, Validate(in), pderr.Internal)
	in.Plan.Changes[len(in.Plan.Changes)-1].Kind = model.ChangeDeleted
	assertCode(t, Validate(in), pderr.OK)
}

func TestValidateBreakerPerCategory(t *testing.T) {
	in := breakerInput(2, 20, 0)
	// Two pin deletions out of three: the pins category trips alone.
	for _, k := range []string{"p1", "p2"} {
		in.Plan.Changes = append(in.Plan.Changes, deleted(model.RowPinnedOrder, k)...)
	}
	in.Before[model.CatPins] = 2
	err := Validate(in)
	assertCode(t, err, pderr.DeletionBreaker)
	if !strings.Contains(err.Error(), string(model.CatPins)) {
		t.Fatalf("category not named: %v", err)
	}
}

func TestValidateErrorsNeverCarryContent(t *testing.T) {
	in := validInput()
	in.Plan.Ops[0] = writeOp(model.RowEnvironments, "claude_desktop_config.json", `{"env":"`+secretCanary+`"}`, "preset/p1")
	in.Plan.Ops[0].Secret = true
	err := Validate(in)
	assertCode(t, err, pderr.Internal)
	if strings.Contains(err.Error(), secretCanary) || strings.Contains(err.Error(), "preset/p1") {
		t.Fatalf("error leaks content or items: %v", err)
	}
	if !strings.Contains(err.Error(), string(model.RowEnvironments)) {
		t.Fatalf("error does not name the row: %v", err)
	}
}
