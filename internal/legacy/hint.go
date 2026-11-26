package legacy

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"syscall"
)

// Hint is what profile adopt --from-legacy takes from the earlier script's
// state file ({active, layout_seeded, in_default}). It is a hint only: it
// never changes an identity, an epoch or a base, and its labels are never
// printed.
type Hint struct {
	Active            string // validated label, "" when absent or dropped
	InDefault         string // validated label
	LayoutSeededCount int
}

// HintWarning is a constant id about the hint.
type HintWarning string

// Hint warnings.
const (
	HintType      HintWarning = "hint_type"       // a member had the wrong type and was dropped
	HintLabel     HintWarning = "hint_label"      // a label was not usable and was dropped
	HintStale     HintWarning = "hint_stale"      // the hint does not match the folders; ignored
	HintMidSwitch HintWarning = "hint_mid_switch" // the earlier script's switch did not finish
)

// Errors of ReadHint and ParseHint. Their texts are constant: they never
// carry file content or the path.
var (
	ErrHintNotAbs     = errors.New("the state file must be given as an absolute path")
	ErrHintNotRegular = errors.New("the state file is not a regular file")
	ErrHintSymlink    = errors.New("the state file is a symbolic link")
	ErrHintMissing    = errors.New("the state file does not exist")
	ErrHintTooLarge   = errors.New("the state file is larger than 64 KiB")
	ErrHintUnreadable = errors.New("the state file could not be read")
	ErrHintSyntax     = errors.New("the state file is not one JSON object")
)

// Limits of the hint file.
const (
	MaxHintBytes        = 64 << 10
	maxHintDepth        = 8
	maxLayoutSeeded     = 64
	hintMemberActive    = "active"
	hintMemberInDefault = "in_default"
	hintMemberLayout    = "layout_seeded"
)

var hintLabelRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,48}$`)

// ReadHint reads and parses the state file at path. The file is opened
// without following a symlink and without blocking on a FIFO, then checked
// on the descriptor (no check-then-open race): it must be a regular file
// of at most 64 KiB.
func ReadHint(path string) (*Hint, []HintWarning, error) {
	if !filepath.IsAbs(path) {
		return nil, nil, ErrHintNotAbs
	}
	f, err := os.OpenFile(path, os.O_RDONLY|openFlags, 0)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil, ErrHintMissing
	case errors.Is(err, syscall.ELOOP):
		return nil, nil, ErrHintSymlink
	case err != nil:
		return nil, nil, ErrHintUnreadable
	}
	defer f.Close()
	fi, err := f.Stat()
	switch {
	case err != nil:
		return nil, nil, ErrHintUnreadable
	case fi.Mode()&fs.ModeSymlink != 0:
		return nil, nil, ErrHintSymlink
	case !fi.Mode().IsRegular():
		return nil, nil, ErrHintNotRegular
	case fi.Size() > MaxHintBytes:
		return nil, nil, ErrHintTooLarge
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxHintBytes+1))
	if err != nil {
		return nil, nil, ErrHintUnreadable
	}
	if len(b) > MaxHintBytes {
		return nil, nil, ErrHintTooLarge
	}
	return ParseHint(b)
}

// ParseHint parses the state file's content: one JSON object (nesting at
// most 8, nothing after it). Unknown members are ignored; a member of the
// wrong type is dropped with HintType; a label that is not
// [A-Za-z0-9._-]{1,48} is dropped with HintLabel. With duplicate keys the
// last one wins. layout_seeded must be an object of at most 64 members; it
// is only counted.
func ParseHint(b []byte) (*Hint, []HintWarning, error) {
	if err := checkJSON(b, maxHintDepth); err != nil {
		return nil, nil, ErrHintSyntax
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, nil, ErrHintSyntax
	}
	h := &Hint{}
	warn := map[HintWarning]bool{}
	label := func(name string) string {
		raw, ok := doc[name]
		if !ok {
			return ""
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil || string(raw) == "null" {
			warn[HintType] = true
			return ""
		}
		if !hintLabelRE.MatchString(s) {
			warn[HintLabel] = true
			return ""
		}
		return s
	}
	h.Active = label(hintMemberActive)
	h.InDefault = label(hintMemberInDefault)
	if raw, ok := doc[hintMemberLayout]; ok {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil || m == nil || len(m) > maxLayoutSeeded {
			warn[HintType] = true
		} else {
			h.LayoutSeededCount = len(m)
		}
	}
	return h, sortedWarnings(warn), nil
}

func sortedWarnings(m map[HintWarning]bool) []HintWarning {
	if len(m) == 0 {
		return nil
	}
	out := make([]HintWarning, 0, len(m))
	for w := range m {
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
