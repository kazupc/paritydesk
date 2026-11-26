package online

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/runner"
)

// Kind is a step kind; its letter starts the step id.
type Kind string

// Built-in kinds. Extension providers add their own (artifact).
const (
	KindRead          Kind = "read"
	KindPin           Kind = "pin"
	KindSidebar       Kind = "sidebar"
	KindSectionCreate Kind = "section_create"
	KindCapture       Kind = "capture"
	KindArtifact      Kind = "artifact"
)

// kindLetters are the step-id letters per kind (x: any other kind).
var kindLetters = map[Kind]byte{
	KindRead: 'r', KindPin: 'p', KindSidebar: 's', KindSectionCreate: 'n', KindCapture: 'c', KindArtifact: 'a',
}

// letterOf returns the step-id letter of a kind.
func letterOf(k Kind) byte {
	if l, ok := kindLetters[k]; ok {
		return l
	}
	return 'x'
}

// Phase says which file a queue lives in.
type Phase string

// Phases.
const (
	PhaseReconcile Phase = "reconcile" // queue.json
	PhaseCapture   Phase = "capture"   // capture.json
)

// StepID is "<kind letter><3 digits>".
type StepID string

// StepStatus is the state of one step.
type StepStatus string

// Step states.
const (
	StatusDormant StepStatus = "dormant" // built, inactive until a provider activates it
	StatusPending StepStatus = "pending"
	StatusOffered StepStatus = "offered"
	StatusDone    StepStatus = "done"
	StatusSkipped StepStatus = "skipped"
	StatusFailed  StepStatus = "failed"
)

// Terminal reports a status that never changes again (except a failed
// step a late result or `online retry` resolves).
func (s StepStatus) Terminal() bool {
	return s == StatusDone || s == StatusSkipped || s == StatusFailed
}

// QueueState is the state of a whole queue.
type QueueState string

// Queue states.
const (
	QueuePending   QueueState = "pending"   // published, nothing offered
	QueueActive    QueueState = "active"    // a batch was offered
	QueueComplete  QueueState = "complete"  // no step left
	QueueCancelled QueueState = "cancelled" // dropped by online cancel
)

// Reasons of skipped and failed steps (4.2).
const (
	ReasonDecisionSkip     = "decision_skip"
	ReasonChangedElsewhere = "changed_elsewhere"
	ReasonNotFound         = "not_found"
	ReasonAmbiguousName    = "ambiguous_name"
	ReasonNotWritable      = "not_writable"
	ReasonToolsUnavailable = "tools_unavailable"
	ReasonToolsOff         = "tools_off"
	ReasonAppRefused       = "app_refused"
	ReasonMismatch         = "mismatch"
	ReasonNoResult         = "no_result"
	ReasonNotCalled        = "not_called"
	ReasonExpired          = "expired"
	ReasonBreakerHeld      = "breaker_held"
	ReasonReplicaUnknown   = "replica_unknown"
	ReasonNotNeeded        = "not_needed"
	ReasonUnknownOutcome   = "unknown_outcome"
	ReasonUnrecognized     = "unrecognized"
)

// Reasons lists every reason value a step may carry, including those of
// the extension providers (spec-artifacts A6). A step with another reason
// is invalid.
var Reasons = map[string]bool{
	"": true, ReasonDecisionSkip: true, ReasonChangedElsewhere: true, ReasonNotFound: true, ReasonAmbiguousName: true,
	ReasonNotWritable: true, ReasonToolsUnavailable: true, ReasonToolsOff: true, ReasonAppRefused: true,
	ReasonMismatch: true, ReasonNoResult: true, ReasonNotCalled: true, ReasonExpired: true, ReasonBreakerHeld: true,
	ReasonReplicaUnknown: true, ReasonNotNeeded: true, ReasonUnknownOutcome: true, ReasonUnrecognized: true,
	"multi_file": true, "capabilities": true, "co_authored": true, "type_made": true, "live_doc": true,
	"version_moved": true, "version_conflict": true, "shared": true, "client_refused": true, "grant_declined": true,
	"not_owned": true, "too_large": true, "gate_closed": true, "blob_missing": true, "title_invalid": true,
	"title_taken": true, "boundary": true, "user_refused": true, "tools_missing": true, "replica_changed": true,
}

// Expect says how a recorded result is recognised (9.3). It is copied
// from the manifest at build time, so hooks never load a manifest.
type Expect struct {
	Format string `json:"format"` // "text" | "json-object" | "json-array"
	// OK are the exact texts of success (format text).
	OK []string `json:"ok,omitempty"`
	// Fields are dotted paths of the parsed result and the values they
	// must have (format json-object).
	Fields map[string]string `json:"fields,omitempty"`
	// Refusals are text prefixes meaning "not writable here".
	Refusals []string `json:"refusals,omitempty"`
	// Unknown are text prefixes meaning "the app may still apply it";
	// checked before Refusals.
	Unknown []string `json:"unknown,omitempty"`
}

// Expect formats.
const (
	FormatText       = "text"
	FormatJSONObject = "json-object"
	FormatJSONArray  = "json-array"
)

// Step is one queued tool call.
type Step struct {
	ID   StepID `json:"id"`
	Kind Kind   `json:"kind"`
	Tool string `json:"tool"`
	// Input is the exact tool input in model.Canonical form; empty for a
	// templated step until Advance fills it, and for a step whose input
	// lives in steps/ (InputSHA256).
	Input       json.RawMessage `json:"input,omitempty"`
	InputSHA256 string          `json:"input_sha256,omitempty"`
	Templated   bool            `json:"templated,omitempty"`
	Status      StepStatus      `json:"status"`
	Needs       []string        `json:"needs"`
	After       []StepID        `json:"after"`
	Card        bool            `json:"card"`
	NoCard      bool            `json:"no_card,omitempty"`
	Offered     int             `json:"offered"`
	Attempts    int             `json:"attempts"`
	// Retries counts `online retry` grants (at most one).
	Retries int `json:"retries,omitempty"`
	// RetryBase is how many in-flight markers the step had when it was
	// retried: only markers numbered above it belong to the new attempt,
	// and the guard lets RetryBase+1 passes through in all (in-flight
	// markers are .1 and .2, so it is 0 or 1).
	RetryBase int    `json:"retry_base,omitempty"`
	Resets    int    `json:"resets,omitempty"` // re-offers of a read whose result aged out
	Reason    string `json:"reason"`
	Expect    Expect `json:"expect"`
	// Data is kind-private, validated JSON (the owning policy's
	// ValidateData checks it on every load).
	Data json.RawMessage `json:"data,omitempty"`
	// EverUnknown is set whenever the step entered failed(no_result) or
	// failed(unknown_outcome); never cleared.
	EverUnknown bool `json:"ever_unknown,omitempty"`
}

// MaxAttempts is how many PreToolUse passes the step may have.
func (s Step) MaxAttempts() int {
	if s.Retries > 0 {
		return s.RetryBase + 1
	}
	return 1
}

// GuardExtra are the manifest values the hooks need (additions to the
// code-fixed guard tables, the PostToolUse payload limit and the
// tools-off texts), copied into the queue at Publish so hooks never load
// a manifest.
type GuardExtra struct {
	AlwaysDeny     []string `json:"always_deny,omitempty"`
	ServersExtra   []string `json:"servers_extra,omitempty"`
	ToolsExtra     []string `json:"tools_extra,omitempty"`
	PostMaxPayload int64    `json:"post_max_payload,omitempty"`
	ToolsOff       []string `json:"tools_off,omitempty"`
}

// Queue is queue.json (phase reconcile) or capture.json (phase capture).
// Field order is part of the format: v, id, phase and state come first
// and steps last, so ReadHead can decode the head from a prefix.
type Queue struct {
	V           int               `json:"v"`
	ID          string            `json:"id"`
	Phase       Phase             `json:"phase"`
	State       QueueState        `json:"state"`
	Kind        string            `json:"kind,omitempty"` // "" or "artifacts"
	Profile     ids.Alias         `json:"profile"`
	From        ids.Alias         `json:"from,omitempty"`
	Replica     model.Replica     `json:"replica"`
	Epoch       uint64            `json:"epoch"`
	Session     string            `json:"session,omitempty"` // capture: the validated --session
	CreatedAt   time.Time         `json:"created_at"`
	LaunchedAt  time.Time         `json:"launched_at,omitzero"`
	ExpiresAt   time.Time         `json:"expires_at"`
	BootID      string            `json:"boot_id,omitempty"`
	MonoNS      int64             `json:"mono_ns,omitempty"`
	Decisions   map[string]string `json:"decisions,omitempty"`
	SettleMS    int64             `json:"settle_ms"`
	FirstNextAt time.Time         `json:"first_next_at,omitzero"`
	Guard       GuardExtra        `json:"guard"`
	Steps       []Step            `json:"steps"`
}

// QueueHead is the head of a queue file.
type QueueHead struct {
	V     int        `json:"v"`
	ID    string     `json:"id"`
	Phase Phase      `json:"phase"`
	State QueueState `json:"state"`
}

// Step returns the step with id, or nil.
func (q *Queue) Step(id StepID) *Step {
	if q == nil {
		return nil
	}
	for i := range q.Steps {
		if q.Steps[i].ID == id {
			return &q.Steps[i]
		}
	}
	return nil
}

// Clone returns a deep copy of q.
func (q *Queue) Clone() *Queue {
	if q == nil {
		return nil
	}
	b, err := json.Marshal(q)
	if err != nil {
		panic(fmt.Sprintf("online: clone queue: %v", err))
	}
	var c Queue
	if err := json.Unmarshal(b, &c); err != nil {
		panic(fmt.Sprintf("online: clone queue: %v", err))
	}
	return &c
}

// Outcome is how a recorded response was recognised.
type Outcome string

// Outcomes.
const (
	OutcomeOK           Outcome = "ok"
	OutcomeError        Outcome = "error"
	OutcomeUnrecognized Outcome = "unrecognized"
)

// Result is one recorded tool result (results/ or captures/). Parsed
// holds validated fields only, never raw text, titles or names.
type Result struct {
	V         int       `json:"v"`
	Queue     string    `json:"queue"`
	Step      StepID    `json:"step"`
	Tool      string    `json:"tool"`
	InputSHA  string    `json:"input_sha"`
	Session   string    `json:"session"`
	ToolUseID string    `json:"tool_use_id,omitempty"`
	At        time.Time `json:"at"`
	MonoNS    int64     `json:"mono_ns"`
	BootID    string    `json:"boot_id"`
	Outcome   Outcome   `json:"outcome"`
	// Verdict is set when a fixed response text decided the step at
	// record time (9.3 rules 1-3): tools_off, unknown, refusals,
	// not_found, archived and scheduled-run texts.
	Verdict *Verdict        `json:"verdict,omitempty"`
	Parsed  json.RawMessage `json:"parsed,omitempty"`
}

// Verdict is a step outcome decided from a fixed response text.
type Verdict struct {
	Status StepStatus `json:"status"`
	Reason string     `json:"reason"`
}

// Marker is an in-flight marker: a PreToolUse pass of a step.
type Marker struct {
	N      int       `json:"n"`
	At     time.Time `json:"at"`
	MonoNS int64     `json:"mono_ns"`
	BootID string    `json:"boot_id"`
}

// Lease binds a reconcile queue to the session working on it.
type Lease struct {
	V              int       `json:"v"`
	Queue          string    `json:"queue"`
	Phase          Phase     `json:"phase"`
	Session        string    `json:"session"` // "" = unbound
	TokenSHA256    string    `json:"token_sha256"`
	BootID         string    `json:"boot_id"`
	MonoDeadlineNS int64     `json:"mono_deadline_ns"`
	ExpiresAt      time.Time `json:"expires_at"`
	// Since is when this caller's first lease was made; a read result
	// older than it does not satisfy a read: need.
	Since time.Time `json:"since"`
}

// Fresh reports whether the lease is usable: same boot, monotonic and
// wall deadlines ahead, and no stop file.
func (l *Lease) Fresh(now time.Time, mono MonoNow, stop bool) bool {
	if l == nil || stop || !mono.OK || l.BootID == "" || l.BootID != mono.BootID {
		return false
	}
	return mono.NS < l.MonoDeadlineNS && now.Before(l.ExpiresAt)
}

// TokenOK reports whether token is the lease's token.
func (l *Lease) TokenOK(token string) bool {
	return l != nil && token != "" && hashHex([]byte(token)) == l.TokenSHA256
}

// Counts are the counts of a queue (model.OnlineCounts).
type Counts = model.OnlineCounts

// Summary is Result.Online of plan, switch and sync.
type Summary struct {
	PinsAdd     int    `json:"pins_add"`
	PinsRemove  int    `json:"pins_remove"`
	PinsKeep    int    `json:"pins_keep"`
	PinsBlocked int    `json:"pins_blocked"`
	Sidebar     int    `json:"sidebar"`
	Other       int    `json:"other"`
	PinsSource  string `json:"pins_source"`
	PinsReason  string `json:"pins_reason"`
	Queued      bool   `json:"queued"`
}

// Any reports whether any count is non-zero.
func (s *Summary) Any() bool {
	return s != nil && (s.PinsAdd+s.PinsRemove+s.PinsKeep+s.PinsBlocked+s.Sidebar+s.Other) > 0
}

// Option is one answer of a question. Label and Description encode as
// their English text; DisplayLabel and DisplayDescription are the same
// in the display language, set only when it is not English.
type Option struct {
	ID                 string    `json:"id"`
	Label              i18n.Text `json:"label"`
	Description        i18n.Text `json:"description"`
	DisplayLabel       string    `json:"display_label,omitempty"`
	DisplayDescription string    `json:"display_description,omitempty"`
}

// Question is a decision `online next` asks the user (through the skill).
// Header and Text encode as English; the display fields are added for a
// display language other than English (Localized).
type Question struct {
	ID            string    `json:"id"`
	Header        i18n.Text `json:"header"`
	Text          i18n.Text `json:"text"`
	Options       []Option  `json:"options"`
	DisplayHeader string    `json:"display_header,omitempty"`
	DisplayText   string    `json:"display_text,omitempty"`
}

// Localized returns a copy of q with the display fields rendered in l
// (none for English). The skill shows them and maps the chosen display
// label back to the option id.
func (q Question) Localized(l i18n.Lang) Question {
	out := q
	out.Options = append([]Option(nil), q.Options...)
	if l == i18n.English {
		return out
	}
	out.DisplayHeader, out.DisplayText = q.Header.In(l), q.Text.In(l)
	for i, o := range out.Options {
		out.Options[i].DisplayLabel, out.Options[i].DisplayDescription = o.Label.In(l), o.Description.In(l)
	}
	return out
}

// Call is one offered call.
type Call struct {
	ID    StepID          `json:"id"`
	Tool  string          `json:"tool"`
	Input json.RawMessage `json:"input"`
}

// Batch states.
const (
	StateCalls         = "calls"
	StateQuestion      = "question"
	StateWait          = "wait"
	StateComplete      = "complete"
	StateNone          = "none"
	StateStale         = "stale"
	StateNoHooks       = "no_hooks"
	StateBusyElsewhere = "busy_elsewhere"
)

// Batch is the answer of `online next` (and, with fewer members, of
// `online capture`).
type Batch struct {
	Queue            string      `json:"queue,omitempty"`
	State            string      `json:"state"`
	Token            string      `json:"token,omitempty"`
	Calls            []Call      `json:"calls"`
	Tools            []string    `json:"tools"`
	Question         *Question   `json:"question"`
	RetryAfterMS     int64       `json:"retry_after_ms"`
	RemainingBatches int         `json:"remaining_batches"`
	Counts           Counts      `json:"counts"`
	Lines            []i18n.Text `json:"lines"`
}

// Warning is a fixed-text warning of a provider.
type Warning struct {
	Msg  string    `json:"msg"`
	Text i18n.Text `json:"text"`
}

// Patterns of validated names.
var (
	toolRE      = regexp.MustCompile(`^mcp__[a-z0-9_-]+__[a-z0-9_]+$`)
	stepIDRE    = regexp.MustCompile(`^[a-z][0-9]{3}$`)
	captureIDRE = regexp.MustCompile(`^cap-[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}$`)
	artQueueRE  = regexp.MustCompile(`^art-[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}-[0-9]{1,2}$`)
	toolUseIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	tokenRE     = regexp.MustCompile(`^[0-9a-f]{32}$`)
	serverRE    = regexp.MustCompile(`^[a-z0-9_-]+$`)
	sessionRE   = regexp.MustCompile(`^local_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// ValidTool reports whether s is an MCP tool name of the allowed form.
func ValidTool(s string) bool { return toolRE.MatchString(s) }

// ValidStepID reports whether s is a step id.
func ValidStepID(s string) bool { return stepIDRE.MatchString(s) }

// ValidQueueID reports whether s is a reconcile queue id: a run id or an
// artifacts round id.
func ValidQueueID(s string) bool { return runner.ValidRunID(s) || artQueueRE.MatchString(s) }

// ValidCaptureID reports whether s is a capture id.
func ValidCaptureID(s string) bool { return captureIDRE.MatchString(s) }

// ValidToken reports whether s is a lease token (32 lower-case hex).
func ValidToken(s string) bool { return tokenRE.MatchString(s) }

// ValidLocalSession reports whether s is a local session id.
func ValidLocalSession(s string) bool { return sessionRE.MatchString(s) }

// hashHex is the hex sha256 of b.
func hashHex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// InputSHA returns the hex sha256 of the canonical form of in, or "" when
// in is not valid JSON.
func InputSHA(in json.RawMessage) string {
	c, err := model.Canonical(in)
	if err != nil {
		return ""
	}
	return hashHex(c)
}
