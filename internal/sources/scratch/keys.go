package scratch

import (
	"encoding/json"
	"errors"
	"path"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
)

// KeyKind is the kind of an item key (spec-scratch 4.2).
type KeyKind int

// Key kinds.
const (
	KeyRoot  KeyKind = iota + 1 // "<acct>/<org>/<leaf>/": a workspace's root marker
	KeyDir                      // "<ws>/<rel>/": a directory (or a git unit)
	KeyEntry                    // "<ws>/<rel>": a file or a symlink
)

// MaxDataPath bounds the data-folder-relative path of a key
// ("<root>/<key>"), in bytes.
const MaxDataPath = 1000

// GitDirName is the folder name of a git unit (compared after FoldName).
const GitDirName = ".git"

// gitFolded is GitDirName after FoldName.
var gitFolded = FoldName(GitDirName)

// IsGitName reports whether a folder name is a git unit's name.
func IsGitName(name string) bool { return FoldName(name) == gitFolded }

// Item kinds (the "k" member of an item value).
const (
	KindRoot = "root"
	KindDir  = "dir"
	KindGit  = "git"
	KindFile = "file"
	KindLink = "link"
)

// ErrKey marks a malformed item key or workspace key.
var ErrKey = errors.New("scratch: malformed key")

// Value is the decoded value of a scratch item (spec-scratch 4.3).
type Value struct {
	K        string `json:"k"`
	Mode     uint32 `json:"mode,omitempty"`
	Over     string `json:"over,omitempty"`
	Digest   string `json:"digest,omitempty"`
	N        int    `json:"n,omitempty"`
	Diverged bool   `json:"diverged,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Target   string `json:"target,omitempty"`
}

// Decode returns the decoded value of a scratch item; ok is false for a
// value that is not a scratch item.
func Decode(it model.Item) (Value, bool) {
	var v Value
	if err := json.Unmarshal(it.Value, &v); err != nil || v.K == "" {
		return Value{}, false
	}
	return v, true
}

// KindOf returns the "k" member of an item ("" when it has none).
func KindOf(it model.Item) string {
	v, _ := Decode(it)
	return v.K
}

// validSegment reports whether s is one segment of the Rel grammar: not
// empty, not "." or "..", no "/", "\" or NUL, valid UTF-8.
func validSegment(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\\x00") && utf8.ValidString(s)
}

// ValidRel reports whether rel follows the Rel grammar (one or more valid
// segments joined by "/").
func ValidRel(rel string) bool {
	if rel == "" {
		return false
	}
	for _, seg := range strings.Split(rel, "/") {
		if !validSegment(seg) {
			return false
		}
	}
	return true
}

// ValidWorkspaceKey reports whether ws is "<uuid>/<uuid>/<segment>".
// The leaf pattern of the manifest is checked where it is known.
func ValidWorkspaceKey(ws string) bool {
	parts := strings.Split(ws, "/")
	return len(parts) == 3 && ids.IsUUID(parts[0]) && ids.IsUUID(parts[1]) && validSegment(parts[2])
}

// SplitKey splits an item key into its workspace key, its path below the
// workspace ("" for the root marker) and its kind.
func SplitKey(key string) (ws, rel string, kind KeyKind, err error) {
	parts := strings.SplitN(key, "/", 4)
	if len(parts) < 4 {
		return "", "", 0, ErrKey
	}
	ws = parts[0] + "/" + parts[1] + "/" + parts[2]
	if !ValidWorkspaceKey(ws) {
		return "", "", 0, ErrKey
	}
	rest := parts[3]
	switch {
	case rest == "":
		return ws, "", KeyRoot, nil
	case strings.HasSuffix(rest, "/"):
		rel, kind = strings.TrimSuffix(rest, "/"), KeyDir
	default:
		rel, kind = rest, KeyEntry
	}
	if !ValidRel(rel) {
		return "", "", 0, ErrKey
	}
	return ws, rel, kind, nil
}

// WorkspaceOf returns the workspace key of an item key ("" when malformed).
func WorkspaceOf(key string) string {
	ws, _, _, err := SplitKey(key)
	if err != nil {
		return ""
	}
	return ws
}

// LinkInside reports whether a symlink at item key k with target stays
// inside its workspace: relative, without NUL, and resolving (lexically,
// from the link's folder) to the workspace or a path below it. A link
// that fails it is never created (spec-scratch 7.3 step 2).
func LinkInside(k, target string) bool {
	if target == "" || strings.HasPrefix(target, "/") || strings.ContainsRune(target, 0) {
		return false
	}
	ws := WorkspaceOf(k)
	if ws == "" {
		return false
	}
	got := path.Clean(path.Dir(k) + "/" + target)
	return got == ws || strings.HasPrefix(got, ws+"/")
}

// RootKey returns the root marker key of a workspace.
func RootKey(ws string) string { return ws + "/" }

// DirKey returns the key of directory rel in workspace ws.
func DirKey(ws, rel string) string { return ws + "/" + rel + "/" }

// EntryKey returns the key of file or link rel in workspace ws.
func EntryKey(ws, rel string) string { return ws + "/" + rel }

// DataPath returns the data-folder-relative path of a key:
// "<root>/" + key without its trailing slash.
func DataPath(root, key string) string {
	return root + "/" + strings.TrimSuffix(key, "/")
}

// relSegments returns the segments of a key below its workspace (none
// for a root marker or a malformed key).
func relSegments(key string) (ws string, segs []string, kind KeyKind) {
	ws, rel, kind, err := SplitKey(key)
	if err != nil || rel == "" {
		return ws, nil, kind
	}
	return ws, strings.Split(rel, "/"), kind
}

// GitUnit returns the outermost git unit key containing key: a directory
// whose segment folds to ".git" at any depth. Only members count; the
// unit key itself returns false.
func GitUnit(key string) (unit string, ok bool) {
	ws, segs, _ := relSegments(key)
	for i, s := range segs {
		if !IsGitName(s) {
			continue
		}
		last := i == len(segs)-1
		if last {
			// The unit directory itself, or a file named .git (a
			// worktree pointer): neither is a member.
			return "", false
		}
		return DirKey(ws, strings.Join(segs[:i+1], "/")), true
	}
	return "", false
}

// IsGitUnitKey reports whether key is a git unit's own directory key: a
// directory key whose last segment folds to ".git" and that is not inside
// another unit.
func IsGitUnitKey(key string) bool {
	ws, segs, kind := relSegments(key)
	if kind != KeyDir || len(segs) == 0 || !IsGitName(segs[len(segs)-1]) {
		return false
	}
	_, inside := GitUnit(key)
	return !inside && ws != ""
}

// Countable reports whether an item counts for the deletion breaker's
// denominator: a file outside any git unit, or a git unit key.
func Countable(key string, it model.Item) bool {
	switch KindOf(it) {
	case KindFile:
		_, member := GitUnit(key)
		return !member
	case KindGit:
		return true
	}
	return false
}

// HeldBackSegment reports whether any segment of rel (compared after
// FoldName) is one of names. Callers pass a directory path.
func HeldBackSegment(rel string, names []string) bool {
	if rel == "" || len(names) == 0 {
		return false
	}
	folded := make([]string, len(names))
	for i, n := range names {
		folded[i] = FoldName(n)
	}
	for _, seg := range strings.Split(rel, "/") {
		if slices.Contains(folded, FoldName(seg)) {
			return true
		}
	}
	return false
}

// HeldBackKey reports whether an item key lies in a held-back folder: a
// directory key whose path has a held-back segment, or a file or link
// whose parent path has one.
func HeldBackKey(key string, names []string) bool {
	_, rel, kind, err := SplitKey(key)
	if err != nil || rel == "" {
		return false
	}
	if kind == KeyEntry {
		rel = path.Dir(rel)
		if rel == "." {
			return false
		}
	}
	return HeldBackSegment(rel, names)
}

// maxNameBytes bounds one file name.
const maxNameBytes = 255

// maxConflictSuffix is the highest number tried for a taken sibling name.
const maxConflictSuffix = 99

// ConflictName returns the sibling path that keeps a conflict loser:
// "<stem> (conflict from <alias> <date>)<ext>", with " 2" ... " 99" before
// <ext> while taken reports the name as used. <ext> starts at the last
// "." when there is a "." after the first character (".env" has none).
// The stem is cut at a rune boundary so the name fits in 255 bytes. ok is
// false when no name is free.
func ConflictName(rel string, loser ids.Alias, date string, taken func(rel string) bool) (string, bool) {
	dir, base := path.Split(rel)
	stem, ext := base, ""
	if i := strings.LastIndex(base, "."); i > 0 {
		stem, ext = base[:i], base[i:]
	}
	tag := " (conflict from " + string(loser) + " " + date + ")"
	for n := 1; n <= maxConflictSuffix; n++ {
		suffix := tag
		if n > 1 {
			suffix += " " + itoa(n)
		}
		room := maxNameBytes - len(suffix) - len(ext)
		if room < 1 {
			return "", false
		}
		s := cutRunes(stem, room)
		if s == "" {
			return "", false
		}
		name := dir + s + suffix + ext
		if taken == nil || !taken(name) {
			return name, true
		}
	}
	return "", false
}

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

// cutRunes returns the longest prefix of s of at most n bytes that ends
// at a rune boundary.
func cutRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// FoldName returns s in Unicode NFC with every rune replaced by the
// smallest rune of its simple case-folding orbit, so names that differ
// only in letter case or composition compare equal.
func FoldName(s string) string {
	s = norm.NFC.String(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		b.WriteRune(foldRune(r))
	}
	return b.String()
}

// foldRune returns the smallest rune of r's simple folding orbit.
func foldRune(r rune) rune {
	min := r
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		if f < min {
			min = f
		}
	}
	return min
}

// FoldRel folds every segment of a slash-separated path.
func FoldRel(rel string) string {
	segs := strings.Split(rel, "/")
	for i, s := range segs {
		segs[i] = FoldName(s)
	}
	return strings.Join(segs, "/")
}
