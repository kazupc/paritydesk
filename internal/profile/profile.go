package profile

import (
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/sources/configjson"
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
	// ScanErr is ScanErrUnreadable when ScanLenient could not identify the
	// folder (every other field but Name, Path and Active is then empty).
	ScanErr string
}

// Scanner lists and identifies folders.
type Scanner struct {
	FS           fsx.Ops
	DataRoot     string
	ReadIdentity IdentityReader
	ListReplicas ReplicaLister
}

// Binding is a profile resolved to its folder.
type Binding struct {
	Profile config.Profile
	Folder  *Folder // nil if the folder is missing
	// Replica is the profile's (account, org). Org comes from the single
	// session directory of the recorded account, or, for the active
	// folder, from Hints.ActiveOrg when several exist; empty when unknown
	// (per-org rows are then skipped, exit 32). A replica that differs from
	// ProfileState.Replica makes BaseValid false (base miss).
	Replica model.Replica
	// BaseValid reports whether state's bases for this profile still match
	// the folder (marker, file id, base hash).
	BaseValid      bool
	BaseInvalidWhy i18n.Msg
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
	// PendingAdd is set when root.PendingAdd is set and Claude/ holds no
	// registered account: the folder (nil when Claude/ is missing) is
	// accepted as the I1 exception for profile add instead of being an
	// error. It is not listed in Unregistered.
	PendingAdd *Folder
}

// Hints carry facts from outside the folders.
type Hints struct {
	// ActiveOrg is the org named by main.log (app.AppControl.ActiveOrg),
	// empty when unknown. It applies to the active folder only.
	ActiveOrg ids.OrgID
}

// NewMarker builds the marker written to a folder after apply (P7).
func NewMarker(alias ids.Alias, rep model.Replica, gen uint64, baseHash string) model.Marker {
	return model.Marker{Version: 1, Alias: alias, Replica: rep, Generation: gen, BaseHash: baseHash}
}

// ForeignTool is another account-switching tool found on this Mac.
type ForeignTool struct {
	Tool    string   `json:"tool"`
	Folders []string `json:"folders,omitempty"` // folder names under the data root
}
