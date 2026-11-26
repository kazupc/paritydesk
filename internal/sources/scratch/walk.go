package scratch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/kazupc/paritydesk/internal/denylist"
	"github.com/kazupc/paritydesk/internal/model"
)

// errUnreadable marks a workspace whose files could not all be read.
var errUnreadable = errors.New("scratch: workspace unreadable")

// errOverLimit stops a listing once the workspace is over a file or byte
// limit: it is not carried, so the rest of it is never listed.
var errOverLimit = errors.New("scratch: workspace over a limit")

// listLimits bound one workspace's listing: more files or bytes than
// these stop it (the per-workspace limits, or what is left of the run's).
type listLimits struct {
	files int
	bytes int64
}

// listed is one entry found by the listing pass.
type listed struct {
	key   string
	rel   string
	kind  string // KindDir, KindGit, KindFile, KindLink
	mode  uint32
	size  int64
	mtime int64 // ms
	stat  FileStat
	hasSt bool
	link  string
	abs   string
}

// workspaceRead is one workspace's listing.
type workspaceRead struct {
	ws      string
	mode    uint32
	entries []listed
	files   int
	bytes   int64
	notes   []model.Change
	badName bool
	// readOnly: the workspace or one of its folders lacks owner write
	// permission, so files cannot be written into it.
	readOnly bool
	// stopped: the listing ended at a limit (files and bytes are then
	// lower bounds, entries partial).
	stopped bool
}

// readRules are the rules one ReadRefs call applies.
type readRules struct {
	root     string
	exclude  []string
	heldBack []string
	reserved []string
}

// note returns a skip note of row scratch.
func note(key, n string) model.Change {
	return model.Change{Row: model.RowScratch, Kind: model.ChangeSkipped, Key: key, Note: n}
}

// listWorkspace lists folder/<root>/<ws> without hashing and without
// following any symlink. It stops (w.stopped) as soon as the workspace
// has more files or bytes than lim allows.
func listWorkspace(ctx context.Context, folder string, ws string, rules readRules, lim listLimits) (*workspaceRead, error) {
	dir := filepath.Join(folder, rules.root, filepath.FromSlash(ws))
	fi, err := os.Lstat(dir)
	if err != nil {
		return nil, errUnreadable
	}
	w := &workspaceRead{ws: ws, mode: uint32(fi.Mode().Perm()), readOnly: !ownerWritable(fi.Mode())}
	switch err := w.list(ctx, dir, "", "", rules, lim); {
	case errors.Is(err, errOverLimit):
		w.stopped, w.entries = true, nil
	case err != nil:
		return nil, err
	}
	return w, nil
}

// ownerWritable reports whether a directory mode lets its owner create
// and rename entries in it.
func ownerWritable(m fs.FileMode) bool { return m.Perm()&0o300 == 0o300 }

// list walks one directory (rel "" is the workspace root). unit is the
// enclosing git unit key, if any.
func (w *workspaceRead) list(ctx context.Context, dir, rel, unit string, rules readRules, lim listLimits) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return errUnreadable
	}
	for _, e := range entries { // os.ReadDir sorts by name
		name := e.Name()
		r := name
		if rel != "" {
			r = rel + "/" + name
		}
		if excluded(r, rules.exclude) {
			continue
		}
		abs := filepath.Join(dir, name)
		fi, err := os.Lstat(abs)
		if err != nil {
			return errUnreadable
		}
		isDir := fi.IsDir()
		key := EntryKey(w.ws, r)
		if isDir {
			key = DirKey(w.ws, r)
		}
		if !validSegment(name) || len(rules.root)+1+len(key) > MaxDataPath {
			w.badName = true
			continue
		}
		if isDir && HeldBackSegment(name, rules.heldBack) {
			w.notes = append(w.notes, note(key, NoteHeldBack))
			continue
		}
		if matchName(rules.reserved, name) {
			w.notes = append(w.notes, note(key, NoteReserved))
			continue
		}
		mt := fi.ModTime().UnixMilli()
		switch {
		case isDir:
			kind, inner := KindDir, unit
			if unit == "" && IsGitName(name) {
				kind, inner = KindGit, key
			}
			if !ownerWritable(fi.Mode()) {
				w.readOnly = true
			}
			w.entries = append(w.entries, listed{key: key, rel: r, kind: kind, mode: uint32(fi.Mode().Perm()), mtime: mt, abs: abs})
			if err := w.list(ctx, abs, r, inner, rules, lim); err != nil {
				return err
			}
		case fi.Mode().IsRegular():
			st, ok := StatOf(fi)
			w.entries = append(w.entries, listed{key: key, rel: r, kind: KindFile, mode: uint32(fi.Mode().Perm()),
				size: fi.Size(), mtime: mt, stat: st, hasSt: ok, abs: abs})
			w.files++
			w.bytes += fi.Size()
			if w.files > lim.files || w.bytes > lim.bytes {
				return errOverLimit
			}
		case fi.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(abs)
			if err != nil {
				return errUnreadable
			}
			if !utf8.ValidString(target) || strings.ContainsRune(target, 0) {
				w.badName = true
				continue
			}
			w.entries = append(w.entries, listed{key: key, rel: r, kind: KindLink, link: target, mtime: mt, abs: abs})
		default:
			w.notes = append(w.notes, note(key, NoteSpecial))
		}
	}
	return nil
}

// excluded reports whether rel matches an exclude glob.
func excluded(rel string, globs []string) bool {
	return slices.ContainsFunc(globs, func(g string) bool { return denylist.MatchGlob(g, rel) })
}

// matchName reports whether a base name matches any glob (path.Match,
// case-insensitive); a glob with a slash is matched by its last segment.
func matchName(globs []string, name string) bool {
	low := strings.ToLower(name)
	for _, g := range globs {
		ok, err := path.Match(strings.ToLower(path.Base(g)), low)
		if ok || err != nil {
			return true
		}
	}
	return false
}

// MatchReserved reports whether a base name matches one of the app's
// tmp recovery globs.
func MatchReserved(globs []string, name string) bool { return matchName(globs, name) }

// hashFile returns the sha256 of a listed regular file: from the cache on
// a hit, else streamed from a descriptor opened without following a
// symlink whose fstat must match the listing.
func hashFile(e listed, folderKey string, cache *StatCache) (string, error) {
	if e.hasSt {
		if sha, ok := cache.Lookup(folderKey, e.key, e.stat); ok {
			return sha, nil
		}
	}
	f, err := openNoFollow(e.abs)
	if err != nil {
		return "", errUnreadable
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() != e.size {
		return "", errUnreadable
	}
	st, ok := StatOf(fi)
	if e.hasSt && (!ok || st.Dev != e.stat.Dev || st.Ino != e.stat.Ino) {
		return "", errUnreadable
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil || n != e.size {
		return "", errUnreadable
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if ok {
		cache.Put(folderKey, e.key, st, sum)
	}
	return sum, nil
}

// items builds the snapshot items of a listed workspace whose files were
// hashed (hashes by key).
func (w *workspaceRead) items(hashes map[string]string) (map[string]model.Item, error) {
	out := map[string]model.Item{}
	root, err := RootItem(w.ws, w.mode, "")
	if err != nil {
		return nil, err
	}
	out[root.Key] = root
	var units []listed
	for _, e := range w.entries {
		var it model.Item
		switch e.kind {
		case KindDir:
			it, err = DirItem(e.key, e.mode)
		case KindGit:
			units = append(units, e)
			continue
		case KindFile:
			it, err = FileItem(e.key, hashes[e.key], e.size, e.mode, e.mtime)
		case KindLink:
			it, err = LinkItem(e.key, e.link, e.mtime)
		}
		if err != nil {
			return nil, err
		}
		out[it.Key] = it
	}
	for _, u := range units {
		it, err := UnitItem(u.key, u.mode, out)
		if err != nil {
			return nil, err
		}
		out[it.Key] = it
	}
	return out, nil
}

// folderKeyOf returns "<dev>:<ino>" of a data folder, or "" when unknown.
func folderKeyOf(folder string) string {
	fi, err := os.Lstat(folder)
	if err != nil {
		return ""
	}
	st, ok := StatOf(fi)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d:%d", st.Dev, st.Ino)
}
