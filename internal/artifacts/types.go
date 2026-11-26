package artifacts

import (
	"context"
	"time"

	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/online"
)

// PairID identifies a pair: the archive's record of one artifact and its
// copies on every profile (^[0-9a-f]{16}$).
type PairID string

// Slug is an artifact id: a lower-case UUID.
type Slug string

// VersionID is an artifact version id (manifest artifacts.version_pattern).
type VersionID string

// Phase is the phase of a round.
type Phase string

// Phases, in chain order. Inspect, read, publish and verify may repeat.
const (
	PhaseInventory Phase = "inventory"
	PhaseInspect   Phase = "inspect"
	PhaseRead      Phase = "read"
	PhasePublish   Phase = "publish"
	PhaseVerify    Phase = "verify"
	PhaseDone      Phase = "done"
)

// Copy states and origins.
const (
	StateLive          = "live"
	StateDeleted       = "deleted"
	StateCreatePending = "create_pending"
	StateUnverified    = "unverified"

	OriginCreated  = "created"
	OriginRecorded = "recorded"
	OriginImported = "imported"
)

// Read modes of an artifact (get_artifact).
const (
	ReadOwner  = "owner"
	ReadUsers  = "users"
	ReadOrg    = "org"
	ReadPublic = "public"
)

// Gate values.
const (
	GateUnverified = "unverified"
	GateExact      = "exact"
	GateMismatch   = "mismatch"
)

// Round-trip classes (7.6).
const (
	ClassExact     = "exact"
	ClassWrapped   = "wrapped"
	ClassTitleOnly = "title_only"
	ClassOther     = "other"
)

// History reasons.
const (
	HistReplaced = "replaced"
	HistConflict = "conflict_lost"
	HistReadback = "roundtrip_readback"
	HistImported = "imported"
)

// Manifest is manifest.json (v3). Field order is the JSON order.
type Manifest struct {
	V         int                          `json:"v"`
	Seq       uint64                       `json:"seq"`
	UpdatedAt time.Time                    `json:"updated_at"`
	Gate      Gate                         `json:"gate"`
	Profiles  map[ids.Alias]*ProfileRecord `json:"profiles"`
	Pairs     map[PairID]*Pair             `json:"pairs"`
	Forgotten map[PairID]*Pair             `json:"forgotten,omitempty"`
	Ignored   map[Slug]*IgnoredItem        `json:"ignored,omitempty"`
	Imports   []ImportRecord               `json:"imports,omitempty"`
	// PublishRefused keeps the last refusal of publishing ("client_refused"
	// or "grant_declined") until the next successful publish, so status
	// reports it also after a new chain started (9.1: sticky). Additive.
	PublishRefused string `json:"publish_refused,omitempty"`
}

// Gate is the round-trip gate (7.6).
type Gate struct {
	Roundtrip  string    `json:"roundtrip"`   // unverified | exact | mismatch
	Class      string    `json:"class"`       // wrapped | other when mismatch; "" otherwise
	AppVersion string    `json:"app_version"` // app version the state belongs to
	At         time.Time `json:"at"`
}

// ProfileRecord is what the archive knows of a profile.
type ProfileRecord struct {
	Replica         model.Replica `json:"replica"`
	LastInventoryAt time.Time     `json:"last_inventory_at,omitzero"`
	LastSyncAt      time.Time     `json:"last_sync_at,omitzero"`
	LastSyncEpoch   uint64        `json:"last_sync_epoch,omitempty"`
}

// Pair is one artifact and its copies.
type Pair struct {
	Title          string              `json:"title"`     // ValidTitle or ""
	TitleSHA       string              `json:"title_sha"` // TitleKey
	TitleChangedAt time.Time           `json:"title_changed_at,omitzero"`
	CreatedAt      time.Time           `json:"created_at"`
	Content        *Content            `json:"content,omitempty"` // nil = not yet harvested
	Copies         map[ids.Alias]*Copy `json:"copies"`
	History        []HistoryEntry      `json:"history,omitempty"`
	Flags          Flags               `json:"flags"`
}

// Content is the pair's current content.
type Content struct {
	SHA256        string    `json:"sha256"`
	Bytes         int64     `json:"bytes"`
	Lines         int       `json:"lines"`
	SavedAt       time.Time `json:"saved_at"`
	SourceProfile ids.Alias `json:"source_profile"`
	SourceVersion VersionID `json:"source_version"`
	ChangedAt     time.Time `json:"changed_at,omitzero"` // zero = unknown
	Icon          string    `json:"icon,omitempty"`      // ^[a-z]{1,24}$
}

// Copy is the pair's artifact on one profile.
type Copy struct {
	Replica       model.Replica  `json:"replica"`
	Slug          Slug           `json:"slug"`
	URL           string         `json:"url,omitempty"`
	State         string         `json:"state"`
	Origin        string         `json:"origin"`
	Read          string         `json:"read,omitempty"`
	SyncedVersion VersionID      `json:"synced_version"`
	SyncedSHA     string         `json:"synced_sha"`
	ServedSHA     string         `json:"served_sha,omitempty"`
	SeenVersion   VersionID      `json:"seen_version,omitempty"`
	SeenTitleSHA  string         `json:"seen_title_sha,omitempty"`
	SeenAt        time.Time      `json:"seen_at,omitzero"`
	DeletedAt     time.Time      `json:"deleted_at,omitzero"`
	CreateAttempt *CreateAttempt `json:"create_attempt,omitempty"`
	Roundtrip     string         `json:"roundtrip,omitempty"` // "" | exact | title_only | mismatch:<class>
}

// CreateAttempt records a create whose result is unknown (8.5).
type CreateAttempt struct {
	At         time.Time `json:"at"`
	TitleSHA   string    `json:"title_sha"`
	ContentSHA string    `json:"content_sha"`
}

// HistoryEntry is a replaced version of a pair.
type HistoryEntry struct {
	SHA256        string    `json:"sha256"`
	Title         string    `json:"title,omitempty"`
	SavedAt       time.Time `json:"saved_at"`
	SourceProfile ids.Alias `json:"source_profile"`
	SourceVersion VersionID `json:"source_version,omitempty"`
	Reason        string    `json:"reason"`
}

// Flags are the pair's per-artifact decisions and detected properties.
type Flags struct {
	Skipped      string      `json:"skipped"` // "" | user | imported
	MultiFile    bool        `json:"multi_file"`
	TypeMade     bool        `json:"type_made"`
	LiveDoc      bool        `json:"live_doc"`
	CoAuthored   bool        `json:"co_authored"`
	Capabilities CapFlags    `json:"capabilities"`
	AllowShared  bool        `json:"allow_shared"`
	AllowPublic  bool        `json:"allow_public"`
	AllowCreate  []ids.Alias `json:"allow_create"`
	Restore      []ids.Alias `json:"restore"`
	// TitleTaken are the profiles where an artifact of the same title
	// turned out to be another one (8.5): no create there until
	// `allow --create`.
	TitleTaken []ids.Alias `json:"title_taken,omitempty"`
}

// CapFlags are a pair's declared capabilities.
type CapFlags struct {
	State         string   `json:"state"` // none | declared | unknown
	Digest        string   `json:"digest"`
	Names         []string `json:"names"`
	AllowedDigest string   `json:"allowed_digest"`
}

// Blocked reports whether the declaration keeps the pair from being read
// or published (8.1 rule 3, 7.2): declared or unknown capabilities that
// the user did not allow with this exact digest. An unknown declaration
// without a digest (an older archive) is never allowed.
func (c CapFlags) Blocked() bool {
	if c.State != CapDeclared && c.State != CapUnknown {
		return false
	}
	return c.Digest == "" || c.AllowedDigest != c.Digest
}

// Capability states.
const (
	CapNone     = "none"
	CapDeclared = "declared"
	CapUnknown  = "unknown"
)

// IgnoredItem is a slug that could not become a copy (M12).
type IgnoredItem struct {
	Alias   ids.Alias `json:"alias"`
	Reason  string    `json:"reason"`
	Version VersionID `json:"version"`
	At      time.Time `json:"at"`
}

// ImportRecord is one legacy import.
type ImportRecord struct {
	LegacySHA256 string    `json:"legacy_sha256"`
	Format       int       `json:"format"`
	At           time.Time `json:"at"`
	Pairs        int       `json:"pairs"`
}

// Inventory is inventory/<alias>.json: the last list_artifacts of a
// profile (titles only as keys).
type Inventory struct {
	V        int           `json:"v"`
	Profile  ids.Alias     `json:"profile"`
	Replica  model.Replica `json:"replica"`
	Chain    string        `json:"chain"`
	At       time.Time     `json:"at"`
	Complete bool          `json:"complete"`
	Pages    int           `json:"pages"`
	Capped   bool          `json:"capped"`
	Items    []InvItem     `json:"items"`
}

// InvItem is one listed artifact of the account (mine only).
type InvItem struct {
	Slug        Slug      `json:"slug"`
	Mine        bool      `json:"mine"`
	Version     VersionID `json:"version"`
	UpdatedAt   time.Time `json:"updated_at,omitzero"`
	SoftDeleted bool      `json:"soft_deleted"`
	TitleSHA    string    `json:"title_sha,omitempty"`
	URL         string    `json:"url,omitempty"`
}

// Item returns the inventory item of slug, or nil.
func (inv *Inventory) Item(s Slug) *InvItem {
	if inv == nil {
		return nil
	}
	for i := range inv.Items {
		if inv.Items[i].Slug == s {
			return &inv.Items[i]
		}
	}
	return nil
}

// Chain is chain.json: the current or last sync.
type Chain struct {
	V          int                `json:"v"`
	ID         string             `json:"id"`
	Profile    ids.Alias          `json:"profile"`
	Replica    model.Replica      `json:"replica"`
	Epoch      uint64             `json:"epoch"`
	AppVersion string             `json:"app_version"`
	Session    string             `json:"session"`
	Started    time.Time          `json:"started"`
	Round      int                `json:"round"`
	Phase      Phase              `json:"phase"`
	Queue      string             `json:"queue"`
	State      string             `json:"state"`
	StaleWhy   string             `json:"stale_why,omitempty"`
	Decision   string             `json:"decision,omitempty"`
	Granted    bool               `json:"granted,omitempty"`
	NoPublish  string             `json:"no_publish,omitempty"`
	Candidates map[Slug]Candidate `json:"candidates,omitempty"`
	Published  []Published        `json:"published,omitempty"`
	Report     Report             `json:"report"`
	// Ingested is the last round whose results are in the archive (a
	// round is ingested once).
	Ingested int `json:"ingested,omitempty"`
	// Tried are the pairs a publish step was planned for in this chain
	// (never planned twice in one chain).
	Tried []PairID `json:"tried,omitempty"`
	// Verified counts the verified publishes of this chain.
	Verified int `json:"verified,omitempty"`
	// Capped: the chain ended at artifacts.max_rounds.
	Capped bool `json:"capped,omitempty"`
}

// Chain states.
const (
	ChainActive    = "active"
	ChainPaused    = "paused"
	ChainComplete  = "complete"
	ChainCancelled = "cancelled"
	ChainStale     = "stale"
)

// Terminal reports a chain that offers nothing more.
func (c *Chain) Terminal() bool {
	return c == nil || c.State == ChainComplete || c.State == ChainCancelled || c.State == ChainStale
}

// Candidate is an artifact the chain inspects, reads or publishes to.
type Candidate struct {
	Pair     PairID    `json:"pair,omitempty"` // "" = new item
	Alias    ids.Alias `json:"alias"`
	Verdict  string    `json:"verdict"` // changed_here | new_here | conflict | verify_import | adopt_check | publish
	Version  VersionID `json:"version"`
	Read     string    `json:"read,omitempty"`
	Icon     string    `json:"icon,omitempty"`
	TitleSHA string    `json:"title_sha,omitempty"`
	Title    string    `json:"title,omitempty"`
	Bytes    int64     `json:"bytes,omitempty"`
	Restarts int       `json:"restarts,omitempty"`
	// Role is "read" (a read candidate) or "target" (a publish target).
	Role string `json:"role"`
	// UpdatedAt is the inventory item's change time.
	UpdatedAt time.Time `json:"updated_at,omitzero"`
	// Inspected: get (and files, and for reads decl) were ingested.
	Inspected  bool      `json:"inspected,omitempty"`
	GetVersion VersionID `json:"get_version,omitempty"`
	EntryOnly  bool      `json:"entry_only,omitempty"`
	Cap        string    `json:"cap,omitempty"`
	CapDigest  string    `json:"cap_digest,omitempty"`
	// Base is the base_version of a conflict loser's update.
	Base VersionID `json:"base,omitempty"`
	// State: "" open, "done" (read saved or target published), "failed",
	// "skipped".
	State  string `json:"state,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Verify marks a verify read of a published copy.
	Verify bool `json:"verify,omitempty"`
}

// Candidate roles and states.
const (
	RoleRead   = "read"
	RoleTarget = "target"

	CandDone   = "done"
	CandFailed = "failed"
	// ReasonPartialUnwritable fails a read whose chunk could not be kept
	// between rounds (the disk refused it).
	ReasonPartialUnwritable = "partial_unwritable"
	CandSkipped             = "skipped"
)

// Published is a publish awaiting verify.
type Published struct {
	Pair     PairID    `json:"pair"`
	Alias    ids.Alias `json:"alias"`
	Slug     Slug      `json:"slug"`
	Version  VersionID `json:"version"`
	SHA      string    `json:"sha"`
	Verify   bool      `json:"verify"`
	Verified bool      `json:"verified,omitempty"`
}

// Report is the chain's outcome.
type Report struct {
	Saved       int          `json:"saved"`
	Published   int          `json:"published"`
	Created     int          `json:"created"`
	Conflicts   int          `json:"conflicts"`
	DeletedHere int          `json:"deleted_here"`
	Skipped     int          `json:"skipped"`
	Failed      int          `json:"failed"`
	Ignored     int          `json:"ignored"`
	Pairs       []PairResult `json:"pairs"`
}

// PairResult is one pair's outcome in a chain.
type PairResult struct {
	Pair   PairID `json:"pair,omitempty"`
	Slug   Slug   `json:"slug,omitempty"`
	Result string `json:"result"`
	Reason string `json:"reason"`
}

// MaxReportPairs bounds Report.Pairs (oldest dropped).
const MaxReportPairs = 1000

// PartialState is the recorded chunks of one slug between read rounds.
type PartialState struct {
	Version VersionID
	Chunks  []Chunk // sorted by offset
}

// ArchivePaths are the absolute archive paths.
type ArchivePaths struct {
	Root, Blobs, Outbox string
}

// ChainFlags are per-chain facts Judge needs beyond the manifest.
type ChainFlags struct {
	Inspect map[Slug]Candidate // get/files/decl results of this chain
	Moved   map[Slug]bool      // version_moved in this chain
	// Claimed maps every slug that is a copy of a pair to that pair.
	Claimed map[Slug]PairID
}

// BoundaryPolicy is the data boundary (8.7).
type BoundaryPolicy struct {
	Exports, Imports, Managed map[ids.Alias]bool
	CrossManaged              bool
	// Strict treats an alias the maps do not name (a removed profile, or
	// a content source cleared by `profile remove`) as neither exporting
	// nor importing and as managed. PolicyFrom sets it: content harvested
	// on a profile that is gone never crosses the boundary by default.
	Strict bool
}

// ArtifactsView is what the orchestrator asks of artifact sync; nil
// turns it off.
type ArtifactsView interface {
	Due(alias ids.Alias, rep model.Replica) (int, error)
	LastSyncEpoch(alias ids.Alias) (uint64, bool, error)
	WriteDue(ctx context.Context, alias ids.Alias, rep model.Replica, epoch uint64) error
	RenameProfile(ctx context.Context, from, to ids.Alias) error
	RemoveProfile(ctx context.Context, alias ids.Alias) error
}

// NextArgs are the flags of `artifacts next`.
type NextArgs struct {
	Session, Token string
	Unavailable    []string
	Refused        []online.StepID
	Wait           bool
}

// Counts are the counts of a `next` answer.
type Counts struct {
	Pairs       int `json:"pairs"`
	InStep      int `json:"in_step"`
	ToRead      int `json:"to_read"`
	ToPublish   int `json:"to_publish"`
	Saved       int `json:"saved"`
	Published   int `json:"published"`
	Conflicts   int `json:"conflicts"`
	DeletedHere int `json:"deleted_here"`
	Skipped     int `json:"skipped"`
	Ignored     int `json:"ignored"`
	Failed      int `json:"failed"`
}

// NextOut is the JSON of `artifacts next` (6.3).
type NextOut struct {
	V                int              `json:"-"`
	Chain            string           `json:"chain,omitempty"`
	Round            int              `json:"round,omitempty"`
	Phase            Phase            `json:"phase,omitempty"`
	Queue            string           `json:"queue,omitempty"`
	State            string           `json:"state"`
	Token            string           `json:"token,omitempty"`
	Calls            []online.Call    `json:"calls"`
	Tools            []string         `json:"tools"`
	Question         *online.Question `json:"question"`
	RetryAfterMS     int64            `json:"retry_after_ms"`
	RemainingBatches int              `json:"remaining_batches"`
	Counts           Counts           `json:"counts"`
	Lines            []i18n.Text      `json:"lines"`
}

// States of a `next` answer beyond online's.
const (
	StateBlocked  = "blocked"
	StatePaused   = "paused"
	StateComplete = "complete"
)

// ImportInput is `artifacts import-legacy`.
type ImportInput struct {
	From  string
	Map   map[ids.AccountID]ids.Alias
	Apply bool
}

// LegacyManifest is a decoded legacy archive manifest.
type LegacyManifest struct {
	Format  int
	Entries []LegacyEntry
}
