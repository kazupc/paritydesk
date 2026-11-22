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

// AppleScript error numbers osascript reports.
const (
	osaNotPermitted = -1743 // errAEEventNotPermitted: Automation denied
	osaProcNotFound = -600  // procNotFound: the app is not running
)

// notifyTitle replaces a title that is empty after sanitizing.
const notifyTitle = "paritydesk"

// osaController drives the app through the fixed command templates.
type osaController struct{ run Runner }

// Quit sends the quit Apple Event. -1743 gives *pderr.Error
// AutomationMissing wrapping ErrQuitNotDelivered and ErrAutomationDenied;
// -600 gives ErrNotRunning; any other failure gives *pderr.Error
// AppDidNotQuit wrapping ErrQuitNotDelivered.
func (c osaController) Quit(ctx context.Context, bundleID string) error {
	_, err := c.run.Run(ctx, TmplQuit, bundleID)
	return mapOSAError("app/quit", err, ErrQuitNotDelivered, pderr.AppDidNotQuit)
}

// ProbeAutomation sends the harmless "get name" event; errors map as for
// Quit, without ErrQuitNotDelivered.
func (c osaController) ProbeAutomation(ctx context.Context, bundleID string) error {
	_, err := c.run.Run(ctx, TmplProbe, bundleID)
	return mapOSAError("app/probe", err, nil, pderr.Internal)
}

// mapOSAError maps an osascript failure. notDelivered, when set, is
// wrapped into every failure; other is the code of unrecognized command
// failures.
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
	switch ce.OSAStatus {
	case osaNotPermitted:
		return pderr.Wrap(pderr.AutomationMissing, op, wrap(ErrAutomationDenied))
	case osaProcNotFound:
		return fmt.Errorf("%s: %w", op, ErrNotRunning)
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
