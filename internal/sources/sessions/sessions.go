package sessions

import (
	"context"

	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/sources"
)

// Sessions is the adapter for row model.RowSessions.
type Sessions struct{}

var _ sources.Adapter = Sessions{}

// NewSessions returns the adapter.
func NewSessions() sources.Adapter { return Sessions{} }

// Row implements sources.Adapter.
func (Sessions) Row() model.RowID { return model.RowSessions }

// Read implements sources.Adapter.
func (Sessions) Read(ctx context.Context, rc sources.ReadContext) (*model.RowSnapshot, error) {
	return nil, pderr.ErrNotImplemented
}

// Ops implements sources.Adapter.
func (Sessions) Ops(ctx context.Context, oc sources.OpsContext, current, desired *model.RowSnapshot) (sources.OpsResult, error) {
	return sources.OpsResult{}, pderr.ErrNotImplemented
}

// Tasks is the adapter for row model.RowTasks.
type Tasks struct{}

var _ sources.Adapter = Tasks{}

// NewTasks returns the adapter.
func NewTasks() sources.Adapter { return Tasks{} }

// Row implements sources.Adapter.
func (Tasks) Row() model.RowID { return model.RowTasks }

// Read implements sources.Adapter.
func (Tasks) Read(ctx context.Context, rc sources.ReadContext) (*model.RowSnapshot, error) {
	return nil, pderr.ErrNotImplemented
}

// Ops implements sources.Adapter.
func (Tasks) Ops(ctx context.Context, oc sources.OpsContext, current, desired *model.RowSnapshot) (sources.OpsResult, error) {
	return sources.OpsResult{}, pderr.ErrNotImplemented
}

// WaitingInput is the adapter for row model.RowWaitingInput.
type WaitingInput struct{}

var _ sources.Adapter = WaitingInput{}

// NewWaitingInput returns the adapter.
func NewWaitingInput() sources.Adapter { return WaitingInput{} }

// Row implements sources.Adapter.
func (WaitingInput) Row() model.RowID { return model.RowWaitingInput }

// Read implements sources.Adapter.
func (WaitingInput) Read(ctx context.Context, rc sources.ReadContext) (*model.RowSnapshot, error) {
	return nil, pderr.ErrNotImplemented
}

// Ops implements sources.Adapter.
func (WaitingInput) Ops(ctx context.Context, oc sources.OpsContext, current, desired *model.RowSnapshot) (sources.OpsResult, error) {
	return sources.OpsResult{}, pderr.ErrNotImplemented
}
