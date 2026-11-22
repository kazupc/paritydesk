package denylist

import (
	"path"
	"strings"
)

// paths are data-folder-relative globs (slash-separated, "**" matches any
// number of path segments). Matching is case-insensitive because the
// default macOS volume is.
var paths = []string{
	"config.json", // identity and token cache; read by a span scan only
	"Cookies",
	"Cookies-journal",
	"Login Data",
	"Login Data-journal",
	"Login Data For Account",
	"Login Data For Account-journal",
	"Web Data",
	"Web Data-journal",
	"Trust Tokens",
	"Trust Tokens-journal",
	"Local State",
	"Network/**",
	"Session Storage/**",
	"IndexedDB/**",
	"Local Storage/**", // written only through Local Storage ops, never as files
}

// lsKeyFragments are case-insensitive substrings that make a Local Storage
// item name off limits.
var lsKeyFragments = []string{
	"token",
	"oauth",
	"auth",
	"credential",
	"cookie",
	"password",
	"secret",
	// The app's own backup of the sidebar store (ADR 0004). Writing it
	// could make the app restore a state paritydesk did not plan.
	"dframe-store.backup",
}

// Paths returns a copy of the fixed path globs.
func Paths() []string { return append([]string(nil), paths...) }

// LSKeyFragments returns a copy of the fixed Local Storage name fragments.
func LSKeyFragments() []string { return append([]string(nil), lsKeyFragments...) }

// MatchPath reports whether rel (relative to a data folder) is covered by
// the floor. A directory named by a "dir/**" glob is itself covered.
func MatchPath(rel string) bool {
	for _, p := range paths {
		if MatchGlob(p, rel) {
			return true
		}
	}
	return false
}

// MatchLSKey reports whether a Local Storage item name is covered by the
// floor.
func MatchLSKey(name string) bool {
	n := strings.ToLower(name)
	for _, f := range lsKeyFragments {
		if strings.Contains(n, f) {
			return true
		}
	}
	return false
}

// MatchGlob matches a slash-separated relative path against pattern,
// case-insensitively. Segments use path.Match syntax; a "**" segment
// matches zero or more segments. A malformed pattern matches everything,
// so a bad deny entry fails closed.
func MatchGlob(pattern, rel string) bool {
	return matchSegs(split(strings.ToLower(pattern)), split(strings.ToLower(rel)))
}

func split(s string) []string {
	s = strings.Trim(s, "/")
	if s == "" {
		return nil
	}
	return strings.Split(s, "/")
}

func matchSegs(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			rest := pat[1:]
			for i := 0; i <= len(segs); i++ {
				if matchSegs(rest, segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		ok, err := path.Match(pat[0], segs[0])
		if err != nil {
			return true
		}
		if !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}
