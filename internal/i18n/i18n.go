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

// T returns the localized text for id, formatted with args. Unknown ids
// return the id itself so missing strings are visible in tests.
func T(id Msg, args ...any) string {
	mu.RLock()
	f, ok := english[id]
	mu.RUnlock()
	if !ok {
		f = string(id)
	}
	if len(args) == 0 {
		return f
	}
	return fmt.Sprintf(f, args...)
}

// Register adds English strings; used by packages at init.
func Register(m map[Msg]string) {
	mu.Lock()
	defer mu.Unlock()
	for k, v := range m {
		english[k] = v
	}
}

// Has reports whether id has an English string.
func Has(id Msg) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := english[id]
	return ok
}

// Catalog returns a copy of every registered English string, for the
// policy tests that grep user-facing text.
func Catalog() map[Msg]string {
	mu.RLock()
	defer mu.RUnlock()
	out := make(map[Msg]string, len(english))
	for k, v := range english {
		out[k] = v
	}
	return out
}
