package pderr

import (
	"errors"
	"fmt"
)

// ErrNotImplemented is returned by every function whose body is still
// pending. It maps to exit code Internal.
var ErrNotImplemented = errors.New("paritydesk: not implemented yet")

// Code is a process exit code.
type Code int

// Exit codes. Keep in sync with docs and the catalog below.
const (
	OK                Code = 0
	Usage             Code = 2
	Declined          Code = 3
	AppDidNotQuit     Code = 10
	RunLockBusy       Code = 11
	SessionsBusy      Code = 12
	UpdatePending     Code = 13
	ForeignTool       Code = 14
	AutomationMissing Code = 15
	IdentityMismatch  Code = 20
	BaseInvalid       Code = 21
	DeletionBreaker   Code = 22
	WorktreeRisk      Code = 23
	CompatRefused     Code = 30
	SwapOnly          Code = 31
	Degraded          Code = 32
	ApplyRolledBack   Code = 40
	NeedsRecover      Code = 41
	VerifyDrift       Code = 50
	NeedsSignIn       Code = 51
	Internal          Code = 70
)

// Error is a paritydesk error that carries an exit code.
type Error struct {
	Code Code   // exit code
	Op   string // short operation name, e.g. "switch/P3"
	Msg  string // human message; must already be sanitized
	Err  error  // optional cause
}

func (e *Error) Error() string {
	switch {
	case e.Msg != "" && e.Err != nil:
		return fmt.Sprintf("%s: %s: %v", e.Op, e.Msg, e.Err)
	case e.Msg != "":
		return fmt.Sprintf("%s: %s", e.Op, e.Msg)
	case e.Err != nil:
		return fmt.Sprintf("%s: %v", e.Op, e.Err)
	default:
		return fmt.Sprintf("%s: %s", e.Op, ID(e.Code))
	}
}

func (e *Error) Unwrap() error { return e.Err }

// New returns an *Error with the given code.
func New(code Code, op, msg string) *Error { return &Error{Code: code, Op: op, Msg: msg} }

// Wrap returns an *Error with the given code wrapping err.
func Wrap(code Code, op string, err error) *Error { return &Error{Code: code, Op: op, Err: err} }

// CodeOf returns the exit code for err: OK for nil, the code of the first
// *Error in the chain, and Internal otherwise.
func CodeOf(err error) Code {
	if err == nil {
		return OK
	}
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return Internal
}

// ID returns the catalog id for a code, e.g. "PD-E22".
func ID(c Code) string { return fmt.Sprintf("PD-E%d", int(c)) }

// Entry is one error-catalog entry.
type Entry struct {
	Code   Code
	Title  string // what happened
	Safe   string // what is safe right now
	Next   string // the exact next command or action
	Anchor string // docs anchor under docs/if-something-goes-wrong.md
}

var catalog = []Entry{
	{OK, "Done.", "Everything.", "", ""},
	{Usage, "The command line was not understood.", "Nothing was changed.", "paritydesk help", "usage"},
	{Declined, "You declined the plan.", "Nothing was changed.", "", "declined"},
	{AppDidNotQuit, "Claude did not quit.", "Nothing was changed. Claude was not forced to quit; if it asked whether to quit, your answer there decides.", "Finish or stop the running work in Claude, then run the command again.", "app-did-not-quit"},
	{RunLockBusy, "Another paritydesk run is in progress.", "Nothing was changed by this run.", "paritydesk status", "run-lock-busy"},
	{SessionsBusy, "Code sessions are still working.", "Nothing was changed.", "Wait for the sessions to finish, then run the command again.", "sessions-busy"},
	{UpdatePending, "A Claude update is staged and would install on quit.", "Nothing was changed.", "paritydesk switch --allow-update", "update-pending"},
	{ForeignTool, "Another account-switching tool was detected.", "Nothing was changed.", "paritydesk doctor", "foreign-tool"},
	{AutomationMissing, "macOS Automation permission for Claude is missing.", "Nothing was changed.", "paritydesk doctor --grant-automation", "automation"},
	{IdentityMismatch, "A data folder holds a different account than its profile expects.", "Nothing was changed. Every account stays signed in.", "paritydesk doctor", "identity-mismatch"},
	{BaseInvalid, "A sync base no longer matches its folder; a first-sync union needs confirmation.", "Nothing was changed.", "paritydesk plan, then paritydesk switch (interactive)", "base-invalid"},
	{DeletionBreaker, "The plan would delete more than the configured limit.", "Nothing was changed.", "paritydesk plan; if intended, add --allow-deletions=N", "deletion-breaker"},
	{WorktreeRisk, "A worktree with uncommitted changes would lose its registration.", "Nothing was changed.", "Commit or stash the changes, or add --accept-worktree-risk", "worktree-risk"},
	{CompatRefused, "This Claude version or folder layout cannot be handled safely.", "Nothing was changed.", "paritydesk doctor", "compat-refused"},
	{SwapOnly, "Switched without syncing (swap-only mode).", "The switch completed; nothing was synced.", "paritydesk doctor", "swap-only"},
	{Degraded, "Some rows were skipped.", "Everything else was synced.", "paritydesk doctor", "degraded"},
	{ApplyRolledBack, "Applying the plan failed and was rolled back.", "The previous account is active again.", "paritydesk log", "apply-rolled-back"},
	{NeedsRecover, "A run was interrupted.", "Do not move data folders by hand.", "paritydesk recover", "needs-recover"},
	{VerifyDrift, "After launch, some values differ from what was written.", "Your data is intact; the drift is recorded.", "paritydesk verify", "verify-drift"},
	{NeedsSignIn, "The account now active needs a sign-in.", "All other accounts stay signed in.", "Sign in with that profile's own account only.", "needs-sign-in"},
	{Internal, "Internal error.", "Run paritydesk status before trying again.", "paritydesk status", "internal"},
}

// Catalog returns every catalog entry in code order.
func Catalog() []Entry { return append([]Entry(nil), catalog...) }

// Lookup returns the catalog entry for a code.
func Lookup(c Code) (Entry, bool) {
	for _, e := range catalog {
		if e.Code == c {
			return e, true
		}
	}
	return Entry{}, false
}
