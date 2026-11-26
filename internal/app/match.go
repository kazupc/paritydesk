package app

import (
	"path/filepath"
	"strings"

	"github.com/kazupc/paritydesk/internal/ids"
)

// pathMatcher reports whether a path equals or lies under one of the
// prefixes. Both open-file backends (the native scan and lsof) use it, so
// they filter identically.
//
// Each prefix is kept as given (cleaned) and, when it differs, in its
// resolved form: the kernel reports resolved vnode paths (/var comes back
// as /private/var), while socket paths are as bound. A path also matches
// when its case-folded, NFC-normalized form lies under a folded prefix:
// APFS is normally case- and normalization-insensitive, so a process may
// hold the folder under another spelling. On a case-sensitive volume the
// fold only over-matches, which makes a folder look busy (the safe
// direction).
type pathMatcher struct{ exact, folded []string }

// newPathMatcher builds the matcher; empty prefixes are ignored.
func newPathMatcher(prefixes []string) pathMatcher {
	var m pathMatcher
	seen := map[string]bool{}
	add := func(p string) {
		if seen[p] {
			return
		}
		seen[p] = true
		m.exact = append(m.exact, p)
		m.folded = append(m.folded, ids.FoldName(p))
	}
	for _, pre := range prefixes {
		if pre == "" {
			continue
		}
		c := filepath.Clean(pre)
		add(c)
		if r, err := filepath.EvalSymlinks(c); err == nil && filepath.Clean(r) != c {
			add(filepath.Clean(r))
		}
	}
	return m
}

// Match reports whether path equals or lies under a prefix, as given,
// resolved, or folded.
func (m pathMatcher) Match(path string) bool {
	p := filepath.Clean(path)
	for _, pre := range m.exact {
		if under(p, pre) {
			return true
		}
	}
	fp := ids.FoldName(p)
	for _, pre := range m.folded {
		if under(fp, pre) {
			return true
		}
	}
	return false
}

// under is the v0.1 rule: p equals pre or lies below it.
func under(p, pre string) bool { return p == pre || strings.HasPrefix(p, pre+"/") }
