package ids

import (
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// AccountID is an account UUID (lower-case, hyphenated).
type AccountID string

// OrgID is an organization UUID (lower-case, hyphenated).
type OrgID string

// SessionID is a desktop Code session id of the form "local_<uuid>".
type SessionID string

// Alias is a profile alias from the config file.
type Alias string

var (
	uuidRE    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	sessionRE = regexp.MustCompile(`^local_[0-9a-f-]{36}$`)
	aliasRE   = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	folderRE  = regexp.MustCompile(`^Claude-[A-Za-z0-9._-]{1,48}$`)
)

// DefaultFolder is the name of the active data folder.
const DefaultFolder = "Claude"

// reservedFolders are folder names that profiles may never use. Compared
// case-insensitively after NFC normalization. Footprints of other tools
// are added from the compat manifest at runtime.
var reservedFolders = []string{"Claude", "Claude-3p"}

// ParseAccount validates s as an account UUID.
func ParseAccount(s string) (AccountID, error) {
	if !uuidRE.MatchString(s) {
		return "", fmt.Errorf("ids: not an account uuid")
	}
	return AccountID(s), nil
}

// ParseOrg validates s as an organization UUID.
func ParseOrg(s string) (OrgID, error) {
	if !uuidRE.MatchString(s) {
		return "", fmt.Errorf("ids: not an org uuid")
	}
	return OrgID(s), nil
}

// IsUUID reports whether s is a lower-case hyphenated UUID.
func IsUUID(s string) bool { return uuidRE.MatchString(s) }

// ParseSession validates s as a local session id ("local_<uuid>").
func ParseSession(s string) (SessionID, error) {
	if !sessionRE.MatchString(s) {
		return "", fmt.Errorf("ids: not a local session id")
	}
	return SessionID(s), nil
}

// ParseAlias validates s as a profile alias.
func ParseAlias(s string) (Alias, error) {
	if !aliasRE.MatchString(s) {
		return "", fmt.Errorf("ids: alias must match [a-z0-9-]{1,32}")
	}
	return Alias(s), nil
}

// DefaultParkedFolder returns "Claude-<alias>".
func DefaultParkedFolder(a Alias) string { return "Claude-" + string(a) }

// ValidateFolder checks a parked-folder name against the folder pattern and
// the reserved list (plus extra reserved names, e.g. other tools' folders).
func ValidateFolder(name string, extraReserved ...string) error {
	if !folderRE.MatchString(name) {
		return fmt.Errorf("ids: folder must match ^Claude-[A-Za-z0-9._-]{1,48}$")
	}
	n := FoldName(name)
	for _, r := range append(append([]string(nil), reservedFolders...), extraReserved...) {
		if n == FoldName(r) {
			return fmt.Errorf("ids: folder name is reserved")
		}
	}
	return nil
}

// FoldName normalizes a folder name for collision checks: NFC, then
// lower-case. APFS is case-insensitive by default.
func FoldName(name string) string { return strings.ToLower(norm.NFC.String(name)) }

// Mask shortens an id for display: the first 4 characters and an ellipsis.
// Strings shorter than 8 characters are fully masked.
func Mask(s string) string {
	if len(s) < 8 {
		return "…"
	}
	return s[:4] + "…"
}
