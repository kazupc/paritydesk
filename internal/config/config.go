package config

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
)

// SchemaVersion is the config schema version this binary writes.
const SchemaVersion = 1

// DefaultBundleID is the desktop app's bundle id.
const DefaultBundleID = "com.anthropic.claudefordesktop"

// Duration is a TOML string such as "60s".
type Duration struct{ time.Duration }

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

// MarshalText implements encoding.TextMarshaler.
func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// Config is the parsed config file.
type Config struct {
	Version  int       `toml:"version" json:"version"`
	App      App       `toml:"app" json:"app"`
	Profiles []Profile `toml:"profile" json:"profile"`
	Sync     Sync      `toml:"sync" json:"sync"`
	Limits   Limits    `toml:"limits" json:"limits"`
	Backups  Backups   `toml:"backups" json:"backups"`
	Log      Log       `toml:"log" json:"log"`
	// Online configures the online reconcile through the app's tools
	// after a launch (pins and sidebar settings).
	Online Online `toml:"online" json:"online"`
	// Artifacts configures artifact sync (/paritydesk:artifacts, beta).
	Artifacts Artifacts `toml:"artifacts" json:"artifacts"`
	// Scratch configures the carry of scratch workspaces.
	Scratch Scratch `toml:"scratch" json:"scratch"`
	// UI holds presentation settings (the message language).
	UI UI `toml:"ui" json:"ui"`
}

// UI holds presentation settings.
type UI struct {
	// Lang is the user's explicit choice: "auto" (default: the locale
	// decides), "en" or "ja". paritydesk never writes it.
	Lang string `toml:"lang" json:"lang"`
	// FallbackLang applies only when LC_ALL, LC_MESSAGES and LANG are all
	// unset: "auto" (default: English), "en" or "ja". init writes "ja".
	FallbackLang string `toml:"fallback_lang" json:"fallback_lang"`
}

// Values of the [ui] keys.
const (
	LangAuto = "auto"
	LangJA   = "ja"
)

// App configures how the desktop app is found and controlled.
type App struct {
	BundleID    string   `toml:"bundle_id" json:"bundle_id"`
	DataRoot    string   `toml:"data_root" json:"data_root"` // "~" is expanded
	QuitTimeout Duration `toml:"quit_timeout" json:"quit_timeout"`
	// LateQuitWatch is how long a run keeps watching after a vetoed quit
	// timed out, so a late "Wait for Claude" quit is followed by a
	// relaunch of the leaving profile instead of leaving the app closed.
	LateQuitWatch Duration `toml:"late_quit_watch" json:"late_quit_watch"`
	RequireTested bool     `toml:"require_tested" json:"require_tested"`
	// BusyStaleAfter is how long a session with a turn start and no turn
	// end in main.log counts as working; after that it is reported as
	// probably stale.
	BusyStaleAfter Duration `toml:"busy_stale_after" json:"busy_stale_after"`
	// OpenFileScan picks how open files are found before anything is
	// written: "auto" (the native scan, lsof when it cannot run),
	// "native" or "lsof".
	OpenFileScan string `toml:"open_file_scan" json:"open_file_scan"`
}

// Profile is one account the user switches between.
type Profile struct {
	Alias        ids.Alias `toml:"alias" json:"alias"`
	Label        string    `toml:"label,omitempty" json:"label,omitempty"`
	Folder       string    `toml:"folder,omitempty" json:"folder,omitempty"` // default "Claude-<alias>"
	ManagedByOrg bool      `toml:"managed_by_org" json:"managed_by_org"`
}

// ParkedFolder returns the profile's parked folder name.
func (p Profile) ParkedFolder() string {
	if p.Folder != "" {
		return p.Folder
	}
	return ids.DefaultParkedFolder(p.Alias)
}

// Sync selects what is carried between accounts.
type Sync struct {
	Sessions     bool     `toml:"sessions" json:"sessions"`
	Tombstones   string   `toml:"tombstones" json:"tombstones"` // "newer-wins" | "delete-wins"
	Exclude      []string `toml:"exclude" json:"exclude"`       // project-path globs and session ids
	Environments bool     `toml:"environments" json:"environments"`
	GroupOrder   bool     `toml:"group_order" json:"group_order"`
	// SidebarView enables row 10 through the OFF + verify path (written to
	// Local Storage and the config mirror together, verified after launch,
	// drift marked contested), as ADR 0003 allows. On by default, as the
	// design specifies; the in-app (ON) path is v0.2.
	SidebarView  bool                `toml:"sidebar_view" json:"sidebar_view"`
	PinnedOrder  bool                `toml:"pinned_order" json:"pinned_order"`
	Worktrees    bool                `toml:"worktrees" json:"worktrees"`
	WindowState  bool                `toml:"window_state" json:"window_state"`
	MCPServers   bool                `toml:"mcp_servers" json:"mcp_servers"`
	AppPrefs     []string            `toml:"app_prefs" json:"app_prefs"`
	Export       map[string][]string `toml:"export" json:"export"`   // alias -> categories
	Import       map[string][]string `toml:"import" json:"import"`   // alias -> categories
	Secrets      string              `toml:"secrets" json:"secrets"` // "same-boundary" | "never"
	SecretsAllow []string            `toml:"secrets_allow" json:"secrets_allow"`
	// Pins carries which sessions are pinned (needs the plugin).
	Pins bool `toml:"pins" json:"pins"`
	// Artifacts carries artifacts (beta; needs the plugin and runs only
	// when /paritydesk:artifacts is used).
	Artifacts bool `toml:"artifacts" json:"artifacts"`
	// CollapsedGroups carries which sidebar groups and fork families are
	// collapsed (row 11, Local Storage only, verified after launch).
	CollapsedGroups bool `toml:"collapsed_groups" json:"collapsed_groups"`
}

// Online configures the online reconcile.
type Online struct {
	// Reconcile is "prompt" (ask in the next session), "auto" or "off"
	// (no capture, no pins row, no sidebar provider).
	Reconcile string `toml:"reconcile" json:"reconcile"`
	// CaptureMaxAge is how long a capture stays usable.
	CaptureMaxAge Duration `toml:"capture_max_age" json:"capture_max_age"`
	// QueueMaxAge drops a queue older than this (written in hours:
	// durations have no day unit).
	QueueMaxAge Duration `toml:"queue_max_age" json:"queue_max_age"`
	LeaseTTL    Duration `toml:"lease_ttl" json:"lease_ttl"`
	// Guard is "armed" (deny the floor tools while a queue is armed) or
	// "always" (deny them in every session).
	Guard string `toml:"guard" json:"guard"`
	// CreateSections creates missing manual sections by name.
	CreateSections bool `toml:"create_sections" json:"create_sections"`
}

// Artifacts configures artifact sync.
type Artifacts struct {
	// Verify is "first" (read back the first publish of every sync) or
	// "all".
	Verify string `toml:"verify" json:"verify"`
	// HistoryKeep is how many replaced versions are kept per artifact.
	HistoryKeep int `toml:"history_keep" json:"history_keep"`
	// CrossManaged carries artifacts between profiles where either is
	// managed_by_org.
	CrossManaged bool `toml:"cross_managed" json:"cross_managed"`
}

// Scratch configures the carry of scratch workspaces.
type Scratch struct {
	Enabled              bool     `toml:"enabled" json:"enabled"`
	MaxFilesPerWorkspace int      `toml:"max_files_per_workspace" json:"max_files_per_workspace"`
	MaxWorkspaceBytes    string   `toml:"max_workspace_bytes" json:"max_workspace_bytes"`
	MaxRunBytes          string   `toml:"max_run_bytes" json:"max_run_bytes"`
	MaxFilesPerRun       int      `toml:"max_files_per_run" json:"max_files_per_run"`
	PropagateDeletions   bool     `toml:"propagate_deletions" json:"propagate_deletions"`
	CrossManaged         bool     `toml:"cross_managed" json:"cross_managed"`
	Exclude              []string `toml:"exclude" json:"exclude"`
	// CacheDirs are folder names held back wherever they occur; an empty
	// list in the config file means none.
	CacheDirs         []string `toml:"cache_dirs" json:"cache_dirs"`
	MaxDeleteFraction float64  `toml:"max_delete_fraction" json:"max_delete_fraction"`
	MaxDeleteMin      int      `toml:"max_delete_min" json:"max_delete_min"`
}

// DefaultCacheDirs are the default scratch.cache_dirs.
var DefaultCacheDirs = []string{"target", ".next", "dist", "build", "__pycache__", ".pytest_cache",
	".mypy_cache", ".gradle", ".cache", ".turbo", ".parcel-cache"}

// Limits holds the deletion breaker settings.
type Limits struct {
	MaxDeleteFraction float64 `toml:"max_delete_fraction" json:"max_delete_fraction"`
	MaxDeleteMin      int     `toml:"max_delete_min" json:"max_delete_min"`
}

// Backups configures generation retention.
type Backups struct {
	Keep              int    `toml:"keep" json:"keep"`
	LevelDBClonesKeep int    `toml:"leveldb_clones_keep" json:"leveldb_clones_keep"`
	MaxSize           string `toml:"max_size" json:"max_size"` // e.g. "2GiB"
}

// Log configures logging.
type Log struct {
	Redact bool `toml:"redact" json:"redact"`
}

// Default returns the defaults of schema version 1.
func Default() *Config {
	return &Config{
		Version: SchemaVersion,
		App: App{
			BundleID:       DefaultBundleID,
			DataRoot:       "~/Library/Application Support",
			QuitTimeout:    Duration{60 * time.Second},
			LateQuitWatch:  Duration{10 * time.Minute},
			BusyStaleAfter: Duration{6 * time.Hour},
			OpenFileScan:   OpenFileScanAuto,
		},
		Sync: Sync{
			Sessions:     true,
			Tombstones:   "newer-wins",
			Environments: true,
			GroupOrder:   true,
			SidebarView:  true,
			PinnedOrder:  true,
			Worktrees:    true,
			WindowState:  true,
			MCPServers:   false,
			AppPrefs:     []string{"sidebarMode", "keepAwakeEnabled"},
			Secrets:      "same-boundary",
			Pins:         true,
			Artifacts:    true,
			// On by default once release checklist SO-1 (collapsed groups
			// survive a relaunch) has passed; the CHANGELOG says so
			// if a release ships it off.
			CollapsedGroups: true,
		},
		Limits:  Limits{MaxDeleteFraction: 0.10, MaxDeleteMin: 3},
		Backups: Backups{Keep: 10, LevelDBClonesKeep: 2, MaxSize: "2GiB"},
		Log:     Log{Redact: true},
		Online: Online{
			Reconcile:     OnlineReconcilePrompt,
			CaptureMaxAge: Duration{10 * time.Minute},
			QueueMaxAge:   Duration{336 * time.Hour},
			LeaseTTL:      Duration{20 * time.Minute},
			Guard:         OnlineGuardArmed,
		},
		Artifacts: Artifacts{Verify: ArtifactsVerifyFirst, HistoryKeep: 10},
		Scratch: Scratch{
			Enabled:              true,
			MaxFilesPerWorkspace: 20000,
			MaxWorkspaceBytes:    "2GiB",
			MaxRunBytes:          "8GiB",
			MaxFilesPerRun:       50000,
			PropagateDeletions:   true,
			Exclude:              []string{},
			CacheDirs:            append([]string(nil), DefaultCacheDirs...),
			MaxDeleteFraction:    0.5,
			MaxDeleteMin:         50,
		},
		UI: UI{Lang: LangAuto, FallbackLang: LangAuto},
	}
}

// ErrNoConfig is returned by Load when the file does not exist. It is one
// pointer, so == and errors.Is keep working.
var ErrNoConfig error = i18n.NewErr(i18n.ErrConfigNoConfig)

// maxUIRead bounds what ReadUI reads.
const maxUIRead = 1 << 20

// ReadUI reads the two [ui] keys of the config file at path, tolerantly:
// at most 1 MiB, unknown keys allowed, and "" for both on any error
// (missing, unreadable or bad TOML). It never writes and never fails, so
// the language can be chosen even when the rest of the config is invalid.
func ReadUI(path string) (lang, fallback string) {
	if path == "" {
		return "", ""
	}
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxUIRead))
	if err != nil {
		return "", ""
	}
	var doc struct {
		UI struct {
			Lang         string `toml:"lang"`
			FallbackLang string `toml:"fallback_lang"`
		} `toml:"ui"`
	}
	if err := toml.NewDecoder(bytes.NewReader(b)).Decode(&doc); err != nil {
		return "", ""
	}
	return doc.UI.Lang, doc.UI.FallbackLang
}

// Load reads path over the defaults. Unknown keys are an error. Load does
// not validate; call Validate.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoConfig
	}
	if err != nil {
		return nil, err
	}
	c := Default()
	dec := toml.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(c); err != nil {
		return nil, pderr.Wrap(pderr.Usage, "config", err)
	}
	return c, nil
}

// Profile returns the profile with the alias.
func (c *Config) Profile(a ids.Alias) (Profile, bool) {
	for _, p := range c.Profiles {
		if p.Alias == a {
			return p, true
		}
	}
	return Profile{}, false
}

// RowEnabled reports whether a row is enabled by the sync settings.
func (c *Config) RowEnabled(r model.RowID) bool {
	s := c.Sync
	switch r {
	case model.RowSessions, model.RowTasks, model.RowWaitingInput:
		return s.Sessions
	case model.RowEnvironments:
		return s.Environments
	case model.RowGroupOrder:
		return s.GroupOrder
	case model.RowSidebarView:
		return s.SidebarView
	case model.RowPinnedOrder:
		return s.PinnedOrder
	case model.RowAppPrefs:
		return len(s.AppPrefs) > 0
	case model.RowWindowState:
		return s.WindowState
	case model.RowMCPServers:
		return s.MCPServers
	case model.RowWorktrees:
		return s.Worktrees
	case model.RowScratch:
		return c.Scratch.Enabled && s.Sessions
	case model.RowSidebarCollapse:
		return s.CollapsedGroups
	case model.RowSidebarSeed:
		return s.SidebarView
	case model.RowPins:
		return s.Pins
	}
	return false
}

// CategoryListed reads sync.export / sync.import: an alias without an
// entry exports and imports everything; otherwise only the listed
// categories ("all" or "*" list everything). A list that names the
// category that implies cat (model.ImpliedBy) names cat too, so scratch
// follows sessions.
func CategoryListed(m map[string][]string, a ids.Alias, cat model.Category) bool {
	list, ok := m[string(a)]
	if !ok {
		return true
	}
	implied := model.ImpliedBy(cat)
	for _, c := range list {
		if c == string(cat) || c == "all" || c == "*" || (implied != "" && c == string(implied)) {
			return true
		}
	}
	return false
}

// Paths are paritydesk's own locations.
type Paths struct {
	ConfigFile string // ~/.config/paritydesk/config.toml or $XDG_CONFIG_HOME/...
	SupportDir string // ~/Library/Application Support/paritydesk (0700)
	StateDir   string // <SupportDir>/state
	GenDir     string // <SupportDir>/generations
	RunDir     string // <SupportDir>/run
	LogDir     string // ~/Library/Logs/paritydesk
}

// Overrides are command-line flag values; empty means unset.
type Overrides struct {
	Config, StateDir, AppSupportDir string
}

// ResolvePaths applies, in order: defaults, environment
// (PARITYDESK_CONFIG, PARITYDESK_STATE_DIR, XDG_CONFIG_HOME), flags.
func ResolvePaths(home string, getenv func(string) string, o Overrides) Paths {
	cfgDir := filepath.Join(home, ".config", "paritydesk")
	if x := getenv("XDG_CONFIG_HOME"); x != "" && filepath.IsAbs(x) {
		cfgDir = filepath.Join(x, "paritydesk")
	}
	p := Paths{
		ConfigFile: filepath.Join(cfgDir, "config.toml"),
		SupportDir: filepath.Join(home, "Library", "Application Support", "paritydesk"),
		LogDir:     filepath.Join(home, "Library", "Logs", "paritydesk"),
	}
	if v := getenv("PARITYDESK_CONFIG"); v != "" {
		p.ConfigFile = v
	}
	if o.Config != "" {
		p.ConfigFile = o.Config
	}
	if o.AppSupportDir != "" {
		p.SupportDir = o.AppSupportDir
	}
	p.StateDir = filepath.Join(p.SupportDir, "state")
	if v := getenv("PARITYDESK_STATE_DIR"); v != "" {
		p.StateDir = v
	}
	if o.StateDir != "" {
		p.StateDir = o.StateDir
	}
	p.GenDir = filepath.Join(p.SupportDir, "generations")
	p.RunDir = filepath.Join(p.SupportDir, "run")
	return p
}

// ExpandHome expands a leading "~/" with home.
func ExpandHome(home, p string) string {
	if p == "~" {
		return home
	}
	if len(p) >= 2 && p[:2] == "~/" {
		return filepath.Join(home, p[2:])
	}
	return p
}
