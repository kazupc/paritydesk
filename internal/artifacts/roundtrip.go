package artifacts

import (
	"regexp"
	"strings"
)

// titleElemRE finds the first <title>...</title> element (ASCII
// case-insensitive tag).
var titleElemRE = regexp.MustCompile(`(?is)<title(?:\s[^>]*)?>.*?</title\s*>`)

// openTagRE finds wrapper-like open tags in a read-back.
var openTagRE = regexp.MustCompile(`<(untrusted-document-[0-9A-Za-z]{1,64})>`)

// Compare classifies a read-back against the archived content it was
// published from (7.6): exact, wrapped (the double-wrapping check),
// title_only, or other.
func Compare(archived, readBack string, k Knobs) string {
	if archived == readBack {
		return ClassExact
	}
	if wrappedCopy(archived, readBack, k) {
		return ClassWrapped
	}
	if stripTitle(archived) == stripTitle(readBack) {
		return ClassTitleOnly
	}
	return ClassOther
}

// wrappedCopy reports a read-back that is the archived content inside a
// wrapper tag the archived content does not contain: whole, in an
// accepted form, or anywhere inside it.
func wrappedCopy(archived, readBack string, k Knobs) bool {
	re := k.tagRE()
	for _, m := range openTagRE.FindAllStringSubmatch(readBack, -1) {
		tag := m[1]
		if !re.MatchString(tag) {
			continue
		}
		open, closing := "<"+tag+">", "</"+tag+">"
		if strings.Contains(archived, open) {
			continue
		}
		trimmed := strings.TrimSpace(readBack)
		if inner, ok := unwrapWith(trimmed, tag); ok {
			forms := k.Forms
			if len(forms) == 0 {
				forms = []string{"bare", "nl"}
			}
			if body, _, err := matchForm(inner, lineCount(archived), forms); err == nil && body == archived {
				return true
			}
		}
		i := strings.Index(readBack, open)
		j := strings.LastIndex(readBack, closing)
		if i >= 0 && j > i && strings.Contains(readBack[i+len(open):j], archived) {
			return true
		}
	}
	return false
}

// stripTitle empties the first title element.
func stripTitle(s string) string {
	loc := titleElemRE.FindStringIndex(s)
	if loc == nil {
		return s
	}
	return s[:loc[0]] + "<title></title>" + s[loc[1]:]
}
