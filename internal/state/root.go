package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strconv"

	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
)

// File and directory names inside the state directory.
const (
	rootFile   = "root.json"
	objectsDir = "objects"
	tmpDir     = ".tmp"
	// preMigrationPrefix names the copy of an older root kept before the
	// first commit that rewrites it: root.json.pre-v<N>.
	preMigrationPrefix = rootFile + ".pre-v"
	filePerm           = 0o600
	dirPerm            = 0o700
	// maxRootSize bounds the read of root.json.
	maxRootSize = 64 << 20
)

// Errors.
var (
	// ErrNewerRoot is returned when root.json was written by a newer
	// paritydesk than this binary.
	ErrNewerRoot error = i18n.NewErr(i18n.ErrStateNewerRoot)
	// ErrNoRun is returned by Begin and Done when no run is open.
	ErrNoRun = errors.New("state: no open run")
	// ErrNoIntent is returned by Done when no intent of that name exists.
	ErrNoIntent = errors.New("state: no such intent in the open run")
)

// migrations[v] upgrades a decoded root document of version v to v+1.
// Migrations work on the generic JSON form so that old field shapes can
// be read without keeping old Go types around.
var migrations = map[int]func(doc map[string]json.RawMessage) error{
	0: migrateV0,
	1: migrateV1,
}

// migrateV0 upgrades the pre-release layout (no "v" field) to version 1.
// Version 0 had the same fields; only the version marker is new.
func migrateV0(doc map[string]json.RawMessage) error {
	doc["v"] = json.RawMessage("1")
	return nil
}

// migrateV1 upgrades version 1 (v0.1) to version 2 (v0.2). Every v0.2
// field is optional, so only the version marker changes; it keeps an
// older binary from rewriting a root that holds fields it would drop.
func migrateV1(doc map[string]json.RawMessage) error {
	doc["v"] = json.RawMessage("2")
	return nil
}

func (s *Store) rootPath() string { return filepath.Join(s.dir, rootFile) }

// readRootBytes returns root.json's bytes, or (nil, nil) when it does not
// exist. Symlinks and non-regular files are refused.
func (s *Store) readRootBytes() ([]byte, error) {
	fi, err := os.Lstat(s.rootPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("state: stat %s: %w", rootFile, err)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("state: %s is not a regular file", rootFile)
	}
	if fi.Size() > maxRootSize {
		return nil, fmt.Errorf("state: %s is too large", rootFile)
	}
	b, err := os.ReadFile(s.rootPath())
	if err != nil {
		return nil, fmt.Errorf("state: read %s: %w", rootFile, err)
	}
	return b, nil
}

// versionOf returns the "v" field of a root document (0 when absent).
func versionOf(b []byte) (int, error) {
	var head struct {
		V *int `json:"v"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return 0, fmt.Errorf("state: parse %s: %w", rootFile, err)
	}
	if head.V == nil {
		return 0, nil
	}
	if *head.V < 0 {
		return 0, fmt.Errorf("state: %s has an invalid version %d", rootFile, *head.V)
	}
	return *head.V, nil
}

// Load reads root.json. It never writes: a missing file yields (nil, nil);
// a root written by a newer binary is refused; older versions are migrated
// in memory only. Load is safe without the run lock and in read-only mode
// (the SessionStart hook and `status` use it).
func (s *Store) Load() (*Root, error) {
	b, err := s.readRootBytes()
	if err != nil || b == nil {
		return nil, err
	}
	return decodeRoot(b)
}

// decodeRoot parses and, when needed, migrates a root document.
func decodeRoot(b []byte) (*Root, error) {
	v, err := versionOf(b)
	if err != nil {
		return nil, err
	}
	if v > RootVersion {
		return nil, i18n.WrapErr(ErrNewerRoot, i18n.Tx(i18n.ErrStateNewerRootVersion, v, RootVersion))
	}
	if v < RootVersion {
		if b, err = migrate(b, v); err != nil {
			return nil, err
		}
	}
	r := &Root{}
	if err := json.Unmarshal(b, r); err != nil {
		return nil, fmt.Errorf("state: parse %s: %w", rootFile, err)
	}
	r.normalize()
	return r, nil
}

// migrate upgrades b from version v to RootVersion in memory.
func migrate(b []byte, v int) ([]byte, error) {
	doc := map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("state: parse %s: %w", rootFile, err)
	}
	for ; v < RootVersion; v++ {
		step, ok := migrations[v]
		if !ok {
			return nil, fmt.Errorf("state: no migration from %s version %d", rootFile, v)
		}
		if err := step(doc); err != nil {
			return nil, fmt.Errorf("state: migrate %s from version %d: %w", rootFile, v, err)
		}
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("state: migrate %s: %w", rootFile, err)
	}
	return out, nil
}

// normalize replaces nil maps with empty ones so callers can assign.
func (r *Root) normalize() {
	if r.Profiles == nil {
		r.Profiles = map[ids.Alias]*ProfileState{}
	}
	if r.Bases == nil {
		r.Bases = map[string]Ref{}
	}
	if r.Canonical == nil {
		r.Canonical = map[model.RowID]Ref{}
	}
	if r.Overlays == nil {
		r.Overlays = map[string]Ref{}
	}
}

// ensureDir creates the state directory (0700) if it does not exist.
func (s *Store) ensureDir() error {
	if _, err := os.Lstat(s.dir); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("state: stat state directory: %w", err)
	}
	if err := s.fs.MkdirAll(s.dir, dirPerm); err != nil {
		return fmt.Errorf("state: create state directory: %w", err)
	}
	return nil
}

func (s *Store) tmpDir() string { return filepath.Join(s.dir, tmpDir) }

// Commit atomically replaces root.json with r. Called only under the run
// lock. When the root on disk has an older version, Commit first keeps it
// as root.json.pre-v<N> (once).
func (s *Store) Commit(r *Root) error {
	if r == nil {
		return errors.New("state: commit: nil root")
	}
	if err := s.ensureDir(); err != nil {
		return err
	}
	if err := s.keepPreMigration(); err != nil {
		return err
	}
	r.Version = RootVersion
	r.normalize()
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("state: encode %s: %w", rootFile, err)
	}
	if err := s.fs.WriteFileAtomic(s.dir, rootFile, b, filePerm, s.tmpDir()); err != nil {
		return fmt.Errorf("state: write %s: %w", rootFile, err)
	}
	if err := s.fs.FullSync(s.dir); err != nil {
		return fmt.Errorf("state: sync state directory: %w", err)
	}
	return nil
}

// keepPreMigration copies an older on-disk root to root.json.pre-v<N>
// unless that copy already exists. A newer on-disk root is never
// overwritten.
func (s *Store) keepPreMigration() error {
	old, err := s.readRootBytes()
	if err != nil || old == nil {
		return err
	}
	v, err := versionOf(old)
	if err != nil {
		// An unparsable root is replaced; keep it for inspection first.
		v = -1
	}
	switch {
	case v > RootVersion:
		return i18n.WrapErr(ErrNewerRoot, i18n.Tx(i18n.ErrStateNewerRootVersion, v, RootVersion))
	case v == RootVersion:
		return nil
	}
	name := preMigrationPrefix + strconv.Itoa(v)
	if v < 0 {
		name = rootFile + ".unparsable"
	}
	if _, err := os.Lstat(filepath.Join(s.dir, name)); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("state: stat %s: %w", name, err)
	}
	if err := s.fs.WriteFileAtomic(s.dir, name, old, filePerm, s.tmpDir()); err != nil {
		return fmt.Errorf("state: keep %s: %w", name, err)
	}
	return nil
}

// Begin appends a not-done intent to r.Run and commits. If the commit
// fails, the intent is removed again so r matches the disk.
func (s *Store) Begin(r *Root, name IntentName, data map[string]string) error {
	if r == nil || r.Run == nil {
		return fmt.Errorf("state: begin %s: %w", name, ErrNoRun)
	}
	r.Run.Intents = append(r.Run.Intents, Intent{
		Name: name,
		At:   s.clock.Now().UTC(),
		Data: maps.Clone(data),
	})
	if err := s.Commit(r); err != nil {
		r.Run.Intents = r.Run.Intents[:len(r.Run.Intents)-1]
		return fmt.Errorf("state: begin %s: %w", name, err)
	}
	return nil
}

// Done marks the last intent named name as done and commits. If the
// commit fails, the intent is marked not done again.
func (s *Store) Done(r *Root, name IntentName) error {
	if r == nil || r.Run == nil {
		return fmt.Errorf("state: done %s: %w", name, ErrNoRun)
	}
	for i := len(r.Run.Intents) - 1; i >= 0; i-- {
		in := &r.Run.Intents[i]
		if in.Name != name {
			continue
		}
		was := in.Done
		in.Done = true
		if err := s.Commit(r); err != nil {
			in.Done = was
			return fmt.Errorf("state: done %s: %w", name, err)
		}
		return nil
	}
	return fmt.Errorf("state: done %s: %w", name, ErrNoIntent)
}

// ObserveReplica records rep as the profile's current replica and adds
// it to Replicas (every replica ever seen in the folder) and its account
// to AccountHistory when they are new.
func (p *ProfileState) ObserveReplica(rep model.Replica) {
	p.Replica = rep
	found := false
	for _, r := range p.Replicas {
		if r == rep {
			found = true
			break
		}
	}
	if !found {
		p.Replicas = append(p.Replicas, rep)
	}
	p.ObserveAccount(rep.Account)
}

// ObserveAccount appends acct to AccountHistory when it differs from the
// last recorded entry, so the history lists every change of the folder's
// account in order.
func (p *ProfileState) ObserveAccount(acct ids.AccountID) {
	if acct == "" {
		return
	}
	if n := len(p.AccountHistory); n > 0 && p.AccountHistory[n-1] == acct {
		return
	}
	p.AccountHistory = append(p.AccountHistory, acct)
}
