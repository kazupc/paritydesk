package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"

	"github.com/kazupc/paritydesk/internal/model"
)

// SnapshotSchema is the only row snapshot schema this binary reads.
// Objects with any other schema are reported with ErrUnknownSchema, and
// callers treat the base as missing (M8).
const SnapshotSchema = 1

// Errors returned by the object store.
var (
	// ErrBadRef is returned for a ref that is not a sha256 hex string.
	ErrBadRef = errors.New("state: malformed object ref")
	// ErrCorruptObject is returned when an object's bytes do not hash to
	// its ref.
	ErrCorruptObject = errors.New("state: object does not match its hash")
	// ErrUnknownSchema is returned for a snapshot schema this binary
	// cannot read; the base is then invalid.
	ErrUnknownSchema = errors.New("state: unknown snapshot schema")
	// ErrSecretInValue is returned by PutSnapshot when a Secret item's
	// Value still contains its raw bytes.
	ErrSecretInValue = errors.New("state: secret item value holds its raw bytes")
)

var refRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (r Ref) valid() bool { return refRE.MatchString(string(r)) }

func (s *Store) objectsDir() string { return filepath.Join(s.dir, objectsDir) }

// PutSnapshot stores a row snapshot as an object and returns its ref.
// Secret items are stored with their hashed values only: Raw and Stale are
// never serialized, and a Secret item whose Value contains its Raw bytes
// is refused.
func (s *Store) PutSnapshot(snap *model.RowSnapshot) (Ref, error) {
	if snap == nil {
		return "", errors.New("state: put snapshot: nil snapshot")
	}
	if err := checkSecrets(snap); err != nil {
		return "", err
	}
	b, err := json.Marshal(snap)
	if err != nil {
		return "", fmt.Errorf("state: encode snapshot of %s: %w", snap.Row, err)
	}
	ref := Ref(model.HashBytes(b))
	rel := filepath.Join(objectsDir, string(ref))
	if old, err := os.ReadFile(filepath.Join(s.dir, rel)); err == nil && bytes.Equal(old, b) {
		return ref, nil // content-addressed: already stored intact
	}
	if err := s.ensureDir(); err != nil {
		return "", err
	}
	if err := s.fs.WriteFileAtomic(s.dir, rel, b, filePerm, s.tmpDir()); err != nil {
		return "", fmt.Errorf("state: write object: %w", err)
	}
	if err := s.fs.FullSync(s.objectsDir()); err != nil {
		return "", fmt.Errorf("state: sync objects: %w", err)
	}
	return ref, nil
}

// checkSecrets refuses a snapshot whose Secret items still carry their
// raw bytes in Value.
func checkSecrets(snap *model.RowSnapshot) error {
	for _, k := range snap.Keys() {
		it := snap.Items[k]
		if it.Secret && len(it.Raw) > 0 && bytes.Contains(it.Value, it.Raw) {
			return fmt.Errorf("state: put snapshot of %s: %w", snap.Row, ErrSecretInValue)
		}
	}
	return nil
}

// GetSnapshot loads a snapshot object after checking its hash. Unknown
// schema versions are reported with ErrUnknownSchema (the base is then
// treated as missing, M8); there is no older schema to migrate yet.
func (s *Store) GetSnapshot(ref Ref) (*model.RowSnapshot, error) {
	if !ref.valid() {
		return nil, ErrBadRef
	}
	// Objects are content-addressed, so a parsed one stays right for the
	// life of the process while its file is unchanged (same inode, size and
	// modification time, so tampering is still caught); a switch reads the
	// same bases at P2 and P5. Callers get their own maps, as they may edit
	// what they get; item values are never mutated in place.
	path := filepath.Join(s.objectsDir(), string(ref))
	st, statOK := objectStat(path)
	snapMu.Lock()
	cached, ok := snapCache[ref]
	snapMu.Unlock()
	if ok && statOK && cached.st == st {
		return shallowClone(cached.snap), nil
	}
	snap, err := s.readSnapshot(ref)
	if err != nil {
		return nil, err
	}
	if st2, ok := objectStat(path); statOK && ok && st2 == st {
		snapMu.Lock()
		if len(snapCache) >= snapCacheLimit {
			snapCache = map[Ref]snapEntry{}
		}
		snapCache[ref] = snapEntry{st: st, snap: shallowClone(snap)}
		snapMu.Unlock()
	}
	return snap, nil
}

type snapEntry struct {
	st   fileStat
	snap *model.RowSnapshot
}

type fileStat struct {
	size, mtime int64
	ino         uint64
}

func objectStat(path string) (fileStat, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStat{}, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fileStat{}, false
	}
	return fileStat{size: fi.Size(), mtime: fi.ModTime().UnixNano(), ino: uint64(st.Ino)}, true
}

// shallowClone copies a snapshot's maps and slices but shares item values.
func shallowClone(s *model.RowSnapshot) *model.RowSnapshot {
	c := *s
	if s.Items != nil {
		c.Items = make(map[string]model.Item, len(s.Items))
		for k, v := range s.Items {
			c.Items[k] = v
		}
	}
	if s.Tombstones != nil {
		c.Tombstones = make(map[string]model.Tombstone, len(s.Tombstones))
		for k, v := range s.Tombstones {
			c.Tombstones[k] = v
		}
	}
	c.Order = append([]string(nil), s.Order...)
	c.Stale = append([]string(nil), s.Stale...)
	return &c
}

const snapCacheLimit = 256

var (
	snapMu    sync.Mutex
	snapCache = map[Ref]snapEntry{}
)

func (s *Store) readSnapshot(ref Ref) (*model.RowSnapshot, error) {
	b, err := os.ReadFile(filepath.Join(s.objectsDir(), string(ref)))
	if err != nil {
		return nil, fmt.Errorf("state: read object %s: %w", ref, err)
	}
	if model.HashBytes(b) != string(ref) {
		return nil, fmt.Errorf("state: object %s: %w", ref, ErrCorruptObject)
	}
	snap := &model.RowSnapshot{}
	if err := json.Unmarshal(b, snap); err != nil {
		return nil, fmt.Errorf("state: parse object %s: %w", ref, err)
	}
	if snap.Schema != SnapshotSchema {
		return nil, fmt.Errorf("state: object %s has schema %d: %w", ref, snap.Schema, ErrUnknownSchema)
	}
	return snap, nil
}

// referenced returns every object ref r points to: bases, canonical,
// overlays (of every profile) and the open run's pending bases.
func referenced(r *Root) map[Ref]bool {
	keep := map[Ref]bool{}
	for _, ref := range r.Bases {
		keep[ref] = true
	}
	for _, ref := range r.Canonical {
		keep[ref] = true
	}
	for _, ref := range r.Overlays {
		keep[ref] = true
	}
	if r.Run != nil {
		for _, ref := range r.Run.PendingBases {
			keep[ref] = true
		}
	}
	return keep
}

// GC removes objects not referenced by r. Files in objects/ whose names
// are not refs are left alone.
func (s *Store) GC(r *Root) error {
	if r == nil {
		return errors.New("state: gc: nil root")
	}
	entries, err := os.ReadDir(s.objectsDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("state: gc: list objects: %w", err)
	}
	keep := referenced(r)
	for _, e := range entries {
		ref := Ref(e.Name())
		if !ref.valid() || keep[ref] {
			continue
		}
		if err := s.fs.RemoveAll(s.objectsDir(), e.Name()); err != nil {
			return fmt.Errorf("state: gc: remove object: %w", err)
		}
	}
	return nil
}
