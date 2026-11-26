package fdscan

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"time"
)

// Kind says how a process holds a path.
type Kind uint8

const (
	KindFD        Kind = iota + 1 // numbered vnode descriptor (lsof "<N>")
	KindSocket                    // descriptor of a bound Unix socket (lsof "<N>")
	KindFileport                  // fileport (lsof "fp.")
	KindCwd                       // working directory (lsof "cwd")
	KindRoot                      // root directory (lsof "rtd")
	KindThreadCwd                 // per-thread working directory (lsof "twd")
	KindMapped                    // mapped file (lsof "txt")
)

// String returns the lsof label: "fd", "socket", "fp.", "cwd", "rtd", "twd", "txt".
func (k Kind) String() string {
	switch k {
	case KindFD:
		return "fd"
	case KindSocket:
		return "socket"
	case KindFileport:
		return "fp."
	case KindCwd:
		return "cwd"
	case KindRoot:
		return "rtd"
	case KindThreadCwd:
		return "twd"
	case KindMapped:
		return "txt"
	}
	return "unknown"
}

// Access is the open mode of a descriptor, socket or fileport.
type Access uint8

const (
	AccessNone      Access = iota // not a descriptor (cwd, txt, ...) or neither flag set
	AccessRead                    // FREAD only
	AccessWrite                   // FWRITE only
	AccessReadWrite               // FREAD and FWRITE
)

// Record is one held path.
type Record struct {
	PID     int
	Started time.Time // the process's start (kern.proc.pid p_starttime)
	Kind    Kind
	FD      int32  // KindFD, KindSocket; -1 otherwise
	Access  Access // KindFD, KindSocket, KindFileport; AccessNone otherwise
	Dev     uint32 // vst_dev; 0 for KindSocket
	Ino     uint64 // vst_ino; 0 for KindSocket
	Path    string // as the kernel reports it; never empty
}

// Options configure one scan.
type Options struct {
	// Match reports whether a path is of interest. Required. It is called
	// concurrently from several goroutines and must not retain the path.
	Match func(path string) bool
	// PIDs, when non-nil, is the exact set of pids to scan and
	// proc_listallpids is not called (tests, self-check).
	PIDs []int
	// SkipPID is not scanned when > 0. Only the self-check and tests set
	// it; app.scanHolders passes 0, so the caller's own pid is reported
	// exactly as lsof reports it, and callers filter it as today.
	SkipPID int
	// Workers bounds parallelism; 0 means min(8, runtime.GOMAXPROCS(0)).
	Workers int
}

// Call classes for the systemic-blindness rule (section 5.4 of the
// design): Stats.Tried[c] same-user processes reached class c, Stats.OK[c]
// of them got at least one successful answer of that class.
const (
	classVnodePath  = iota // step 2, PROC_PIDVNODEPATHINFO
	classFDList            // PROC_PIDLISTFDS returned at least one entry
	classFDInfo            // a PROC_PIDFDVNODEPATHINFO call succeeded
	classThreadList        // PROC_PIDLISTTHREADS returned at least one handle
	classThreadPath        // a PROC_PIDTHREADPATHINFO call succeeded
	classRegion            // the first region call succeeded
	numClasses
)

// Stats describe a scan; they never contain paths or names.
type Stats struct {
	Listed       int  // pids from proc_listallpids (or len(Options.PIDs))
	Scanned      int  // processes whose tables were read completely
	Denied       int  // another user's process: not readable (as with lsof)
	Unreadable   int  // own user's live process whose step 2 failed (after one retry)
	Partial      int  // own user's live process with a confirmed gap in a later step (after one retry)
	Exited       int  // gone or zombie before or during its scan
	Reused       int  // pid reuse detected; the pid was scanned again
	RegionFlavor int  // 22 or 8
	SocketCheck  bool // the self-check verified the socket flavor (false: socketpair failed, sub-check skipped)
	Calls        int  // libproc calls made
	Elapsed      time.Duration
	// Flavor success counters over same-user processes other than the
	// caller: Tried[c] processes reached class c, OK[c] of them got at
	// least one successful answer of that class.
	Tried, OK [numClasses]int
}

// add sums o into s (counts only; flavor and socket fields are not
// touched).
func (s *Stats) add(o Stats) {
	s.Scanned += o.Scanned
	s.Denied += o.Denied
	s.Unreadable += o.Unreadable
	s.Partial += o.Partial
	s.Exited += o.Exited
	s.Reused += o.Reused
	s.Calls += o.Calls
	for c := range numClasses {
		s.Tried[c] += o.Tried[c]
		s.OK[c] += o.OK[c]
	}
}

// Result is a scan's outcome. Records are sorted by PID, Kind, FD, Path.
type Result struct {
	Records []Record
	Stats   Stats
}

// Scanner scans processes for held paths.
type Scanner interface {
	// Scan reads every process (or Options.PIDs) and returns the records
	// Options.Match accepts. Another user's process is counted (Denied),
	// not an error; an own-user process that could not be read fully
	// makes the scan fail with ErrPartial or ErrBlind, and the Result
	// still carries Stats (Records is nil). Errors: ErrUnsupported,
	// ErrLayout, ErrListPIDs, ErrProcInfo, ErrTooLarge, ErrPartial,
	// ErrBlind, ErrNoMatch, ctx.Err(). Scan runs the self-check first (see
	// SelfCheck) and fails with its error.
	Scan(ctx context.Context, opt Options) (Result, error)
	// SelfCheck verifies, on the calling process only, that the kernel
	// answers at all (a 0 where the own process certainly has entries is
	// ErrBlind: a sandbox or policy denies process information) and that
	// its answers decode as expected (otherwise ErrLayout), and picks the
	// region flavor. The first conclusive outcome (success, ErrBlind or
	// ErrLayout) is cached; an outcome that errors.Is a context error is
	// never cached. The check itself runs under context.Background() with
	// a 2 s timeout, so a cancelled first caller cannot poison the cache;
	// ctx is only checked before starting. It returns the region flavor it
	// picked.
	SelfCheck(ctx context.Context) (regionFlavor int, err error)
}

// Errors.
var (
	ErrUnsupported = errors.New("fdscan: not supported on this platform")
	ErrLayout      = errors.New("fdscan: libproc returned an unexpected layout")
	ErrListPIDs    = errors.New("fdscan: the process list could not be read")
	ErrProcInfo    = errors.New("fdscan: kern.proc.pid could not be read")
	ErrTooLarge    = errors.New("fdscan: a process table exceeded the scan limit")
	ErrPartial     = errors.New("fdscan: some processes could not be read completely")
	ErrBlind       = errors.New("fdscan: other processes' open files cannot be read (sandbox or policy)")
	ErrNoMatch     = errors.New("fdscan: Options.Match is required")
)

// sortRecords orders records by PID, Kind, FD, Path (then Dev, Ino), so
// a scan's output does not depend on the worker schedule.
func sortRecords(rs []Record) {
	slices.SortFunc(rs, func(a, b Record) int {
		return cmp.Or(
			cmp.Compare(a.PID, b.PID),
			cmp.Compare(a.Kind, b.Kind),
			cmp.Compare(a.FD, b.FD),
			cmp.Compare(a.Path, b.Path),
			cmp.Compare(a.Dev, b.Dev),
			cmp.Compare(a.Ino, b.Ino),
		)
	})
}
