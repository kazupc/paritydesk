package config

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/pelletier/go-toml/v2"

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
}

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
}

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
			BundleID:      DefaultBundleID,
			DataRoot:      "~/Library/Application Support",
			QuitTimeout:   Duration{60 * time.Second},
			LateQuitWatch: Duration{10 * time.Minute},
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
		},
		Limits:  Limits{MaxDeleteFraction: 0.10, MaxDeleteMin: 3},
		Backups: Backups{Keep: 10, LevelDBClonesKeep: 2, MaxSize: "2GiB"},
		Log:     Log{Redact: true},
	}
}

// ErrNoConfig is returned by Load when the file does not exist.
var ErrNoConfig = errors.New("config: no config file; run paritydesk init")

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
