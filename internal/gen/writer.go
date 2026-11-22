package gen

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sort"

	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
)

// ErrFinished is returned by a Writer used after Finish or Abort.
var ErrFinished = errors.New("gen: generation already finished or aborted")

// lsRecord is the stored form of one Local Storage value:
// ls/<sha256(name)>.json. Raw is base64 in JSON.
type lsRecord struct {
	Name    string `json:"name"`
	Present bool   `json:"present"`
	Raw     []byte `json:"raw,omitempty"`
}

// conflictRecord is the stored form of one merge loser.
type conflictRecord struct {
	Row model.RowID `json:"row"`
	Key string      `json:"key"`
	Raw []byte      `json:"raw"`
}

type writer struct {
	s         *Store
	m         Manifest
	dir       string
	seen      map[string]bool
	conflicts int
	closed    bool
}

var _ Writer = (*writer)(nil)

// Begin creates generations/<n>/ (0700). n must be positive and unused.
func (s *Store) Begin(n uint64, runID string, alias ids.Alias, rep model.Replica) (Writer, error) {
	if n == 0 {
		return nil, errors.New("gen: begin: generation numbers start at 1")
	}
	dir := s.genDir(n)
	if _, err := os.Lstat(dir); err == nil {
		return nil, fmt.Errorf("gen: begin %d: %w", n, fs.ErrExist)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("gen: begin %d: %w", n, err)
	}
	for _, d := range []string{s.dir, dir, filepath.Join(dir, quarantineDir)} {
		if err := s.fs.MkdirAll(d, dirPerm); err != nil {
			return nil, fmt.Errorf("gen: begin %d: %w", n, err)
		}
	}
	return &writer{
		s:   s,
		dir: dir,
		m: Manifest{
			Version: ManifestVersion,
			N:       n,
			RunID:   runID,
			Alias:   alias,
			Replica: rep,
			Created: s.clock.Now().UTC(),
			Entries: []Entry{},
			SHA256:  map[string]string{},
		},
		seen: map[string]bool{},
	}, nil
}

// Dir returns the generation directory.
func (w *writer) Dir() string { return w.dir }

// QuarantineDir is where apply moves files instead of deleting them.
func (w *writer) QuarantineDir() string { return filepath.Join(w.dir, quarantineDir) }

// claim refuses use after Finish or Abort and duplicate items.
func (w *writer) claim(id string) error {
	if w.closed {
		return ErrFinished
	}
	if w.seen[id] {
		return fmt.Errorf("gen: %s saved twice", id)
	}
	w.seen[id] = true
	return nil
}

// SaveFile clones folder/rel into the generation (if it exists) and
// records an EntryFile with its pre-hash and the planned post-hash.
func (w *writer) SaveFile(row model.RowID, folder, rel, postHash string) error {
	src, err := fsx.Contained(folder, rel)
	if err != nil {
		return fmt.Errorf("gen: save file: %w", err)
	}
	if err := w.claim("file " + rel); err != nil {
		return err
	}
	e := Entry{Kind: EntryFile, Row: row, Path: rel, PostHash: postHash}
	fi, err := os.Lstat(src)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		w.m.Entries = append(w.m.Entries, e)
		return nil
	case err != nil:
		return fmt.Errorf("gen: save file %s: %w", rel, err)
	case !fi.Mode().IsRegular():
		return fmt.Errorf("gen: save file %s: not a regular file", rel)
	}
	stored := filepath.Join(filesDir, rel)
	dst := filepath.Join(w.dir, stored)
	if err := w.s.fs.MkdirAll(filepath.Dir(dst), dirPerm); err != nil {
		return fmt.Errorf("gen: save file %s: %w", rel, err)
	}
	if err := w.s.fs.Clone(src, dst); err != nil {
		return fmt.Errorf("gen: save file %s: %w", rel, err)
	}
	h, err := hashFile(dst)
	if err != nil {
		return fmt.Errorf("gen: save file %s: %w", rel, err)
	}
	e.Present, e.PreHash, e.Stored = true, h, stored
	w.m.SHA256[stored] = h
	w.m.Entries = append(w.m.Entries, e)
	return nil
}

// writeRecord stores v as JSON at rel inside the generation and records
// its hash.
func (w *writer) writeRecord(rel string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := w.s.fs.WriteFileAtomic(w.dir, rel, b, filePerm, w.s.tmpDir()); err != nil {
		return err
	}
	w.m.SHA256[rel] = model.HashBytes(b)
	return nil
}

// SaveLSValue records the previous raw bytes of a Local Storage item.
func (w *writer) SaveLSValue(row model.RowID, name string, raw []byte, present bool, postHash string) error {
	if err := w.claim("ls " + name); err != nil {
		return err
	}
	rec := lsRecord{Name: name, Present: present}
	e := Entry{Kind: EntryLSValue, Row: row, Key: name, Present: present, PostHash: postHash}
	if present {
		rec.Raw = raw
		e.PreHash = model.HashBytes(raw)
	}
	e.Stored = filepath.Join(lsDir, model.HashBytes([]byte(name))+".json")
	if err := w.writeRecord(e.Stored, rec); err != nil {
		return fmt.Errorf("gen: save Local Storage value: %w", err)
	}
	w.m.Entries = append(w.m.Entries, e)
	return nil
}

// SaveConflict stores a merge loser's bytes.
func (w *writer) SaveConflict(row model.RowID, key string, raw []byte) error {
	if w.closed {
		return ErrFinished
	}
	stored := filepath.Join(conflictsDir, fmt.Sprintf("%06d.json", w.conflicts))
	if err := w.writeRecord(stored, conflictRecord{Row: row, Key: key, Raw: raw}); err != nil {
		return fmt.Errorf("gen: save conflict: %w", err)
	}
	w.conflicts++
	w.m.Entries = append(w.m.Entries, Entry{
		Kind: EntryConflict, Row: row, Key: key, Present: true,
		PreHash: model.HashBytes(raw), Stored: stored,
	})
	return nil
}

// CloneLevelDB clones the whole LevelDB directory (without LOCK).
func (w *writer) CloneLevelDB(dir string) error {
	if w.closed {
		return ErrFinished
	}
	if w.m.LevelDB != "" {
		return errors.New("gen: LevelDB already cloned")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("gen: clone LevelDB: %w", err)
	}
	dst := filepath.Join(w.dir, levelDBDir)
	if err := w.s.fs.MkdirAll(dst, dirPerm); err != nil {
		return fmt.Errorf("gen: clone LevelDB: %w", err)
	}
	for _, e := range entries {
		if e.Name() == lockName {
			continue
		}
		if e.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("gen: clone LevelDB: %w", fsx.ErrSymlink)
		}
		if err := w.s.fs.Clone(filepath.Join(dir, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return fmt.Errorf("gen: clone LevelDB: %w", err)
		}
	}
	hashes, err := hashTree(w.dir, levelDBDir)
	if err != nil {
		return fmt.Errorf("gen: clone LevelDB: %w", err)
	}
	maps.Copy(w.m.SHA256, hashes)
	w.m.LevelDB = levelDBDir
	return nil
}

// hashTree hashes every regular file under root/rel, keyed by its path
// relative to root.
func hashTree(root, rel string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(filepath.Join(root, rel), func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		h, err := hashFile(p)
		if err != nil {
			return err
		}
		r, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		out[r] = h
		return nil
	})
	return out, err
}

// Finish syncs every stored file, then writes manifest.json with
// Complete=true and syncs it.
func (w *writer) Finish() (*Manifest, error) {
	if w.closed {
		return nil, ErrFinished
	}
	rels := make([]string, 0, len(w.m.SHA256))
	for rel := range w.m.SHA256 {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		if err := w.s.fs.FullSync(filepath.Join(w.dir, rel)); err != nil {
			return nil, fmt.Errorf("gen: finish %d: %w", w.m.N, err)
		}
	}
	w.m.Complete = true
	if err := w.s.writeManifest(&w.m); err != nil {
		w.m.Complete = false
		return nil, err
	}
	if err := w.s.fs.FullSync(w.s.dir); err != nil {
		return nil, fmt.Errorf("gen: finish %d: %w", w.m.N, err)
	}
	w.closed = true
	m := w.m
	m.Entries = append([]Entry(nil), w.m.Entries...)
	m.SHA256 = maps.Clone(w.m.SHA256)
	return &m, nil
}

// Abort removes the incomplete generation (fsx.Ops.RemoveAll with the
// generations directory as root).
func (w *writer) Abort() error {
	if w.closed {
		return ErrFinished
	}
	w.closed = true
	return w.s.remove(name(w.m.N))
}
