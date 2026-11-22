package hook

import (
	"context"
	"io"

	"github.com/kazupc/paritydesk/internal/state"
)

// SessionStartInput is the subset of the hook payload paritydesk reads.
// Every field is untrusted.
type SessionStartInput struct {
	SessionID     string `json:"session_id"`
	HookEventName string `json:"hook_event_name"`
	Source        string `json:"source"`
}

// StateReader loads root.json without taking the run lock.
type StateReader interface {
	Load() (*state.Root, error)
}

// SessionStart reads the payload from in (bounded size), consults state
// and writes at most one fixed line to out. It always returns nil.
func SessionStart(ctx context.Context, in io.Reader, out io.Writer, st StateReader) error {
	return nil
}
