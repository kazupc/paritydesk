package legacy

import (
	"errors"
	"strings"

	"github.com/kazupc/paritydesk/internal/ids"
)

// Errors of DeriveAlias (constant texts).
var (
	// ErrNoDerivedAlias: the folder is Claude itself (or not a parked
	// folder name), which has no alias in its name.
	ErrNoDerivedAlias = errors.New("legacy: the folder name gives no alias")
	// ErrAliasChars: the name holds a character an alias cannot carry.
	ErrAliasChars = errors.New("legacy: the folder name has characters an alias cannot hold")
	// ErrAliasEmpty: nothing usable is left of the name.
	ErrAliasEmpty = errors.New("legacy: no alias can be made from the folder name")
)

// maxAliasBytes is the longest alias ids.ParseAlias accepts.
const maxAliasBytes = 32

// DeriveAlias derives a profile alias from a parked folder name
// (Claude-<Name>): lower-case, "." and "_" become "-", runs of "-"
// collapse, "-" is trimmed at both ends, the result is cut to 32 bytes
// and trimmed again, and it must pass ids.ParseAlias.
func DeriveAlias(folder string) (ids.Alias, error) {
	rest, ok := strings.CutPrefix(folder, ids.DefaultFolder+"-")
	if !ok || folder == ids.DefaultFolder {
		return "", ErrNoDerivedAlias
	}
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(rest) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case r == '-' || r == '.' || r == '_':
			if !dash {
				b.WriteByte('-')
			}
			dash = true
		default:
			return "", ErrAliasChars
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > maxAliasBytes {
		s = strings.TrimRight(s[:maxAliasBytes], "-")
	}
	if s == "" {
		return "", ErrAliasEmpty
	}
	a, err := ids.ParseAlias(s)
	if err != nil {
		return "", ErrAliasEmpty
	}
	return a, nil
}
