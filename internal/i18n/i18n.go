package i18n

import "fmt"

// Msg is a message id.
type Msg string

var english = map[Msg]string{}

// T returns the localized text for id, formatted with args. Unknown ids
// return the id itself so missing strings are visible in tests.
func T(id Msg, args ...any) string {
	f, ok := english[id]
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
	for k, v := range m {
		english[k] = v
	}
}
