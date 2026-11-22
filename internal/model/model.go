package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/kazupc/paritydesk/internal/ids"
)

// RowID names one synced row module. Several numbered rows of
// the row table can share a module when they are merged together.
type RowID string

// v0.1 row modules.
const (
	RowSessions     RowID = "sessions"      // rows 1, 2, 3, 8: records, tombstones, archive index, env ids
	RowTasks        RowID = "tasks"         // row 4: backlog and scheduled tasks
	RowWaitingInput RowID = "waiting-input" // row 5
	RowEnvironments RowID = "environments"  // rows 6, 7: presets and the session→environment map
	RowGroupOrder   RowID = "group-order"   // row 9: OFF + verify + contested
	RowSidebarView  RowID = "sidebar-view"  // row 10: OFF + verify, opt-in until the ON path ships
	RowPinnedOrder  RowID = "pinned-order"  // row 11 (pinnedOrder only): OFF + verify
	RowAppPrefs     RowID = "app-prefs"     // row 14
	RowWindowState  RowID = "window-state"  // row 16
	RowMCPServers   RowID = "mcp-servers"   // row 17, opt-in
	RowWorktrees    RowID = "worktrees"     // row 18
)

// AllRows lists the v0.1 rows in apply order. Environments come before
// sessions because the environment remap feeds session records (row 8).
var AllRows = []RowID{
	RowEnvironments, RowSessions, RowTasks, RowWaitingInput, RowWorktrees,
	RowGroupOrder, RowSidebarView, RowPinnedOrder, RowAppPrefs, RowWindowState, RowMCPServers,
}

// Category is a user-facing group of rows for --only and --skip.
type Category string

// Categories for --only and --skip. "artifacts" is reserved for v0.2.
const (
	CatSessions     Category = "sessions"
	CatEnvironments Category = "environments"
	CatOrder        Category = "order"
	CatSidebar      Category = "sidebar"
	CatPins         Category = "pins"
	CatPrefs        Category = "prefs"
	CatWorktrees    Category = "worktrees"
	CatWindow       Category = "window"
	CatMCP          Category = "mcp"
)

// AllCategories lists the v0.1 categories.
var AllCategories = []Category{CatSessions, CatEnvironments, CatOrder, CatSidebar, CatPins, CatPrefs, CatWorktrees, CatWindow, CatMCP}

// CategoryOf maps a row to its category.
func CategoryOf(r RowID) Category {
	switch r {
	case RowSessions, RowTasks, RowWaitingInput:
		return CatSessions
	case RowEnvironments:
		return CatEnvironments
	case RowGroupOrder:
		return CatOrder
	case RowSidebarView:
		return CatSidebar
	case RowPinnedOrder:
		return CatPins
	case RowAppPrefs:
		return CatPrefs
	case RowWorktrees:
		return CatWorktrees
	case RowWindowState:
		return CatWindow
	case RowMCPServers:
		return CatMCP
	}
	return ""
}

// Replica is one (account, org) scope. Folders hold replicas; with folder
// swap each parked folder holds exactly one (D6).
type Replica struct {
	Account ids.AccountID `json:"account"`
	Org     ids.OrgID     `json:"org"`
}

// Key returns "account/org".
func (r Replica) Key() string { return string(r.Account) + "/" + string(r.Org) }

// Stamp orders competing versions of an item (D7).
type Stamp struct {
	// Epoch is the switch counter value during which the replica that holds
	// this version was last live. Valid only when the item's base is valid.
	Epoch uint64 `json:"epoch,omitempty"`
	// AppTime is an app-written time in Unix milliseconds from this Mac
	// (for example lastActivityAt). Zero if unknown.
	AppTime int64 `json:"app_time,omitempty"`
	// ToolWritten is true when paritydesk itself wrote AppTime. A
	// tool-written time never wins a tie-break.
	ToolWritten bool `json:"tool_written,omitempty"`
}

// Item is one keyed element of a row: a record, a set member, a list
// element, a scalar or a whole file.
type Item struct {
	Key string `json:"key"`
	// Value is the normalized value as canonical JSON. Secret parts are
	// replaced by their hashes before a snapshot is persisted (D27).
	Value json.RawMessage `json:"value,omitempty"`
	// Hash is the sha256 (hex) of the item's original bytes. Used for
	// equality and for secret values.
	Hash  string `json:"hash"`
	Stamp Stamp  `json:"stamp"`
	// Secret marks items whose Value carries hashed secrets.
	Secret bool `json:"secret,omitempty"`
	// Raw holds the original bytes in memory only, including secrets. It is
	// never persisted, logged or rendered.
	Raw []byte `json:"-"`
}

// Tombstone records a deletion and when it happened (D9).
type Tombstone struct {
	Key string `json:"key"`
	At  int64  `json:"at"` // Unix ms, app-written
}

// RowSnapshot is the normalized content of one row on one replica.
type RowSnapshot struct {
	Row RowID `json:"row"`
	// Schema is the version of this row's normalized model, for base
	// migrations (M8).
	Schema     int                  `json:"schema"`
	Items      map[string]Item      `json:"items,omitempty"`
	Order      []string             `json:"order,omitempty"` // for ordered rows: item keys in order
	Tombstones map[string]Tombstone `json:"tombstones,omitempty"`
}

// Clone returns a deep copy of s (Raw slices are shared, never mutated).
func (s *RowSnapshot) Clone() *RowSnapshot {
	if s == nil {
		return nil
	}
	c := &RowSnapshot{Row: s.Row, Schema: s.Schema}
	if s.Items != nil {
		c.Items = make(map[string]Item, len(s.Items))
		for k, v := range s.Items {
			v.Value = append(json.RawMessage(nil), v.Value...)
			c.Items[k] = v
		}
	}
	c.Order = append([]string(nil), s.Order...)
	if s.Tombstones != nil {
		c.Tombstones = make(map[string]Tombstone, len(s.Tombstones))
		for k, v := range s.Tombstones {
			c.Tombstones[k] = v
		}
	}
	return c
}

// Keys returns the item keys in sorted order.
func (s *RowSnapshot) Keys() []string {
	if s == nil {
		return nil
	}
	out := make([]string, 0, len(s.Items))
	for k := range s.Items {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Hash returns a stable sha256 (hex) over the snapshot's persisted form.
// encoding/json sorts map keys, so the encoding is deterministic.
func (s *RowSnapshot) Hash() string {
	if s == nil {
		return ""
	}
	b, err := json.Marshal(s)
	if err != nil {
		// Only json.RawMessage values can fail; they are produced by
		// json.Marshal or validated on read.
		panic("model: snapshot not encodable: " + err.Error())
	}
	return HashBytes(b)
}

// HashBytes returns the hex sha256 of b.
func HashBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ChangeKind classifies one change in a plan summary.
type ChangeKind string

// Change kinds.
const (
	ChangeAdded    ChangeKind = "added"
	ChangeUpdated  ChangeKind = "updated"
	ChangeDeleted  ChangeKind = "deleted"
	ChangeRevived  ChangeKind = "revived" // newer than its tombstone (D9)
	ChangeRestored ChangeKind = "restored"
	ChangeMoved    ChangeKind = "moved"
	ChangeCapped   ChangeKind = "capped" // permissionMode capped (D8)
	ChangeRemapped ChangeKind = "remapped"
	ChangeSkipped  ChangeKind = "skipped"
)

// Change is one item-level change for the entering replica.
type Change struct {
	Row  RowID      `json:"row"`
	Kind ChangeKind `json:"kind"`
	Key  string     `json:"key"`
	// Note is a short, constant-ish explanation; never untrusted text.
	Note string `json:"note,omitempty"`
}

// Destructive reports whether the change removes data from the entering
// replica.
func (c Change) Destructive() bool { return c.Kind == ChangeDeleted }

// Conflict records a both-sides change and its loser, which is kept in the
// generation and in state (D7).
type Conflict struct {
	Row       RowID     `json:"row"`
	Key       string    `json:"key"`
	Winner    ids.Alias `json:"winner"`
	Loser     ids.Alias `json:"loser"`
	LoserHash string    `json:"loser_hash"`
	Reason    string    `json:"reason"`
}

// Contested marks a row whose value after launch differed from what
// paritydesk wrote (P12). A later harvest counts the row as changed on
// that replica only if its value differs from both hashes.
type Contested struct {
	Row          RowID   `json:"row"`
	Replica      Replica `json:"replica"`
	ServerHash   string  `json:"server_hash"`
	IntendedHash string  `json:"intended_hash"`
}

// Marker is stored in the folder's xattr (dev.paritydesk.marker) and ties a
// folder to the bases recorded for it (D4).
type Marker struct {
	Version    int       `json:"v"`
	Alias      ids.Alias `json:"alias"`
	Replica    Replica   `json:"replica"`
	Generation uint64    `json:"gen"`
	BaseHash   string    `json:"base"`
}
