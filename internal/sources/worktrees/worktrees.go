package worktrees

import (
	"context"

	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/sources"
)

// Worktrees is the adapter for row model.RowWorktrees.
type Worktrees struct{}

var _ sources.Adapter = Worktrees{}

// New returns the adapter.
func New() sources.Adapter { return Worktrees{} }

// Row implements sources.Adapter.
func (Worktrees) Row() model.RowID { return model.RowWorktrees }

// Read implements sources.Adapter.
func (Worktrees) Read(ctx context.Context, rc sources.ReadContext) (*model.RowSnapshot, error) {
	return nil, pderr.ErrNotImplemented
}

// Ops implements sources.Adapter.
func (Worktrees) Ops(ctx context.Context, oc sources.OpsContext, current, desired *model.RowSnapshot) (sources.OpsResult, error) {
	return sources.OpsResult{}, pderr.ErrNotImplemented
}

// Referenced returns the worktree paths that the session records in s
// reference (s is a sessions-row snapshot).
func Referenced(sessions *model.RowSnapshot) ([]string, error) {
	return nil, pderr.ErrNotImplemented
}

// LostRegistrations returns the worktree paths referenced by the leaving
// replica's sessions that the entering replica will not reference after
// apply. The orchestrator runs a read-only git status on each.
func LostRegistrations(leavingSessions, desiredSessions *model.RowSnapshot) ([]string, error) {
	return nil, pderr.ErrNotImplemented
}

// Guard exposes LostRegistrations behind a method for the orchestrator.
type Guard struct{}

// LostRegistrations calls the package function.
func (Guard) LostRegistrations(leavingSessions, desiredSessions *model.RowSnapshot) ([]string, error) {
	return LostRegistrations(leavingSessions, desiredSessions)
}
