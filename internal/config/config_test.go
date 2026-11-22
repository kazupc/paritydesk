package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/pderr"
)

var testTags = map[string]string{
	"sidebarMode":       "ui_state",
	"keepAwakeEnabled":  "other",
	"secretKnob":        "security",
	"internalCounter":   "internal_state",
	"anotherUIToggle":   "ui_state",
	"unusedButAllowedX": "other",
}

var testReserved = []string{"Claude-otherTool"}

func valid() *Config {
	c := Default()
	c.Profiles = []Profile{{Alias: "work"}, {Alias: "home", Label: "Home account"}}
	return c
}

func TestDefaultIsValid(t *testing.T) {
	if err := valid().Validate(testReserved, testTags); err != nil {
		t.Fatal(err)
	}
	if err := Default().Validate(nil, testTags); err != nil {
		t.Fatal(err)
	}
	d := Default()
	if !d.Sync.SidebarView || d.App.LateQuitWatch.Duration != 10*time.Minute {
		t.Fatalf("defaults changed: %+v", d)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(c *Config)
		want   string // substring of the error; "" means valid
	}{
		{"version", func(c *Config) { c.Version = 2 }, "version"},
		{"bad alias", func(c *Config) { c.Profiles[0].Alias = "Work" }, "profile[0].alias"},
		{"alias starting with dash", func(c *Config) { c.Profiles[0].Alias = "-x" }, "profile[0].alias"},
		{"alias too long", func(c *Config) { c.Profiles[0].Alias = ids.Alias("a" + strings.Repeat("b", 32)) }, "profile[0].alias"},
		{"duplicate alias", func(c *Config) { c.Profiles[1].Alias = "work" }, "used twice"},
		{"folder pattern", func(c *Config) { c.Profiles[0].Folder = "Other" }, "profile[0].folder"},
		{"folder traversal", func(c *Config) { c.Profiles[0].Folder = "Claude-../x" }, "profile[0].folder"},
		{"folder is Claude", func(c *Config) { c.Profiles[0].Folder = "Claude" }, "must differ"},
		{"folder is claude lower", func(c *Config) { c.Profiles[0].Folder = "claude" }, "must differ"},
		{"built-in reserved", func(c *Config) { c.Profiles[0].Folder = "Claude-3p" }, "reserved"},
		{"manifest reserved", func(c *Config) { c.Profiles[0].Folder = "Claude-otherTool" }, "reserved"},
		{"manifest reserved other case", func(c *Config) { c.Profiles[0].Folder = "Claude-OTHERTOOL" }, "reserved"},
		{"case collision", func(c *Config) {
			c.Profiles[0].Folder = "Claude-Work"
			c.Profiles[1].Folder = "Claude-work"
		}, "used by another profile"},
		{"collision with a default folder", func(c *Config) { c.Profiles[1].Folder = "Claude-WORK" }, "used by another profile"},
		{"NFC collision", func(c *Config) {
			// U+00E9 against "e" + U+0301: the folder pattern refuses both
			// before normalization matters, so the collision rule is
			// exercised through FoldName directly below.
			c.Profiles[0].Folder = "Claude-caf\u00e9"
		}, "profile[0].folder"},
		{"label control", func(c *Config) { c.Profiles[0].Label = "a\x1b[31m" }, "label"},
		{"label too long", func(c *Config) { c.Profiles[0].Label = strings.Repeat("x", 65) }, "label"},
		{"tombstones enum", func(c *Config) { c.Sync.Tombstones = "oldest" }, "sync.tombstones"},
		{"delete-wins ok", func(c *Config) { c.Sync.Tombstones = "delete-wins" }, ""},
		{"secrets enum", func(c *Config) { c.Sync.Secrets = "always" }, "sync.secrets"},
		{"secrets never ok", func(c *Config) { c.Sync.Secrets = "never" }, ""},
		{"exclude bad glob", func(c *Config) { c.Sync.Exclude = []string{"["} }, "sync.exclude[0]"},
		{"exclude ok", func(c *Config) { c.Sync.Exclude = []string{"/tmp/*", "local_x"} }, ""},
		{"app_prefs unknown", func(c *Config) { c.Sync.AppPrefs = []string{"nope"} }, "unknown preference"},
		{"app_prefs security", func(c *Config) { c.Sync.AppPrefs = []string{"secretKnob"} }, `tagged "security"`},
		{"app_prefs internal_state", func(c *Config) { c.Sync.AppPrefs = []string{"internalCounter"} }, `tagged "internal_state"`},
		{"app_prefs duplicate", func(c *Config) { c.Sync.AppPrefs = []string{"sidebarMode", "sidebarMode"} }, "listed twice"},
		{"app_prefs other ok", func(c *Config) { c.Sync.AppPrefs = []string{"unusedButAllowedX", "anotherUIToggle"} }, ""},
		{"export unknown alias", func(c *Config) { c.Sync.Export = map[string][]string{"nobody": {"sessions"}} }, "sync.export"},
		{"export unknown category", func(c *Config) { c.Sync.Export = map[string][]string{"work": {"artifacts"}} }, "not a category"},
		{"import unknown alias", func(c *Config) { c.Sync.Import = map[string][]string{"x\x1b": {"sessions"}} }, "sync.import"},
		{"import ok", func(c *Config) { c.Sync.Import = map[string][]string{"home": {"sessions", "pins"}} }, ""},
		{"fraction zero", func(c *Config) { c.Limits.MaxDeleteFraction = 0 }, "max_delete_fraction"},
		{"fraction above one", func(c *Config) { c.Limits.MaxDeleteFraction = 1.5 }, "max_delete_fraction"},
		{"fraction one ok", func(c *Config) { c.Limits.MaxDeleteFraction = 1 }, ""},
		{"min negative", func(c *Config) { c.Limits.MaxDeleteMin = -1 }, "max_delete_min"},
		{"min zero ok", func(c *Config) { c.Limits.MaxDeleteMin = 0 }, ""},
		{"quit_timeout low", func(c *Config) { c.App.QuitTimeout.Duration = 4 * time.Second }, "quit_timeout"},
		{"quit_timeout high", func(c *Config) { c.App.QuitTimeout.Duration = 11 * time.Minute }, "quit_timeout"},
		{"quit_timeout bounds ok", func(c *Config) { c.App.QuitTimeout.Duration = 10 * time.Minute }, ""},
		{"late_quit_watch negative", func(c *Config) { c.App.LateQuitWatch.Duration = -time.Second }, "late_quit_watch"},
		{"late_quit_watch high", func(c *Config) { c.App.LateQuitWatch.Duration = time.Hour + time.Second }, "late_quit_watch"},
		{"late_quit_watch zero ok", func(c *Config) { c.App.LateQuitWatch.Duration = 0 }, ""},
		{"late_quit_watch one hour ok", func(c *Config) { c.App.LateQuitWatch.Duration = time.Hour }, ""},
		{"bundle id", func(c *Config) { c.App.BundleID = "-evil" }, "bundle_id"},
		{"data root relative", func(c *Config) { c.App.DataRoot = "Library" }, "data_root"},
		{"backups keep", func(c *Config) { c.Backups.Keep = 0 }, "backups.keep"},
		{"backups clones", func(c *Config) { c.Backups.LevelDBClonesKeep = -1 }, "leveldb_clones_keep"},
		{"backups size", func(c *Config) { c.Backups.MaxSize = "lots" }, "max_size"},
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
			if strings.ContainsRune(err.Error(), 0x1b) {
				t.Fatalf("control character echoed: %q", err)
			}
		})
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	c := valid()
	c.Version = 0
	c.Sync.Secrets = ""
	c.Limits.MaxDeleteMin = -2
	err := c.Validate(nil, testTags)
	for _, want := range []string{"version", "sync.secrets", "max_delete_min"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
}

func TestFoldNameCollisions(t *testing.T) {
	// The NFC and case rule that Validate relies on for uniqueness.
	if ids.FoldName("Claude-Caf\u00e9") != ids.FoldName("Claude-cafe\u0301") {
		t.Fatal("NFC forms must collide")
	}
	if ids.FoldName("Claude-A") != ids.FoldName("Claude-a") || ids.FoldName("Claude-a") == ids.FoldName("Claude-b") {
		t.Fatal("case folding")
	}
}

func TestParseSize(t *testing.T) {
	tests := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"", 0, true},
		{"2GiB", 2 << 30, true},
		{"512MiB", 512 << 20, true},
		{"1GB", 1e9, true},
		{"100", 100, true},
		{"10B", 10, true},
		{"-1GiB", 0, false},
		{"GiB", 0, false},
		{"1.5GiB", 0, false},
		{"99999999999TiB", 0, false},
	}
	for _, tc := range tests {
		got, err := ParseSize(tc.in)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("ParseSize(%q) = %d, %v", tc.in, got, err)
		}
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Run("missing", func(t *testing.T) {
		if _, err := Load(filepath.Join(dir, "none.toml")); !errors.Is(err, ErrNoConfig) {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("overrides defaults", func(t *testing.T) {
		c, err := Load(write("a.toml", "version = 1\n[app]\nquit_timeout = \"90s\"\n[[profile]]\nalias = \"work\"\n"))
		if err != nil {
			t.Fatal(err)
		}
		if c.App.QuitTimeout.Duration != 90*time.Second || c.App.LateQuitWatch.Duration != 10*time.Minute || len(c.Profiles) != 1 {
			t.Fatalf("got %+v", c)
		}
	})
	t.Run("unknown top-level key", func(t *testing.T) {
		_, err := Load(write("b.toml", "version = 1\nsurprise = true\n"))
		if err == nil || pderr.CodeOf(err) != pderr.Usage {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("unknown nested key", func(t *testing.T) {
		if _, err := Load(write("c.toml", "[sync]\nartifacts = true\n")); err == nil {
			t.Fatal("unknown key accepted")
		}
	})
	t.Run("bad duration", func(t *testing.T) {
		if _, err := Load(write("d.toml", "[app]\nquit_timeout = \"soon\"\n")); err == nil {
			t.Fatal("bad duration accepted")
		}
	})
}

func TestSaveRoundTripAndMode(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new", "paritydesk")
	p := filepath.Join(dir, "config.toml")
	c := valid()
	c.Sync.Export = map[string][]string{"work": {"sessions"}}
	c.Sync.Import = map[string][]string{"home": {"pins"}}
	c.Sync.Exclude = []string{"/tmp/*"}
	c.Sync.SecretsAllow = []string{"preset/VAR"}
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v", fi.Mode().Perm())
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", di.Mode().Perm())
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, c) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, c)
	}
	// Overwrite keeps 0600 and leaves no temporary files behind.
	if err := c.Save(p); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
}
