package rows

import (
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
)

// Side is one replica's input.
type Side struct {
	Alias   ids.Alias
	Replica model.Replica
	// Snapshot is the harvested row. nil means the row is absent or was
	// skipped (no information; never a removal).
	Snapshot *model.RowSnapshot
	// Base is this replica's base for the row; BaseValid is false when the
	// base is missing or invalid (union, no deletions, confirmation).
	Base      *model.RowSnapshot
	BaseValid bool
	// LiveEpoch is the epoch during which this replica was last live.
	LiveEpoch uint64
	// Contested holds, by item key, the items whose value P12 saw the app
	// replace after a write. Such an item counts as changed on this side
	// only if its hash differs from both contested hashes.
	Contested map[string]model.Contested
	// ToolStamps are app-time values paritydesk wrote on this replica.
	ToolStamps map[string]int64
	// Seeded: the base was seeded by profile adopt --from-legacy from
	// values every folder agreed on. A key absent from such a base merges
	// as under an invalid base (union: no deletion, no epoch rule) on both
	// sides.
	Seeded bool
}

// Policy carries the user's choices and the data boundary for one switch.
type Policy struct {
	TombstoneMode string   // "newer-wins" | "delete-wins"
	Exclude       []string // sync.exclude: project-path globs and session ids
	// LeavingExports: the leaving profile's edits in this row's category
	// may reach canonical. When false they stay in the leaving overlay.
	LeavingExports bool
	// EnteringExports: the entering profile's own edits since its base
	// (made outside paritydesk, or drift) may reach canonical. When false
	// they stay in the entering overlay and never reach canonical.
	EnteringExports bool
	// EnteringImports: canonical reaches the entering profile. When false
	// Desired is the entering side's own state (its overlay), unchanged.
	EnteringImports bool
	// LeavingManaged and EnteringManaged are the managed_by_org flags.
	LeavingManaged, EnteringManaged bool
	// SecretsMayCross applies sync.secrets; SecretsAllow lists
	// "<preset>/<VAR>" exceptions.
	SecretsMayCross bool
	SecretsAllow    map[string]bool
	// PermissionAcks are the entering folder's own acknowledgements, used
	// to cap permissionMode (D8).
	PermissionAcks map[string]bool
	// EnvRemap maps environment ids of one side to the other, produced by
	// the environments row and consumed by the sessions row (rows 7, 8).
	EnvRemap map[string]string
	// Fields are the row's manifest field lists by role ("per_field",
	// "account_bound", "grant", "run_state", "max_time", "parent",
	// "worktree" and the others named in engine.go), copied from
	// compat.RowSpec.Fields. Modules never hard-code on-disk field names.
	// A record field that no role lists comes from the winning record.
	Fields map[string][]string
	// Limits are the row's manifest limits (compat.RowSpec.Limits), copied by
	// the orchestrator. A module that enforces them keeps the target's value
	// for an item over a limit.
	Limits map[string]int
	// EnteringStoreMissing: the entering folder lacks the row's persisted
	// store, so nothing can be written there; the module keeps the target's
	// own values (sources.TargetStater).
	EnteringStoreMissing bool
	// EnteringSeeded: the entering folder's sidebar scope for its replica is
	// seeded, or its state could not be read (sources.TargetStater).
	EnteringSeeded bool
	// SeedFrom holds, for the sidebar-seed row only, the values derived from
	// the sidebar-view row's Desired of this switch (SeedValues), by item key.
	SeedFrom map[string]json.RawMessage
	// Scratch is the scratch row's policy; nil for every other row.
	Scratch *ScratchPolicy
}

// ScratchPolicy is the policy of row scratch (scratch workspaces).
type ScratchPolicy struct {
	PropagateDeletions bool
	Exclude            []string       // manifest exclude + config scratch.exclude
	HeldBack           []string       // dependency_dirs + vcs_dirs + cache_dirs
	ReservedGlobs      []string       // manifest tmp_recovery_globs
	Leaf               *regexp.Regexp // workspace folder names the app mints
	ConflictDate       string         // "YYYY-MM-DD", local date of the run clock
	Refs               []string       // the carry set (keys), sorted
	Referenced         []string       // every workspace key Desired sessions name (superset of Refs)
}

// ManagedEdge reports whether either side is managed_by_org.
func (p Policy) ManagedEdge() bool { return p.LeavingManaged || p.EnteringManaged }

// Input is a module's input.
type Input struct {
	Row       model.RowID
	Leaving   Side
	Entering  Side
	Canonical *model.RowSnapshot // nil on a first sync
	// LeavingOverlay and EnteringOverlay are each profile's private items
	// (D27); either may be nil.
	LeavingOverlay  *model.RowSnapshot
	EnteringOverlay *model.RowSnapshot
	Policy          Policy
}

// Output is a module's result.
type Output struct {
	Canonical *model.RowSnapshot
	// LeavingOverlay and EnteringOverlay are both profiles' overlays after
	// the merge. An overlay never loses an item unless its own side
	// removed it relative to a valid base.
	LeavingOverlay  *model.RowSnapshot
	EnteringOverlay *model.RowSnapshot
	// Desired is what the entering replica must hold after apply
	// (canonical ⊕ entering overlay when importing).
	Desired   *model.RowSnapshot
	Changes   []model.Change
	Conflicts []model.Conflict
	// EnvRemap is set by the environments row.
	EnvRemap map[string]string
	// Known lists every item key the row's inputs held: both sides, both
	// bases, canonical and both overlays, tombstones included. The
	// sessions row sets it so PostMerge can tell a removed session from
	// one this merge never saw (another org's or another account's
	// session in the same folder, or a remote session_ id).
	Known map[string]bool
	// NeedsConfirmation is true when a side merged as a union because its
	// base was invalid.
	NeedsConfirmation bool
}

// Module merges one row.
type Module interface {
	Row() model.RowID
	Merge(in Input) (Output, error)
}

// Registry returns the module of every row that has one.
func Registry() map[model.RowID]Module {
	mods := []Module{
		environmentsModule{},
		sessionsModule{},
		tasksModule{},
		waitingInputModule{},
		worktreesModule{},
		orderModule{row: model.RowGroupOrder},
		sidebarModule{},
		orderModule{row: model.RowPinnedOrder},
		keyedModule{row: model.RowAppPrefs, noFieldMerge: true},
		keyedModule{row: model.RowWindowState, noFieldMerge: true},
		mcpModule{},
		scratchModule{},

		collapseModule{}, seedModule{},

		keyedModule{row: model.RowPins, noFieldMerge: true},
	}
	m := make(map[model.RowID]Module, len(mods))
	for _, mod := range mods {
		m[mod.Row()] = mod
	}
	return m
}

// checkRow refuses an input meant for another row.
func checkRow(in Input, row model.RowID) error {
	if in.Row != "" && in.Row != row {
		return fmt.Errorf("rows: input for row %q given to module %q", in.Row, row)
	}
	return nil
}

// ExcludedSessionIDs returns the session ids that sync.exclude keeps
// private, judged by the sessions row's own rule (sessionExcluder: ids
// named in policy.Exclude, and records whose project path matches a glob,
// failing closed). policy is the sessions row's policy (its Exclude and
// Fields). Rows keyed by session id (pins) remove these keys from every
// merge input, so they never travel.
func ExcludedSessionIDs(snaps []*model.RowSnapshot, policy Policy) map[string]bool {
	excl := sessionExcluder(policy.Exclude, first(policy.Fields[RoleProjectPath]))
	out := map[string]bool{}
	for _, pat := range policy.Exclude {
		if excl(pat, nil) {
			out[pat] = true
		}
	}
	for _, s := range snaps {
		if s == nil {
			continue
		}
		for k, it := range s.Items {
			if excl(k, &it) {
				out[k] = true
			}
		}
	}
	return out
}
