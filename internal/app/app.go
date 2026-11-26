package app

import (
	"context"
	"errors"
	"time"

	"github.com/kazupc/paritydesk/internal/clock"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/fdscan"
	"github.com/kazupc/paritydesk/internal/ids"
)

// Errors.
var (
	// ErrQuitNotDelivered: the quit Apple Event failed before the app
	// could answer it (refused by macOS, for example -10004 for a
	// sandboxed sender, or an error osascript did not explain). Nothing
	// is known to have quit. Only when it comes with ErrAutomationDenied
	// is Automation the cause. A timeout is ErrEventTimedOut instead.
	ErrQuitNotDelivered = errors.New("app: quit event not delivered")
	// ErrEventTimedOut: an Apple Event got no reply in time (AppleScript
	// error -1712). It may have been delivered: the app can hold its
	// reply while it asks the user whether to quit. macOS also reports it
	// when its Automation prompt was not answered, so it is neither a
	// refusal nor proof of missing Automation.
	ErrEventTimedOut = errors.New("app: the Apple Event got no reply in time")
	// ErrAutomationDenied: macOS refused Automation for Claude
	// (AppleScript error -1743 or -1744, and only those).
	ErrAutomationDenied = errors.New("app: automation permission denied")
	// ErrQuitRefused: the app received the quit event and answered that
	// it cancelled the quit (AppleScript error -128, userCanceledErr, as
	// AppKit replies when applicationShouldTerminate returns
	// terminateCancel). The app keeps running; it may still quit by
	// itself later. It is a quit veto, not missing Automation.
	ErrQuitRefused = errors.New("app: the app refused to quit")
	// ErrNotRunning: the app is not running.
	ErrNotRunning = errors.New("app: not running")
	// ErrInvalidSession: a session id failed validation.
	ErrInvalidSession = errors.New("app: invalid session id")
	// ErrSignalUnknown: the manifest has no pattern for a needed main.log
	// signal; the caller falls back (records and the quit veto only).
	ErrSignalUnknown = errors.New("app: log signal unknown for this version")
	// ErrInvalidArgument: Argv refused a command argument.
	ErrInvalidArgument = errors.New("app: invalid command argument")
)

// JobLabelPrefix prefixes every launchd job label paritydesk creates; a
// run id ([0-9a-z-]{8,64}) completes it. runner.LabelPrefix is this value,
// and Argv accepts only labels of this form.
const JobLabelPrefix = "io.github.kazupc.paritydesk.run."

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
	Access  string    // "r", "w", "u" (read-write), "cwd", "txt"
	Started time.Time // the holder's start time from the native scan; zero from lsof
}

// FileHolders finds processes holding paths under prefixes.
type FileHolders interface {
	Holders(ctx context.Context, prefixes []string) ([]Holder, error)
}

// ScanMode is config app.open_file_scan.
type ScanMode string

// Open-file scan modes.
const (
	ScanAuto   ScanMode = "auto"   // native, lsof when the native scan is unavailable or fails
	ScanNative ScanMode = "native" // native only; a failure is an error
	ScanLsof   ScanMode = "lsof"   // the v0.1 path
)

// HolderScanInfo describes the open-file scan in use, as doctor sees it.
type HolderScanInfo struct {
	Mode ScanMode
	// Backend is "native", "lsof" or "none" (native mode whose native
	// scan cannot be used, or a blind scan).
	Backend string
	// Reason says why not native: "", "config", "test", "unsupported",
	// "self-check", "partial", "blind", "scan-error".
	Reason       string
	RegionFlavor int  // 22 or 8 when the self-check ran and passed; 0 otherwise
	SocketCheck  bool // the self-check verified the socket flavor
	LsofOK       bool // /usr/sbin/lsof exists and is executable
	// Counts from doctor's probe scan (0 when it did not run). Counts
	// only: no pid, name or path.
	Unreadable, Partial int
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
	// WaitUpdateInstalled waits, after the app has quit, until the updater
	// has started (or the staged state is gone) and exited, up to timeout.
	// The caller then re-runs Locate for a new identity baseline.
	WaitUpdateInstalled(ctx context.Context, bundleID string, timeout time.Duration) error
}

// Controller sends Apple Events and opens the app.
type Controller interface {
	// Quit sends the quit Apple Event and returns once it was delivered
	// (not once the app exited). ErrQuitRefused if the app answered that
	// it cancelled the quit; ErrEventTimedOut if no reply came in time;
	// ErrQuitNotDelivered (with ErrAutomationDenied for -1743 or -1744)
	// if it failed otherwise.
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
	// Busy returns the sessions with a turn start and no later turn end,
	// scanning from the later of from and the last AppStarted line, and at
	// most the last 8 MiB. Ids are validated (ids.ParseSession); anything
	// else is skipped. ErrSignalUnknown when TurnStarted or TurnEnded is
	// empty.
	Busy(ctx context.Context, from int64, sig compat.Signals) ([]ids.SessionID, error)
	// ActiveOrg returns the org named by the last ActiveOrg line (bounded
	// tail scan), validated as a UUID, when that line names account; found
	// is false when none was seen or the last line is another account's.
	// ErrSignalUnknown when the pattern is empty.
	ActiveOrg(ctx context.Context, sig compat.Signals, account ids.AccountID) (org ids.OrgID, found bool, err error)
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
	// Clock drives every poll and timeout (250 ms polls in QuitAndWait,
	// WaitExited and WaitTurnEnded), so tests use clock.Fake.
	Clock clock.Clock
}

// DarwinConfig configures the macOS platform.
type DarwinConfig struct {
	Home     string // for ~/Library/Logs/Claude/main.log and updater caches
	BundleID string
	Exec     Runner      // nil: the real runner in exec.go
	Clock    clock.Clock // nil: the real clock
	// AppName is the bundle's file name without ".app"; "" means
	// "Claude".
	AppName string
	// AppDirs are the directories searched for the bundle, in order; nil
	// means /Applications then Home/Applications (under `go test`, only
	// Home/Applications).
	AppDirs []string
	// OpenFileScan is config app.open_file_scan; "" means auto.
	OpenFileScan ScanMode
	// FDScan is the native scanner; nil means fdscan.System() outside
	// tests. Under go test a nil FDScan disables the native path (lsof
	// through Exec only), so no test reads the real process table.
	FDScan fdscan.Scanner
}

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

// QuitResult is the outcome of QuitAndWait.
type QuitResult struct {
	// Vetoed is true when main.log showed the quit-vetoed line: the app
	// showed its "still working" dialog.
	Vetoed bool
	// Refused is true when the app answered the quit event with a
	// cancel (ErrQuitRefused).
	Refused bool
	// NoReply is true when the quit event got no reply in time
	// (ErrEventTimedOut).
	NoReply bool
	Elapsed time.Duration
	Exited  bool
	// Pending is true when the app was still running at the timeout
	// after a veto, a refusal or an unanswered quit event: the app may
	// still quit later (for example "Wait for Claude" in its dialog). The
	// caller then watches with WaitExited instead of claiming the app
	// keeps running.
	Pending bool
}
