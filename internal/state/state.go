package state

import (
	"time"

	"github.com/kazupc/paritydesk/internal/clock"
	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
)

// RootVersion is the root.json schema version.
const RootVersion = 1

// Ref is the sha256 (hex) of an object.
type Ref string

// ProfileState is what state remembers about one profile's folder.
type ProfileState struct {
	Alias   ids.Alias     `json:"alias"`
	Replica model.Replica `json:"replica"`
	// FolderID and MarkerBase tie the bases to the physical folder (D4).
	FolderID   fsx.FileID `json:"folder_id"`
	MarkerBase string     `json:"marker_base"`
	// LiveEpoch is the epoch during which this profile was last active.
	LiveEpoch uint64 `json:"live_epoch"`
	// AccountHistory lists every account seen in the folder, for native
	// multi-account detection.
	AccountHistory []ids.AccountID `json:"account_history,omitempty"`
}

// BaseKey identifies a base: one (profile, row).
type BaseKey struct {
	Alias ids.Alias   `json:"alias"`
	Row   model.RowID `json:"row"`
}

// IntentName names a recorded step of a run.
type IntentName string

// Intents, in phase order.
const (
	IntentQuit      IntentName = "quit"      // P3
	IntentPlanned   IntentName = "planned"   // P5
	IntentBackedUp  IntentName = "backed_up" // P6
	IntentApplied   IntentName = "applied"   // P7
	IntentSwapping  IntentName = "swapping"  // P8, before renamex_np
	IntentSwapped   IntentName = "swapped"   // P8, after
	IntentParked    IntentName = "parked"    // P9
	IntentCommitted IntentName = "committed" // P10
	IntentLaunched  IntentName = "launched"  // P11
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
	QuitByUs bool `json:"quit_by_us"`
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
	// Bases are per (profile, row); a missing entry means no valid base.
	Bases     map[string]Ref             `json:"bases"` // key: BaseKey.String()
	Canonical map[model.RowID]Ref        `json:"canonical"`
	Overlays  map[string]Ref             `json:"overlays"` // key: BaseKey.String()
	Contested map[string]model.Contested `json:"contested,omitempty"`
	// ToolStamps are app-time values paritydesk wrote, per (profile,row).
	ToolStamps map[string]map[string]int64 `json:"tool_stamps,omitempty"`
	// TombstoneLedger maps "row/key" to the aliases that have applied it;
	// an entry is dropped only when every registered profile has (D9).
	TombstoneLedger map[string][]ids.Alias `json:"tombstone_ledger,omitempty"`
	Run             *Run                   `json:"run,omitempty"`
	Generations     []GenerationRef        `json:"generations,omitempty"`
	// LastVerify is the outcome of the last P12, for status and the
	// SessionStart hook.
	LastVerify *VerifyRecord `json:"last_verify,omitempty"`
}

// VerifyRecord is the stored outcome of a verify.
type VerifyRecord struct {
	RunID   string        `json:"run"`
	At      time.Time     `json:"at"`
	Drift   []model.RowID `json:"drift,omitempty"`
	SignIn  bool          `json:"sign_in_needed,omitempty"`
	Focused bool          `json:"session_focused,omitempty"`
}

// String returns "alias/row".
func (k BaseKey) String() string { return string(k.Alias) + "/" + string(k.Row) }

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
	dir   string
	fs    fsx.Ops
	clock clock.Clock
}

// Open returns a Store rooted at dir, creating it with mode 0700.
func Open(dir string, fs fsx.Ops, clk clock.Clock) (*Store, error) {
	return &Store{dir: dir, fs: fs, clock: clk}, nil
}

// Dir returns the state directory.
func (s *Store) Dir() string { return s.dir }

// Load reads root.json. A missing file yields (nil, nil); a root written
// by a newer binary is refused; older versions are migrated in memory
// after keeping a pre-migration copy.
func (s *Store) Load() (*Root, error) { return nil, pderr.ErrNotImplemented }

// Commit atomically replaces root.json with r.
func (s *Store) Commit(r *Root) error { return pderr.ErrNotImplemented }

// Begin appends a not-done intent to r.Run and commits.
func (s *Store) Begin(r *Root, name IntentName, data map[string]string) error {
	return pderr.ErrNotImplemented
}

// Done marks the last intent named name as done and commits.
func (s *Store) Done(r *Root, name IntentName) error { return pderr.ErrNotImplemented }

// PutSnapshot stores a row snapshot as an object and returns its ref.
// Secret items are stored with their hashed values only.
func (s *Store) PutSnapshot(snap *model.RowSnapshot) (Ref, error) {
	return "", pderr.ErrNotImplemented
}

// GetSnapshot loads a snapshot object. Unknown schema versions are migrated
// or reported as invalid (the base is then treated as missing, M8).
func (s *Store) GetSnapshot(ref Ref) (*model.RowSnapshot, error) {
	return nil, pderr.ErrNotImplemented
}

// GC removes objects not referenced by r.
func (s *Store) GC(r *Root) error { return pderr.ErrNotImplemented }

// Lock is the held run lock.
type Lock struct{ release func() error }

// Release releases the lock.
func (l *Lock) Release() error {
	if l == nil || l.release == nil {
		return nil
	}
	return l.release()
}

// Lock takes run.lock (flock on a 0600 file next to the state dir). A held
// lock gives *pderr.Error with code RunLockBusy. The artifacts commands of
// later versions share this lock.
func (s *Store) Lock() (*Lock, error) { return nil, pderr.ErrNotImplemented }
