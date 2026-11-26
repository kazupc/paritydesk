package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
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
	if bytes.HasPrefix(b, objectPrefix) {
		// A generic object (PutObject), not a row snapshot. PutObject
		// writes canonical bytes (sorted keys, no spaces), so its envelope
		// always starts with "kind"; a snapshot has no such member. A
		// prefix test avoids parsing large snapshots twice.
		return nil, fmt.Errorf("state: object %s is not a row snapshot: %w", ref, ErrUnknownSchema)
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
// overlays (of every profile), and the open run's pending bases, online
// draft and scratch post-hashes.
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
		for _, ref := range []Ref{r.Run.Online, r.Run.PostHashesRef} {
			if ref != "" {
				keep[ref] = true
			}
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

// ObjectSchema is the envelope schema of objects stored by PutObject.
const ObjectSchema = 1

// Errors of the generic object API.
var (
	// ErrBadKind is returned for an object kind that is not a short
	// lowercase name.
	ErrBadKind = errors.New("state: malformed object kind")
	// ErrKindMismatch is returned by GetObject when the stored object has
	// another kind than the one asked for.
	ErrKindMismatch = errors.New("state: object has another kind")
	// ErrObjectTooDeep is returned by PutObject for a value nested too
	// deeply to be checked for secrets.
	ErrObjectTooDeep = errors.New("state: object too deep to check for secrets")
)

var kindRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

// objectEnvelope is the stored form of a generic object.
type objectEnvelope struct {
	Kind   string          `json:"kind"`
	Schema int             `json:"schema"`
	Value  json.RawMessage `json:"value"`
}

// PutObject stores any JSON-marshalable value as an object (sha256 ref of
// the canonical bytes, 0600, same directory and garbage collection as
// snapshots) and returns its ref. The stored envelope is {"kind": kind,
// "schema": 1, "value": ...}. The secrets check of PutSnapshot applies to
// every model.Item reachable from v, and the raw bytes of a secret item
// may not appear in any string value of the object either.
func (s *Store) PutObject(kind string, v any) (Ref, error) {
	if !kindRE.MatchString(kind) {
		return "", ErrBadKind
	}
	val, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("state: encode object %s: %w", kind, err)
	}
	if err := checkObjectSecrets(v, val); err != nil {
		return "", fmt.Errorf("state: put object %s: %w", kind, err)
	}
	env, err := json.Marshal(objectEnvelope{Kind: kind, Schema: ObjectSchema, Value: val})
	if err != nil {
		return "", fmt.Errorf("state: encode object %s: %w", kind, err)
	}
	b, err := model.Canonical(env)
	if err != nil {
		return "", fmt.Errorf("state: encode object %s: %w", kind, err)
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

// GetObject loads an object stored by PutObject into v after checking its
// hash. A kind mismatch is ErrKindMismatch; an unknown envelope schema is
// ErrUnknownSchema.
func (s *Store) GetObject(ref Ref, kind string, v any) error {
	if !ref.valid() {
		return ErrBadRef
	}
	if !kindRE.MatchString(kind) {
		return ErrBadKind
	}
	b, err := os.ReadFile(filepath.Join(s.objectsDir(), string(ref)))
	if err != nil {
		return fmt.Errorf("state: read object %s: %w", ref, err)
	}
	if model.HashBytes(b) != string(ref) {
		return fmt.Errorf("state: object %s: %w", ref, ErrCorruptObject)
	}
	var env objectEnvelope
	if err := json.Unmarshal(b, &env); err != nil {
		return fmt.Errorf("state: parse object %s: %w", ref, err)
	}
	switch {
	case env.Kind != kind:
		return fmt.Errorf("state: object %s: %w", ref, ErrKindMismatch)
	case env.Schema != ObjectSchema:
		return fmt.Errorf("state: object %s has schema %d: %w", ref, env.Schema, ErrUnknownSchema)
	}
	if err := json.Unmarshal(env.Value, v); err != nil {
		return fmt.Errorf("state: parse object %s: %w", ref, err)
	}
	return nil
}

// objectPrefix starts every envelope PutObject writes (canonical JSON,
// keys sorted: "kind" comes first).
var objectPrefix = []byte(`{"kind":`)

// maxSecretsDepth bounds the walk of checkObjectSecrets. A value nested
// deeper is refused with ErrObjectTooDeep instead of being stored
// unchecked.
const maxSecretsDepth = 1000

// checkObjectSecrets applies PutSnapshot's rule to every model.Item
// reachable from v through exported fields, maps, slices and pointers (a
// secret item's Value may not hold its raw bytes), and refuses an encoded
// object in which any string value contains a secret item's raw bytes.
func checkObjectSecrets(v any, encoded []byte) error {
	var raws [][]byte
	var walkErr error
	itemType := reflect.TypeOf(model.Item{})
	// A pointer is remembered by address and type: a pointer to a struct
	// and a pointer to its first field share an address, and skipping the
	// struct because its first field was seen would leave its other fields
	// unchecked.
	type ptrKey struct {
		p uintptr
		t reflect.Type
	}
	seen := map[ptrKey]bool{}
	var walk func(x reflect.Value, depth int)
	walk = func(x reflect.Value, depth int) {
		if walkErr != nil || !x.IsValid() {
			return
		}
		if depth > maxSecretsDepth {
			// Refuse rather than pass a value that was not fully checked.
			walkErr = ErrObjectTooDeep
			return
		}
		switch x.Kind() {
		case reflect.Pointer:
			k := ptrKey{x.Pointer(), x.Type()}
			if x.IsNil() || seen[k] {
				return
			}
			seen[k] = true
			walk(x.Elem(), depth+1)
		case reflect.Interface:
			if !x.IsNil() {
				walk(x.Elem(), depth+1)
			}
		case reflect.Struct:
			if x.Type() == itemType {
				it := model.Item{Value: x.FieldByName("Value").Bytes(), Raw: x.FieldByName("Raw").Bytes(), Secret: x.FieldByName("Secret").Bool()}
				if it.Secret && len(it.Raw) > 0 {
					if bytes.Contains(it.Value, it.Raw) {
						walkErr = ErrSecretInValue
						return
					}
					raws = append(raws, it.Raw)
				}
				return
			}
			for i := 0; i < x.NumField(); i++ {
				if x.Type().Field(i).IsExported() {
					walk(x.Field(i), depth+1)
				}
			}
		case reflect.Map:
			it := x.MapRange()
			for it.Next() {
				walk(it.Value(), depth+1)
			}
		case reflect.Slice, reflect.Array:
			if x.Type().Elem().Kind() == reflect.Uint8 {
				return
			}
			for i := 0; i < x.Len(); i++ {
				walk(x.Index(i), depth+1)
			}
		}
	}
	walk(reflect.ValueOf(v), 0)
	if walkErr != nil || len(raws) == 0 {
		return walkErr
	}
	var doc any
	if err := json.Unmarshal(encoded, &doc); err != nil {
		return err
	}
	if stringHolds(doc, raws) {
		return ErrSecretInValue
	}
	return nil
}

// stringHolds reports whether any string (value or object key) in the
// decoded JSON doc contains one of raws.
func stringHolds(doc any, raws [][]byte) bool {
	switch x := doc.(type) {
	case string:
		for _, r := range raws {
			if strings.Contains(x, string(r)) {
				return true
			}
		}
	case []any:
		for _, e := range x {
			if stringHolds(e, raws) {
				return true
			}
		}
	case map[string]any:
		for k, e := range x {
			if stringHolds(k, raws) || stringHolds(e, raws) {
				return true
			}
		}
	}
	return false
}
