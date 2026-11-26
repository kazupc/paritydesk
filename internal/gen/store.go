package gen

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/kazupc/paritydesk/internal/fsx"
)

// Names inside the generations directory and inside one generation.
const (
	manifestFile  = "manifest.json"
	filesDir      = "files"
	lsDir         = "ls"
	conflictsDir  = "conflicts"
	levelDBDir    = "leveldb"
	quarantineDir = "quarantine"
	stagingDir    = "restore-staging"
	replacedDir   = "leveldb-replaced"
	tmpDirName    = ".tmp"
	lockName      = "LOCK"
	// levelDBRel is the Local Storage LevelDB directory inside a data
	// folder (the same path as sources.LevelDBDir).
	levelDBRel = "Local Storage/leveldb"
	dirPerm    = 0o700
	filePerm   = 0o600
)

// Errors.
var (
	// ErrIncomplete is returned by Load for a generation without a
	// complete manifest.
	ErrIncomplete = errors.New("gen: generation is incomplete")
	// ErrTampered is returned by Load when a stored file is missing or
	// does not match its recorded sha256.
	ErrTampered = errors.New("gen: generation content does not match its manifest")
	// ErrNoLevelDBClone is returned by RestoreLevelDB when the generation
	// keeps no LevelDB clone.
	ErrNoLevelDBClone = errors.New("gen: generation has no LevelDB clone")
	// ErrNewerManifest is returned for a manifest written by a newer
	// paritydesk.
	ErrNewerManifest = errors.New("gen: manifest was written by a newer paritydesk")
)

// name returns the directory name of generation n.
func name(n uint64) string { return strconv.FormatUint(n, 10) }

// parseName returns the generation number of a directory name; ok is
// false for anything that is not a canonical decimal number.
func parseName(s string) (uint64, bool) {
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || name(n) != s {
		return 0, false
	}
	return n, true
}

func (s *Store) genDir(n uint64) string { return filepath.Join(s.dir, name(n)) }

func (s *Store) tmpDir() string { return filepath.Join(s.dir, tmpDirName) }

// hashFile returns the hex sha256 of a regular file's content.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// readManifest reads generations/<n>/manifest.json without verifying the
// stored files.
func (s *Store) readManifest(n uint64) (*Manifest, error) {
	b, err := os.ReadFile(filepath.Join(s.genDir(n), manifestFile))
	if err != nil {
		return nil, fmt.Errorf("gen: read manifest of %d: %w", n, err)
	}
	m := &Manifest{}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, fmt.Errorf("gen: parse manifest of %d: %w", n, err)
	}
	if m.Version > ManifestVersion {
		return nil, fmt.Errorf("gen: generation %d: %w", n, ErrNewerManifest)
	}
	if m.N != n {
		return nil, fmt.Errorf("gen: generation %d: manifest names generation %d: %w", n, m.N, ErrTampered)
	}
	if m.SHA256 == nil {
		m.SHA256 = map[string]string{}
	}
	return m, nil
}

// writeManifest atomically writes m and syncs the generation directory.
func (s *Store) writeManifest(m *Manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("gen: encode manifest of %d: %w", m.N, err)
	}
	rel := filepath.Join(name(m.N), manifestFile)
	if err := s.fs.WriteFileAtomic(s.dir, rel, b, filePerm, s.tmpDir()); err != nil {
		return fmt.Errorf("gen: write manifest of %d: %w", m.N, err)
	}
	if err := s.fs.FullSync(s.genDir(m.N)); err != nil {
		return fmt.Errorf("gen: sync generation %d: %w", m.N, err)
	}
	return nil
}

// List returns every generation, newest first. A generation without a
// readable, complete manifest is listed with Complete=false.
func (s *Store) List() ([]Info, error) {
	ns, err := s.numbers()
	if err != nil {
		return nil, err
	}
	out := make([]Info, 0, len(ns))
	for _, n := range ns {
		info := Info{N: n}
		if m, err := s.readManifest(n); err == nil {
			info = Info{N: n, RunID: m.RunID, Alias: m.Alias, Created: m.Created,
				Entries: len(m.Entries), LevelDB: m.LevelDB != "", Complete: m.Complete,
				Pinned: m.Pinned, Purpose: m.Purpose}
			info.ScratchBytes = s.scratchBytes(m)
		}
		if info.Bytes, err = dirSize(s.genDir(n)); err != nil {
			return nil, fmt.Errorf("gen: size of %d: %w", n, err)
		}
		out = append(out, info)
	}
	return out, nil
}

// numbers lists the generation directories, newest first.
func (s *Store) numbers() ([]uint64, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("gen: list generations: %w", err)
	}
	var ns []uint64
	for _, e := range entries {
		if n, ok := parseName(e.Name()); ok && e.IsDir() {
			ns = append(ns, n)
		}
	}
	sort.Slice(ns, func(i, j int) bool { return ns[i] > ns[j] })
	return ns, nil
}

// dirSize sums the sizes of the regular files under dir.
func dirSize(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			fi, err := d.Info()
			if err != nil {
				return err
			}
			total += fi.Size()
		}
		return nil
	})
	return total, err
}

// Load returns a generation's manifest after checking its sha256 entries.
func (s *Store) Load(n uint64) (*Manifest, error) {
	m, err := s.readManifest(n)
	if err != nil {
		return nil, err
	}
	if !m.Complete {
		return nil, fmt.Errorf("gen: generation %d: %w", n, ErrIncomplete)
	}
	if err := s.verify(m); err != nil {
		return nil, err
	}
	return m, nil
}

// verify checks every stored file against the manifest.
func (s *Store) verify(m *Manifest) error {
	dir := s.genDir(m.N)
	for _, e := range m.Entries {
		if e.Stored != "" && m.SHA256[e.Stored] == "" {
			return fmt.Errorf("gen: generation %d: %s has no hash: %w", m.N, e.Stored, ErrTampered)
		}
	}
	rels := make([]string, 0, len(m.SHA256))
	for rel := range m.SHA256 {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		p, err := fsx.Contained(dir, rel)
		if err != nil {
			return fmt.Errorf("gen: generation %d: %w", m.N, err)
		}
		got, err := hashFile(p)
		if err != nil || got != m.SHA256[rel] {
			return fmt.Errorf("gen: generation %d: %s: %w", m.N, rel, ErrTampered)
		}
	}
	return nil
}

// DeleteIncomplete removes generation n when it is incomplete: its
// directory has no manifest, or its manifest says Complete=false (used by
// recover after an interrupted P6, with the run's own generation). A
// complete generation, a manifest written by a newer paritydesk and a
// manifest that cannot be read or parsed are left alone. It reports
// whether the generation was removed.
func (s *Store) DeleteIncomplete(n uint64) (bool, error) {
	if n == 0 {
		return false, nil
	}
	if _, err := os.Lstat(s.genDir(n)); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("gen: stat generation %d: %w", n, err)
	}
	m, err := s.readManifest(n)
	switch {
	case err == nil && m.Complete:
		return false, nil
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return false, err
	}
	if err := s.remove(name(n)); err != nil {
		return false, err
	}
	return true, nil
}

// remove deletes rel under the generations directory.
func (s *Store) remove(rel string) error {
	if err := s.fs.RemoveAll(s.dir, rel); err != nil {
		return fmt.Errorf("gen: remove %s: %w", rel, err)
	}
	return nil
}

// PrunePolicy is the retention policy. Keep <= 0 and MaxBytes <= 0 mean
// no limit; LevelDBClones < 0 means no limit and 0 keeps no clone. The
// newest complete generation is never removed by MaxBytes.
type PrunePolicy struct {
	Keep          int
	LevelDBClones int
	MaxBytes      int64
}

// Prune applies the policy to complete generations: drop LevelDB clones
// beyond LevelDBClones, then whole generations beyond Keep or MaxBytes
// (oldest first). Incomplete generations are left to DeleteIncomplete.
// Pinned generations are never pruned and count toward neither Keep nor
// MaxBytes.
func (s *Store) Prune(p PrunePolicy) error {
	complete, err := s.completeInfos()
	if err != nil {
		return err
	}
	if p.LevelDBClones >= 0 {
		kept := 0
		for _, in := range complete {
			if !in.LevelDB {
				continue
			}
			if kept < p.LevelDBClones {
				kept++
				continue
			}
			if err := s.dropClone(in.N); err != nil {
				return err
			}
		}
	}
	// Sizes changed when clones were dropped.
	if complete, err = s.completeInfos(); err != nil {
		return err
	}
	var total int64
	for i, in := range complete {
		// Quarantined scratch files are kept (scratch-kept), so they do
		// not count against the size limit.
		total += in.Bytes - in.ScratchBytes
		overCount := p.Keep > 0 && i >= p.Keep
		overSize := p.MaxBytes > 0 && i > 0 && total > p.MaxBytes
		if overCount || overSize {
			if err := s.keepScratch(in.N); err != nil {
				return err
			}
			if err := s.remove(name(in.N)); err != nil {
				return err
			}
		}
	}
	return nil
}

// completeInfos lists the complete generations Prune may touch (never a
// pinned one).
func (s *Store) completeInfos() ([]Info, error) {
	infos, err := s.List()
	if err != nil {
		return nil, err
	}
	out := infos[:0]
	for _, in := range infos {
		if in.Complete && !in.Pinned {
			out = append(out, in)
		}
	}
	return out, nil
}

// dropClone removes a generation's LevelDB clone. The manifest is
// rewritten first, so a crash in between leaves only unlisted files.
func (s *Store) dropClone(n uint64) error {
	m, err := s.readManifest(n)
	if err != nil {
		return err
	}
	if m.LevelDB == "" {
		return nil
	}
	prefix := m.LevelDB + "/"
	for rel := range m.SHA256 {
		if strings.HasPrefix(rel, prefix) {
			delete(m.SHA256, rel)
		}
	}
	dir := m.LevelDB
	m.LevelDB = ""
	if err := s.writeManifest(m); err != nil {
		return err
	}
	return s.remove(filepath.Join(name(n), dir))
}

// Purge removes every generation (backups purge).
func (s *Store) Purge() error {
	ns, err := s.numbers()
	if err != nil {
		return err
	}
	for _, n := range ns {
		if err := s.remove(name(n)); err != nil {
			return err
		}
	}
	if _, err := os.Lstat(filepath.Join(s.dir, ScratchKeptDir)); err == nil {
		if err := s.remove(ScratchKeptDir); err != nil {
			return err
		}
	}
	if _, err := os.Lstat(s.tmpDir()); err == nil {
		return s.remove(tmpDirName)
	}
	return nil
}
