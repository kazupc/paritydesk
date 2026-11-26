package scratch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
)

// ActiveFolder is the data folder name the app's own absolute paths name
// (the app records <data root>/Claude/... while a folder is active).
const ActiveFolder = ids.DefaultFolder

// Ref is one workspace a session record names.
type Ref struct {
	Key     string // "<acct>/<org>/<leaf>"
	Account ids.AccountID
	Org     ids.OrgID
	Leaf    string
}

// ErrLink is returned by Present when a component of the workspace path
// is a symbolic link.
var ErrLink = errors.New("scratch: workspace path contains a symbolic link")

// Prefixes returns <dataRoot>/Claude/<root>, plus the same with
// dataRoot's symlinks evaluated when that differs.
func Prefixes(dataRoot, root string) []string {
	if dataRoot == "" || root == "" {
		return nil
	}
	p := filepath.Join(dataRoot, ActiveFolder, root)
	out := []string{p}
	if ev, err := filepath.EvalSymlinks(dataRoot); err == nil {
		if q := filepath.Join(ev, ActiveFolder, root); q != p {
			out = append(out, q)
		}
	}
	return out
}

// Refs returns the workspaces named by any record of a sessions-row
// snapshot, sorted by Key and unique, and the number of strings that
// contain "/<root>/" but match no prefix or name no valid scope. A string
// names <a>/<o>/<l> when it equals prefix+"/"+a+"/"+o+"/"+l or starts
// with that plus "/", a and o pass ids.IsUUID, l matches leaf, and the
// remainder has no "." or ".." segment. Strings under a prefix whose
// leaf does not match or whose remainder has such a segment are ignored.
// Only the listed top-level members of each record are examined, at any
// depth. Records are never modified.
func Refs(sessions *model.RowSnapshot, members, prefixes []string, leaf *regexp.Regexp) (refs []Ref, unrecognized int, err error) {
	if sessions == nil || len(members) == 0 || len(prefixes) == 0 || leaf == nil {
		return nil, 0, nil
	}
	root := filepath.Base(prefixes[0])
	marker := "/" + root + "/"
	seen := map[string]bool{}
	visit := func(s string) {
		if !strings.Contains(s, marker) {
			return
		}
		ref, outcome := matchRef(s, prefixes, leaf)
		switch outcome {
		case refFound:
			if !seen[ref.Key] {
				seen[ref.Key] = true
				refs = append(refs, ref)
			}
		case refUnrecognized:
			unrecognized++
		}
	}
	for _, k := range sessions.Keys() {
		it := sessions.Items[k]
		var obj map[string]json.RawMessage
		if json.Unmarshal(it.Value, &obj) != nil {
			continue // tombstones and the archive index are not objects
		}
		for _, m := range members {
			raw, ok := obj[m]
			if !ok {
				continue
			}
			if err := walkStrings(raw, visit); err != nil {
				return nil, 0, fmt.Errorf("scratch: refs: record member %s: %w", m, err)
			}
		}
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Key < refs[j].Key })
	return refs, unrecognized, nil
}

type refOutcome int

const (
	refIgnored refOutcome = iota
	refFound
	refUnrecognized
)

// matchRef matches one string against the prefixes.
func matchRef(s string, prefixes []string, leaf *regexp.Regexp) (Ref, refOutcome) {
	for _, p := range prefixes {
		rest, ok := strings.CutPrefix(s, p+"/")
		if !ok {
			continue
		}
		parts := strings.SplitN(rest, "/", 4)
		if len(parts) < 3 || !ids.IsUUID(parts[0]) || !ids.IsUUID(parts[1]) {
			return Ref{}, refUnrecognized
		}
		if !leaf.MatchString(parts[2]) {
			return Ref{}, refIgnored
		}
		if len(parts) == 4 {
			for _, seg := range strings.Split(parts[3], "/") {
				if seg == "." || seg == ".." {
					return Ref{}, refIgnored
				}
			}
		}
		return Ref{
			Key:     parts[0] + "/" + parts[1] + "/" + parts[2],
			Account: ids.AccountID(parts[0]), Org: ids.OrgID(parts[1]), Leaf: parts[2],
		}, refFound
	}
	return Ref{}, refUnrecognized
}

// walkStrings calls f for every JSON string inside raw, at any depth.
func walkStrings(raw json.RawMessage, f func(string)) error {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			f(t)
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(t[k])
			}
		}
	}
	walk(v)
	return nil
}

// Keys returns the keys of refs.
func Keys(refs []Ref) []string {
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r.Key
	}
	return out
}

// Present reports whether folder/<root>/<key> exists with every component
// below folder a real directory (Lstat); a symlink anywhere gives false
// and ErrLink. A component that is a file gives false.
func Present(folder, root, key string) (bool, error) {
	if !ValidWorkspaceKey(key) || !validSegment(root) {
		return false, ErrKey
	}
	cur := folder
	for _, seg := range append([]string{root}, strings.Split(key, "/")...) {
		cur = filepath.Join(cur, seg)
		fi, err := os.Lstat(cur)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return false, nil
		case err != nil:
			return false, err
		case fi.Mode()&fs.ModeSymlink != 0:
			return false, ErrLink
		case !fi.IsDir():
			return false, nil
		}
	}
	return true, nil
}

// JoinRefs encodes a carry set for intent data: keys joined by ","; ""
// is the empty set.
func JoinRefs(keys []string) string { return strings.Join(keys, ",") }

// SplitRefs decodes JoinRefs's form, refusing a malformed key.
func SplitRefs(s string) ([]string, error) {
	if s == "" {
		return []string{}, nil
	}
	out := strings.Split(s, ",")
	for _, k := range out {
		if !ValidWorkspaceKey(k) {
			return nil, ErrKey
		}
	}
	return out, nil
}
