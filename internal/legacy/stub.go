package legacy

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kazupc/paritydesk/internal/compat"
)

// StubFile lists the members an empty stub file may hold.
type StubFile struct {
	Empty []string // must be [], {} or null when present
	Flags []string // must be booleans when present
}

// StubRules are the manifest's [[legacy.stub]] files and ignore_names.
type StubRules struct {
	Files  map[string]StubFile
	Ignore []string
}

// Stub limits.
const (
	maxStubEntries = 4
	maxStubBytes   = 64 << 10
	maxStubDepth   = 4
)

// StubRulesFrom builds the rules from a manifest's [legacy] section.
func StubRulesFrom(l compat.Legacy) StubRules {
	r := StubRules{Files: map[string]StubFile{}, Ignore: slices.Clone(l.IgnoreNames)}
	for _, s := range l.Stubs {
		r.Files[s.File] = StubFile{Empty: slices.Clone(s.EmptyMembers), Flags: slices.Clone(s.FlagMembers)}
	}
	return r
}

func (r StubRules) ignored(name string) bool { return slices.Contains(r.Ignore, name) }

// IsStub reports whether dir holds nothing but empty stub files: no
// subdirectory, at most 4 entries, only regular files named by a stub rule
// or ignore_names, each at most 64 KiB, and every stub file empty by its
// rule. Ignored names (.DS_Store) are never opened. An empty directory is
// a stub. dir itself must be a directory, not a symlink.
func IsStub(dir string, r StubRules) (bool, error) {
	fi, err := os.Lstat(dir)
	if err != nil {
		return false, err
	}
	if !fi.IsDir() {
		return false, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	if len(entries) > maxStubEntries {
		return false, nil
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			return false, nil
		}
		info, err := e.Info()
		if err != nil {
			return false, err
		}
		if info.Size() > maxStubBytes {
			return false, nil
		}
		if r.ignored(e.Name()) {
			continue
		}
		rule, ok := r.Files[e.Name()]
		if !ok {
			return false, nil
		}
		b, err := readSmall(filepath.Join(dir, e.Name()), maxStubBytes)
		if err != nil {
			return false, err
		}
		if !isStubJSON(b, rule) {
			return false, nil
		}
	}
	return true, nil
}

var errNotRegular = errors.New("legacy: not a regular file")

// readSmall reads at most limit bytes of a regular file, without
// following a symlink.
func readSmall(path string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|openFlags, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errNotRegular
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fs.ErrInvalid
	}
	return b, nil
}

// isStubJSON reports whether b is an empty stub: zero bytes, or one JSON
// object (depth at most 4) whose every member is an empty member with
// value [], {} or null, or a flag member with a boolean value.
func isStubJSON(b []byte, f StubFile) bool {
	if len(b) == 0 {
		return true
	}
	if checkJSON(b, maxStubDepth) != nil {
		return false
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return false
	}
	for k, raw := range doc {
		v := strings.Join(strings.Fields(string(raw)), "")
		switch {
		case slices.Contains(f.Empty, k):
			if v != "[]" && v != "{}" && v != "null" {
				return false
			}
		case slices.Contains(f.Flags, k):
			if v != "true" && v != "false" {
				return false
			}
		default:
			return false
		}
	}
	return true
}
