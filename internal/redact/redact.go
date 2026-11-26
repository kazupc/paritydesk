package redact

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"regexp"
	"strings"
)

// IDPlaceholder replaces an id embedded in a key name.
const IDPlaceholder = "<id>"

var (
	// uuidRE matches a UUID in either case.
	uuidRE = regexp.MustCompile(`[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}`)
	// longHexRE matches long hex runs (hashes, device ids) in key names.
	longHexRE = regexp.MustCompile(`\b[0-9A-Fa-f]{16,}\b`)
	// usersRE matches any macOS home path, for homes other than Home.
	usersRE = regexp.MustCompile(`/Users/[^/\s"'<>|:]+`)
	// scratchRE matches the start of a scratch workspace key or path:
	// account and organization UUIDs and the workspace leaf. The path
	// inside it runs to the same ends as a home path (maskScratchPaths).
	scratchRE = regexp.MustCompile(`([0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12})/` +
		`([0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12})/` +
		`(scratch-\d{4}-\d{2}-\d{2}-[0-9a-f]{6})`)
)

// Redactor holds the values to hide.
type Redactor struct {
	Home string
	// Unredacted disables redaction (--log-unredacted; stays local).
	Unredacted bool
}

// String redacts home paths, other users' home directories and UUIDs in
// s. Paths under Home become ~/<8 hex digits of the sha256 of the rest>;
// a path runs to a quote, a newline, a tab, one of <>| or ": " (the
// separator of wrapped errors), so a folder name with spaces is hashed
// whole. UUIDs are masked to their first 4 characters. A scratch
// workspace key or path "<uuid>/<uuid>/scratch-<date>-<hex>/<rest>"
// becomes "<mask>/<mask>/<leaf>/<8 hex digits of the sha256 of rest>",
// rest running to the same ends as a home path (so a file name with
// spaces is hashed whole); file names inside a workspace never reach a
// log or a bundle.
func (r Redactor) String(s string) string {
	if r.Unredacted || s == "" {
		return s
	}
	s = maskScratchPaths(s)
	if home := r.home(); home != "" {
		s = r.replaceHomePaths(s, home)
	}
	s = usersRE.ReplaceAllStringFunc(s, func(m string) string { return "/Users/" + shortHash(m[len("/Users/"):]) })
	return uuidRE.ReplaceAllStringFunc(s, maskUUID)
}

// Path redacts a path: paths under Home become ~/<short hash of the rest>
// and Home itself becomes ~. Other paths keep their text, with UUIDs
// masked.
func (r Redactor) Path(p string) string {
	if r.Unredacted || p == "" {
		return p
	}
	if home := r.home(); home != "" {
		clean := filepath.Clean(p)
		if clean == home {
			return "~"
		}
		if rest, ok := strings.CutPrefix(clean, home+"/"); ok {
			return "~/" + shortHash(rest)
		}
	}
	return r.String(p)
}

// Key redacts ids embedded in a Local Storage or JSON key name: UUIDs and
// long hex runs become <id>.
func (r Redactor) Key(k string) string {
	if r.Unredacted {
		return k
	}
	k = uuidRE.ReplaceAllString(k, IDPlaceholder)
	return longHexRE.ReplaceAllString(k, IDPlaceholder)
}

func (r Redactor) home() string {
	if r.Home == "" || !filepath.IsAbs(r.Home) {
		return ""
	}
	h := filepath.Clean(r.Home)
	if h == "/" {
		return ""
	}
	return h
}

// replaceHomePaths hashes every path that starts with home.
func (r Redactor) replaceHomePaths(s, home string) string {
	var b strings.Builder
	for {
		i := strings.Index(s, home)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		end := i + len(home)
		if end < len(s) && s[end] != '/' && !isPathEnd(s, end) {
			// A longer name that merely starts with home.
			b.WriteString(s[:end])
			s = s[end:]
			continue
		}
		b.WriteString(s[:i])
		rest := s[end:]
		n := pathLen(rest)
		b.WriteString(r.Path(home + rest[:n]))
		s = rest[n:]
	}
}

// pathLen returns the length of the path text at the start of s.
func pathLen(s string) int {
	for i := 0; i < len(s); i++ {
		if isPathEnd(s, i) {
			return i
		}
	}
	return len(s)
}

func isPathEnd(s string, i int) bool {
	switch s[i] {
	case '"', '\'', '\n', '\r', '\t', '<', '>', '|', '`':
		return true
	case ':':
		return i+1 < len(s) && s[i+1] == ' '
	}
	return false
}

func shortHash(s string) string {
	h := sha256.Sum256([]byte(strings.TrimSuffix(s, "/")))
	return hex.EncodeToString(h[:])[:8]
}

func maskUUID(u string) string { return u[:4] + "\u2026" }

// maskScratchPaths masks every scratch workspace key or path in s: the
// account and organization UUIDs, and the path inside the workspace up
// to a quote, a newline, a tab, one of <>|` or ": " (pathLen).
func maskScratchPaths(s string) string {
	var b strings.Builder
	for {
		loc := scratchRE.FindStringSubmatchIndex(s)
		if loc == nil {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:loc[0]])
		b.WriteString(maskUUID(s[loc[2]:loc[3]]) + "/" + maskUUID(s[loc[4]:loc[5]]) + "/" + s[loc[6]:loc[7]])
		s = s[loc[1]:]
		if strings.HasPrefix(s, "/") {
			n := pathLen(s)
			if rest := s[1:n]; rest != "" {
				b.WriteString("/" + shortHash(rest))
			}
			s = s[n:]
		}
	}
}
