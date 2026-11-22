package orchestrator

import (
	"context"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/state"
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
	Status  string         `json:"status"` // "start" | "ok" | "warn" | "fail"
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
)

// Confirmation is a question for the user.
type Confirmation struct {
	Kind    ConfirmKind
	Plan    *plan.Plan
	Summary *plan.Summary
	// Lines are extra sanitized lines (busy sessions, identities masked).
	Lines []string
	// Expires bounds how long the answer stays valid (P2: 120 s).
	Expires time.Time
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
	ReopenCLISession   string
	AllowDeletions     int
	AcceptWorktreeRisk bool
	AllowUpdate        bool // ADR 0003: wait for a staged update, re-probe
}

// Result is the outcome of a run.
type Result struct {
	RunID      string                  `json:"run"`
	From       ids.Alias               `json:"from"`
	To         ids.Alias               `json:"to"`
	Mode       compat.Mode             `json:"mode"`
	Plan       *plan.Plan              `json:"plan,omitempty"`
	Summary    *plan.Summary           `json:"summary,omitempty"`
	Generation uint64                  `json:"generation,omitempty"`
	Code       pderr.Code              `json:"code"`
	Timings    map[Phase]time.Duration `json:"timings,omitempty"`
	Warnings   []string                `json:"warnings,omitempty"`
	Detached   bool                    `json:"detached,omitempty"`
}

// Orchestrator runs commands.
type Orchestrator struct{ d Deps }

// New returns an Orchestrator.
func New(d Deps) *Orchestrator { return &Orchestrator{d: d} }

// Switch runs P0–P12 (or hands off to the detached runner first).
func (o *Orchestrator) Switch(ctx context.Context, opts SwitchOptions) (*Result, error) {
	return nil, pderr.ErrNotImplemented
}

// Preview computes the approximate plan (P0, P2) without quitting.
func (o *Orchestrator) Preview(ctx context.Context, to ids.Alias, only, skip []model.Category) (*Result, error) {
	return nil, pderr.ErrNotImplemented
}

// SyncOptions are the flags of `sync`.
type SyncOptions struct {
	Yes            bool
	AllowDeletions int
	NoLaunch       bool
}

// Sync re-applies canonical state to the active profile: P3–P7, P10–P12,
// no swap.
func (o *Orchestrator) Sync(ctx context.Context, opts SyncOptions) (*Result, error) {
	return nil, pderr.ErrNotImplemented
}

// VerifyResult is the outcome of P12.
type VerifyResult struct {
	Drift          []model.RowID `json:"drift,omitempty"`
	SignInNeeded   bool          `json:"sign_in_needed"`
	SessionFocused bool          `json:"session_focused"`
	PairingOK      bool          `json:"pairing_ok"`
	BridgeSeen     bool          `json:"bridge_connected"`
}

// Verify runs P12 for the last run: waits for LOCK and a stable config
// mirror, compares verify rows (reading JSON only, never LevelDB), marks
// drifted rows contested, and checks main.log signals.
func (o *Orchestrator) Verify(ctx context.Context) (*VerifyResult, error) {
	return nil, pderr.ErrNotImplemented
}

// RecoverOptions are the flags of `recover`.
type RecoverOptions struct {
	Rollback bool // undo an interrupted run instead of completing it
	Yes      bool
}

// RecoverResult reports what recover did.
type RecoverResult struct {
	Actions []string  `json:"actions"`
	Active  ids.Alias `json:"active"`
}

// Recover reconciles an interrupted run from state intents and folder
// identities, or, without a journal, from identities alone. It never
// creates, merges or deletes data folders automatically.
func (o *Orchestrator) Recover(ctx context.Context, opts RecoverOptions) (*RecoverResult, error) {
	return nil, pderr.ErrNotImplemented
}

// RollbackResult reports a rollback.
type RollbackResult struct {
	Generation uint64   `json:"generation"`
	Restored   int      `json:"restored"`
	Conflicts  []string `json:"conflicts,omitempty"`
}

// Rollback restores generation n conditionally (app stopped), records an
// epoch event, and sets bases according to propagate (M6).
func (o *Orchestrator) Rollback(ctx context.Context, n uint64, propagate bool) (*RollbackResult, error) {
	return nil, pderr.ErrNotImplemented
}

// Rebase drops every base; the next run is a first sync (M14).
func (o *Orchestrator) Rebase(ctx context.Context) error { return pderr.ErrNotImplemented }

// ProfileStatus is one profile in `status`.
type ProfileStatus struct {
	Alias     ids.Alias `json:"alias"`
	Label     string    `json:"label,omitempty"`
	Folder    string    `json:"folder"`
	Active    bool      `json:"active"`
	Account   string    `json:"account_masked,omitempty"`
	Managed   bool      `json:"managed_by_org"`
	BaseValid bool      `json:"base_valid"`
	Problem   string    `json:"problem,omitempty"`
}

// Status is the output of `status`.
type Status struct {
	Active       ids.Alias           `json:"active,omitempty"`
	Previous     ids.Alias           `json:"previous,omitempty"`
	AppRunning   bool                `json:"app_running"`
	NeedsRecover bool                `json:"needs_recover"`
	UpdateStaged bool                `json:"update_staged"`
	Profiles     []ProfileStatus     `json:"profiles"`
	LastVerify   *state.VerifyRecord `json:"last_verify,omitempty"`
	Generations  int                 `json:"generations"`
	Warnings     []string            `json:"warnings,omitempty"`
}

// Status reports the current state. With short, it reads only root.json
// and folder names (under 50 ms, never opens LevelDB, G5).
func (o *Orchestrator) Status(ctx context.Context, short bool) (*Status, error) {
	return nil, pderr.ErrNotImplemented
}

// Check is one doctor finding.
type Check struct {
	ID     string `json:"id"`
	Status string `json:"status"` // "ok" | "warn" | "fail"
	Detail string `json:"detail"`
}

// DoctorOptions are the flags of `doctor`.
type DoctorOptions struct {
	GrantAutomation bool
}

// Doctor checks the installation: app version and mode, probes per folder,
// folder identities (masked), markers, unclassified top-level entries,
// other tools, Automation, staged update, Chrome bridge connection after
// the last launch and the one-profile-per-account setup (ADR 0003), and
// account-looks-managed hints.
func (o *Orchestrator) Doctor(ctx context.Context, opts DoctorOptions) ([]Check, error) {
	return nil, pderr.ErrNotImplemented
}

// InitOptions are the answers `init` collects interactively.
type InitOptions struct {
	Alias   ids.Alias
	Label   string
	Managed bool
}

// Init records the active folder's account as a profile, writes the config
// (printing each change first), detects other tools and asks for
// Automation while the user is present.
func (o *Orchestrator) Init(ctx context.Context, opts InitOptions) error {
	return pderr.ErrNotImplemented
}

// ProfileAdopt registers a parked folder after checking its identity.
func (o *Orchestrator) ProfileAdopt(ctx context.Context, alias ids.Alias, folder string) error {
	return pderr.ErrNotImplemented
}

// ProfileAdd adds an account without signing out: quit the app, park the
// current folder, launch on an empty default folder so the user signs in
// through the app's own flow.
func (o *Orchestrator) ProfileAdd(ctx context.Context, alias ids.Alias, managed bool) error {
	return pderr.ErrNotImplemented
}

// ProfileRename renames an alias (state and config; the folder is renamed
// only if it uses the default name and the app is stopped).
func (o *Orchestrator) ProfileRename(ctx context.Context, from, to ids.Alias) error {
	return pderr.ErrNotImplemented
}

// ProfileRemove unregisters a profile; its folder is left intact.
func (o *Orchestrator) ProfileRemove(ctx context.Context, alias ids.Alias) error {
	return pderr.ErrNotImplemented
}
