package app

import (
	"github.com/kazupc/paritydesk/internal/ids"
)

// sessionURLPrefix is the link form confirmed by spike S10 (ADR 0003):
// it focuses a given local session, warm or cold.
const sessionURLPrefix = "claude://claude.ai/epitaxy/"

// SessionURL rebuilds the reopen link from a session id. The id is
// re-validated here so that no caller can pass an arbitrary string to the
// open command (C5).
func SessionURL(id ids.SessionID) (string, error) {
	v, err := ids.ParseSession(string(id))
	if err != nil {
		return "", ErrInvalidSession
	}
	return sessionURLPrefix + string(v), nil
}
