package orchestrator

import (
	"context"
	"time"

	"github.com/kazupc/paritydesk/internal/app"
	"github.com/kazupc/paritydesk/internal/apply"
	"github.com/kazupc/paritydesk/internal/clock"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/gen"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/profile"
	"github.com/kazupc/paritydesk/internal/rows"
	"github.com/kazupc/paritydesk/internal/runner"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/sources/worktrees"
	"github.com/kazupc/paritydesk/internal/state"
	"github.com/kazupc/paritydesk/internal/store/kv"
)

// StateStore is the state contract (state.Store).
type StateStore interface {
	Load() (*state.Root, error)
	Commit(r *state.Root) error
	Begin(r *state.Root, name state.IntentName, data map[string]string) error
	Done(r *state.Root, name state.IntentName) error
	PutSnapshot(s *model.RowSnapshot) (state.Ref, error)
	GetSnapshot(ref state.Ref) (*model.RowSnapshot, error)
	// PutObject and GetObject store any JSON value as an object of a
	// kind (the online draft, the scratch post-hashes).
	PutObject(kind string, v any) (state.Ref, error)
	GetObject(ref state.Ref, kind string, v any) error
	GC(r *state.Root) error
	Lock(ctx context.Context, wait time.Duration) (*state.Lock, error)
}

// Generations is the backup contract (gen.Store).
type Generations interface {
	Begin(n uint64, runID string, alias ids.Alias, rep model.Replica) (gen.Writer, error)
	List() ([]gen.Info, error)
	Load(n uint64) (*gen.Manifest, error)
	DeleteIncomplete(n uint64) (bool, error)
	Prune(p gen.PrunePolicy) error
	Purge() error
	Restore(ctx context.Context, in gen.RestoreInput) (gen.RestoreReport, error)
	RestoreLevelDB(ctx context.Context, n uint64, folder string) error
}

// FolderScanner lists and identifies data folders (profile.Scanner).
type FolderScanner interface {
	Scan(ctx context.Context) ([]profile.Folder, error)
}

// AppControl is everything the orchestrator asks of the app (app.Platform).
type AppControl interface {
	Locate(ctx context.Context, bundleID string) (app.BundleIdentity, error)
	UpdateStaged(ctx context.Context, bundleID string) (bool, error)
	Running(ctx context.Context, bundlePath string) ([]app.Process, error)
	CheckQuiescence(ctx context.Context, in app.QuiescenceInput) (app.QuiescenceReport, error)
	QuitAndWait(ctx context.Context, bundleID, bundlePath string, sig compat.Signals, timeout time.Duration, onVeto func()) (app.QuitResult, error)
	WaitExited(ctx context.Context, bundlePath string, timeout time.Duration) (bool, error)
	WaitTurnEnded(ctx context.Context, from int64, sig compat.Signals, session ids.SessionID, timeout time.Duration) error
	BusySessions(ctx context.Context, from int64, sig compat.Signals) ([]ids.SessionID, error)
	WaitUpdateInstalled(ctx context.Context, bundleID string, timeout time.Duration) error
	ActiveOrg(ctx context.Context, sig compat.Signals, account ids.AccountID) (ids.OrgID, bool, error)
	Launch(ctx context.Context, bundleID string) error
	OpenSession(ctx context.Context, id ids.SessionID) error
	ProbeAutomation(ctx context.Context, bundleID string) error
	Notify(ctx context.Context, title, text string) error
	LogSize() (int64, error)
	LogEvents(ctx context.Context, from int64, sig compat.Signals, session ids.SessionID) ([]app.LogEvent, int64, error)
	GitDirty(ctx context.Context, dir string) (bool, error)
	LockHeld(path string) (bool, error)
}

// CompatGate selects manifests, runs probes and decides the mode
// (compat.Gate).
type CompatGate interface {
	Manifest(ctx context.Context, version string) (*compat.Manifest, bool, error)
	Probes(ctx context.Context, m *compat.Manifest, env compat.ProbeEnv) ([]compat.ProbeResult, error)
	Decide(in compat.DecideInput) compat.Decision
}

// SessionDirectory reads session records outside row merges
// (sources.Directory).
type SessionDirectory interface {
	ListSessions(ctx context.Context, folder string, r model.Replica, spec compat.RowSpec) ([]sources.SessionInfo, error)
	ResolveByCLISession(ctx context.Context, folder string, r model.Replica, cliSessionID string) (ids.SessionID, error)
	DiscoverReplicas(folder string) ([]model.Replica, error)
}

// WorktreeGuard computes the worktree loss guard's input (worktrees.Guard).
// fields are the sessions row's manifest fields.worktree_path.
type WorktreeGuard interface {
	LostRegistrations(leavingSessions, desiredSessions *model.RowSnapshot, fields []string) ([]string, error)
	LostEntries(current, desired *model.RowSnapshot, pathFields []string) ([]string, error)
}

// Applier executes plans (apply.Applier).
type Applier interface {
	Apply(ctx context.Context, in apply.Input) (apply.Result, error)
	Check(ctx context.Context, p *plan.Plan, folder string) (bool, []string, error)
}

// Detacher hands a run to launchd (runner.Detacher).
type Detacher interface {
	InsideApp(ctx context.Context) (bool, error)
	// Submit starts a job with args from SwitchOptions.JobArgs.
	Submit(ctx context.Context, args []string) (string, error)
}

// PlanStore keeps confirmed plans between `plan` (or the interactive
// parent of a detached switch) and the detached job (runner.PlanStore).
type PlanStore interface {
	Save(p *plan.Plan) (string, error)
	Load(hash string, now time.Time) (*plan.Plan, error)
}

// Deps are the orchestrator's dependencies.
type Deps struct {
	Config *config.Config
	Paths  config.Paths
	Home   string
	// DataRoot is the expanded app.data_root.
	DataRoot string
	Clock    clock.Clock
	FS       fsx.Ops
	State    StateStore
	Gens     Generations
	Folders  FolderScanner
	// Resolve is profile.Resolve.
	Resolve func(cfg *config.Config, root *state.Root, folders []profile.Folder, hints profile.Hints) (*profile.Resolution, error)
	// ReadMarker and WriteMarker are profile.ReadMarker / WriteMarker.
	ReadMarker  func(fs fsx.Ops, folder string) (*model.Marker, error)
	WriteMarker func(fs fsx.Ops, folder string, m model.Marker) error
	App         AppControl
	Compat      CompatGate
	// KV opens Local Storage. In read-only mode it is kv.ReadOnly over the
	// LevelDB opener, whose own filesystem access is confined to its temp
	// root, so snapshots still work.
	KV       kv.Opener
	Adapters sources.Registry
	Rows     map[model.RowID]rows.Module
	// PostMerge is rows.PostMerge, run after every row's Merge.
	PostMerge func(outs map[model.RowID]*rows.Output) error
	// Compose is plan.Compose: adapter edits, in model.AllRows order, to
	// one op per location.
	Compose func(edits []plan.Edit, src plan.Source) ([]plan.Op, error)
	// Validate is plan.Validate.
	Validate func(in plan.ValidateInput) error
	// PermissionAcks is prefs.PermissionAcks, read from the entering folder
	// for rows.Policy.PermissionAcks.
	PermissionAcks func(ctx context.Context, folder string, r model.Replica) (map[string]bool, error)
	Applier        Applier
	Sessions       SessionDirectory
	Worktrees      WorktreeGuard
	Detach         Detacher
	Plans          PlanStore
	UI             UI
	// Crash, if non-nil, is consulted after every intent, and by a switch
	// or sync once more with P10Commit just before P10 closes the run;
	// returning true stops the run immediately without cleanup, as if the
	// process had been killed (PARITYDESK_FAULT=after:<phase> and
	// recovery tests).
	Crash func(after Phase) bool
	// ReadOnly plans only (--read-only, PARITYDESK_READONLY=1): no run
	// lock, no state commit, no config save, no plan save, no runner
	// submit; FS is fsx.ReadOnly and KV is kv.ReadOnly.
	ReadOnly bool
}

// Compile-time checks that the real implementations satisfy the contracts.
var (
	_ StateStore       = (*state.Store)(nil)
	_ Generations      = (*gen.Store)(nil)
	_ FolderScanner    = profile.Scanner{}
	_ AppControl       = (*app.Platform)(nil)
	_ CompatGate       = compat.Gate{}
	_ SessionDirectory = sources.Directory{}
	_ WorktreeGuard    = worktrees.Guard{}
	_ Applier          = (*apply.Applier)(nil)
	_ Detacher         = runner.Detacher{}
	_ PlanStore        = runner.PlanStore{}
	_ Service          = (*Orchestrator)(nil)
)
