package i18n

import (
	"fmt"
	"sync"
)

// Msg is a message id.
type Msg string

var (
	mu      sync.RWMutex
	english = map[Msg]string{}
)

func init() { Register(catalog) }

// T formats id. Unknown ids return the id itself so missing strings are
// visible in tests. Arguments are normalized as by Tx; Text and List
// arguments are rendered first.
func T(id Msg, args ...any) string {
	f := lookup(id)
	if len(args) == 0 {
		return f
	}
	out := make([]any, len(args))
	for i, a := range args {
		out[i] = renderArg(normalize(a))
	}
	return fmt.Sprintf(f, out...)
}

// renderArg renders Text and List arguments.
func renderArg(a any) any {
	switch v := a.(type) {
	case Text:
		return v.String()
	case List:
		return v.String()
	case CommaList:
		return v.String()
	}
	return a
}

// lookup returns the format of id, or the id itself.
func lookup(id Msg) string {
	mu.RLock()
	f, ok := english[id]
	mu.RUnlock()
	if ok {
		return f
	}
	return string(id)
}

// Register adds strings; used by this package at init (and by its tests).
func Register(m map[Msg]string) {
	mu.Lock()
	defer mu.Unlock()
	for k, v := range m {
		english[k] = v
	}
}

// Has reports whether id has a string.
func Has(id Msg) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := english[id]
	return ok
}

// Catalog returns a copy of every registered string, for the policy
// tests that grep user-facing text.
func Catalog() map[Msg]string {
	mu.RLock()
	defer mu.RUnlock()
	out := make(map[Msg]string, len(english))
	for k, v := range english {
		out[k] = v
	}
	return out
}
