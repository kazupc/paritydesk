package profile

import (
	"context"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/sources/configjson"
	"github.com/kazupc/paritydesk/internal/state"
)

// IdentityReader reads a folder's identity; configjson.ReadIdentity in
// production, a fake in tests.
type IdentityReader func(folder string) (configjson.Identity, error)

// ReplicaLister lists a folder's (account, org) session directories;
// sources.DiscoverReplicas in production.
type ReplicaLister func(folder string) ([]model.Replica, error)

// Folder is one Claude or Claude-* directory under the data root.
type Folder struct {
	Name        string               // "Claude" or "Claude-<x>"
	Path        string               // absolute
	Active      bool                 // Name == "Claude"
	Identity    *configjson.Identity // nil if unreadable
	IdentityErr string               // constant text when Identity is nil
	Replicas    []model.Replica      // session directories found
	Marker      *model.Marker        // nil if absent or unparsable
	ID          fsx.FileID
}

// Scanner lists and identifies folders.
type Scanner struct {
	FS           fsx.Ops
	DataRoot     string
	ReadIdentity IdentityReader
	ListReplicas ReplicaLister
}

// Scan lists Claude and Claude-* directories (not symlinks) and identifies
// each. It never writes.
func (s Scanner) Scan(ctx context.Context) ([]Folder, error) {
	return nil, pderr.ErrNotImplemented
}

// Binding is a profile resolved to its folder.
type Binding struct {
	Profile config.Profile
	Folder  *Folder // nil if the folder is missing
	// Replica is the profile's (account, org). Org comes from the single
	// session directory of the recorded account, or from main.log when
	// several exist; empty when unknown (per-org rows are then skipped,
	// exit 32).
	Replica model.Replica
	// BaseValid reports whether state's bases for this profile still match
	// the folder (marker, file id, base hash).
	BaseValid      bool
	BaseInvalidWhy string
}

// Resolution is the full mapping for one run.
type Resolution struct {
	Active   *Binding // the profile in Claude/
	Bindings map[ids.Alias]*Binding
	// Unregistered lists folders that belong to no profile.
	Unregistered []Folder
	// OutsideSwitch is true when the active identity differs from state's
	// recorded active profile (a switch made outside paritydesk; K21).
	OutsideSwitch bool
}

// Resolve binds every configured profile to a folder by identity and
// checks markers against state. An alias whose folder holds a different
// account gives *pderr.Error IdentityMismatch (exit 20); the message never
// suggests signing out.
func Resolve(cfg *config.Config, root *state.Root, folders []Folder) (*Resolution, error) {
	return nil, pderr.ErrNotImplemented
}

// NewMarker builds the marker written to a folder after apply (P7).
func NewMarker(alias ids.Alias, rep model.Replica, gen uint64, baseHash string) model.Marker {
	return model.Marker{Version: 1, Alias: alias, Replica: rep, Generation: gen, BaseHash: baseHash}
}

// ReadMarker reads and parses a folder's marker.
func ReadMarker(fs fsx.Ops, folder string) (*model.Marker, error) {
	return nil, pderr.ErrNotImplemented
}

// WriteMarker sets a folder's marker.
func WriteMarker(fs fsx.Ops, folder string, m model.Marker) error {
	return pderr.ErrNotImplemented
}

// ForeignTool is another account-switching tool found on this Mac.
type ForeignTool struct {
	Tool    string   `json:"tool"`
	Folders []string `json:"folders,omitempty"` // folder names under the data root
}

// DetectForeign matches folders and home paths against manifest footprints.
func DetectForeign(folders []Folder, home string, fps []compat.Footprint) ([]ForeignTool, error) {
	return nil, pderr.ErrNotImplemented
}

// NativeMultiAccount reports signs of the app's own multi-account mode: a
// change of the recorded account inside one folder's history, or the
// manifest's main.log signal (checked by the caller).
func NativeMultiAccount(ps *state.ProfileState, current ids.AccountID) bool { return false }
