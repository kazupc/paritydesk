package gen

import (
	"context"
	"time"

	"github.com/kazupc/paritydesk/internal/clock"
	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/store/kv"
)

// ManifestVersion is the generation manifest format version.
const ManifestVersion = 1

// EntryKind is the kind of a backed-up item.
type EntryKind string

// Entry kinds.
const (
	EntryFile       EntryKind = "file"       // a plain file's previous bytes
	EntryQuarantine EntryKind = "quarantine" // a file moved here instead of deleted
	EntryLSValue    EntryKind = "ls-value"   // a Local Storage value's previous bytes
	EntryConflict   EntryKind = "conflict"   // a merge loser kept for the record
)

// Entry is one item of a generation.
type Entry struct {
	Kind     EntryKind   `json:"kind"`
	Row      model.RowID `json:"row"`
	Path     string      `json:"path,omitempty"` // relative to the data folder
	Key      string      `json:"key,omitempty"`  // LS item name
	Present  bool        `json:"present"`        // existed before the run
	PreHash  string      `json:"pre"`
	PostHash string      `json:"post"`
	Stored   string      `json:"stored,omitempty"` // relative path inside the generation
}

// Manifest is generations/<n>/manifest.json.
type Manifest struct {
	Version  int           `json:"v"`
	N        uint64        `json:"n"`
	RunID    string        `json:"run"`
	Alias    ids.Alias     `json:"alias"`
	Replica  model.Replica `json:"replica"`
	Created  time.Time     `json:"created"`
	Entries  []Entry       `json:"entries"`
	LevelDB  string        `json:"leveldb,omitempty"` // clone directory, if kept
	Complete bool          `json:"complete"`
	// SHA256 maps stored relative paths to their hashes.
	SHA256 map[string]string `json:"sha256"`
}

// Info summarizes a generation for listings.
type Info struct {
	N        uint64    `json:"n"`
	RunID    string    `json:"run"`
	Alias    ids.Alias `json:"alias"`
	Created  time.Time `json:"created"`
	Entries  int       `json:"entries"`
	LevelDB  bool      `json:"leveldb"`
	Complete bool      `json:"complete"`
	Bytes    int64     `json:"bytes"`
}

// Store manages the generations directory.
type Store struct {
	dir   string
	fs    fsx.Ops
	clock clock.Clock
}

// Open returns a Store rooted at dir.
func Open(dir string, fs fsx.Ops, clk clock.Clock) *Store {
	return &Store{dir: dir, fs: fs, clock: clk}
}

// Writer builds one generation.
type Writer interface {
	// Dir returns the generation directory.
	Dir() string
	// QuarantineDir is where apply moves files instead of deleting them.
	QuarantineDir() string
	// SaveFile clones folder/rel into the generation (if it exists) and
	// records an EntryFile with its pre-hash and the planned post-hash.
	SaveFile(row model.RowID, folder, rel, postHash string) error
	// SaveLSValue records the previous raw bytes of a Local Storage item.
	SaveLSValue(row model.RowID, name string, raw []byte, present bool, postHash string) error
	// SaveConflict stores a merge loser's bytes.
	SaveConflict(row model.RowID, key string, raw []byte) error
	// CloneLevelDB clones the whole LevelDB directory (without LOCK).
	CloneLevelDB(dir string) error
	// Finish writes manifest.json with Complete=true and syncs it.
	Finish() (*Manifest, error)
	// Abort removes the incomplete generation.
	Abort() error
}

type writer struct {
	s *Store
	m Manifest
}

var _ Writer = (*writer)(nil)

// Begin creates generations/<n>/ (0700). n must be unused.
func (s *Store) Begin(n uint64, runID string, alias ids.Alias, rep model.Replica) (Writer, error) {
	return nil, pderr.ErrNotImplemented
}

// Dir returns the generation directory.
func (w *writer) Dir() string { return "" }

// QuarantineDir is where apply moves files instead of deleting them.
func (w *writer) QuarantineDir() string { return "" }

// SaveFile clones folder/rel into the generation (if it exists) and
// records an EntryFile with its pre-hash and the planned post-hash.
func (w *writer) SaveFile(row model.RowID, folder, rel, postHash string) error {
	return pderr.ErrNotImplemented
}

// SaveLSValue records the previous raw bytes of a Local Storage item.
func (w *writer) SaveLSValue(row model.RowID, name string, raw []byte, present bool, postHash string) error {
	return pderr.ErrNotImplemented
}

// SaveConflict stores a merge loser's bytes.
func (w *writer) SaveConflict(row model.RowID, key string, raw []byte) error {
	return pderr.ErrNotImplemented
}

// CloneLevelDB clones the whole LevelDB directory (without LOCK).
func (w *writer) CloneLevelDB(dir string) error { return pderr.ErrNotImplemented }

// Finish writes manifest.json with Complete=true and syncs it.
func (w *writer) Finish() (*Manifest, error) { return nil, pderr.ErrNotImplemented }

// Abort removes the incomplete generation.
func (w *writer) Abort() error { return pderr.ErrNotImplemented }

// List returns every generation, newest first.
func (s *Store) List() ([]Info, error) { return nil, pderr.ErrNotImplemented }

// Load returns a generation's manifest after checking its sha256 entries.
func (s *Store) Load(n uint64) (*Manifest, error) { return nil, pderr.ErrNotImplemented }

// DeleteIncomplete removes generations without a complete manifest (used
// by recover after an interrupted P6).
func (s *Store) DeleteIncomplete() ([]uint64, error) { return nil, pderr.ErrNotImplemented }

// PrunePolicy is the retention policy.
type PrunePolicy struct {
	Keep          int
	LevelDBClones int
	MaxBytes      int64
}

// Prune applies the policy: drop LevelDB clones beyond LevelDBClones, then
// whole generations beyond Keep or MaxBytes (oldest first).
func (s *Store) Prune(p PrunePolicy) error { return pderr.ErrNotImplemented }

// Purge removes every generation (backups purge).
func (s *Store) Purge() error { return pderr.ErrNotImplemented }

// RestoreInput describes a conditional restore.
type RestoreInput struct {
	N      uint64
	Folder string    // the folder to restore into; the app must be stopped
	LS     kv.Writer // the folder's Local Storage, opened writable
	TmpDir string    // for atomic file writes
	DryRun bool
}

// RestoreReport lists what was restored and what conflicted.
type RestoreReport struct {
	Restored  []Entry `json:"restored"`
	Conflicts []Entry `json:"conflicts"` // current hash != post-hash
}

// Restore restores every entry whose current hash equals its post-hash.
func (s *Store) Restore(ctx context.Context, in RestoreInput) (RestoreReport, error) {
	return RestoreReport{}, pderr.ErrNotImplemented
}

// RestoreLevelDB replaces the folder's LevelDB directory with the
// generation's full clone (used when apply fails after a writable open).
func (s *Store) RestoreLevelDB(ctx context.Context, n uint64, folder string) error {
	return pderr.ErrNotImplemented
}
