package rows

import (
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
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
	// Contested is set when P12 saw the app replace what was written.
	Contested *model.Contested
	// ToolStamps are app-time values paritydesk wrote on this replica.
	ToolStamps map[string]int64
}

// Policy carries the user's choices and the data boundary for one switch.
type Policy struct {
	TombstoneMode string   // "newer-wins" | "delete-wins"
	Exclude       []string // sync.exclude: project-path globs and session ids
	// Export and Import say whether the row's category crosses from the
	// leaving profile and into the entering profile.
	Export, Import bool
	// ManagedEdge is true when either profile is managed_by_org.
	ManagedEdge bool
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
}

// Input is a module's input.
type Input struct {
	Row       model.RowID
	Leaving   Side
	Entering  Side
	Canonical *model.RowSnapshot // nil on a first sync
	Overlay   *model.RowSnapshot // the entering profile's overlay, may be nil
	Policy    Policy
}

// Output is a module's result.
type Output struct {
	Canonical *model.RowSnapshot
	// LeavingOverlay is the leaving profile's overlay after the harvest.
	LeavingOverlay *model.RowSnapshot
	// Desired is what the entering replica must hold after apply.
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
	m := map[model.RowID]Module{}
	for _, r := range model.AllRows {
		m[r] = pending{row: r}
	}
	return m
}

type pending struct{ row model.RowID }

func (p pending) Row() model.RowID            { return p.row }
func (p pending) Merge(Input) (Output, error) { return Output{}, pderr.ErrNotImplemented }
