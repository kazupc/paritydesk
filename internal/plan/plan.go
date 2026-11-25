package plan

import (
	"encoding/json"
	"io/fs"
	"sort"
	"time"

	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/jsonx"
	"github.com/kazupc/paritydesk/internal/model"
)

// Version is the plan format version.
const Version = 1

// Kind is an operation kind.
type Kind string

// Operation kinds. JSON files and Local Storage values are rewritten whole
// with contents computed at plan time by Compose, so pre- and post-hashes
// are exact. There is exactly one op per target (file path or LS key).
const (
	OpWriteFile  Kind = "write-file" // create or replace Folder/Path with Content
	OpQuarantine Kind = "quarantine" // move Folder/Path into the generation
	OpLSPut      Kind = "ls-put"     // put Local Storage item Key = Content (encoded value bytes)
	OpLSDelete   Kind = "ls-delete"  // delete Local Storage item Key
)

// Reasons a destructive op may carry when it removes no user data. Every
// other destructive op (Destructive, OpQuarantine, OpLSDelete) must map to
// a model.ChangeDeleted of the same row and item; Validate enforces it.
const (
	// ReasonAppTmpCandidate: an app-written *.json.tmp file that was read
	// as a candidate version and is moved into the generation (D30).
	ReasonAppTmpCandidate = "app-tmp-candidate"
	// ReasonRevivedTombstone: a session tombstone (deleted_<id>) moved into
	// the generation because the merge revived its session; removing it
	// restores data rather than deleting it.
	ReasonRevivedTombstone = "revived-tombstone"
)

// NonUserDataReasons lists every accepted Reason.
var NonUserDataReasons = []string{ReasonAppTmpCandidate, ReasonRevivedTombstone}

// Op is one operation on the entering folder.
type Op struct {
	Kind Kind `json:"kind"`
	// Row is the first row that contributed to the op; Rows lists every
	// contributing row in order when edits of several rows were composed
	// onto one target (for example claude_desktop_config.json).
	Row  model.RowID   `json:"row"`
	Rows []model.RowID `json:"rows,omitempty"`
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
	// Items are the row item keys this op realizes, for summaries and for
	// mapping destructive ops to changes.
	Items []string `json:"items,omitempty"`
	// Reason is one of NonUserDataReasons for a destructive op that removes
	// no user data, else empty.
	Reason string `json:"reason,omitempty"`
}

// Location names one write target: a file relative to the data folder,
// or a Local Storage item name. Exactly one field is set.
type Location struct {
	File  string `json:"file,omitempty"`
	LSKey string `json:"ls_key,omitempty"`
}

// EditKind is the kind of one edit.
type EditKind string

// Edit kinds.
const (
	// EditSet sets Path to Value with jsonx.Set (every parent must exist
	// in the document at that point of the composition).
	EditSet EditKind = "set"
	// EditDelete removes Path with jsonx.Delete; deleting an absent path is
	// a no-op.
	EditDelete EditKind = "delete"
	// EditReplace replaces the whole document with Value (file bytes, or
	// for Local Storage the decoded text before encoding). Used for new
	// files, non-JSON files and regenerated files (archived-sessions.idx).
	EditReplace EditKind = "replace"
	// EditRemove quarantines the file or deletes the LS item. It may not be
	// combined with any other edit on the same location.
	EditRemove EditKind = "remove"
)

// Edit is one adapter-produced change to one location. Adapters return
// edits, never whole ops, so that rows sharing a file or an LS value
// (claude_desktop_config.json holds the epitaxyPrefs mirror of several
// rows and mcpServers; the dframe-store value holds both the sidebar view
// and the pin order) compose instead of overwriting each other.
type Edit struct {
	Loc  Location    `json:"loc"`
	Row  model.RowID `json:"row"`
	Item string      `json:"item,omitempty"`
	Kind EditKind    `json:"kind"`
	// Path is the literal JSON path for set and delete. For Local Storage
	// it is a path inside the decoded value text (for example
	// ["value", ...] inside an LSS envelope, or ["state", ...] inside a
	// persisted store).
	Path jsonx.Path `json:"path,omitempty"`
	// Value is the JSON value for set, or the whole new document for
	// replace. It may hold secret bytes copied from the leaving folder, so
	// it is never serialized.
	Value json.RawMessage `json:"-"`
	// Mode is the file mode for a file created by this edit (0: 0600).
	Mode fs.FileMode `json:"mode,omitempty"`
	// BaseHash is the sha256 of the location's bytes the adapter computed
	// the edit against ("" when absent). Compose refuses the edit when the
	// original bytes differ.
	BaseHash    string `json:"base"`
	Destructive bool   `json:"destructive,omitempty"`
	Secret      bool   `json:"secret,omitempty"`
	// Reason is one of NonUserDataReasons for a destructive edit that
	// removes no user data.
	Reason string `json:"reason,omitempty"`
}

// Source returns a location's current bytes in the entering folder: the
// file's bytes, or the encoded Local Storage value. present is false when
// the file or key does not exist.
type Source func(loc Location) (data []byte, present bool, err error)

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

// hashedOp is the part of an op that the plan hash covers.
type hashedOp struct {
	Kind        Kind          `json:"k"`
	Row         model.RowID   `json:"r"`
	Rows        []model.RowID `json:"rs,omitempty"`
	Path        string        `json:"p,omitempty"`
	Key         string        `json:"key,omitempty"`
	Pre         string        `json:"pre"`
	Post        string        `json:"post"`
	Destructive bool          `json:"d,omitempty"`
	Secret      bool          `json:"s,omitempty"`
	Items       []string      `json:"i,omitempty"`
	Reason      string        `json:"why,omitempty"`
}

// hashed is the canonical form the plan hash is computed over: the format
// version, the source alias, the target alias and replica, every op's
// identity and hashes, the changes, and NeedsConfirmation. Run id,
// timestamps, the folder path, contents and the approximate flag are
// excluded, so the same changes on the same bytes hash the same in any
// process.
type hashed struct {
	Version           int            `json:"v"`
	From              ids.Alias      `json:"from"`
	To                ids.Alias      `json:"to"`
	Replica           model.Replica  `json:"rep"`
	Ops               []hashedOp     `json:"ops"`
	Changes           []model.Change `json:"changes"`
	NeedsConfirmation bool           `json:"nc,omitempty"`
}

// Hash returns a stable hex sha256 over the plan (see hashed). A nil plan
// hashes to "".
func (p *Plan) Hash() string {
	if p == nil {
		return ""
	}
	h := hashed{Version: p.Version, From: p.From, To: p.Target.Alias, Replica: p.Target.Replica,
		Ops: make([]hashedOp, len(p.Ops)), Changes: p.Changes, NeedsConfirmation: p.NeedsConfirmation}
	for i, o := range p.Ops {
		h.Ops[i] = hashedOp{Kind: o.Kind, Row: o.Row, Rows: o.Rows, Path: o.Path, Key: o.Key,
			Pre: o.PreHash, Post: o.PostHash, Destructive: o.Destructive, Secret: o.Secret,
			Items: o.Items, Reason: o.Reason}
	}
	b, err := json.Marshal(h)
	if err != nil {
		panic("plan: hash encoding failed: " + err.Error())
	}
	return model.HashBytes(b)
}

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

// Summarize tallies the plan's changes per category: added; updated
// (updated, capped, remapped); deleted; revived (revived, restored);
// moved; and conflicts. Skipped changes are not counted. Destructive
// counts deleted changes.
func (p *Plan) Summarize() Summary {
	s := Summary{Categories: map[model.Category]Counts{}, Destructive: map[model.Category]int{}}
	if p == nil {
		return s
	}
	for _, c := range p.Changes {
		cat := model.CategoryOf(c.Row)
		n := s.Categories[cat]
		switch c.Kind {
		case model.ChangeAdded:
			n.Added++
		case model.ChangeUpdated, model.ChangeCapped, model.ChangeRemapped:
			n.Updated++
		case model.ChangeDeleted:
			n.Deleted++
			s.Destructive[cat]++
		case model.ChangeRevived, model.ChangeRestored:
			n.Revived++
		case model.ChangeMoved:
			n.Moved++
		default:
			continue
		}
		s.Categories[cat] = n
	}
	for _, c := range p.Conflicts {
		cat := model.CategoryOf(c.Row)
		n := s.Categories[cat]
		n.Conflicts++
		s.Categories[cat] = n
	}
	return s
}

// destructiveKey identifies a destructive change.
func destructiveKey(c model.Change) string { return string(c.Row) + "\x00" + c.Key }

// NewDestructive returns the destructive changes in final that were not in
// preview, matched by row and key (D18). A nil preview counts as empty. A
// non-empty result needs a new confirmation, or aborts a detached run.
func NewDestructive(preview, final *Plan) []model.Change {
	if final == nil {
		return nil
	}
	seen := map[string]bool{}
	if preview != nil {
		for _, c := range preview.Changes {
			if c.Destructive() {
				seen[destructiveKey(c)] = true
			}
		}
	}
	var out []model.Change
	for _, c := range final.Changes {
		if c.Destructive() && !seen[destructiveKey(c)] {
			out = append(out, c)
		}
	}
	return out
}

// DestructiveDigest returns a hex sha256 over the sorted (row, key) pairs
// of the plan's destructive changes, or "" when there are none. `plan
// --json` prints it next to the plan hash so the confirmed destructive set
// can be named in logs and result files.
func (p *Plan) DestructiveDigest() string {
	if p == nil {
		return ""
	}
	var keys []string
	for _, c := range p.Changes {
		if c.Destructive() {
			keys = append(keys, destructiveKey(c))
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	b, err := json.Marshal(keys)
	if err != nil {
		panic("plan: digest encoding failed: " + err.Error())
	}
	return model.HashBytes(b)
}

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
	// DenyPaths and DenyLSKeys are the manifest's never_touched globs.
	// The fixed floor in package denylist always applies on top of them,
	// even when both are empty.
	DenyPaths  []string
	DenyLSKeys []string
	// AllowPaths, if non-empty, restricts file ops to these globs.
	AllowPaths []string
	// AllowLSKeys, if non-empty, restricts LS ops to these item names (the
	// expanded ls_keys of the enabled rows).
	AllowLSKeys []string
	// TmpRecoveryGlobs are the manifest's app tmp recovery globs (D30); no
	// op may create or write a path matching them.
	TmpRecoveryGlobs []string
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
