package state

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/kazupc/paritydesk/internal/clock"
	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
)

// RootVersion is the root.json schema version.
const RootVersion = 1

// Ref is the sha256 (hex) of an object.
type Ref string

// ProfileState is what state remembers about one profile's folder.
type ProfileState struct {
	Alias ids.Alias `json:"alias"`
	// Replica is the profile's current (account, org); Replicas lists every
	// replica seen in the folder. Bases are kept per replica, so a change
	// of the active org is a base miss (union and confirmation), never a
	// set of edits (D6, M7).
	Replica  model.Replica   `json:"replica"`
	Replicas []model.Replica `json:"replicas,omitempty"`
	// FolderID and MarkerBase tie the bases to the physical folder (D4).
	FolderID   fsx.FileID `json:"folder_id"`
	MarkerBase string     `json:"marker_base"`
	// LiveEpoch is the epoch during which this profile was last active.
	LiveEpoch uint64 `json:"live_epoch"`
	// AccountHistory lists every account seen in the folder, for native
	// multi-account detection.
	AccountHistory []ids.AccountID `json:"account_history,omitempty"`
}

// BaseKey identifies a base, an overlay, tool stamps and contested
// entries: one (profile, replica, row).
type BaseKey struct {
	Alias   ids.Alias     `json:"alias"`
	Replica model.Replica `json:"replica"`
	Row     model.RowID   `json:"row"`
}

// IntentName names a recorded step of a run.
type IntentName string

// Intents. A run is closed in the same commit as its last step (P10 for
// switch and sync), so there is no "committed" or "launched" intent;
// launching and verifying happen after the run is closed.
const (
	IntentQuit     IntentName = "quit"      // P3 (also the --allow-update quit), before the Apple Event
	IntentPlanned  IntentName = "planned"   // P5
	IntentBackedUp IntentName = "backed_up" // P6
	IntentApplied  IntentName = "applied"   // P7
	IntentSwapping IntentName = "swapping"  // P8, before renamex_np
	IntentSwapped  IntentName = "swapped"   // P8, after
	// IntentSwapBack: P8 post-swap check found an intruder; swapping back.
	IntentSwapBack IntentName = "swap_back"
	// The journaled two-rename fallback when RENAME_SWAP is unsupported:
	// first Claude -> Claude-<S folder> (no Claude/ exists between the two
	// steps), then Claude-<T folder> -> Claude.
	IntentFallbackSParked IntentName = "fallback_s_parked"
	IntentFallbackTPlaced IntentName = "fallback_t_placed"
	IntentParked          IntentName = "parked" // P9
	// IntentRestoring: rollback (RunRollback) is restoring a generation.
	IntentRestoring IntentName = "restoring"
	// IntentAddParked: profile add (RunAdd) moved Claude/ to the active
	// profile's parked folder so the app starts on an empty folder.
	IntentAddParked IntentName = "add_parked"
)

// Intent is one recorded step. Done is false between "before" and "after".
type Intent struct {
	Name IntentName        `json:"name"`
	Done bool              `json:"done"`
	At   time.Time         `json:"at"`
	Data map[string]string `json:"data,omitempty"`
}

// RunKind is the kind of an open run.
type RunKind string

// Run kinds.
const (
	RunSwitch   RunKind = "switch"
	RunSync     RunKind = "sync"
	RunRollback RunKind = "rollback"
	RunAdd      RunKind = "profile-add"
)

// Run is an open (uncommitted) run.
type Run struct {
	ID         string    `json:"id"`
	Kind       RunKind   `json:"kind"`
	From       ids.Alias `json:"from"`
	To         ids.Alias `json:"to"`
	Generation uint64    `json:"generation,omitempty"`
	Started    time.Time `json:"started"`
	// PlanHash and PostHashes are recorded with the "planned" intent; recover
	// restores T's bases only if every post-hash still matches (C1).
	PlanHash   string            `json:"plan_hash,omitempty"`
	PostHashes map[string]string `json:"post_hashes,omitempty"`
	// PendingBases are the bases to commit at P10, by row.
	PendingBases map[model.RowID]Ref `json:"pending_bases,omitempty"`
	Intents      []Intent            `json:"intents"`
	// QuitByUs records that paritydesk quit the app, so an abort relaunches.
	// It is committed with the quit intent, before the Apple Event.
	QuitByUs bool `json:"quit_by_us"`
	// ConfirmedPlan is the hash of the plan the user confirmed outside the
	// run (switch --confirmed-plan), for the log and result file.
	ConfirmedPlan string `json:"confirmed_plan,omitempty"`
}

// PendingAdd records a profile add whose new account has not signed in
// yet. This is the one documented exception to I1: until the sign-in,
// Claude/ may be empty or unidentified. The next command that finds an
// identity in Claude/ binds it to Alias (after confirmation) and clears
// PendingAdd; `recover` can instead move the parked profile back.
type PendingAdd struct {
	Alias   ids.Alias `json:"alias"`
	Managed bool      `json:"managed_by_org"`
	// ParkedFrom is the profile whose folder was parked for the add.
	ParkedFrom ids.Alias `json:"parked_from"`
	Started    time.Time `json:"started"`
}

// Last returns the most recent intent, or nil.
func (r *Run) Last() *Intent {
	if r == nil || len(r.Intents) == 0 {
		return nil
	}
	return &r.Intents[len(r.Intents)-1]
}

// GenerationRef records a committed generation.
type GenerationRef struct {
	N      uint64    `json:"n"`
	RunID  string    `json:"run"`
	Target ids.Alias `json:"target"`
	At     time.Time `json:"at"`
}

// Root is root.json.
type Root struct {
	Version  int                         `json:"v"`
	DataRoot string                      `json:"data_root"` // binds state to one data root (two-Mac rule)
	Epoch    uint64                      `json:"epoch"`
	Active   ids.Alias                   `json:"active,omitempty"`
	Previous ids.Alias                   `json:"previous,omitempty"`
	Profiles map[ids.Alias]*ProfileState `json:"profiles"`
	// Bases are per (profile, replica, row); a missing entry means no valid
	// base.
	Bases     map[string]Ref      `json:"bases"` // key: BaseKey.String()
	Canonical map[model.RowID]Ref `json:"canonical"`
	Overlays  map[string]Ref      `json:"overlays"` // key: BaseKey.String()
	// Contested is keyed by ContestedKey(BaseKey, item key).
	Contested map[string]model.Contested `json:"contested,omitempty"`
	// ToolStamps are app-time values paritydesk wrote, per BaseKey.String().
	ToolStamps map[string]map[string]int64 `json:"tool_stamps,omitempty"`
	// TombstoneLedger maps "row/key" to the aliases that have applied it;
	// an entry is dropped only when every registered profile has (D9).
	TombstoneLedger map[string][]ids.Alias `json:"tombstone_ledger,omitempty"`
	Run             *Run                   `json:"run,omitempty"`
	Generations     []GenerationRef        `json:"generations,omitempty"`
	// LastVerify is the outcome of the last P12, for status and the
	// SessionStart hook.
	LastVerify *VerifyRecord `json:"last_verify,omitempty"`
	// PendingAdd is set between a profile add and the new account's first
	// sign-in.
	PendingAdd *PendingAdd `json:"pending_add,omitempty"`
}

// VerifyRecord is the stored outcome of a verify.
type VerifyRecord struct {
	RunID   string        `json:"run"`
	At      time.Time     `json:"at"`
	Drift   []model.RowID `json:"drift,omitempty"`
	SignIn  bool          `json:"sign_in_needed,omitempty"`
	Focused bool          `json:"session_focused,omitempty"`
}

// String returns "alias/account/org/row".
func (k BaseKey) String() string {
	return string(k.Alias) + "/" + k.Replica.Key() + "/" + string(k.Row)
}

// ContestedKey returns the Root.Contested key of one item.
func ContestedKey(k BaseKey, item string) string { return k.String() + "/" + item }

// NewRoot returns an empty root bound to dataRoot.
func NewRoot(dataRoot string) *Root {
	return &Root{
		Version:   RootVersion,
		DataRoot:  dataRoot,
		Profiles:  map[ids.Alias]*ProfileState{},
		Bases:     map[string]Ref{},
		Canonical: map[model.RowID]Ref{},
		Overlays:  map[string]Ref{},
	}
}

// Store reads and writes state.
type Store struct {
	dir      string
	lockFile string
	fs       fsx.Ops
	clock    clock.Clock
}

// LockFile returns the run lock path for a data root:
// <supportDir>/locks/<first 16 hex digits of sha256(dataRoot)>.lock. The
// lock is keyed to the data root, not to the state directory, so two
// invocations with different --state-dir values on the same data root
// still exclude each other.
func LockFile(supportDir, dataRoot string) string {
	return filepath.Join(supportDir, "locks", model.HashBytes([]byte(filepath.Clean(dataRoot)))[:16]+".lock")
}

// Open returns a Store rooted at dir that locks lockFile (see LockFile).
// It does not create anything: the state directory is created (0700) by
// the first Commit and the locks directory by the first Lock, so read-only
// commands never write.
func Open(dir, lockFile string, fs fsx.Ops, clk clock.Clock) (*Store, error) {
	switch {
	case dir == "" || !filepath.IsAbs(dir):
		return nil, fmt.Errorf("state: open: state directory must be an absolute path")
	case lockFile == "" || !filepath.IsAbs(lockFile):
		return nil, fmt.Errorf("state: open: lock file must be an absolute path")
	case fs == nil || clk == nil:
		return nil, fmt.Errorf("state: open: filesystem and clock are required")
	}
	for _, p := range []string{dir, lockFile} {
		if err := fsx.CheckTestPath(p); err != nil {
			return nil, fmt.Errorf("state: open: %w", err)
		}
	}
	return &Store{dir: filepath.Clean(dir), lockFile: filepath.Clean(lockFile), fs: fs, clock: clk}, nil
}

// Dir returns the state directory.
func (s *Store) Dir() string { return s.dir }
