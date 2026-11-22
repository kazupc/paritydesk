package profile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/state"
)

// DetectForeign matches folders and home paths against manifest
// footprints. Folder globs match single names under the data root: the
// names of the scanned folders and, since other tools may use names that
// are not Claude-*, the entries of the data root the folders live in.
// Paths under home are relative paths checked with lstat. It never writes.
func DetectForeign(folders []Folder, home string, fps []compat.Footprint) ([]ForeignTool, error) {
	dataRoot := ""
	if len(folders) > 0 {
		dataRoot = filepath.Dir(folders[0].Path)
	}
	names, err := rootNames(dataRoot, folders)
	if err != nil {
		return nil, err
	}
	var out []ForeignTool
	for _, fp := range fps {
		matched, err := matchGlobs(names, fp.FolderGlobs)
		if err != nil {
			return nil, fmt.Errorf("profile: footprint %s: %w", fp.Tool, err)
		}
		found, err := homePathExists(home, fp.PathsUnderHome)
		if err != nil {
			return nil, fmt.Errorf("profile: footprint %s: %w", fp.Tool, err)
		}
		if len(matched) > 0 || found {
			out = append(out, ForeignTool{Tool: fp.Tool, Folders: matched})
		}
	}
	return out, nil
}

// rootNames returns the scanned folder names plus every entry name of
// dataRoot (when known), sorted and unique.
func rootNames(dataRoot string, folders []Folder) ([]string, error) {
	var names []string
	for _, f := range folders {
		names = append(names, f.Name)
	}
	if dataRoot != "" {
		entries, err := os.ReadDir(dataRoot)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("profile: list data root: %w", err)
		}
		for _, e := range entries {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

// matchGlobs returns the names matching any glob. A glob must be a
// single path component.
func matchGlobs(names, globs []string) ([]string, error) {
	var out []string
	for _, g := range globs {
		if g == "" || strings.ContainsRune(g, filepath.Separator) || g == "." || g == ".." {
			return nil, fmt.Errorf("folder glob %q is not a single name", g)
		}
		for _, n := range names {
			ok, err := filepath.Match(g, n)
			if err != nil {
				return nil, fmt.Errorf("folder glob %q: %w", g, err)
			}
			if ok && !slices.Contains(out, n) {
				out = append(out, n)
			}
		}
	}
	slices.Sort(out)
	return out, nil
}

// homePathExists reports whether any relative path exists under home.
func homePathExists(home string, rels []string) (bool, error) {
	if home == "" {
		return false, nil
	}
	for _, rel := range rels {
		p, err := fsx.Contained(home, rel)
		if err != nil {
			return false, fmt.Errorf("path %q: %w", rel, err)
		}
		if _, err := os.Lstat(p); err == nil {
			return true, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return false, fmt.Errorf("check path: %w", err)
		}
	}
	return false, nil
}

// NativeMultiAccount reports signs of the app's own multi-account mode: a
// change of the recorded account inside one folder's history, or the
// manifest's main.log signal (checked by the caller).
//
// It is true when current differs from the folder's last recorded account
// and the history already holds an account other than that last one, so
// the folder has changed accounts before. A single change (for example
// the first sign-in of another account in a folder) is an identity
// mismatch, not a sign of the native mode.
func NativeMultiAccount(ps *state.ProfileState, current ids.AccountID) bool {
	if ps == nil || current == "" {
		return false
	}
	last := ps.Replica.Account
	if last == "" && len(ps.AccountHistory) > 0 {
		last = ps.AccountHistory[len(ps.AccountHistory)-1]
	}
	if last == "" || current == last {
		return false
	}
	for _, a := range ps.AccountHistory {
		if a != last {
			return true
		}
	}
	return false
}
