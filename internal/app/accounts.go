package app

import (
	"context"
	"fmt"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/ids"
)

// maxAccountChanges bounds the changes AccountChanges keeps (the newest).
const maxAccountChanges = 256

// AccountChange is one account_changed line: the data folder active at
// that time held From and was signed in to To.
type AccountChange struct {
	From, To ids.AccountID
	At       time.Time // zero when the line had no known time
}

// AccountChanges returns the account_changed lines of the window, oldest
// first: back to since (since zero: the newest windowBytes of the log
// set). Ids are validated (ids.ParseAccount); a line with an invalid id or
// From == To is skipped. At most 256 changes are kept (the newest).
// truncated is true when the window did not reach since.
// ErrSignalUnknown when the pattern is empty. Record lines that go back
// in time by more than 65 minutes within one file are ignored.
func (l fileLog) AccountChanges(ctx context.Context, sig compat.Signals, since time.Time) (changes []AccountChange, truncated bool, err error) {
	return l.accountChanges(ctx, sig, windowOpts{Since: since})
}

func (l fileLog) accountChanges(ctx context.Context, sig compat.Signals, o windowOpts) ([]AccountChange, bool, error) {
	if sig.AccountChanged == "" {
		return nil, false, ErrSignalUnknown
	}
	re, err := compileSignal(sig.AccountChanged, "")
	if err != nil {
		return nil, false, fmt.Errorf("app: account change: %w", err)
	}
	from, to := re.re.SubexpIndex("from"), re.re.SubexpIndex("to")
	if from < 0 || to < 0 {
		return nil, false, fmt.Errorf("app: account change: %w: pattern has no from or to group", ErrSignalUnknown)
	}
	var (
		g   orderGuard
		out []AccountChange
	)
	o.OnFile = g.reset
	_, truncated, err := l.readWindow(ctx, o, func(t time.Time, msg []byte) {
		if !g.ok(t) {
			return
		}
		m := re.find(msg)
		if m == nil {
			return
		}
		a, aerr := ids.ParseAccount(string(m[from]))
		b, berr := ids.ParseAccount(string(m[to]))
		if aerr != nil || berr != nil || a == b {
			return
		}
		out = append(out, AccountChange{From: a, To: b, At: t})
		if len(out) > maxAccountChanges {
			copy(out, out[1:])
			out = out[:maxAccountChanges]
		}
	})
	if err != nil {
		return nil, false, err
	}
	return out, truncated, nil
}
