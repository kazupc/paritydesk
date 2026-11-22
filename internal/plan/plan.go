package plan

import (
	"io/fs"
	"time"

	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
)

// Version is the plan format version.
const Version = 1

// Kind is an operation kind.
type Kind string

// Operation kinds. JSON files are rewritten whole (OpWriteFile) with
// contents computed at plan time by jsonx splices, so pre- and post-hashes
// are exact.
const (
	OpWriteFile  Kind = "write-file" // create or replace Folder/Path with Content
	OpQuarantine Kind = "quarantine" // move Folder/Path into the generation
	OpLSPut      Kind = "ls-put"     // put Local Storage item Key = Content (encoded value bytes)
	OpLSDelete   Kind = "ls-delete"  // delete Local Storage item Key
)

// Op is one operation on the entering folder.
type Op struct {
	Kind Kind        `json:"kind"`
	Row  model.RowID `json:"row"`
	// Path is relative to the data folder (files).
	Path string `json:"path,omitempty"`
	// Key is the Local Storage item name (not the encoded LevelDB key).
	Key string `json:"key,omitempty"`
	// Content is the new file bytes or the new encoded LS value. Never
	// serialized.
	Content []byte      `json:"-"`
	Mode    fs.FileMode `json:"mode,omitempty"`
	// PreHash is the sha256 of the current bytes ("" = must not exist).
	PreHash string `json:"pre"`
	// PostHash is the sha256 of Content ("" for removals).
	PostHash string `json:"post"`
	// Destructive marks ops that remove user data (counted on the fixed
	// DESTRUCTIVE line and by the deletion breaker).
	Destructive bool `json:"destructive,omitempty"`
	// Secret marks ops whose Content holds secret values (D27).
	Secret bool `json:"secret,omitempty"`
	// Item is the row item key this op realizes, for summaries.
	Item string `json:"item,omitempty"`
}

// Target names the entering replica and its folder.
type Target struct {
	Alias   ids.Alias     `json:"alias"`
	Replica model.Replica `json:"replica"`
	// Folder is the folder's path at apply time (parked, or Claude/ for
	// sync).
	Folder string `json:"folder"`
}

// Plan is an ordered list of ops plus everything needed to explain it.
type Plan struct {
	Version     int              `json:"v"`
	RunID       string           `json:"run"`
	From        ids.Alias        `json:"from"`
	Target      Target           `json:"target"`
	Approximate bool             `json:"approximate"`
	CreatedAt   time.Time        `json:"created_at"`
	ExpiresAt   time.Time        `json:"expires_at"`
	Ops         []Op             `json:"ops"`
	Changes     []model.Change   `json:"changes"`
	Conflicts   []model.Conflict `json:"conflicts,omitempty"`
	// Skipped lists rows not planned and why (degraded mode, disabled).
	Skipped map[model.RowID]string `json:"skipped,omitempty"`
	// NeedsConfirmation is set when any row merged as a union because its
	// base was invalid (exit 21 when non-interactive).
	NeedsConfirmation bool `json:"needs_confirmation,omitempty"`
}

// Empty reports whether the plan has no ops (fast path: P6 and P7 skipped).
func (p *Plan) Empty() bool { return len(p.Ops) == 0 }

// Hash returns a stable hash over the plan's ops (kind, row, path, key,
// pre and post hashes) and changes. Timestamps and contents are excluded.
func (p *Plan) Hash() string { return "" }

// Counts is the per-category tally of a plan.
type Counts struct {
	Added     int `json:"added"`
	Updated   int `json:"updated"`
	Deleted   int `json:"deleted"`
	Revived   int `json:"revived"`
	Moved     int `json:"moved"`
	Conflicts int `json:"conflicts"`
}

// Summary is what P2 prints: counts per category, destructive counts last.
type Summary struct {
	Categories  map[model.Category]Counts `json:"categories"`
	Destructive map[model.Category]int    `json:"destructive"`
}

// Summarize tallies the plan's changes.
func (p *Plan) Summarize() Summary { return Summary{} }

// NewDestructive returns the destructive changes in final that were not in
// preview (D18). A non-empty result needs a new confirmation, or aborts a
// detached run.
func NewDestructive(preview, final *Plan) []model.Change { return nil }

// Limits configure the deletion breaker.
type Limits struct {
	MaxDeleteFraction float64
	MaxDeleteMin      int
	// AllowDeletions is --allow-deletions=N; 0 means not given.
	AllowDeletions int
}

// WorktreeRisk is a worktree that the source references and the target
// will not, with uncommitted changes (row 18 loss guard, H2).
type WorktreeRisk struct {
	Path  string `json:"path"` // shown redacted
	Dirty bool   `json:"dirty"`
}

// ValidateInput is everything the validator checks against.
type ValidateInput struct {
	Plan *Plan
	// Before is the number of items per category in the entering replica
	// before the plan (for the breaker's fraction and "becomes empty").
	Before map[model.Category]int
	Limits Limits
	// Denylist holds path globs (relative to the data folder) and LS key
	// globs that must never be written (classification "never touched").
	DenyPaths  []string
	DenyLSKeys []string
	// AllowPaths, if non-empty, restricts file ops to these globs.
	AllowPaths []string
	// SecretsMayCross is false across a managed_by_org edge unless allowed
	// per key; secret ops are then refused.
	SecretsMayCross    bool
	SecretsAllowed     map[string]bool // "<preset>/<VAR>" allowed explicitly
	Worktrees          []WorktreeRisk
	AcceptWorktreeRisk bool
	// MaxOps and MaxBytes bound the plan size.
	MaxOps   int
	MaxBytes int64
}

// Validate runs every check of phase P5: denylist, path containment
// (clean relative paths, no symlink-prone names, no names matching the
// app's tmp recovery globs), limits, the deletion breaker (exit 22),
// the secret boundary, and the worktree guard (exit 23). It returns nil or
// a *pderr.Error.
func Validate(in ValidateInput) error { return pderr.ErrNotImplemented }
