package compat

import (
	"context"
	"embed"
	"strings"

	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
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
	// PersistVersion is the expected persisted-store version, 0 if none.
	PersistVersion int            `toml:"persist_version"`
	Limits         map[string]int `toml:"limits"`
	// Fields lists record field names with special handling (D8, D10),
	// keyed by role: "per_field", "account_bound", "run_state", "max_time".
	Fields map[string][]string `toml:"fields"`
	Probes []string            `toml:"probes"`
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
// replaced by a validated session id before compiling.
type Signals struct {
	QuitVetoed         string `toml:"quit_vetoed"`
	TurnEnded          string `toml:"turn_ended"`
	FocusedSession     string `toml:"focused_session"`
	BridgeConnected    string `toml:"bridge_connected"`
	PairingDisabled    string `toml:"pairing_disabled"`
	UncleanExit        string `toml:"unclean_exit"`
	LoginRequired      string `toml:"login_required"`
	NativeMultiAccount string `toml:"native_multi_account"`
}

// Footprint identifies another account-switching tool (D28).
type Footprint struct {
	Tool           string   `toml:"tool"`
	FolderGlobs    []string `toml:"folder_globs"`     // under the data root
	PathsUnderHome []string `toml:"paths_under_home"` // e.g. config dirs
}

// Classification lists top-level data-folder entries by class.
type Classification struct {
	NeverTouched         []string `toml:"never_touched"`
	NeverUntilClassified []string `toml:"never_until_classified"`
	Cache                []string `toml:"cache"`
	Synced               []string `toml:"synced"`
	Other                []string `toml:"other"`
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
func Load(overrideDir string) ([]*Manifest, error) {
	_ = embedded
	return nil, pderr.ErrNotImplemented
}

// Select returns the manifest whose range contains version, and whether the
// version is listed as tested. Versions compare numerically per component.
func Select(ms []*Manifest, version string) (m *Manifest, tested bool, err error) {
	return nil, false, pderr.ErrNotImplemented
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
	return nil, pderr.ErrNotImplemented
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
	Probes        []ProbeResult
	SQLiteLS      bool // a SQLite Local Storage backend was found
}

// Decision is the outcome.
type Decision struct {
	Mode        Mode          `json:"mode"`
	SkippedRows []model.RowID `json:"skipped_rows,omitempty"`
	Reasons     []string      `json:"reasons,omitempty"`
}

// Decide maps the inputs to a mode (see docs/dev/implementation-plan.md).
func Decide(in DecideInput) Decision {
	return Decision{Mode: ModeRefuse, Reasons: []string{pderr.ErrNotImplemented.Error()}}
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
