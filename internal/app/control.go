package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/pderr"
)

// AppleScript and Apple Event error numbers osascript reports.
const (
	osaNotPermitted   = -1743  // errAEEventNotPermitted: Automation denied
	osaWouldNeedUser  = -1744  // errAEEventWouldRequireUserConsent: Automation not decided, no prompt possible
	osaUserCanceled   = -128   // userCanceledErr: the app cancelled the quit (terminateCancel)
	osaProcNotFound   = -600   // procNotFound: the app is not running
	osaTimeout        = -1712  // errAETimeout: no reply in time
	osaPrivilegeError = -10004 // errAEPrivilegeError: refused before delivery (a sandboxed sender)
)

// osaOutcome classifies an osascript failure.
type osaOutcome int

const (
	osaOther      osaOutcome = iota // anything else, including no number
	osaAutomation                   // -1743, -1744: Automation (TCC) refused the event
	osaRefused                      // -128: the target answered that it cancelled
	osaNotRunning                   // -600: the target is not running
	osaTimedOut                     // -1712: no reply in time
)

// classifyOSA maps an AppleScript error number to its outcome. Only -1743
// and -1744 mean missing Automation (an unanswered Automation prompt
// shows as -1712, which cannot be told apart from a slow reply); -10004,
// an unknown number and no number at all are osaOther.
func classifyOSA(status int) osaOutcome {
	switch status {
	case osaNotPermitted, osaWouldNeedUser:
		return osaAutomation
	case osaUserCanceled:
		return osaRefused
	case osaProcNotFound:
		return osaNotRunning
	case osaTimeout:
		return osaTimedOut
	default:
		return osaOther
	}
}

// notifyTitle replaces a title that is empty after sanitizing.
const notifyTitle = "paritydesk"

// osaController drives the app through the fixed command templates.
type osaController struct{ run Runner }

// Quit sends the quit Apple Event. -1743 or -1744 give *pderr.Error
// AutomationMissing wrapping ErrQuitNotDelivered and ErrAutomationDenied;
// -128 (the app cancelled the quit) gives *pderr.Error AppDidNotQuit
// wrapping ErrQuitRefused; -1712 (no reply in time) gives *pderr.Error
// AppDidNotQuit wrapping ErrEventTimedOut; -600 gives ErrNotRunning; any
// other failure (-10004, no number, ...) gives *pderr.Error AppDidNotQuit
// wrapping ErrQuitNotDelivered. The raw *CommandError stays in the chain.
func (c osaController) Quit(ctx context.Context, bundleID string) error {
	_, err := c.run.Run(ctx, TmplQuit, bundleID)
	return mapQuitError(err)
}

// ProbeAutomation sends the harmless "get name" event; -1743 or -1744
// give *pderr.Error AutomationMissing wrapping ErrAutomationDenied, -600
// gives ErrNotRunning, anything else *pderr.Error Internal (wrapping
// ErrEventTimedOut for -1712).
func (c osaController) ProbeAutomation(ctx context.Context, bundleID string) error {
	_, err := c.run.Run(ctx, TmplProbe, bundleID)
	return mapOSAError("app/probe", err, nil, pderr.Internal)
}

// mapQuitError maps the failure of the quit command. A refusal is a
// delivered event: it never wraps ErrQuitNotDelivered or
// ErrAutomationDenied.
func mapQuitError(err error) error {
	var ce *CommandError
	if errors.As(err, &ce) && classifyOSA(ce.OSAStatus) == osaRefused {
		return pderr.Wrap(pderr.AppDidNotQuit, "app/quit", fmt.Errorf("%w: %w", ErrQuitRefused, err))
	}
	return mapOSAError("app/quit", err, ErrQuitNotDelivered, pderr.AppDidNotQuit)
}

// mapOSAError maps an osascript failure. notDelivered, when set, is
// wrapped into every failure except -600 and -1712 (a timed-out event may
// have been delivered; it wraps ErrEventTimedOut instead); other is the
// code of every failure that is neither Automation nor -600. The raw
// error stays in the chain, so callers can read the AppleScript error
// number from it (the quit step names it in its message).
func mapOSAError(op string, err, notDelivered error, other pderr.Code) error {
	if err == nil {
		return nil
	}
	wrap := func(e error) error {
		if notDelivered == nil {
			return e
		}
		return fmt.Errorf("%w: %w", notDelivered, e)
	}
	var ce *CommandError
	if !errors.As(err, &ce) {
		return fmt.Errorf("%s: %w", op, wrap(err))
	}
	switch classifyOSA(ce.OSAStatus) {
	case osaAutomation:
		return pderr.Wrap(pderr.AutomationMissing, op, wrap(fmt.Errorf("%w: %w", ErrAutomationDenied, err)))
	case osaNotRunning:
		return fmt.Errorf("%s: %w", op, ErrNotRunning)
	case osaTimedOut:
		return pderr.Wrap(other, op, fmt.Errorf("%w: %w", ErrEventTimedOut, err))
	default:
		return pderr.Wrap(other, op, wrap(err))
	}
}

// Launch opens the app by bundle id.
func (c osaController) Launch(ctx context.Context, bundleID string) error {
	if _, err := c.run.Run(ctx, TmplOpenBundle, bundleID); err != nil {
		return fmt.Errorf("app/launch: %w", err)
	}
	return nil
}

// OpenSession opens the session link rebuilt from a validated id.
func (c osaController) OpenSession(ctx context.Context, id ids.SessionID) error {
	url, err := SessionURL(id)
	if err != nil {
		return err
	}
	if _, err := c.run.Run(ctx, TmplOpenURL, url); err != nil {
		return fmt.Errorf("app/open-session: %w", err)
	}
	return nil
}

// Notify posts a notification. Both strings are sanitized (control
// characters dropped, leading '-' removed, length bounded); an empty title
// becomes the constant "paritydesk", and an empty text sends nothing.
func (c osaController) Notify(ctx context.Context, title, text string) error {
	title = sanitizeNotify(title, maxNotifyTitle)
	if title == "" {
		title = notifyTitle
	}
	text = sanitizeNotify(text, maxNotifyText)
	if text == "" {
		return nil
	}
	if _, err := c.run.Run(ctx, TmplNotify, title, text); err != nil {
		return fmt.Errorf("app/notify: %w", err)
	}
	return nil
}

// sanitizeNotify makes s acceptable to Argv's notification check: valid
// UTF-8, control characters replaced by spaces, surrounding spaces and
// leading '-' removed, at most max runes.
func sanitizeNotify(s string, max int) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimLeftFunc(s, func(r rune) bool { return r == '-' || unicode.IsSpace(r) })
	s = strings.TrimRightFunc(s, unicode.IsSpace)
	if utf8.RuneCountInString(s) > max {
		r := []rune(s)
		s = strings.TrimSpace(string(r[:max]))
	}
	return s
}

// gitPorcelain answers GitStatus with the hardened git template.
type gitPorcelain struct{ run Runner }

// Dirty reports whether `git status --porcelain` lists anything.
func (g gitPorcelain) Dirty(ctx context.Context, dir string) (bool, error) {
	out, err := g.run.Run(ctx, TmplGitPorcelain, dir)
	if err != nil {
		return false, fmt.Errorf("app/git-status: %w", err)
	}
	return len(bytes.TrimSpace(out)) > 0, nil
}
