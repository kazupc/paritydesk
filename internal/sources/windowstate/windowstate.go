package windowstate

import (
	"context"

	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/sources"
)

// WindowState is the adapter for row model.RowWindowState.
type WindowState struct{}

var _ sources.Adapter = WindowState{}

// New returns the adapter.
func New() sources.Adapter { return WindowState{} }

// Row implements sources.Adapter.
func (WindowState) Row() model.RowID { return model.RowWindowState }

// Read implements sources.Adapter.
func (WindowState) Read(ctx context.Context, rc sources.ReadContext) (*model.RowSnapshot, error) {
	return nil, pderr.ErrNotImplemented
}

// Ops implements sources.Adapter.
func (WindowState) Ops(ctx context.Context, oc sources.OpsContext, current, desired *model.RowSnapshot) (sources.OpsResult, error) {
	return sources.OpsResult{}, pderr.ErrNotImplemented
}
