package gen

import (
	"time"

	"github.com/kazupc/paritydesk/internal/clock"
	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
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
	EntryDir        EntryKind = "dir"        // a directory the run created (row scratch)
	EntryLink       EntryKind = "link"       // a symlink the run created (row scratch)
)

// Entry is one item of a generation.
//
// PreHash and PostHash are model.HashBytes of the item's bytes: a plain
// file's content, or a Local Storage item's raw stored value (encoding
// flag included). An empty hash means the item is absent. PreHash is
// what the item was before the run; PostHash is what the run's plan
// writes (empty when the plan removes or quarantines the item).
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
	// ScratchRoot is the manifest's scratch root when the run created
	// directories of row scratch: Restore removes such a directory only
	// below it, also when the app's manifest is not at hand.
	ScratchRoot string `json:"scratch_root,omitempty"`
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
	// ScratchBytes are the bytes of row-scratch stored and quarantined
	// entries (moved to scratch-kept, not removed, when pruned).
	ScratchBytes int64 `json:"scratch_bytes"`
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
	// SaveCreated records a path the run will create (a directory or a
	// symlink of row scratch); it must not exist now.
	SaveCreated(kind EntryKind, row model.RowID, folder, rel, postHash string) error
	// SetScratchRoot records the manifest's scratch root (Manifest.ScratchRoot).
	SetScratchRoot(root string)
	// SaveConflict stores a merge loser's bytes.
	SaveConflict(row model.RowID, key string, raw []byte) error
	// CloneLevelDB clones the whole LevelDB directory (without LOCK).
	CloneLevelDB(dir string) error
	// Finish writes manifest.json with Complete=true and syncs it.
	Finish() (*Manifest, error)
	// Abort removes the incomplete generation (fsx.Ops.RemoveAll with the
	// generations directory as root).
	Abort() error
}
