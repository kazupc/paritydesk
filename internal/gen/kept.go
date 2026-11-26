package gen

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
)

// ScratchKeptDir is the directory under the generations directory that
// keeps quarantined scratch files of pruned generations. Nothing removes
// it automatically (spec-scratch 9.4).
const ScratchKeptDir = "scratch-kept"

// keptFile is the listing of one kept set.
const keptFile = "kept.json"

// keptVersion is the format version of kept.json.
const keptVersion = 1

// KeptEntry is one kept file: its data-folder-relative path and the hash
// of its bytes.
type KeptEntry struct {
	Path string `json:"path"`
	Pre  string `json:"pre"`
}

// KeptListing is scratch-kept/<n>/kept.json.
type KeptListing struct {
	V       int         `json:"v"`
	N       uint64      `json:"n"`
	Run     string      `json:"run"`
	Alias   ids.Alias   `json:"alias"`
	Created time.Time   `json:"created"`
	Entries []KeptEntry `json:"entries"`
}

// KeptInfo summarizes one kept set.
type KeptInfo struct {
	N       uint64    `json:"n"`
	Created time.Time `json:"created"`
	Files   int       `json:"files"`
	Bytes   int64     `json:"bytes"`
	// Listed is false for a set whose kept.json is missing (a crash
	// between moving its files and writing the listing).
	Listed bool `json:"listed"`
}

// isScratchRemoval reports whether e is a row-scratch file the run
// removed (quarantined).
func isScratchRemoval(e Entry) bool {
	return e.Row == model.RowScratch && (e.Kind == EntryFile || e.Kind == EntryQuarantine) && e.Present && e.PostHash == ""
}

// scratchBytes sums the stored and quarantined bytes of row-scratch
// entries of a generation.
func (s *Store) scratchBytes(m *Manifest) int64 {
	var total int64
	dir := s.genDir(m.N)
	for _, e := range m.Entries {
		if e.Row != model.RowScratch {
			continue
		}
		if e.Stored != "" {
			if fi, err := os.Lstat(filepath.Join(dir, e.Stored)); err == nil && fi.Mode().IsRegular() {
				total += fi.Size()
			}
		}
		if e.Path != "" {
			if p, err := fsx.Contained(filepath.Join(dir, quarantineDir), e.Path); err == nil {
				if fi, err := os.Lstat(p); err == nil && fi.Mode().IsRegular() {
					total += fi.Size()
				}
			}
		}
	}
	return total
}

// keepScratch moves the files row scratch quarantined in generation n
// into scratch-kept/<n>/ (renames on the same volume) and then writes
// kept.json: files first, listing second, so a crash leaves files
// without a listing, never a listing without files. The quarantined
// file is taken when it still has its recorded hash, else the stored
// copy.
func (s *Store) keepScratch(n uint64) error {
	m, err := s.readManifest(n)
	if err != nil {
		return nil // nothing readable to keep; prune as before
	}
	var removed []Entry
	for _, e := range m.Entries {
		if isScratchRemoval(e) {
			removed = append(removed, e)
		}
	}
	if len(removed) == 0 {
		return nil
	}
	dir := s.genDir(n)
	keptRel := filepath.Join(ScratchKeptDir, name(n))
	kept := filepath.Join(s.dir, keptRel)
	files := filepath.Join(kept, filesDir)
	if err := s.fs.MkdirAll(files, dirPerm); err != nil {
		return fmt.Errorf("gen: keep scratch files of %d: %w", n, err)
	}
	listing := KeptListing{V: keptVersion, N: n, Run: m.RunID, Alias: m.Alias, Created: m.Created}
	for _, e := range removed {
		from := ""
		for _, base := range []string{filepath.Join(dir, quarantineDir), filepath.Join(dir, filesDir)} {
			p, err := fsx.Contained(base, e.Path)
			if err != nil {
				continue
			}
			if h, err := currentFileHash(p); err == nil && h == e.PreHash {
				from = base
				break
			}
		}
		if from == "" {
			continue
		}
		if _, err := os.Lstat(filepath.Join(files, filepath.FromSlash(e.Path))); err == nil {
			continue // already moved by an interrupted prune
		}
		if err := s.fs.MoveInto(from, e.Path, files); err != nil {
			return fmt.Errorf("gen: keep scratch files of %d: %w", n, err)
		}
		listing.Entries = append(listing.Entries, KeptEntry{Path: e.Path, Pre: e.PreHash})
	}
	b, err := json.MarshalIndent(listing, "", "  ")
	if err != nil {
		return err
	}
	if err := s.fs.WriteFileAtomic(s.dir, filepath.Join(keptRel, keptFile), b, filePerm, s.tmpDir()); err != nil {
		return fmt.Errorf("gen: keep scratch files of %d: %w", n, err)
	}
	return nil
}

// ScratchKept lists the kept sets, oldest first.
func (s *Store) ScratchKept() ([]KeptInfo, error) {
	root := filepath.Join(s.dir, ScratchKeptDir)
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("gen: list scratch-kept: %w", err)
	}
	var out []KeptInfo
	for _, e := range entries {
		n, ok := parseName(e.Name())
		if !ok || !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		info := KeptInfo{N: n}
		if b, err := os.ReadFile(filepath.Join(dir, keptFile)); err == nil {
			var l KeptListing
			if json.Unmarshal(b, &l) == nil {
				info.Listed, info.Created = true, l.Created
			}
		}
		if info.Created.IsZero() {
			if fi, err := os.Lstat(dir); err == nil {
				info.Created = fi.ModTime().UTC()
			}
		}
		_ = filepath.WalkDir(filepath.Join(dir, filesDir), func(_ string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() {
				if fi, err := d.Info(); err == nil {
					info.Files++
					info.Bytes += fi.Size()
				}
			}
			return nil
		})
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].N < out[j].N })
	return out, nil
}

// RemoveScratchKept removes every kept set created before cutoff (every
// set when cutoff is zero) and returns how many sets and bytes it
// removed. Only `backups prune --scratch` and purge call it, after a
// confirmation.
func (s *Store) RemoveScratchKept(cutoff time.Time) (int, int64, error) {
	sets, err := s.ScratchKept()
	if err != nil {
		return 0, 0, err
	}
	n, bytes := 0, int64(0)
	for _, k := range sets {
		if !cutoff.IsZero() && !k.Created.Before(cutoff) {
			continue
		}
		if err := s.remove(filepath.Join(ScratchKeptDir, strconv.FormatUint(k.N, 10))); err != nil {
			return n, bytes, err
		}
		n++
		bytes += k.Bytes
	}
	return n, bytes, nil
}

// ScratchKeptPath returns the scratch-kept directory (for doctor's text).
func (s *Store) ScratchKeptPath() string { return filepath.Join(s.dir, ScratchKeptDir) }
