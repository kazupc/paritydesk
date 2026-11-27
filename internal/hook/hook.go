package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/online"
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
	// PermissionMode is the session's permission mode, when the payload
	// carries it (only known values are kept).
	PermissionMode string `json:"permission_mode"`
}

// Sources of a SessionStart that may show the online line, and the
// permission modes of attended sessions (only they get the "auto" text).
var (
	onlineSources   = map[string]bool{"startup": true, "resume": true}
	attendedModes   = map[string]bool{"default": true, "acceptEdits": true, "plan": true}
	knownPermModes  = map[string]bool{"default": true, "acceptEdits": true, "plan": true, "bypassPermissions": true, "dontAsk": true}
	knownSourceVals = map[string]bool{"startup": true, "resume": true, "clear": true, "compact": true}
)

// OnlineReader is what SessionStart reads of the online folder: the head
// of queue.json and the per-queue show counter.
type OnlineReader interface {
	ReadHead() (*online.QueueHead, error)
	// CreateShown takes the next free show counter of a queue; false when
	// all are taken.
	CreateShown(key string, max int) bool
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
	// Online is the online folder (nil: no online line). Mode is config
	// online.reconcile ("prompt", "auto" or "off"); MaxShows is the
	// manifest's session_start_max_shows (0: 3).
	Online   OnlineReader
	Mode     string
	MaxShows int
	// ArtifactsDue reads the artifact archive's due.json (nil: no
	// artifacts line); ArtifactsOn is config sync.artifacts.
	ArtifactsDue func() (ArtifactsDue, bool)
	ArtifactsOn  bool
}

// ArtifactsDue is the artifact archive's due.json (spec-artifacts 4.6):
// how many artifacts wait for a profile in an epoch.
type ArtifactsDue struct {
	Seq     uint64
	Profile string
	Epoch   uint64
	Count   int
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
	p, ok := ReadPayload(in)
	if !ok {
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
	if line := sessionLine(root, p, d); line != "" {
		_, _ = fmt.Fprintln(out, line)
	}
	return nil
}

// sessionLine applies the online line's reach limits (11): only for a
// startup or resume, at most MaxShows times per queue, the "auto" text
// only in attended permission modes. When the online line may not be
// shown, the next item (drift) is considered instead.
func sessionLine(root *state.Root, p SessionStartInput, d Deps) string {
	var head *online.QueueHead
	if d.Online != nil && d.Mode != "off" {
		head, _ = d.Online.ReadHead()
	}
	id, isOnline := pendingItem(root, head, d.Mode)
	max := d.MaxShows
	if max <= 0 {
		max = 3
	}
	if isOnline {
		if onlineSources[p.Source] && d.Online.CreateShown(head.ID, max) {
			if id == i18n.HookOnlineAuto && !attendedModes[p.PermissionMode] {
				id = i18n.HookOnlinePrompt
			}
			return i18n.T(id)
		}
		id, _ = pendingItem(root, nil, d.Mode)
	}
	// Item 4b: artifacts from another profile wait (after the online
	// line, before drift).
	if (id == "" || id == i18n.HookDrift) && artifactsDue(root, p, d, max) {
		return i18n.T(i18n.HookArtifactsDue)
	}
	return msgText(id)
}

// artifactsDue reports the artifacts line: for a startup or resume, when
// due.json names the active profile and epoch with a count, artifact
// sync is on, and the line was shown fewer than max times for that
// due.json.
func artifactsDue(root *state.Root, p SessionStartInput, d Deps, max int) bool {
	if !d.ArtifactsOn || d.ArtifactsDue == nil || d.Online == nil || !onlineSources[p.Source] {
		return false
	}
	due, ok := d.ArtifactsDue()
	if !ok || due.Count <= 0 || due.Profile != string(root.Active) || due.Epoch != root.Epoch {
		return false
	}
	return d.Online.CreateShown("due-"+strconv.FormatUint(due.Seq, 10), max)
}

func msgText(id i18n.Msg) string {
	if id == "" {
		return ""
	}
	return i18n.T(id)
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
	if !knownSourceVals[p.Source] {
		p.Source = ""
	}
	if !knownPermModes[p.PermissionMode] {
		p.PermissionMode = ""
	}
	return p, true
}

// Skewed reports whether the plugin's version, when given, differs from
// the binary's in major or minor version. An unparsable version counts as
// skew, except a binary built from a branch head (Homebrew's
// "HEAD-<commit>" from brew install --HEAD, Go's "(devel)", or a Go
// pseudo-version such as v0.0.0-20060102150405-0123456789ab from go
// install in a clone), which has no release version to compare.
func Skewed(plugin, binary string) bool {
	if plugin == "" || headBuild(binary) {
		return false
	}
	pm, ok1 := majorMinor(plugin)
	bm, ok2 := majorMinor(binary)
	return !ok1 || !ok2 || pm != bm
}

// pseudoVersion matches a Go module pseudo-version (vX.0.0-<time>-<hash>,
// vX.Y.Z-pre.0.<time>-<hash> or vX.Y.(Z+1)-0.<time>-<hash>, with a
// 14-digit UTC time and a 12-digit commit hash), optionally marked +dirty
// as go build stamps a modified checkout.
var pseudoVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+-(?:[0-9A-Za-z.-]*\.)?[0-9]{14}-[0-9a-f]{12}(?:\+dirty)?$`)

// headBuild reports a version string of a build from a branch head.
func headBuild(v string) bool {
	v = strings.TrimSpace(v)
	return strings.HasPrefix(v, "HEAD-") || v == "HEAD" || v == "(devel)" || pseudoVersion.MatchString(v)
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
// its sign-in, then a needed sign-in, then changes waiting to be applied
// in Claude (head is queue.json's head; mode is online.reconcile), then
// drift found by the last verify. The reach limits of the online line are
// SessionStart's (sessionLine).
func PendingLine(root *state.Root, head *online.QueueHead, mode string) string {
	id, _ := pendingItem(root, head, mode)
	return msgText(id)
}

// pendingItem returns the message of the first pending item and whether
// it is the online line.
func pendingItem(root *state.Root, head *online.QueueHead, mode string) (i18n.Msg, bool) {
	switch {
	case root == nil:
		return "", false
	case root.Run != nil:
		return i18n.HookRecover, false
	case root.PendingAdd != nil:
		return i18n.HookPendingAdd, false
	case root.LastVerify != nil && root.LastVerify.SignIn:
		return i18n.HookSignIn, false
	case OnlinePending(root, head) && (mode == "prompt" || mode == "auto"):
		if mode == "auto" {
			return i18n.HookOnlineAuto, true
		}
		return i18n.HookOnlinePrompt, true
	case root.LastVerify != nil && len(root.LastVerify.Drift) > 0:
		return i18n.HookDrift, false
	}
	return "", false
}

// OnlinePending reports a reconcile queue waiting for a session: the root
// record is pending or active for this epoch and the active profile, and
// queue.json's head has the same id and is not complete or cancelled.
// Artifacts queues have their own line.
func OnlinePending(root *state.Root, head *online.QueueHead) bool {
	rec := root.Online
	if rec == nil || head == nil || rec.Kind != "" {
		return false
	}
	if rec.State != string(online.QueuePending) && rec.State != string(online.QueueActive) {
		return false
	}
	if rec.Epoch != root.Epoch || rec.Profile != root.Active || head.ID != rec.Queue {
		return false
	}
	return head.State != online.QueueComplete && head.State != online.QueueCancelled
}
