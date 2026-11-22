package runner

import (
	"context"
	"time"

	"github.com/kazupc/paritydesk/internal/app"
	"github.com/kazupc/paritydesk/internal/pderr"
)

// LabelPrefix prefixes every job label; the run id completes it.
const LabelPrefix = "io.github.kazupc.paritydesk.run."

// Job is one detached run.
type Job struct {
	Label      string
	Binary     string   // absolute path, resolved and verified at submit
	Args       []string // fixed arguments, e.g. switch <alias> --yes --from-runner
	PlistPath  string   // <run dir>/<label>.plist
	ResultPath string   // <run dir>/<run id>.result.json
	StdoutPath string   // <log dir>/<run id>.out
}

// NewJob validates binary (absolute, regular, executable, not
// world-writable) and builds the job for runID.
func NewJob(runDir, logDir, binary, runID string, args []string) (*Job, error) {
	return nil, pderr.ErrNotImplemented
}

// Plist renders the launchd property list (XML, no shell, RunAtLoad,
// no KeepAlive, AbandonProcessGroup).
func (j *Job) Plist() ([]byte, error) { return nil, pderr.ErrNotImplemented }

// Launchd submits and removes jobs.
type Launchd interface {
	Bootstrap(ctx context.Context, uid int, plistPath string) error
	Bootout(ctx context.Context, uid int, label string) error
}

// NewLaunchd returns the launchctl implementation over app's exec runner.
func NewLaunchd(r app.Runner) Launchd { return nil }

// Submit writes the plist (0600) and bootstraps the job.
func Submit(ctx context.Context, j *Job, ld Launchd, uid int) error {
	return pderr.ErrNotImplemented
}

// Cleanup boots the job out and removes its plist. Called by the job
// itself at the end of the run.
func Cleanup(ctx context.Context, j *Job, ld Launchd, uid int) error {
	return pderr.ErrNotImplemented
}

// Result is the job's result file.
type Result struct {
	RunID    string    `json:"run"`
	Code     int       `json:"code"`
	ErrorID  string    `json:"error_id,omitempty"` // PD-E<code>
	From     string    `json:"from,omitempty"`
	To       string    `json:"to,omitempty"`
	Finished time.Time `json:"finished"`
}

// WriteResult writes the result file atomically (0600).
func WriteResult(path string, r Result) error { return pderr.ErrNotImplemented }

// ReadResult reads a result file.
func ReadResult(path string) (Result, error) { return Result{}, pderr.ErrNotImplemented }

// InsideApp reports whether pid descends from a process of the app's
// process set (walks the parent chain via the process table).
func InsideApp(ctx context.Context, procs app.ProcessTable, pid int, bundlePath string) (bool, error) {
	return false, pderr.ErrNotImplemented
}

// Detacher is what the orchestrator uses to hand a run to launchd.
type Detacher struct {
	RunDir, LogDir string
	Binary         string // os.Executable(), resolved
	UID            int
	Launchd        Launchd
	Procs          app.ProcessTable
	BundlePath     string
}

// InsideApp reports whether the current process runs inside the app.
func (d Detacher) InsideApp(ctx context.Context) (bool, error) {
	return false, pderr.ErrNotImplemented
}

// Submit creates and submits a job running the binary with args, and
// returns the run id whose result file the caller may wait on.
func (d Detacher) Submit(ctx context.Context, args []string) (runID string, err error) {
	return "", pderr.ErrNotImplemented
}
