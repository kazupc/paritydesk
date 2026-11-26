package compat

import (
	"regexp/syntax"
	"strings"

	"github.com/kazupc/paritydesk/internal/ids"
)

// SessionPlaceholder is replaced in signal patterns by a group that
// captures a local session id, or by one quoted session id.
const SessionPlaceholder = "{session}"

// SessionGroup is the capture group that replaces SessionPlaceholder when
// no single session is asked about.
const SessionGroup = `(?P<session>local_` + ids.UUIDPattern + `)`

// RequiredLiteral returns the longest literal text that every match of a
// signal pattern contains: the longest case-sensitive OpLiteral among the
// top-level concatenation of the simplified syntax tree (or the tree
// itself when it is one literal). {session} is first replaced by the
// session capture group (never by the sample id, which would become the
// longest literal and make the prefilter reject every real line); "" when
// the pattern does not parse or has no such literal.
func RequiredLiteral(pattern string) string {
	p := strings.ReplaceAll(pattern, SessionPlaceholder, SessionGroup)
	re, err := syntax.Parse(p, syntax.Perl)
	if err != nil {
		return ""
	}
	re = re.Simplify()
	best := ""
	consider := func(r *syntax.Regexp) {
		if r.Op != syntax.OpLiteral || r.Flags&syntax.FoldCase != 0 {
			return
		}
		if s := string(r.Rune); len(s) > len(best) {
			best = s
		}
	}
	switch re.Op {
	case syntax.OpLiteral:
		consider(re)
	case syntax.OpConcat:
		for _, sub := range re.Sub {
			consider(sub)
		}
	}
	return best
}
