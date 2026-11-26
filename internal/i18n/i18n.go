package i18n

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

// Msg is a message id.
type Msg string

// Lang is a message language.
type Lang string

// The supported languages.
const (
	English  Lang = "en"
	Japanese Lang = "ja"
)

// Langs lists the supported languages in a fixed order.
var Langs = []Lang{English, Japanese}

var (
	mu      sync.RWMutex
	english = map[Msg]string{}
	others  = map[Lang]map[Msg]string{}

	// compat caches, per (language, id), whether the translated format
	// may replace the English one (Compatible). Register and RegisterIn
	// replace it, so a later registration is checked again.
	compat atomic.Pointer[sync.Map]

	process atomic.Value // Lang
	display atomic.Value // Lang
)

func init() {
	compat.Store(new(sync.Map))
	process.Store(English)
	display.Store(English)
	Register(catalog)
}

// SetLang sets the process language (the language of every human line)
// and returns the previous one. Tests restore it with
// defer i18n.SetLang(i18n.SetLang(i18n.Japanese)).
func SetLang(l Lang) (prev Lang) {
	prev = Current()
	process.Store(normalLang(l))
	return prev
}

// Current returns the process language.
func Current() Lang { return process.Load().(Lang) }

// SetDisplayLang sets the language of the additive display_lines JSON
// field and returns the previous one.
func SetDisplayLang(l Lang) (prev Lang) {
	prev = DisplayLang()
	display.Store(normalLang(l))
	return prev
}

// DisplayLang returns the language of display_lines.
func DisplayLang() Lang { return display.Load().(Lang) }

// normalLang maps anything unsupported to English.
func normalLang(l Lang) Lang {
	for _, s := range Langs {
		if l == s {
			return l
		}
	}
	return English
}

// englishOnlyIDs are ids without a translation by design, beyond the
// model-facing prefixes: the detached job's macOS notifications, which
// stay English until a manual check shows osascript renders Japanese
// under the job's LC_ALL=C environment.
var englishOnlyIDs = map[Msg]bool{
	NotifySwitched: true,
	NotifyFinished: true,
	NotifyCheck:    true,
	NotifyStopped:  true,
}

// EnglishOnly reports ids that have no translation by design: the
// model-facing prefixes "hook." and "guard." and the explicit list of
// notification ids.
func EnglishOnly(id Msg) bool {
	s := string(id)
	return strings.HasPrefix(s, "hook.") || strings.HasPrefix(s, "guard.") || englishOnlyIDs[id]
}

// T formats id in the process language. Unknown ids return the id itself
// so missing strings are visible in tests.
func T(id Msg, args ...any) string { return TIn(Current(), id, args...) }

// En formats id in English (the spec's i18n.English; the name English is
// the language constant). The hook package and completion use it.
func En(id Msg, args ...any) string { return TIn(English, id, args...) }

// TIn formats id in l. Lookup order: l's catalog (skipped for
// English-only ids, and skipped when l's format is not verb-compatible
// with the English one), then English, then the id itself. Arguments are
// normalized as by Tx; Text and List arguments are rendered in l first.
func TIn(l Lang, id Msg, args ...any) string {
	f := lookup(l, id)
	if len(args) == 0 {
		return f
	}
	out := make([]any, len(args))
	for i, a := range args {
		out[i] = renderArg(l, normalize(a))
	}
	return fmt.Sprintf(f, out...)
}

// renderArg renders Text and List arguments in l.
func renderArg(l Lang, a any) any {
	switch v := a.(type) {
	case Text:
		return v.In(l)
	case List:
		return v.In(l)
	case CommaList:
		return v.In(l)
	}
	return a
}

// lookup returns the format of id in l, with the fallbacks of TIn.
func lookup(l Lang, id Msg) string {
	mu.RLock()
	en, enOK := english[id]
	var tr string
	trOK := false
	if l != English && !EnglishOnly(id) {
		tr, trOK = others[l][id]
	}
	mu.RUnlock()
	if trOK && enOK && compatible(l, id, en, tr) {
		return tr
	}
	if enOK {
		return en
	}
	return string(id)
}

type compatKey struct {
	l  Lang
	id Msg
}

// compatible caches Compatible(en, tr) per (l, id). The decision depends
// only on the two formats, never on argument data.
func compatible(l Lang, id Msg, en, tr string) bool {
	c := compat.Load()
	k := compatKey{l, id}
	if v, ok := c.Load(k); ok {
		return v.(bool)
	}
	ok := Compatible(en, tr)
	c.Store(k, ok)
	return ok
}

// Register adds English strings; used by this package and pderr at init.
func Register(m map[Msg]string) {
	mu.Lock()
	defer mu.Unlock()
	for k, v := range m {
		english[k] = v
	}
	compat.Store(new(sync.Map))
}

// RegisterIn adds strings of language l (English goes to Register).
func RegisterIn(l Lang, m map[Msg]string) {
	if l == English {
		Register(m)
		return
	}
	mu.Lock()
	defer mu.Unlock()
	cat := others[l]
	if cat == nil {
		cat = map[Msg]string{}
		others[l] = cat
	}
	for k, v := range m {
		cat[k] = v
	}
	compat.Store(new(sync.Map))
}

// Has reports whether id has an English string.
func Has(id Msg) bool { return HasIn(English, id) }

// HasIn reports whether id has a string in l.
func HasIn(l Lang, id Msg) bool {
	mu.RLock()
	defer mu.RUnlock()
	if l == English {
		_, ok := english[id]
		return ok
	}
	_, ok := others[l][id]
	return ok
}

// Catalog returns a copy of every registered English string, for the
// policy tests that grep user-facing text.
func Catalog() map[Msg]string { return CatalogIn(English) }

// CatalogIn returns a copy of every registered string of l.
func CatalogIn(l Lang) map[Msg]string {
	mu.RLock()
	defer mu.RUnlock()
	src := english
	if l != English {
		src = others[l]
	}
	out := make(map[Msg]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
