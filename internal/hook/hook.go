package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/state"
)

// MaxPayload bounds how much of the hook payload is read.
const MaxPayload = 64 << 10

// EventSessionStart is the hook_event_name of the SessionStart payload.
const EventSessionStart = "SessionStart"

// SessionStartInput is the subset of the hook payload paritydesk reads.
// Every field is untrusted.
type SessionStartInput struct {
	SessionID     string `json:"session_id"`
	HookEventName string `json:"hook_event_name"`
	Source        string `json:"source"`
}

// StateReader loads root.json without taking the run lock (state.Store.Load
// never writes).
type StateReader interface {
	Load() (*state.Root, error)
}

// Deps are what SessionStart consults.
type Deps struct {
	State StateReader
	// InsideApp reports whether the hook runs inside the desktop app's
	// process tree (runner.InsideApp over the process table). The hook is
	// silent outside the app, and when InsideApp is nil or fails.
	InsideApp func(ctx context.Context) (bool, error)
	// PluginVersion is the version the plugin's shim passes
	// (--plugin-version); BinaryVersion is this binary's version. When the
	// plugin passes a version and its major.minor differs from the
	// binary's, the hook stays silent (version skew, M11).
	PluginVersion, BinaryVersion string
}

// SessionStart reads at most MaxPayload bytes of the payload from in,
// consults state and writes at most one fixed line to out: when a run is
// open (recover needed), when the last verify found drift or a needed
// sign-in, or when a profile add waits for its sign-in. The line never
// contains untrusted text. It always returns nil: a malformed or oversized
// payload, version skew, a process outside the app and unreadable state
// all mean silence. It never opens Local Storage and never scans folders.
func SessionStart(ctx context.Context, in io.Reader, out io.Writer, d Deps) (err error) {
	defer func() {
		if recover() != nil {
			err = nil
		}
	}()
	if _, ok := ReadPayload(in); !ok {
		return nil
	}
	if Skewed(d.PluginVersion, d.BinaryVersion) {
		return nil
	}
	if d.InsideApp == nil || d.State == nil {
		return nil
	}
	if inside, ierr := d.InsideApp(ctx); ierr != nil || !inside {
		return nil
	}
	root, lerr := d.State.Load()
	if lerr != nil || root == nil {
		return nil
	}
	if line := PendingLine(root); line != "" {
		_, _ = fmt.Fprintln(out, line)
	}
	return nil
}

// ReadPayload reads and validates the payload: at most MaxPayload bytes
// holding one JSON object whose hook_event_name, if present, is
// SessionStart. Its content is not used beyond this check.
func ReadPayload(in io.Reader) (SessionStartInput, bool) {
	if in == nil {
		return SessionStartInput{}, false
	}
	b, err := io.ReadAll(io.LimitReader(in, MaxPayload+1))
	if err != nil || len(b) > MaxPayload {
		return SessionStartInput{}, false
	}
	return ParsePayload(b)
}

// ParsePayload validates one payload document.
func ParsePayload(b []byte) (SessionStartInput, bool) {
	var p SessionStartInput
	b = bytes.TrimSpace(b)
	if len(b) == 0 || b[0] != '{' {
		return p, false
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return SessionStartInput{}, false
	}
	if p.HookEventName != "" && p.HookEventName != EventSessionStart {
		return SessionStartInput{}, false
	}
	return p, true
}

// Skewed reports whether the plugin's version, when given, differs from
// the binary's in major or minor version. An unparsable version counts as
// skew.
func Skewed(plugin, binary string) bool {
	if plugin == "" {
		return false
	}
	pm, ok1 := majorMinor(plugin)
	bm, ok2 := majorMinor(binary)
	return !ok1 || !ok2 || pm != bm
}

// majorMinor returns "<major>.<minor>" of a version such as v0.1.2-dev.
func majorMinor(v string) (string, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return "", false
	}
	major, ok := leadingNumber(parts[0], true)
	if !ok {
		return "", false
	}
	minor, ok := leadingNumber(parts[1], len(parts) == 3)
	if !ok {
		return "", false
	}
	return major + "." + minor, true
}

// leadingNumber returns the decimal number at the start of s. When whole
// is set, s must be only that number.
func leadingNumber(s string, whole bool) (string, bool) {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	if n == 0 || n > 9 || (whole && n != len(s)) {
		return "", false
	}
	if n < len(s) && s[n] != '-' && s[n] != '+' {
		return "", false
	}
	v, err := strconv.Atoi(s[:n])
	if err != nil {
		return "", false
	}
	return strconv.Itoa(v), true
}

// PendingLine returns the one fixed line for what is pending in root, or
// "" when nothing is: an open run first, then a profile add waiting for
// its sign-in, then a sign-in or drift found by the last verify.
func PendingLine(root *state.Root) string {
	switch {
	case root == nil:
		return ""
	case root.Run != nil:
		return i18n.T(i18n.HookRecover)
	case root.PendingAdd != nil:
		return i18n.T(i18n.HookPendingAdd)
	case root.LastVerify != nil && root.LastVerify.SignIn:
		return i18n.T(i18n.HookSignIn)
	case root.LastVerify != nil && len(root.LastVerify.Drift) > 0:
		return i18n.T(i18n.HookDrift)
	}
	return ""
}
