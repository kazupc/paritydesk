package compat

import (
	"context"
	"embed"
	"errors"
	"path"
	"sort"
	"strings"

	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/store/kv"
)

//go:embed manifests/*.toml
var embedded embed.FS

// WritePath is how a row reaches the entering replica.
type WritePath string

// Write paths.
const (
	PathOff       WritePath = "off"        // written while the app is stopped
	PathOffVerify WritePath = "off-verify" // OFF, then confirmed after launch (P12)
	PathOn        WritePath = "on"         // through the app's tools after launch (v0.2)
	PathCap       WritePath = "cap"        // captured through the app's tools (v0.2)
	PathNone      WritePath = "none"       // not carried
)

// RowSpec is one row's declaration. Templates may contain {acct} and {org}.
type RowSpec struct {
	Row       model.RowID `toml:"row"`
	WritePath WritePath   `toml:"write_path"`
	Contested bool        `toml:"contested"`
	// LSKeys are Local Storage item names.
	LSKeys []string `toml:"ls_keys"`
	// MirrorFile is the JSON file that mirrors LS values (usually
	// claude_desktop_config.json); MirrorPaths are literal paths in it.
	MirrorFile  string     `toml:"mirror_file"`
	MirrorPaths [][]string `toml:"mirror_paths"`
	// Files are paths relative to the data folder (or to the replica's
	// session directory when SessionScoped is true).
	Files         []string `toml:"files"`
	SessionScoped bool     `toml:"session_scoped"`
	// PerOrg marks rows whose data is scoped by organization (session
	// directories, per-scope sidebar keys). They are skipped (exit 32)
	// when the active org is unknown and the account has several orgs.
	PerOrg bool `toml:"per_org"`
	// PersistVersion is the expected persisted-store version, 0 if none.
	PersistVersion int            `toml:"persist_version"`
	Limits         map[string]int `toml:"limits"`
	// Fields lists record field names with special handling (D8, D10),
	// keyed by role: "per_field", "account_bound", "grant", "run_state",
	// "busy", "max_time", "parent" (fork and routine family links),
	// "worktree" and others (see ADR 0004). A role value may be a dotted
	// path into nested objects where the role says so. The orchestrator
	// copies them into rows.Policy.Fields; no other package hard-codes field
	// names.
	Fields map[string][]string `toml:"fields"`
	Probes []string            `toml:"probes"`
	// IDB is [row.idb] of row pins: where the value is read in the app's
	// IndexedDB.
	IDB *IDBSpec `toml:"idb"`
	// Scratch is [row.scratch] of row scratch.
	Scratch *ScratchSpec `toml:"scratch"`
}

// Expand returns a copy with {acct} and {org} filled in.
func (s RowSpec) Expand(rep model.Replica) RowSpec {
	r := strings.NewReplacer("{acct}", string(rep.Account), "{org}", string(rep.Org))
	out := s
	out.LSKeys = mapStrings(s.LSKeys, r.Replace)
	out.Files = mapStrings(s.Files, r.Replace)
	out.MirrorPaths = make([][]string, len(s.MirrorPaths))
	for i, p := range s.MirrorPaths {
		out.MirrorPaths[i] = mapStrings(p, r.Replace)
	}
	return out
}

func mapStrings(in []string, f func(string) string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = f(s)
	}
	return out
}

// ProbeKind is what a probe checks.
type ProbeKind string

// Probe kinds.
const (
	ProbeFile           ProbeKind = "file"            // a file exists and is regular
	ProbeDir            ProbeKind = "dir"             // a directory exists
	ProbeJSONShape      ProbeKind = "json-shape"      // a JSON path exists with a JSON type
	ProbeLSKey          ProbeKind = "ls-key"          // a Local Storage key exists
	ProbeLSShape        ProbeKind = "ls-shape"        // an LS value's JSON path has a type
	ProbePersistVersion ProbeKind = "persist-version" // a persisted store's version matches
	ProbeIdxVersion     ProbeKind = "idx-version"     // archived-sessions.idx has v == 1
	ProbeJSONVersion    ProbeKind = "json-version"    // a file's numeric member at path equals version
	ProbeDirOptional    ProbeKind = "dir-optional"    // absent passes; present must be a real directory (Lstat)
)

// Probe is one shape check.
type Probe struct {
	ID       string      `toml:"id"`
	Row      model.RowID `toml:"row"` // empty: identity/session probe
	Kind     ProbeKind   `toml:"kind"`
	Target   string      `toml:"target"` // file template or LS key template
	Path     []string    `toml:"path"`   // literal JSON path
	Type     string      `toml:"type"`   // "object" | "array" | "string" | "number" | "bool"
	Version  int         `toml:"version"`
	Critical bool        `toml:"critical"` // failure gives swap-only
}

// Signals are main.log line patterns (Go regexp syntax). {session} is
// replaced by regexp.QuoteMeta of a validated session id before compiling.
// An empty pattern means the signal is unknown for this version; callers
// then fall back (for example, busy detection uses session records and the
// app's own quit veto only).
type Signals struct {
	QuitVetoed  string `toml:"quit_vetoed"`
	TurnStarted string `toml:"turn_started"` // with {session}, or a named group (?P<session>...)
	TurnEnded   string `toml:"turn_ended"`   // with {session}, or a named group (?P<session>...)
	// AppStarted marks a fresh app start; busy scans begin after the last
	// one.
	AppStarted      string `toml:"app_started"`
	FocusedSession  string `toml:"focused_session"`
	BridgeConnected string `toml:"bridge_connected"`
	PairingDisabled string `toml:"pairing_disabled"`
	UncleanExit     string `toml:"unclean_exit"`
	LoginRequired   string `toml:"login_required"`
	// ActiveOrg has a named group (?P<org>...) holding the active
	// organization's UUID; the value is validated with ids.ParseOrg.
	ActiveOrg          string `toml:"active_org"`
	NativeMultiAccount string `toml:"native_multi_account"`
}

// Footprint identifies another account-switching tool (D28).
type Footprint struct {
	Tool           string   `toml:"tool"`
	FolderGlobs    []string `toml:"folder_globs"`     // under the data root
	PathsUnderHome []string `toml:"paths_under_home"` // e.g. config dirs
}

// Classification lists top-level data-folder entries by class. Entries
// that no list names are treated as never touched: plan validation allows
// file writes only below Synced (see ADR 0004).
type Classification struct {
	NeverTouched         []string `toml:"never_touched"`
	NeverUntilClassified []string `toml:"never_until_classified"`
	Cache                []string `toml:"cache"`
	// Synced globs may hold {acct} and {org}; they are expanded for the
	// entering replica before plan validation, so writes stay inside that
	// replica's own session directory.
	Synced []string `toml:"synced"`
	Other  []string `toml:"other"`
	// LSNeverTouched are Local Storage item name globs (path.Match syntax,
	// case-insensitive) that paritydesk never writes, on top of the fixed
	// fragments in package denylist. An override may add entries but never
	// remove one.
	LSNeverTouched []string `toml:"ls_never_touched"`
}

// Manifest is one compat/<range>.toml.
type Manifest struct {
	Name            string         `toml:"name"`
	MinVersion      string         `toml:"min_version"`
	MaxVersion      string         `toml:"max_version"`
	Tested          []string       `toml:"tested"`
	KnownBad        []string       `toml:"known_bad"`
	LSBackend       string         `toml:"ls_backend"` // "leveldb"
	Compressions    []int          `toml:"compressions"`
	Rows            []RowSpec      `toml:"row"`
	Probes          []Probe        `toml:"probe"`
	Classification  Classification `toml:"classification"`
	Signals         Signals        `toml:"signals"`
	Footprints      []Footprint    `toml:"footprint"`
	ReservedFolders []string       `toml:"reserved_folders"`
	// TmpRecoveryGlobs are app file names the app promotes at startup;
	// paritydesk never creates files matching them (D30).
	TmpRecoveryGlobs []string `toml:"tmp_recovery_globs"`
	// PrefTags tags every known app preference key: "ui_state" and "other"
	// may be listed in sync.app_prefs; "security" and "internal_state" are
	// rejected by config.Validate. Keys absent from the map are rejected.
	PrefTags map[string]string `toml:"pref_tags"`
	// PermissionAcks maps a session permission mode to the app preference
	// names that acknowledge it for an account. A mode is carried to a
	// session record new to the target only when one of them is true in
	// the target folder, or when the target's own records already use the
	// mode (D8).
	PermissionAcks map[string][]string `toml:"permission_acks"`
	// Online is the online layer (pins and sidebar through the app's
	// tools); absent means off.
	Online OnlineSpec `toml:"online"`
	// Artifacts is artifact sync (beta); absent means off.
	Artifacts ArtifactsSpec `toml:"artifacts"`
	// Legacy lists the layouts profile adopt --from-legacy inspects.
	Legacy Legacy `toml:"legacy"`
}

// Preference tags.
const (
	TagUIState       = "ui_state"
	TagOther         = "other"
	TagSecurity      = "security"
	TagInternalState = "internal_state"
)

// SyncedFor returns the synced globs with {acct} and {org} filled in for
// rep. Plan validation allows file writes only below these globs.
func (m *Manifest) SyncedFor(rep model.Replica) []string {
	r := strings.NewReplacer("{acct}", string(rep.Account), "{org}", string(rep.Org))
	return mapStrings(m.Classification.Synced, r.Replace)
}

// Classified reports whether a top-level entry name of a data folder is
// named by any classification list (by the first segment of each glob,
// matched with path.Match or literally).
func (m *Manifest) Classified(name string) bool {
	for _, g := range m.allClassGlobs() {
		first, _, _ := strings.Cut(g, "/")
		if ok, _ := path.Match(first, name); ok || first == name {
			return true
		}
	}
	return false
}

// Row returns the spec of a row.
func (m *Manifest) Row(r model.RowID) (RowSpec, bool) {
	for _, s := range m.Rows {
		if s.Row == r {
			return s, true
		}
	}
	return RowSpec{}, false
}

// Load returns the embedded manifests, with files from overrideDir
// (paritydesk/compat.d) taking precedence by name. Unknown keys are errors.
// An override may add to classification.never_touched but never remove an
// entry of the embedded manifest of the same name; such an override is
// refused. (The fixed floor in package denylist applies regardless.)
func Load(overrideDir string) ([]*Manifest, error) {
	byName, order, err := loadEmbedded()
	if err != nil {
		return nil, err
	}
	if overrideDir != "" {
		if err := applyOverrides(overrideDir, byName, &order); err != nil {
			return nil, err
		}
	}
	out := make([]*Manifest, 0, len(order))
	for _, n := range order {
		out = append(out, byName[n])
	}
	sort.SliceStable(out, func(i, j int) bool {
		return compareVersions(out[i].MinVersion, out[j].MinVersion) < 0
	})
	return out, nil
}

// Select returns the manifest whose range contains version, and whether the
// version is listed as tested. Versions compare numerically per component.
func Select(ms []*Manifest, version string) (m *Manifest, tested bool, err error) {
	if _, err := parseVersion(version); err != nil {
		return nil, false, err
	}
	for _, c := range ms {
		if compareVersions(version, c.MinVersion) >= 0 && compareVersions(version, c.MaxVersion) <= 0 {
			for _, t := range c.Tested {
				if compareVersions(version, t) == 0 {
					return c, true, nil
				}
			}
			return c, false, nil
		}
	}
	return nil, false, nil
}

// ProbeEnv is what probes may look at for one folder.
type ProbeEnv struct {
	Folder  string
	Replica model.Replica
	LS      kv.Reader // a snapshot; may be nil if Local Storage is absent
}

// ProbeResult is one probe outcome. Detail never contains values.
type ProbeResult struct {
	ProbeID  string      `json:"probe"`
	Row      model.RowID `json:"row,omitempty"`
	OK       bool        `json:"ok"`
	Critical bool        `json:"critical"`
	Detail   string      `json:"detail,omitempty"`
}

// RunProbes runs every probe of m against env.
func RunProbes(ctx context.Context, m *Manifest, env ProbeEnv) ([]ProbeResult, error) {
	if m == nil {
		return nil, errors.New("compat: no manifest")
	}
	out := make([]ProbeResult, 0, len(m.Probes))
	for _, p := range m.Probes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ok, detail := runProbe(p, env)
		out = append(out, ProbeResult{ProbeID: p.ID, Row: p.Row, OK: ok, Critical: p.Critical, Detail: detail})
	}
	return out, nil
}

// Mode is the run mode decided from version and probes.
type Mode string

// Modes.
const (
	ModeFull        Mode = "full"
	ModeFullWarn    Mode = "full-warn"    // unknown version, probes pass
	ModePreviewOnly Mode = "preview-only" // unknown version with require_tested
	ModeDegraded    Mode = "degraded"     // some rows skipped (exit 32)
	ModeSwapOnly    Mode = "swap-only"    // exit 31
	ModeRefuse      Mode = "refuse"       // exit 30
)

// DecideInput gathers what Decide needs.
type DecideInput struct {
	Manifest      *Manifest // nil when no manifest covers the version
	Version       string
	Tested        bool
	RequireTested bool
	Downgraded    bool
	IdentityOK    bool // default folder identity readable
	// Probes include the critical idx-version probe (archived-sessions.idx
	// v == 1); its failure gives swap-only like any critical probe.
	Probes   []ProbeResult
	SQLiteLS bool // a SQLite Local Storage backend was found
	// CompressionOK is false when kv.Opener.CheckFormat found an unknown
	// block compression in the S or T Local Storage (swap-only, exit 31).
	CompressionOK bool
	// NativeMultiAccount is true when the app's own multi-account mode is
	// detected (folder account history, or the manifest signal): swap-only.
	NativeMultiAccount bool
	// OrgUnknown is true when the account has several org directories and
	// the active org could not be determined: rows with PerOrg are
	// skipped (degraded, exit 32).
	OrgUnknown bool
}

// Decision is the outcome.
type Decision struct {
	Mode        Mode          `json:"mode"`
	SkippedRows []model.RowID `json:"skipped_rows,omitempty"`
	Reasons     []string      `json:"reasons,omitempty"`
}

// Decide maps the inputs to a mode (see docs/dev/implementation-plan.md):
//
//	identity unreadable, or a downgrade          -> refuse
//	no manifest covers the version               -> swap-only
//	known-bad version                            -> swap-only
//	SQLite LS, unknown compression, native
//	multi-account, critical or identity probe
//	failed                                       -> swap-only
//	row probes failed, or org unknown            -> degraded (rows skipped)
//	unknown version (probes pass)                -> full-warn, or
//	                                                preview-only with RequireTested
//	known version, probes pass                   -> full
func Decide(in DecideInput) Decision {
	switch {
	case !in.IdentityOK:
		return Decision{Mode: ModeRefuse, Reasons: []string{"default folder identity unreadable"}}
	case in.Downgraded:
		return Decision{Mode: ModeRefuse, Reasons: []string{"app version is older than the last one seen"}}
	}
	if r := swapOnlyReasons(in); len(r) > 0 {
		return Decision{Mode: ModeSwapOnly, Reasons: r}
	}
	d := Decision{Mode: ModeFull}
	if !in.Tested {
		d.Mode = ModeFullWarn
		d.Reasons = append(d.Reasons, "app version not tested")
		if in.RequireTested {
			d.Mode = ModePreviewOnly
		}
	}
	d.SkippedRows = skippedRows(in)
	if len(d.SkippedRows) > 0 {
		if d.Mode != ModePreviewOnly {
			d.Mode = ModeDegraded
		}
		d.Reasons = append(d.Reasons, "some rows skipped")
	}
	return d
}

func swapOnlyReasons(in DecideInput) []string {
	var r []string
	if in.Manifest == nil {
		return []string{"no manifest covers the app version"}
	}
	for _, v := range in.Manifest.KnownBad {
		if compareVersions(in.Version, v) == 0 {
			r = append(r, "app version is known bad")
		}
	}
	if in.SQLiteLS {
		r = append(r, "Local Storage uses an unsupported backend")
	}
	if !in.CompressionOK {
		r = append(r, "Local Storage uses an unknown block compression")
	}
	if in.NativeMultiAccount {
		r = append(r, "the app's own multi-account mode is in use")
	}
	for _, p := range in.Probes {
		if !p.OK && (p.Critical || p.Row == "") {
			r = append(r, "critical probe failed: "+p.ProbeID)
		}
	}
	return r
}

func skippedRows(in DecideInput) []model.RowID {
	skip := map[model.RowID]bool{}
	for _, p := range in.Probes {
		if !p.OK && p.Row != "" {
			skip[p.Row] = true
		}
	}
	if in.OrgUnknown {
		for _, s := range in.Manifest.Rows {
			if s.PerOrg {
				skip[s.Row] = true
			}
		}
	}
	var out []model.RowID
	for _, r := range model.AllRows {
		if skip[r] {
			out = append(out, r)
			delete(skip, r)
		}
	}
	rest := make([]string, 0, len(skip))
	for r := range skip {
		rest = append(rest, string(r))
	}
	sort.Strings(rest)
	for _, r := range rest {
		out = append(out, model.RowID(r))
	}
	return out
}

// Gate bundles the loaded manifests behind methods, so the orchestrator
// can depend on an interface.
type Gate struct{ Manifests []*Manifest }

// Manifest selects the manifest for version.
func (g Gate) Manifest(ctx context.Context, version string) (*Manifest, bool, error) {
	return Select(g.Manifests, version)
}

// Probes runs a manifest's probes.
func (g Gate) Probes(ctx context.Context, m *Manifest, env ProbeEnv) ([]ProbeResult, error) {
	return RunProbes(ctx, m, env)
}

// Decide maps inputs to a mode.
func (g Gate) Decide(in DecideInput) Decision { return Decide(in) }
