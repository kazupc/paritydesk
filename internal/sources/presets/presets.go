package presets

import (
	"context"

	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/sources"
)

// Environments is the adapter for row model.RowEnvironments.
type Environments struct{}

var _ sources.Adapter = Environments{}

// New returns the adapter.
func New() sources.Adapter { return Environments{} }

// Row implements sources.Adapter.
func (Environments) Row() model.RowID { return model.RowEnvironments }

// Read implements sources.Adapter.
func (Environments) Read(ctx context.Context, rc sources.ReadContext) (*model.RowSnapshot, error) {
	return nil, pderr.ErrNotImplemented
}

// Ops implements sources.Adapter.
func (Environments) Ops(ctx context.Context, oc sources.OpsContext, current, desired *model.RowSnapshot) (sources.OpsResult, error) {
	return sources.OpsResult{}, pderr.ErrNotImplemented
}
