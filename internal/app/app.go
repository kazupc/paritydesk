package app

import (
	"context"
	"errors"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/pderr"
)

// Errors.
var (
	// ErrQuitNotDelivered: the Apple Event could not be delivered (for
	// example Automation permission denied). Nothing was quit.
	ErrQuitNotDelivered = errors.New("app: quit event not delivered")
	// ErrAutomationDenied: macOS refused Automation for Claude.
	ErrAutomationDenied = errors.New("app: automation permission denied")
	// ErrNotRunning: the app is not running.
	ErrNotRunning = errors.New("app: not running")
	// ErrInvalidSession: a session id failed validation.
	ErrInvalidSession = errors.New("app: invalid session id")
)

// Process is one running process.
type Process struct {
	PID     int
	PPID    int
	Exe     string   // absolute executable path (from KERN_PROCARGS2)
	Args    []string // argv[1:]; may contain --user-data-dir
	Started time.Time
}

// ProcessTable lists processes.
type ProcessTable interface {
	Processes(ctx context.Context) ([]Process, error)
}

// Holder is a process with a file open (or cwd) under a watched prefix.
type Holder struct {
	PID     int
	Command string
	Path    string
	Access  string // "r", "w", "u" (read-write), "cwd", "txt"
}

// FileHolders finds processes holding paths under prefixes.
type FileHolders interface {
	Holders(ctx context.Context, prefixes []string) ([]Holder, error)
}

// LockProbe reports whether another process holds an fcntl lock on path.
type LockProbe interface {
	LockHeld(path string) (bool, error)
}

// BundleIdentity identifies the installed app; a change between P0 and
// P5/P11 means an update installed during the run (H12).
type BundleIdentity struct {
	BundleID  string
	Path      string // e.g. /Applications/Claude.app (any install path)
	Version   string // CFBundleShortVersionString
	Build     string // CFBundleVersion
	ExecIno   uint64
	ExecMtime time.Time
	ExecSize  int64
}

// Bundle locates the app and its updater state.
type Bundle interface {
	Locate(ctx context.Context, bundleID string) (BundleIdentity, error)
	// UpdateStaged reports a staged update that would install on quit
	// (updater state file present, or the updater process running).
	UpdateStaged(ctx context.Context, bundleID string) (bool, error)
}

// Controller sends Apple Events and opens the app.
type Controller interface {
	// Quit sends the quit Apple Event and returns once it was delivered
	// (not once the app exited). ErrQuitNotDelivered if it was not.
	Quit(ctx context.Context, bundleID string) error
	// Launch runs open -b bundleID.
	Launch(ctx context.Context, bundleID string) error
	// OpenSession opens the claude:// link for a validated session id.
	OpenSession(ctx context.Context, id ids.SessionID) error
	// ProbeAutomation sends a harmless "get name" event, so macOS asks for
	// Automation while the user is present (init, doctor --grant-automation).
	ProbeAutomation(ctx context.Context, bundleID string) error
	// Notify posts a user notification with constant title and text built
	// from validated fields only.
	Notify(ctx context.Context, title, text string) error
}

// LogEvent is a recognized main.log signal.
type LogEvent string

// Log events (patterns come from the compat manifest's Signals).
const (
	EvQuitVetoed      LogEvent = "quit-vetoed"
	EvTurnEnded       LogEvent = "turn-ended"
	EvFocusedSession  LogEvent = "focused-session"
	EvBridgeConnected LogEvent = "bridge-connected"
	EvPairingDisabled LogEvent = "pairing-disabled"
	EvUncleanExit     LogEvent = "unclean-exit"
	EvLoginRequired   LogEvent = "login-required"
	EvNativeMulti     LogEvent = "native-multi-account"
)

// MainLog reads the app's main.log from byte offsets.
type MainLog interface {
	// Size returns the current size (the offset to read from later).
	Size() (int64, error)
	// Events scans from offset and returns recognized events (with the
	// session id for session events) and the new offset. Lines are never
	// returned or logged verbatim.
	Events(ctx context.Context, from int64, sig compat.Signals, session ids.SessionID) ([]LogEvent, int64, error)
}

// GitStatus runs a read-only `git -C <dir> status --porcelain`.
type GitStatus interface {
	Dirty(ctx context.Context, dir string) (bool, error)
}

// Platform bundles every app-facing dependency.
type Platform struct {
	Procs   ProcessTable
	Holders FileHolders
	Locks   LockProbe
	Bundle  Bundle
	Control Controller
	Log     MainLog
	Git     GitStatus
}

// DarwinConfig configures the macOS platform.
type DarwinConfig struct {
	Home     string // for ~/Library/Logs/Claude/main.log and updater caches
	BundleID string
	Exec     Runner // nil: the real runner in exec.go
}

// NewDarwin returns the macOS implementation.
func NewDarwin(cfg DarwinConfig) *Platform {
	return &Platform{}
}

// IsAppProcess reports whether p belongs to the app's process set: its
// executable lies inside bundlePath, and it is not one of the excluded
// helpers (Chrome's native messaging host, the updater, the crash handler).
func IsAppProcess(p Process, bundlePath string) bool { return false }

// QuiescenceInput describes one quiescence check (D15).
type QuiescenceInput struct {
	BundlePath string
	BundleID   string
	DataRoot   string
	// Folders are the data folders that must have no holders.
	Folders []string
	// LockFiles are the LevelDB LOCK files to probe.
	LockFiles []string
	// After, if set, flags processes started after this time (post-swap
	// check, P8).
	After time.Time
}

// QuiescenceReport is the outcome. Quiet is true only when every signal is
// clear; ReadOnlyHolders (Spotlight, backupd) are warnings only.
type QuiescenceReport struct {
	Quiet           bool
	AppPIDs         []int
	UpdaterRunning  bool
	LocksHeld       []string
	WriteHolders    []Holder
	ReadOnlyHolders []Holder
	StartedAfter    []Process
}

// CheckQuiescence evaluates every D15 signal once.
func (p *Platform) CheckQuiescence(ctx context.Context, in QuiescenceInput) (QuiescenceReport, error) {
	return QuiescenceReport{}, pderr.ErrNotImplemented
}

// QuitResult is the outcome of QuitAndWait.
type QuitResult struct {
	Vetoed  bool // the app showed its "still working" dialog
	Elapsed time.Duration
	Exited  bool
}

// QuitAndWait sends the quit event and waits until the app's process set
// is empty, up to timeout. A veto is reported through onVeto (so the UI
// can tell the user to choose "Wait for Claude" or "Cancel") and waited
// on. A timeout returns *pderr.Error AppDidNotQuit; no signal is ever sent.
func (p *Platform) QuitAndWait(ctx context.Context, bundleID, bundlePath string, sig compat.Signals, timeout time.Duration, onVeto func()) (QuitResult, error) {
	return QuitResult{}, pderr.ErrNotImplemented
}

// WaitTurnEnded waits until main.log reports that the given session's turn
// ended, then a settle delay, so an in-app switch does not trip the app's
// own quit veto (ADR 0003 finding 1).
func (p *Platform) WaitTurnEnded(ctx context.Context, from int64, sig compat.Signals, session ids.SessionID, timeout time.Duration) error {
	return pderr.ErrNotImplemented
}

// The methods below expose the Platform's parts as one interface for the
// orchestrator.

// Locate delegates to Bundle.
func (p *Platform) Locate(ctx context.Context, bundleID string) (BundleIdentity, error) {
	return BundleIdentity{}, pderr.ErrNotImplemented
}

// UpdateStaged delegates to Bundle.
func (p *Platform) UpdateStaged(ctx context.Context, bundleID string) (bool, error) {
	return false, pderr.ErrNotImplemented
}

// Running returns the app's process set (IsAppProcess over Procs).
func (p *Platform) Running(ctx context.Context, bundlePath string) ([]Process, error) {
	return nil, pderr.ErrNotImplemented
}

// Launch delegates to Control.
func (p *Platform) Launch(ctx context.Context, bundleID string) error { return pderr.ErrNotImplemented }

// OpenSession delegates to Control.
func (p *Platform) OpenSession(ctx context.Context, id ids.SessionID) error {
	return pderr.ErrNotImplemented
}

// ProbeAutomation delegates to Control.
func (p *Platform) ProbeAutomation(ctx context.Context, bundleID string) error {
	return pderr.ErrNotImplemented
}

// Notify delegates to Control.
func (p *Platform) Notify(ctx context.Context, title, text string) error {
	return pderr.ErrNotImplemented
}

// LogSize delegates to Log.
func (p *Platform) LogSize() (int64, error) { return 0, pderr.ErrNotImplemented }

// LogEvents delegates to Log.
func (p *Platform) LogEvents(ctx context.Context, from int64, sig compat.Signals, session ids.SessionID) ([]LogEvent, int64, error) {
	return nil, 0, pderr.ErrNotImplemented
}

// GitDirty delegates to Git.
func (p *Platform) GitDirty(ctx context.Context, dir string) (bool, error) {
	return false, pderr.ErrNotImplemented
}

// LockHeld delegates to Locks.
func (p *Platform) LockHeld(path string) (bool, error) { return false, pderr.ErrNotImplemented }
