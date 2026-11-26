package artifacts

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// DefaultMaxTitleUnits is artifacts.max_title_units of the embedded
// manifest.
const DefaultMaxTitleUnits = 256

// wrapperPrefix starts every per-call wrapper tag of the artifact tools.
const wrapperPrefix = "<untrusted-document-"

// defaultTagRE is the default wrapper tag pattern (unanchored copy of
// artifacts.wrapper_tag_pattern), used to unwrap a title without a notice.
var defaultTagRE = regexp.MustCompile(`^untrusted-document-[0-9a-f]{8,64}$`)

// ValidTitle reports whether t can be stored and passed in a publish
// input: valid UTF-8, 1 to DefaultMaxTitleUnits UTF-16 code units, no
// control, format or bidi characters, no line or paragraph separator,
// and no wrapper tag text (8.8).
func ValidTitle(t string) bool { return validTitleN(t, DefaultMaxTitleUnits) }

func validTitleN(t string, maxUnits int) bool {
	if t == "" || !utf8.ValidString(t) || strings.Contains(t, wrapperPrefix) {
		return false
	}
	units := 0
	for _, r := range t {
		if unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Bidi_Control, r) ||
			r == 0x2028 || r == 0x2029 {
			return false
		}
		units += utf16.RuneLen(r)
	}
	return units <= maxUnits
}

// NFCStable reports whether t is already in NFC (8.8, m13): only such a
// title is passed in a publish input.
func NFCStable(t string) bool { return norm.NFC.String(t) == t }

// TitleKey is the key every title is stored under (M11): the raw title
// with one exact wrapper removed (the tag a notice names, or any tag of
// the wrapper pattern), then "" when it is not a valid title, else the
// hex sha256 of the title.
func TitleKey(raw string, tag string) string {
	t := UnwrapTitle(raw, tag)
	if !ValidTitle(t) {
		return ""
	}
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// UnwrapTitle removes one wrapper from raw: "<tag>t</tag>" with the
// named tag, or, without one, with any tag of the default pattern.
func UnwrapTitle(raw, tag string) string {
	if tag != "" {
		if t, ok := unwrapWith(raw, tag); ok {
			return t
		}
	}
	if strings.HasPrefix(raw, wrapperPrefix) {
		end := strings.IndexByte(raw, '>')
		if end > 1 {
			name := raw[1:end]
			if defaultTagRE.MatchString(name) {
				if t, ok := unwrapWith(raw, name); ok {
					return t
				}
			}
		}
	}
	return raw
}

func unwrapWith(raw, tag string) (string, bool) {
	open, closing := "<"+tag+">", "</"+tag+">"
	if len(raw) >= len(open)+len(closing) && strings.HasPrefix(raw, open) && strings.HasSuffix(raw, closing) {
		return raw[len(open) : len(raw)-len(closing)], true
	}
	return "", false
}

// titleValue returns the title to store: the unwrapped title when valid,
// else "".
func titleValue(raw, tag string) string {
	t := UnwrapTitle(raw, tag)
	if !ValidTitle(t) {
		return ""
	}
	return t
}
