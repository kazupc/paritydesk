package gen

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/kazupc/paritydesk/internal/fsx"
)

// ErrTreeTooLarge is returned by TreeFingerprint for a tree with more than
// maxEntries entries.
var ErrTreeTooLarge = errors.New("gen: tree is too large to fingerprint")

// treePrefix marks a tree fingerprint (format version 1).
const treePrefix = "tree1:"

// finderFile is skipped by TreeFingerprint: Finder may write one into a
// quarantined copy when a person looks at it.
const finderFile = ".DS_Store"

// TreeFingerprint fingerprints the tree below root/rel with an lstat walk
// that never follows a symlink and skips every .DS_Store. One line per
// entry, paths relative to root/rel: files "path NUL f NUL size NUL
// mtime-ns NUL perm", symlinks "path NUL l NUL target-length",
// directories "path NUL d NUL perm" (no directory mtime: Finder changes it
// whenever it adds a .DS_Store). The sorted lines are hashed (sha256 hex
// with the prefix "tree1:"). An absent root/rel gives "". A rename keeps
// sizes, mtimes and modes, so the moved copy fingerprints the same.
func TreeFingerprint(root, rel string, maxEntries int) (fp string, files int, bytes int64, err error) {
	top, err := fsx.Contained(root, rel)
	if err != nil {
		return "", 0, 0, err
	}
	if _, err := os.Lstat(top); errors.Is(err, fs.ErrNotExist) {
		return "", 0, 0, nil
	} else if err != nil {
		return "", 0, 0, err
	}
	var lines []string
	err = filepath.WalkDir(top, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == finderFile && p != top {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if maxEntries > 0 && len(lines) >= maxEntries {
			return ErrTreeTooLarge
		}
		r, err := filepath.Rel(top, p)
		if err != nil {
			return err
		}
		r = filepath.ToSlash(r)
		fi, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			lines = append(lines, r+"\x00l\x00"+strconv.Itoa(len(target)))
		case fi.IsDir():
			lines = append(lines, r+"\x00d\x00"+strconv.FormatUint(uint64(fi.Mode().Perm()), 8))
		default:
			files++
			bytes += fi.Size()
			lines = append(lines, r+"\x00f\x00"+strconv.FormatInt(fi.Size(), 10)+"\x00"+
				strconv.FormatInt(fi.ModTime().UnixNano(), 10)+"\x00"+strconv.FormatUint(uint64(fi.Mode().Perm()), 8))
		}
		return nil
	})
	if err != nil {
		return "", 0, 0, err
	}
	sort.Strings(lines)
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l))
		h.Write([]byte{'\n'})
	}
	return treePrefix + hex.EncodeToString(h.Sum(nil)), files, bytes, nil
}

// defaultTreeEntries caps SaveTree's fingerprint walk.
const defaultTreeEntries = 1_000_000

// SaveTree records an EntryQuarantineDir for the directory folder/rel.
func (w *writer) SaveTree(folder, rel string) (Entry, error) {
	p, err := fsx.Contained(folder, rel)
	if err != nil {
		return Entry{}, fmt.Errorf("gen: save tree: %w", err)
	}
	fi, err := os.Lstat(p)
	switch {
	case err != nil:
		return Entry{}, fmt.Errorf("gen: save tree %s: %w", rel, err)
	case fi.Mode()&fs.ModeSymlink != 0:
		return Entry{}, fmt.Errorf("gen: save tree %s: %w", rel, fsx.ErrSymlink)
	case !fi.IsDir():
		return Entry{}, fmt.Errorf("gen: save tree %s: not a directory", rel)
	}
	if err := w.claim("tree " + rel); err != nil {
		return Entry{}, err
	}
	h, _, _, err := TreeFingerprint(folder, rel, defaultTreeEntries)
	if err != nil {
		return Entry{}, fmt.Errorf("gen: save tree %s: %w", rel, err)
	}
	e := Entry{Kind: EntryQuarantineDir, Path: rel, Present: true, PreHash: h}
	w.m.Entries = append(w.m.Entries, e)
	return e, nil
}

// Pin marks the generation pinned with a purpose.
func (w *writer) Pin(purpose string) {
	w.m.Pinned, w.m.Purpose = true, purpose
}

// onlyTrees reports whether every entry of m is an EntryQuarantineDir.
func onlyTrees(m *Manifest) bool {
	for _, e := range m.Entries {
		if e.Kind != EntryQuarantineDir {
			return false
		}
	}
	return true
}

// DeleteUnmoved deletes generation n only when it is a complete
// legacy-quarantine generation (pinned, every entry an
// EntryQuarantineDir) whose quarantine holds none of its entries' paths:
// nothing was moved into it. Otherwise it reports false and keeps it.
func (s *Store) DeleteUnmoved(n uint64) (bool, error) {
	if n == 0 {
		return false, nil
	}
	m, err := s.readManifest(n)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !m.Complete || !m.Pinned || m.Purpose != PurposeLegacyQuarantine || !onlyTrees(m) {
		return false, nil
	}
	q := filepath.Join(s.genDir(n), quarantineDir)
	for _, e := range m.Entries {
		p, err := fsx.Contained(q, e.Path)
		if err != nil {
			return false, nil
		}
		if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
	}
	if err := s.remove(name(n)); err != nil {
		return false, err
	}
	return true, nil
}

// tree handles an EntryQuarantineDir: a tree still in place is restored
// (nothing was moved); an absent tree is moved back from the quarantine
// only when the quarantined copy still has the recorded fingerprint;
// anything else (a directory recreated since, a changed copy) is a
// conflict and nothing is written.
func (r *restorer) tree(e Entry) (decision, error) {
	cur, _, _, err := TreeFingerprint(r.in.Folder, e.Path, defaultTreeEntries)
	if err != nil {
		return conflict, nil
	}
	d := decide(e, cur)
	if d != atPost {
		return d, nil
	}
	qdir := filepath.Join(r.dir, quarantineDir)
	moved, _, _, err := TreeFingerprint(qdir, e.Path, defaultTreeEntries)
	if err != nil || moved != e.PreHash {
		return conflict, nil
	}
	if r.in.DryRun {
		return atPost, nil
	}
	if err := r.s.fs.MoveInto(qdir, e.Path, r.in.Folder); err != nil {
		return conflict, fmt.Errorf("gen: put back %s: %w", e.Path, err)
	}
	return atPost, nil
}
