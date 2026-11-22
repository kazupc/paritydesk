package rows

import (
	"fmt"

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
	// "account_bound", "run_state", "max_time", "parent"), copied from
	// compat.RowSpec.Fields. Modules never hard-code on-disk field names.
	Fields map[string][]string
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
	// NeedsConfirmation is true when a side merged as a union because its
	// base was invalid.
	NeedsConfirmation bool
}

// Module merges one row.
type Module interface {
	Row() model.RowID
	Merge(in Input) (Output, error)
}

// Registry returns the module of every v0.1 row.
func Registry() map[model.RowID]Module {
	mods := []Module{
		environmentsModule{},
		sessionsModule{},
		keyedModule{row: model.RowTasks},
		waitingInputModule{},
		keyedModule{row: model.RowWorktrees},
		orderModule{row: model.RowGroupOrder},
		sidebarModule{},
		orderModule{row: model.RowPinnedOrder},
		keyedModule{row: model.RowAppPrefs, noFieldMerge: true},
		keyedModule{row: model.RowWindowState, noFieldMerge: true},
		mcpModule{},
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
