package idb

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Spec says where the key lives and bounds the read (compat.IDBSpec after
// validation, through its Reader method).
type Spec struct {
	// Dir is the data-folder-relative LevelDB directory, for example
	// "IndexedDB/https_claude.ai_0.indexeddb.leveldb".
	Dir string
	// BackendPrefix is the shared prefix of the origin's IndexedDB
	// entries ("IndexedDB/https_claude.ai_0.indexeddb."); another entry
	// with it while Dir is absent means another storage backend.
	BackendPrefix string
	Database      string // "keyval-store"
	ObjectStore   string // "keyval"
	Key           string // "store:pin-state:dframe-starred-code"
	// Accepted Blink SSV and V8 serializer versions (inclusive ranges).
	BlinkMin, BlinkMax uint32
	V8Min, V8Max       uint32
	MaxValueBytes      int           // decoded value cap
	MaxCopyBytes       int64         // budget when files must be byte-copied
	MaxCloneBytes      int64         // hard budget even for APFS clones
	MaxFiles           int           // files in Dir
	MaxRead            time.Duration // time budget of one ReadString
}

// Value is the decoded record.
type Value struct {
	Text           string // the V8 string, as UTF-8
	Seq            uint64 // LevelDB sequence of the winning entry
	DB, OS         uint64
	DBFromMetadata bool   // DB resolved from the DatabaseName key
	OSFromMetadata bool   // OS resolved from the ObjectStoreNames key
	Wrapped        string // "" or "snappy"
	Blink, V8      uint32 // envelope versions seen
	TailDropped    bool   // a torn last record of the newest log was ignored
	Copied         bool   // the clone fell back to a byte copy
}

// Reader reads one key from the IndexedDB of a data folder.
type Reader func(ctx context.Context, folder string, spec Spec) (Value, error)

// Errors. Every error of this package wraps exactly one of them.
var (
	ErrDirAbsent   = errors.New("idb: directory not present")
	ErrKeyAbsent   = errors.New("idb: key not present") // no live put of the key
	ErrAmbiguous   = errors.New("idb: key under several stores")
	ErrUnsupported = errors.New("idb: unsupported format")
	ErrBlob        = fmt.Errorf("%w: value stored as a blob", ErrUnsupported)
	ErrVersion     = errors.New("idb: unknown serializer version")
	ErrTorn        = errors.New("idb: files changed while reading")
	ErrUnclean     = errors.New("idb: damaged record (unclean shutdown or damage)")
	ErrPending     = errors.New("idb: a transaction touching the key is not finished")
	ErrTooLarge    = errors.New("idb: over a size or time limit")
	ErrBackend     = errors.New("idb: another storage backend")
)

// Reason is the stable enum used in JSON and messages.
type Reason string

// Reasons. The last three are set by the pins adapter, not by this
// package.
const (
	ReasonDirAbsent   Reason = "dir_absent"
	ReasonKeyAbsent   Reason = "key_absent"
	ReasonAmbiguous   Reason = "ambiguous"
	ReasonUnsupported Reason = "unsupported"
	ReasonBlob        Reason = "blob_wrapped"
	ReasonVersion     Reason = "version"
	ReasonTorn        Reason = "torn"
	ReasonUnclean     Reason = "unclean"
	ReasonPending     Reason = "pending"
	ReasonTooLarge    Reason = "too_large"
	ReasonBackend     Reason = "backend"
	ReasonFormat      Reason = "format"      // adapter: JSON shape, persist version, ids
	ReasonStaleStore  Reason = "stale_store" // adapter: freshness probe failed
	ReasonOther       Reason = "other"       // I/O and anything else
)

// Reasons lists every Reason, in a fixed order.
var Reasons = []Reason{ReasonDirAbsent, ReasonKeyAbsent, ReasonAmbiguous, ReasonUnsupported, ReasonBlob,
	ReasonVersion, ReasonTorn, ReasonUnclean, ReasonPending, ReasonTooLarge, ReasonBackend, ReasonFormat,
	ReasonStaleStore, ReasonOther}

// ReasonOf maps any error of this package (wrapped or not) to a Reason;
// ErrBlob is checked before ErrUnsupported.
func ReasonOf(err error) Reason {
	for _, c := range []struct {
		err error
		r   Reason
	}{
		{ErrBlob, ReasonBlob}, {ErrUnsupported, ReasonUnsupported}, {ErrDirAbsent, ReasonDirAbsent},
		{ErrKeyAbsent, ReasonKeyAbsent}, {ErrAmbiguous, ReasonAmbiguous}, {ErrVersion, ReasonVersion},
		{ErrTorn, ReasonTorn}, {ErrUnclean, ReasonUnclean}, {ErrPending, ReasonPending},
		{ErrTooLarge, ReasonTooLarge}, {ErrBackend, ReasonBackend},
	} {
		if errors.Is(err, c.err) {
			return c.r
		}
	}
	return ReasonOther
}

// fail wraps a sentinel with the step that found it. detail may name a
// base file name; it never holds key or value bytes.
func fail(step string, sentinel error) error {
	return fmt.Errorf("idb: %s: %w", step, sentinel)
}
