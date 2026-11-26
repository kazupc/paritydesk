package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
)

func TestV02Defaults(t *testing.T) {
	d := Default()
	if d.App.BusyStaleAfter.Duration != 6*time.Hour || d.App.OpenFileScan != "auto" {
		t.Errorf("app: %+v", d.App)
	}
	if !d.Sync.Pins || !d.Sync.Artifacts || !d.Sync.CollapsedGroups {
		t.Errorf("sync: %+v", d.Sync)
	}
	wantOnline := Online{Reconcile: "prompt", CaptureMaxAge: Duration{10 * time.Minute}, QueueMaxAge: Duration{336 * time.Hour},
		LeaseTTL: Duration{20 * time.Minute}, Guard: "armed"}
	if d.Online != wantOnline {
		t.Errorf("online: %+v", d.Online)
	}
	if d.Artifacts != (Artifacts{Verify: "first", HistoryKeep: 10}) {
		t.Errorf("artifacts: %+v", d.Artifacts)
	}
	s := d.Scratch
	if !s.Enabled || s.MaxFilesPerWorkspace != 20000 || s.MaxWorkspaceBytes != "2GiB" || s.MaxRunBytes != "8GiB" ||
		s.MaxFilesPerRun != 50000 || !s.PropagateDeletions || s.CrossManaged || len(s.Exclude) != 0 ||
		s.MaxDeleteFraction != 0.5 || s.MaxDeleteMin != 50 {
		t.Errorf("scratch: %+v", s)
	}
	want := []string{"target", ".next", "dist", "build", "__pycache__", ".pytest_cache", ".mypy_cache", ".gradle", ".cache", ".turbo", ".parcel-cache"}
	if !reflect.DeepEqual(s.CacheDirs, want) {
		t.Errorf("cache_dirs %v", s.CacheDirs)
	}
	// The default list is a copy: editing one config never changes another.
	d.Scratch.CacheDirs[0] = "x"
	if Default().Scratch.CacheDirs[0] != "target" {
		t.Error("default cache_dirs shared between configs")
	}
}

func TestV02Validate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(c *Config)
		want   string // exact message part; "" means valid
	}{
		{"busy_stale_after low", func(c *Config) { c.App.BusyStaleAfter.Duration = 9 * time.Minute }, "app.busy_stale_after: must be between 10m0s and 48h0m0s"},
		{"busy_stale_after high", func(c *Config) { c.App.BusyStaleAfter.Duration = 49 * time.Hour }, "app.busy_stale_after: must be between 10m0s and 48h0m0s"},
		{"busy_stale_after min ok", func(c *Config) { c.App.BusyStaleAfter.Duration = MinBusyStaleAfter }, ""},
		{"busy_stale_after max ok", func(c *Config) { c.App.BusyStaleAfter.Duration = MaxBusyStaleAfter }, ""},
		{"open_file_scan bad", func(c *Config) { c.App.OpenFileScan = "proc" }, "app.open_file_scan: must be auto, native or lsof"},
		{"open_file_scan empty", func(c *Config) { c.App.OpenFileScan = "" }, "app.open_file_scan: must be auto, native or lsof"},
		{"open_file_scan native ok", func(c *Config) { c.App.OpenFileScan = "native" }, ""},
		{"open_file_scan lsof ok", func(c *Config) { c.App.OpenFileScan = "lsof" }, ""},
		{"reconcile bad", func(c *Config) { c.Online.Reconcile = "sometimes" }, "online.reconcile: must be prompt, auto or off"},
		{"reconcile auto ok", func(c *Config) { c.Online.Reconcile = "auto" }, ""},
		{"reconcile off ok", func(c *Config) { c.Online.Reconcile = "off" }, ""},
		{"capture_max_age low", func(c *Config) { c.Online.CaptureMaxAge.Duration = 59 * time.Second }, "online.capture_max_age: must be between 1m0s and 1h0m0s"},
		{"capture_max_age high", func(c *Config) { c.Online.CaptureMaxAge.Duration = time.Hour + time.Second }, "online.capture_max_age"},
		{"capture_max_age bounds ok", func(c *Config) { c.Online.CaptureMaxAge.Duration = time.Hour }, ""},
		{"capture_max_age lower bound ok", func(c *Config) { c.Online.CaptureMaxAge.Duration = time.Minute }, ""},
		{"queue_max_age low", func(c *Config) { c.Online.QueueMaxAge.Duration = 59 * time.Minute }, "online.queue_max_age: must be between 1h0m0s and 2160h0m0s"},
		{"queue_max_age high", func(c *Config) { c.Online.QueueMaxAge.Duration = 2161 * time.Hour }, "online.queue_max_age"},
		{"queue_max_age bounds ok", func(c *Config) { c.Online.QueueMaxAge.Duration = 2160 * time.Hour }, ""},
		{"queue_max_age lower bound ok", func(c *Config) { c.Online.QueueMaxAge.Duration = time.Hour }, ""},
		{"lease_ttl low", func(c *Config) { c.Online.LeaseTTL.Duration = 4 * time.Minute }, "online.lease_ttl: must be between 5m0s and 2h0m0s"},
		{"lease_ttl high", func(c *Config) { c.Online.LeaseTTL.Duration = 3 * time.Hour }, "online.lease_ttl"},
		{"lease_ttl bounds ok", func(c *Config) { c.Online.LeaseTTL.Duration = 5 * time.Minute }, ""},
		{"lease_ttl upper bound ok", func(c *Config) { c.Online.LeaseTTL.Duration = 2 * time.Hour }, ""},
		{"guard bad", func(c *Config) { c.Online.Guard = "never" }, "online.guard: must be armed or always"},
		{"guard always ok", func(c *Config) { c.Online.Guard = "always" }, ""},
		{"verify bad", func(c *Config) { c.Artifacts.Verify = "none" }, "artifacts.verify: must be first or all"},
		{"verify all ok", func(c *Config) { c.Artifacts.Verify = "all" }, ""},
		{"history_keep zero", func(c *Config) { c.Artifacts.HistoryKeep = 0 }, "artifacts.history_keep: must be between 1 and 100"},
		{"history_keep high", func(c *Config) { c.Artifacts.HistoryKeep = 101 }, "artifacts.history_keep"},
		{"history_keep bounds ok", func(c *Config) { c.Artifacts.HistoryKeep = 100 }, ""},
		{"history_keep lower bound ok", func(c *Config) { c.Artifacts.HistoryKeep = 1 }, ""},
		{"files per workspace zero", func(c *Config) { c.Scratch.MaxFilesPerWorkspace = 0 }, "scratch.max_files_per_workspace: must be between 1 and 1000000"},
		{"files per run high", func(c *Config) { c.Scratch.MaxFilesPerRun = 1_000_001 }, "scratch.max_files_per_run: must be between 1 and 1000000"},
		{"files bounds ok", func(c *Config) { c.Scratch.MaxFilesPerWorkspace, c.Scratch.MaxFilesPerRun = 1, 1_000_000 }, ""},
		{"workspace bytes not a size", func(c *Config) { c.Scratch.MaxWorkspaceBytes = "big" }, "scratch.max_workspace_bytes: not a size"},
		{"workspace bytes empty", func(c *Config) { c.Scratch.MaxWorkspaceBytes = "" }, "scratch.max_workspace_bytes: must be between 1MiB and 1TiB"},
		{"workspace bytes too small", func(c *Config) { c.Scratch.MaxWorkspaceBytes = "1023KiB" }, "scratch.max_workspace_bytes: must be between 1MiB and 1TiB"},
		{"run bytes too large", func(c *Config) { c.Scratch.MaxRunBytes = "2TiB" }, "scratch.max_run_bytes: must be between 1MiB and 1TiB"},
		{"workspace above run", func(c *Config) { c.Scratch.MaxWorkspaceBytes, c.Scratch.MaxRunBytes = "4GiB", "2GiB" }, "scratch.max_workspace_bytes: must not be larger than scratch.max_run_bytes"},
		{"sizes bounds ok", func(c *Config) { c.Scratch.MaxWorkspaceBytes, c.Scratch.MaxRunBytes = "1MiB", "1TiB" }, ""},
		{"fraction zero", func(c *Config) { c.Scratch.MaxDeleteFraction = 0 }, "scratch.max_delete_fraction"},
		{"fraction one ok", func(c *Config) { c.Scratch.MaxDeleteFraction = 1 }, ""},
		{"fraction above one", func(c *Config) { c.Scratch.MaxDeleteFraction = 1.01 }, "scratch.max_delete_fraction"},
		{"delete min negative", func(c *Config) { c.Scratch.MaxDeleteMin = -1 }, "scratch.max_delete_min"},
		{"delete min zero ok", func(c *Config) { c.Scratch.MaxDeleteMin = 0 }, ""},
		{"exclude empty", func(c *Config) { c.Scratch.Exclude = []string{""} }, "scratch.exclude[0]"},
		{"exclude absolute", func(c *Config) { c.Scratch.Exclude = []string{"/etc"} }, "scratch.exclude[0]"},
		{"exclude parent", func(c *Config) { c.Scratch.Exclude = []string{"a/../b"} }, "scratch.exclude[0]"},
		{"exclude backslash", func(c *Config) { c.Scratch.Exclude = []string{`a\b`} }, "scratch.exclude[0]"},
		{"exclude bad glob", func(c *Config) { c.Scratch.Exclude = []string{"a/["} }, "scratch.exclude[0]"},
		{"exclude ok", func(c *Config) { c.Scratch.Exclude = []string{"**/*.log", "data/big.bin"} }, ""},
		{"cache dir with slash", func(c *Config) { c.Scratch.CacheDirs = []string{"a/b"} }, "scratch.cache_dirs[0]: must be one folder name"},
		{"cache dir dotdot", func(c *Config) { c.Scratch.CacheDirs = []string{".."} }, "scratch.cache_dirs[0]"},
		{"cache dirs none ok", func(c *Config) { c.Scratch.CacheDirs = []string{} }, ""},
		{"export unknown v02 category", func(c *Config) { c.Sync.Export = map[string][]string{"work": {"artifact"}} }, "not a category"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := valid()
			tc.mutate(c)
			err := c.Validate(testReserved, testTags)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v, want %q", err, tc.want)
			}
			if pderr.CodeOf(err) != pderr.Usage {
				t.Fatalf("code %d", pderr.CodeOf(err))
			}
		})
	}
}

func TestV02RowEnabled(t *testing.T) {
	c := Default()
	for _, r := range []model.RowID{model.RowScratch, model.RowSidebarCollapse, model.RowSidebarSeed, model.RowPins} {
		if !c.RowEnabled(r) {
			t.Errorf("%s disabled by default", r)
		}
	}
	tests := []struct {
		name   string
		mutate func(c *Config)
		row    model.RowID
	}{
		{"scratch off", func(c *Config) { c.Scratch.Enabled = false }, model.RowScratch},
		{"scratch follows sessions", func(c *Config) { c.Sync.Sessions = false }, model.RowScratch},
		{"collapsed groups off", func(c *Config) { c.Sync.CollapsedGroups = false }, model.RowSidebarCollapse},
		{"seed follows sidebar view", func(c *Config) { c.Sync.SidebarView = false }, model.RowSidebarSeed},
		{"pins off", func(c *Config) { c.Sync.Pins = false }, model.RowPins},
	}
	for _, tc := range tests {
		c := Default()
		tc.mutate(c)
		if c.RowEnabled(tc.row) {
			t.Errorf("%s: %s still enabled", tc.name, tc.row)
		}
	}
	c = Default()
	c.Sync.CollapsedGroups = false
	if !c.RowEnabled(model.RowSidebarView) || !c.RowEnabled(model.RowSidebarSeed) {
		t.Error("collapsed_groups turned off another sidebar row")
	}
}

func TestCategoryListed(t *testing.T) {
	m := map[string][]string{
		"work":  {"sessions"},
		"home":  {"scratch"},
		"all":   {"all"},
		"star":  {"*"},
		"none":  {},
		"pins":  {"pins"},
		"arts":  {"artifacts"},
		"mixed": {"environments", "sessions"},
	}
	tests := []struct {
		alias string
		cat   model.Category
		want  bool
	}{
		{"absent", model.CatScratch, true},
		{"absent", model.CatArtifacts, true},
		{"work", model.CatSessions, true},
		{"work", model.CatScratch, true}, // implied by sessions
		{"work", model.CatPins, false},
		{"home", model.CatScratch, true},
		{"home", model.CatSessions, false}, // scratch does not imply sessions
		{"all", model.CatArtifacts, true},
		{"star", model.CatMCP, true},
		{"none", model.CatSessions, false},
		{"none", model.CatScratch, false},
		{"pins", model.CatScratch, false},
		{"arts", model.CatArtifacts, true},
		{"arts", model.CatSessions, false},
		{"mixed", model.CatScratch, true},
	}
	for _, tc := range tests {
		if got := CategoryListed(m, ids.Alias(tc.alias), tc.cat); got != tc.want {
			t.Errorf("CategoryListed(%s, %s) = %v", tc.alias, tc.cat, got)
		}
	}
}

func TestLoadV02Keys(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "v02.toml")
	body := `version = 1
[app]
busy_stale_after = "2h"
open_file_scan = "lsof"
[sync]
pins = false
artifacts = true
collapsed_groups = false
[online]
reconcile = "auto"
capture_max_age = "5m"
queue_max_age = "48h"
lease_ttl = "30m"
guard = "always"
create_sections = true
[artifacts]
verify = "all"
history_keep = 3
cross_managed = true
[scratch]
enabled = false
max_files_per_workspace = 100
max_workspace_bytes = "1GiB"
max_run_bytes = "4GiB"
max_files_per_run = 200
propagate_deletions = false
cross_managed = true
exclude = ["**/*.tmp"]
cache_dirs = []
max_delete_fraction = 0.25
max_delete_min = 5
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(nil, testTags); err != nil {
		t.Fatal(err)
	}
	if c.App.BusyStaleAfter.Duration != 2*time.Hour || c.App.OpenFileScan != "lsof" {
		t.Errorf("app %+v", c.App)
	}
	if c.Sync.Pins || !c.Sync.Artifacts || c.Sync.CollapsedGroups {
		t.Errorf("sync %+v", c.Sync)
	}
	if c.Online != (Online{Reconcile: "auto", CaptureMaxAge: Duration{5 * time.Minute}, QueueMaxAge: Duration{48 * time.Hour},
		LeaseTTL: Duration{30 * time.Minute}, Guard: "always", CreateSections: true}) {
		t.Errorf("online %+v", c.Online)
	}
	if c.Artifacts != (Artifacts{Verify: "all", HistoryKeep: 3, CrossManaged: true}) {
		t.Errorf("artifacts %+v", c.Artifacts)
	}
	s := c.Scratch
	if s.Enabled || s.MaxFilesPerWorkspace != 100 || s.MaxWorkspaceBytes != "1GiB" || s.MaxRunBytes != "4GiB" ||
		s.MaxFilesPerRun != 200 || s.PropagateDeletions || !s.CrossManaged || !reflect.DeepEqual(s.Exclude, []string{"**/*.tmp"}) ||
		s.MaxDeleteFraction != 0.25 || s.MaxDeleteMin != 5 {
		t.Errorf("scratch %+v", s)
	}
	// An empty list in the file means "none", not the defaults.
	if s.CacheDirs == nil || len(s.CacheDirs) != 0 {
		t.Errorf("cache_dirs %#v", s.CacheDirs)
	}
	for _, bad := range []string{"[online]\nsurprise = 1\n", "[artifacts]\nsurprise = 1\n", "[scratch]\nsurprise = 1\n", "[app]\nopen_file_scan = 3\n"} {
		q := filepath.Join(dir, "bad.toml")
		if err := os.WriteFile(q, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(q); err == nil || pderr.CodeOf(err) != pderr.Usage {
			t.Errorf("%q accepted: %v", bad, err)
		}
	}
}
