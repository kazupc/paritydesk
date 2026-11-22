package sidebar

import (
	"context"

	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/sources"
)

// GroupOrder is the adapter for row model.RowGroupOrder.
type GroupOrder struct{}

var _ sources.Adapter = GroupOrder{}

// NewGroupOrder returns the adapter.
func NewGroupOrder() sources.Adapter { return GroupOrder{} }

// Row implements sources.Adapter.
func (GroupOrder) Row() model.RowID { return model.RowGroupOrder }

// Read implements sources.Adapter.
func (GroupOrder) Read(ctx context.Context, rc sources.ReadContext) (*model.RowSnapshot, error) {
	return nil, pderr.ErrNotImplemented
}

// Ops implements sources.Adapter.
func (GroupOrder) Ops(ctx context.Context, oc sources.OpsContext, current, desired *model.RowSnapshot) (sources.OpsResult, error) {
	return sources.OpsResult{}, pderr.ErrNotImplemented
}

// SidebarView is the adapter for row model.RowSidebarView.
type SidebarView struct{}

var _ sources.Adapter = SidebarView{}

// NewSidebarView returns the adapter.
func NewSidebarView() sources.Adapter { return SidebarView{} }

// Row implements sources.Adapter.
func (SidebarView) Row() model.RowID { return model.RowSidebarView }

// Read implements sources.Adapter.
func (SidebarView) Read(ctx context.Context, rc sources.ReadContext) (*model.RowSnapshot, error) {
	return nil, pderr.ErrNotImplemented
}

// Ops implements sources.Adapter.
func (SidebarView) Ops(ctx context.Context, oc sources.OpsContext, current, desired *model.RowSnapshot) (sources.OpsResult, error) {
	return sources.OpsResult{}, pderr.ErrNotImplemented
}

// PinnedOrder is the adapter for row model.RowPinnedOrder.
type PinnedOrder struct{}

var _ sources.Adapter = PinnedOrder{}

// NewPinnedOrder returns the adapter.
func NewPinnedOrder() sources.Adapter { return PinnedOrder{} }

// Row implements sources.Adapter.
func (PinnedOrder) Row() model.RowID { return model.RowPinnedOrder }

// Read implements sources.Adapter.
func (PinnedOrder) Read(ctx context.Context, rc sources.ReadContext) (*model.RowSnapshot, error) {
	return nil, pderr.ErrNotImplemented
}

// Ops implements sources.Adapter.
func (PinnedOrder) Ops(ctx context.Context, oc sources.OpsContext, current, desired *model.RowSnapshot) (sources.OpsResult, error) {
	return sources.OpsResult{}, pderr.ErrNotImplemented
}
