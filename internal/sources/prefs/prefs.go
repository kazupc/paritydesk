package prefs

import (
	"context"

	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/sources"
)

// AppPrefs is the adapter for row model.RowAppPrefs.
type AppPrefs struct{}

var _ sources.Adapter = AppPrefs{}

// NewAppPrefs returns the adapter.
func NewAppPrefs() sources.Adapter { return AppPrefs{} }

// Row implements sources.Adapter.
func (AppPrefs) Row() model.RowID { return model.RowAppPrefs }

// Read implements sources.Adapter.
func (AppPrefs) Read(ctx context.Context, rc sources.ReadContext) (*model.RowSnapshot, error) {
	return nil, pderr.ErrNotImplemented
}

// Ops implements sources.Adapter.
func (AppPrefs) Ops(ctx context.Context, oc sources.OpsContext, current, desired *model.RowSnapshot) (sources.OpsResult, error) {
	return sources.OpsResult{}, pderr.ErrNotImplemented
}

// MCPServers is the adapter for row model.RowMCPServers.
type MCPServers struct{}

var _ sources.Adapter = MCPServers{}

// NewMCPServers returns the adapter.
func NewMCPServers() sources.Adapter { return MCPServers{} }

// Row implements sources.Adapter.
func (MCPServers) Row() model.RowID { return model.RowMCPServers }

// Read implements sources.Adapter.
func (MCPServers) Read(ctx context.Context, rc sources.ReadContext) (*model.RowSnapshot, error) {
	return nil, pderr.ErrNotImplemented
}

// Ops implements sources.Adapter.
func (MCPServers) Ops(ctx context.Context, oc sources.OpsContext, current, desired *model.RowSnapshot) (sources.OpsResult, error) {
	return sources.OpsResult{}, pderr.ErrNotImplemented
}

// PermissionAcks reads the folder's own permission-mode acknowledgement
// preferences as names and booleans only (never copied anywhere). The
// sessions merge uses them to cap permissionMode (D8, H11).
func PermissionAcks(ctx context.Context, folder string, r model.Replica) (map[string]bool, error) {
	return nil, pderr.ErrNotImplemented
}
