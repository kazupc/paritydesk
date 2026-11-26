package orchestrator

import (
	"context"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/plan"
)

// Phase names a step of the switch algorithm.
type Phase string

// Phases. P1 (capture) and P13 (online reconcile) are v0.2.
const (
	P0Preflight Phase = "P0"
	P2Preview   Phase = "P2"
	P3Quit      Phase = "P3"
	P4Quiesce   Phase = "P4"
	P5Harvest   Phase = "P5"
	P6Backup    Phase = "P6"
	P7Apply     Phase = "P7"
	P8Swap      Phase = "P8"
	P9Park      Phase = "P9"
	P10Commit   Phase = "P10"
	P11Launch   Phase = "P11"
	P12Verify   Phase = "P12"
)

// Event is one progress event. Message is already localized and
// sanitized; Data holds only validated, non-secret fields.
type Event struct {
	Phase   Phase          `json:"phase"`
	Status  string         `json:"status"` // "start" | "ok" | "warn" | "fail"; "paused" only from the e2e fault (PARITYDESK_FAULT=pause:P7)
	Message string         `json:"message"`
	Data    map[string]any `json:"data,omitempty"`
	Elapsed time.Duration  `json:"elapsed_ns,omitempty"`
}

// ConfirmKind says what a confirmation is about.
type ConfirmKind string

// Confirmation kinds.
const (
	ConfirmPlan           ConfirmKind = "plan"            // P2 preview
	ConfirmNewDestructive ConfirmKind = "new-destructive" // P5 differs from P2
	ConfirmBaseInvalid    ConfirmKind = "base-invalid"    // union first sync (exit 21 non-interactive)
	ConfirmBusy           ConfirmKind = "busy"            // sessions working (exit 12 non-interactive)
	ConfirmOutsideSwitch  ConfirmKind = "outside-switch"  // offer sync
	ConfirmRecover        ConfirmKind = "recover"         // recover proposes a move
	ConfirmRollback       ConfirmKind = "rollback"
	ConfirmAddBind        ConfirmKind = "add-bind" // bind a newly signed-in account to the pending profile add
)

// StatusInfo marks a progress event that front ends keep in the run log
// without showing it: nothing for the person to do.
const StatusInfo = "info"

// Timing contracts.
const (
	// DetachedLockWait is how long the detached job retries the run lock.
	DetachedLockWait = 10 * time.Second
	// CommitTimeout bounds the uncancellable section (P5 commit to P10)
	// and every abort, rollback and relaunch path, which run on
	// context.WithoutCancel so a SIGINT or SIGTERM cannot strand a
	// half-applied run.
	CommitTimeout = 5 * time.Minute
)

// Confirmation is a question for the user.
type Confirmation struct {
	Kind    ConfirmKind
	Plan    *plan.Plan
	Summary *plan.Summary
	// Lines are extra sanitized lines (busy sessions, identities masked).
	Lines []string
}

// UI is the front end's side of the conversation.
type UI interface {
	Progress(ev Event)
	// Confirm asks; it returns (false, nil) when declined and an error when
	// no answer is possible (non-interactive without --yes).
	Confirm(ctx context.Context, c Confirmation) (bool, error)
	Interactive() bool
}

// SwitchOptions are the flags of `switch`.
type SwitchOptions struct {
	To                 ids.Alias // empty or "-": the previous profile
	Yes                bool
	Only, Skip         []model.Category
	NoLaunch, NoReopen bool
	Detach             bool // force the detached runner
	FromRunner         bool // this process is the detached job
	// ReopenCLISession is the invoking Claude Code session id (UUID), used
	// to find the local session to reopen and to wait for its turn to end.
	// Empty means no reopen and no turn wait.
	ReopenCLISession   string
	AllowDeletions     int
	AcceptWorktreeRisk bool
	// LogOffset is main.log's size recorded by the invoking process before
	// the hand-off (P0, before anything else), so the job's turn-end wait
	// sees a turn that ended before P3. -1 means unset.
	LogOffset int64
	// ConfirmedPlan is the hash of a plan the user already confirmed (saved
	// in the PlanStore by `plan` or by the interactive parent). The job's
	// P2 loads it and aborts before quitting when its own plan has a
	// destructive change the confirmed plan did not (plan.NewDestructive),
	// or when it is missing. A confirmed plan has no time limit: that
	// check, made after Claude quits, is what keeps the person's answer
	// true.
	ConfirmedPlan string
	// ConfirmFirstSync confirms a union first sync (invalid bases) for a
	// run with --yes; without it such a run exits 21. A confirmed plan
	// that showed the first-sync line needs no flag.
	ConfirmFirstSync bool
}

// PreviewOptions are the flags of `plan`.
type PreviewOptions struct {
	To         ids.Alias
	Only, Skip []model.Category
	// Save stores the plan in the PlanStore so a later switch can name it
	// with --confirmed-plan (ignored in read-only mode).
	Save bool
}

// Result is the outcome of a run.
type Result struct {
	RunID   string        `json:"run"`
	From    ids.Alias     `json:"from"`
	To      ids.Alias     `json:"to"`
	Mode    compat.Mode   `json:"mode"`
	Plan    *plan.Plan    `json:"plan,omitempty"`
	Summary *plan.Summary `json:"summary,omitempty"`
	// PlanHash and DestructiveDigest identify the plan for
	// --confirmed-plan and for logs.
	PlanHash          string                  `json:"plan_hash,omitempty"`
	DestructiveDigest string                  `json:"destructive_digest,omitempty"`
	Generation        uint64                  `json:"generation,omitempty"`
	Code              pderr.Code              `json:"code"`
	Timings           map[Phase]time.Duration `json:"timings,omitempty"`
	Warnings          []string                `json:"warnings,omitempty"`
	Detached          bool                    `json:"detached,omitempty"`
	// QuitPending is set when the app vetoed the quit and had not quit by
	// the end of the late-quit watch: the user's answer in the app decides.
	QuitPending bool `json:"quit_pending,omitempty"`
	// v0.2 anchor: result busy

	// v0.2 anchor: result online

}

// Service is every command the front end calls. cli depends on this
// interface, so its tests use a fake.
type Service interface {
	Switch(ctx context.Context, opts SwitchOptions) (*Result, error)
	Preview(ctx context.Context, opts PreviewOptions) (*Result, error)
	Sync(ctx context.Context, opts SyncOptions) (*Result, error)
	Verify(ctx context.Context) (*VerifyResult, error)
	// VerifyAfter is the background check a terminal switch hands off:
	// it waits for Claude to settle without holding the run lock, then
	// records only if runID is still the latest run into the active
	// profile and no run is open.
	VerifyAfter(ctx context.Context, runID string) (*VerifyResult, error)
	Recover(ctx context.Context, opts RecoverOptions) (*RecoverResult, error)
	Rollback(ctx context.Context, n uint64, propagate bool) (*RollbackResult, error)
	Rebase(ctx context.Context) error
	Status(ctx context.Context, short bool) (*Status, error)
	Doctor(ctx context.Context, opts DoctorOptions) ([]Check, error)
	Init(ctx context.Context, opts InitOptions) error
	ProfileAdopt(ctx context.Context, alias ids.Alias, folder string, managed bool) error
	ProfileAdd(ctx context.Context, alias ids.Alias, managed bool) error
	ProfileRename(ctx context.Context, from, to ids.Alias) error
	ProfileRemove(ctx context.Context, alias ids.Alias) error
}

// Orchestrator runs commands.
type Orchestrator struct{ d Deps }

// New returns an Orchestrator.
func New(d Deps) *Orchestrator { return &Orchestrator{d: d} }

// VerifyResult is the outcome of P12.
type VerifyResult struct {
	// Drift lists rows whose values differ from what was written and that
	// the manifest does not expect the server to replace.
	Drift []model.RowID `json:"drift,omitempty"`
	// Replaced lists contested rows (values the app may keep per account
	// or device) that changed after paritydesk wrote them. They are
	// recorded like drift right after a launch but are not an error.
	Replaced       []model.RowID `json:"replaced,omitempty"`
	SignInNeeded   bool          `json:"sign_in_needed"`
	SessionFocused bool          `json:"session_focused"`
	PairingOK      bool          `json:"pairing_ok"`
	BridgeSeen     bool          `json:"bridge_connected"`
}
