package config

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
)

// Enum values.
const (
	TombstonesNewerWins  = "newer-wins"
	TombstonesDeleteWins = "delete-wins"
	SecretsSameBoundary  = "same-boundary"
	SecretsNever         = "never"
)

// Preference tags that sync.app_prefs may list (compat.TagUIState and
// compat.TagOther; config cannot import compat, which imports config's
// importers).
const (
	prefTagUIState = "ui_state"
	prefTagOther   = "other"
)

// Ranges.
const (
	MinQuitTimeout   = 5 * time.Second
	MaxQuitTimeout   = 10 * time.Minute
	MaxLateQuitWatch = time.Hour
	MaxLabelRunes    = 64
)

var bundleIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,254}$`)

// Validate checks every config rule: version, alias and folder
// patterns, folder uniqueness (case-insensitive, NFC), reserved names
// (reservedFolders from the compat manifest), enum values, app_prefs keys
// against prefTags (compat.Manifest.PrefTags: only "ui_state" and "other"
// keys are allowed; unknown keys and keys tagged "security" or
// "internal_state" are rejected), export/import aliases and categories,
// limits ranges, quit_timeout 5s..10m and late_quit_watch 0..1h. Every
// problem is reported; the error is a *pderr.Error with code Usage.
func (c *Config) Validate(reservedFolders []string, prefTags map[string]string) error {
	v := &validator{}
	if c.Version != SchemaVersion {
		v.addf("version: must be %d", SchemaVersion)
	}
	v.app(c.App)
	aliases := v.profiles(c.Profiles, reservedFolders)
	v.sync(c.Sync, aliases, prefTags)
	v.limits(c.Limits)
	v.backups(c.Backups)
	if len(v.errs) == 0 {
		return nil
	}
	msgs := make([]string, len(v.errs))
	for i, err := range v.errs {
		msgs[i] = err.Error()
	}
	return &pderr.Error{Code: pderr.Usage, Op: "config", Msg: "invalid config: " + strings.Join(msgs, "; ")}
}

type validator struct{ errs []error }

func (v *validator) addf(format string, args ...any) {
	v.errs = append(v.errs, fmt.Errorf(format, args...))
}

func (v *validator) app(a App) {
	if !bundleIDRE.MatchString(a.BundleID) {
		v.addf("app.bundle_id: not a bundle id")
	}
	if a.DataRoot != "~" && !strings.HasPrefix(a.DataRoot, "~/") && !path.IsAbs(a.DataRoot) {
		v.addf("app.data_root: must be absolute or start with ~/")
	}
	if d := a.QuitTimeout.Duration; d < MinQuitTimeout || d > MaxQuitTimeout {
		v.addf("app.quit_timeout: must be between %s and %s", MinQuitTimeout, MaxQuitTimeout)
	}
	if d := a.LateQuitWatch.Duration; d < 0 || d > MaxLateQuitWatch {
		v.addf("app.late_quit_watch: must be between 0s and %s", MaxLateQuitWatch)
	}
}

// profiles checks aliases and folders and returns the valid aliases.
func (v *validator) profiles(ps []Profile, reserved []string) map[ids.Alias]bool {
	aliases := map[ids.Alias]bool{}
	folders := map[string]bool{}
	for i, p := range ps {
		if _, err := ids.ParseAlias(string(p.Alias)); err != nil {
			v.addf("profile[%d].alias: must match ^[a-z0-9][a-z0-9-]{0,31}$", i)
		} else if aliases[p.Alias] {
			v.addf("profile[%d].alias: %s is used twice", i, p.Alias)
		} else {
			aliases[p.Alias] = true
		}
		v.folder(i, p.ParkedFolder(), reserved, folders)
		if !validLabel(p.Label) {
			v.addf("profile[%d].label: at most %d printable characters", i, MaxLabelRunes)
		}
	}
	return aliases
}

func (v *validator) folder(i int, name string, reserved []string, seen map[string]bool) {
	folded := ids.FoldName(name)
	switch {
	case folded == ids.FoldName(ids.DefaultFolder):
		v.addf("profile[%d].folder: must differ from %s", i, ids.DefaultFolder)
	case ids.ValidateFolder(name, reserved...) != nil:
		v.addf("profile[%d].folder: must match ^Claude-[A-Za-z0-9._-]{1,48}$ and not be a reserved name", i)
	case seen[folded]:
		v.addf("profile[%d].folder: used by another profile (names compare without case)", i)
	default:
		seen[folded] = true
	}
}

func validLabel(s string) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > MaxLabelRunes {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool { return !unicode.IsPrint(r) })
}

func (v *validator) sync(s Sync, aliases map[ids.Alias]bool, prefTags map[string]string) {
	if s.Tombstones != TombstonesNewerWins && s.Tombstones != TombstonesDeleteWins {
		v.addf("sync.tombstones: must be %q or %q", TombstonesNewerWins, TombstonesDeleteWins)
	}
	if s.Secrets != SecretsSameBoundary && s.Secrets != SecretsNever {
		v.addf("sync.secrets: must be %q or %q", SecretsSameBoundary, SecretsNever)
	}
	for i, g := range s.Exclude {
		if _, err := path.Match(g, ""); g == "" || err != nil {
			v.addf("sync.exclude[%d]: not a valid pattern", i)
		}
	}
	seen := map[string]bool{}
	for i, k := range s.AppPrefs {
		switch tag, ok := prefTags[k]; {
		case !ok:
			v.addf("sync.app_prefs[%d]: unknown preference", i)
		case tag != prefTagUIState && tag != prefTagOther:
			v.addf("sync.app_prefs[%d]: preference tagged %q cannot be synced", i, tag)
		case seen[k]:
			v.addf("sync.app_prefs[%d]: listed twice", i)
		}
		seen[k] = true
	}
	v.boundary("sync.export", s.Export, aliases)
	v.boundary("sync.import", s.Import, aliases)
	for i, a := range s.SecretsAllow {
		if a == "" || strings.ContainsFunc(a, unicode.IsControl) {
			v.addf("sync.secrets_allow[%d]: empty or not printable", i)
		}
	}
}

// boundary checks an export or import table: keys are known aliases and
// values known categories.
func (v *validator) boundary(name string, m map[string][]string, aliases map[ids.Alias]bool) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if !aliases[ids.Alias(k)] {
			v.addf("%s: %q is not a profile alias", name, sanitizeKey(k))
			continue
		}
		for _, c := range m[k] {
			if !slices.Contains(model.AllCategories, model.Category(c)) {
				v.addf("%s.%s: %q is not a category", name, k, sanitizeKey(c))
			}
		}
	}
}

// sanitizeKey keeps a user-written key printable in an error message.
func sanitizeKey(s string) string {
	if len(s) > 40 {
		s = s[:40]
	}
	return strings.Map(func(r rune) rune {
		if r > unicode.MaxASCII || !unicode.IsPrint(r) {
			return '?'
		}
		return r
	}, s)
}

func (v *validator) limits(l Limits) {
	if !(l.MaxDeleteFraction > 0 && l.MaxDeleteFraction <= 1) {
		v.addf("limits.max_delete_fraction: must be greater than 0 and at most 1")
	}
	if l.MaxDeleteMin < 0 {
		v.addf("limits.max_delete_min: must not be negative")
	}
}

func (v *validator) backups(b Backups) {
	if b.Keep < 1 {
		v.addf("backups.keep: must be at least 1")
	}
	if b.LevelDBClonesKeep < 0 {
		v.addf("backups.leveldb_clones_keep: must not be negative")
	}
	if _, err := ParseSize(b.MaxSize); err != nil {
		v.addf("backups.max_size: %v", err)
	}
}

// ParseSize parses a byte size such as "512MiB", "2GiB", "1GB" or
// "1048576". Units are B, KB, MB, GB, TB (powers of 1000) and KiB, MiB,
// GiB, TiB (powers of 1024). The empty string means 0 (no limit).
func ParseSize(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	units := []struct {
		suffix string
		mult   int64
	}{
		{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}, {"TiB", 1 << 40},
		{"KB", 1e3}, {"MB", 1e6}, {"GB", 1e9}, {"TB", 1e12}, {"B", 1},
	}
	num, mult := s, int64(1)
	for _, u := range units {
		if n, ok := strings.CutSuffix(s, u.suffix); ok {
			num, mult = n, u.mult
			break
		}
	}
	n, err := strconv.ParseInt(strings.TrimSpace(num), 10, 64)
	if err != nil || n < 0 || n > (1<<62)/mult {
		return 0, errors.New("not a size such as 2GiB")
	}
	return n * mult, nil
}
