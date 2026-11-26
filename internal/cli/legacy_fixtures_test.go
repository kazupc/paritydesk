package cli

import (
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/orchestrator"
	"github.com/kazupc/paritydesk/internal/pderr"
)

func errDegraded() error {
	return pderr.New(pderr.Degraded, "profile adopt", "some leftovers were kept for now")
}

// The legacy fixtures use masked fake ids and generic folder names only.
const legacyHash = "0123456789abcdef0123456789abcdef.fedcba9876543210fedcba9876543210"

func legacyFolders(moved bool) []orchestrator.LegacyFolder {
	left := []orchestrator.LegacyLeftover{
		{Path: "claude-code-sessions/0000\u2026", Kind: "other_account", Owner: "work", OwnerFolder: "Claude", Files: 12, Bytes: 40960, Unique: 0, Action: "eligible"},
		{Path: "claude-code-sessions/0000\u2026/0000\u2026", Kind: "mixed_org_stub", Owner: "work", OwnerFolder: "Claude", Files: 1, Bytes: 98, Unique: -1, Action: "eligible"},
		{Path: "local-agent-mode-sessions/0000\u2026", Kind: "unrecognized", Files: 4, Bytes: 2048, Unique: -1, Action: "report_only", Why: "unrecognized"},
		{Path: "local-agent-mode-sessions/0000\u2026", Kind: "other_account", Owner: "work", OwnerFolder: "Claude", Files: 2, Bytes: 10, Unique: 1, Action: "report_only", Why: "unique_sessions"},
		{Path: "scratch-workspaces/0000\u2026", Kind: "other_account", Owner: "work", OwnerFolder: "Claude", Files: 3, Bytes: 0, Unique: -1, Action: "report_only", Why: "scratch"},
	}
	if moved {
		left[0].Action, left[0].Generation = "moved", 4
		left[1].Action, left[1].Why = "deferred", "in_use"
	}
	return []orchestrator.LegacyFolder{
		{Folder: "Claude", Active: true, Alias: "work", AliasSource: "flag", Account: "0000\u2026", Org: "0000\u2026", Status: "adopt"},
		{Folder: "Claude-Home", Alias: "home", AliasSource: "folder", Account: "0000\u2026", Org: "0000\u2026", Status: "adopt", Leftovers: left},
		{Folder: "Claude-Old", Account: "0000\u2026", Status: "skip", Reason: "duplicate", ReasonArg: "Claude-Home"},
	}
}

func legacySeed() orchestrator.LegacySeed {
	return orchestrator.LegacySeed{Seeded: true, Rows: []orchestrator.LegacySeedRow{
		{Row: model.RowEnvironments, Agreed: 3, Differing: 1, Seeded: true},
		{Row: model.RowSessions, Agreed: 310, Differing: 7, Seeded: true},
		{Row: model.RowSidebarView, Reason: "unreadable"},
		{Row: model.RowScratch, Reason: "no_adapter"},
	}}
}

func legacyDryFixture() *orchestrator.LegacyResult {
	return &orchestrator.LegacyResult{DryRun: true, Status: "ok", PlanHash: legacyHash,
		ApplyCommand: "paritydesk profile adopt --from-legacy --apply --legacy-alias Claude=work --legacy-plan " + legacyHash,
		Folders:      legacyFolders(false), Seed: legacySeed(), Hint: &orchestrator.LegacyHint{Used: false}, Moves: 2}
}

func legacyApplyFixture() *orchestrator.LegacyResult {
	r := &orchestrator.LegacyResult{Applied: true, Status: "ok", PlanHash: legacyHash, Folders: legacyFolders(false), Seed: legacySeed()}
	return r
}

func legacyDegradedFixture() *orchestrator.LegacyResult {
	r := &orchestrator.LegacyResult{Applied: true, Status: "ok", PlanHash: legacyHash, Folders: legacyFolders(true), Seed: legacySeed(),
		Generations: []orchestrator.LegacyGeneration{{N: 4, Alias: "home", Folder: "Claude-Home", Planned: 2, Moved: 1}},
		Moves:       1, MovesDeferred: 1, Warnings: []string{"marker:home"}, Quarantine: true}
	return r
}

func legacyNothingFixture() *orchestrator.LegacyResult {
	return &orchestrator.LegacyResult{DryRun: true, Status: "ok", PlanHash: legacyHash,
		Folders: []orchestrator.LegacyFolder{
			{Folder: "Claude", Active: true, Alias: "work", Account: "0000\u2026", Org: "0000\u2026", Status: "registered"},
			{Folder: "Claude-Home", Alias: "home", Account: "0000\u2026", Org: "0000\u2026", Status: "registered"},
		},
		Seed: orchestrator.LegacySeed{Reason: "has_bases"}}
}

func legacyNoActiveFixture() *orchestrator.LegacyResult {
	return &orchestrator.LegacyResult{DryRun: true, Status: "no_active", PlanHash: legacyHash,
		Folders:  []orchestrator.LegacyFolder{{Folder: "Claude-Home", Alias: "home", AliasSource: "folder", Account: "0000\u2026", Status: "adopt"}},
		Warnings: []string{"no_active"}}
}
