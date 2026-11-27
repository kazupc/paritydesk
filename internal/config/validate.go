package config

import (
	"errors"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/kazupc/paritydesk/internal/i18n"
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

	OpenFileScanAuto   = "auto"
	OpenFileScanNative = "native"
	OpenFileScanLsof   = "lsof"

	OnlineReconcilePrompt = "prompt"
	OnlineReconcileAuto   = "auto"
	OnlineReconcileOff    = "off"
	OnlineGuardArmed      = "armed"
	OnlineGuardAlways     = "always"

	ArtifactsVerifyFirst = "first"
	ArtifactsVerifyAll   = "all"
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

	MinBusyStaleAfter = 10 * time.Minute
	MaxBusyStaleAfter = 48 * time.Hour

	MinCaptureMaxAge = time.Minute
	MaxCaptureMaxAge = time.Hour
	MinQueueMaxAge   = time.Hour
	MaxQueueMaxAge   = 2160 * time.Hour // 90 days
	MinLeaseTTL      = 5 * time.Minute
	MaxLeaseTTL      = 2 * time.Hour

	MinHistoryKeep = 1
	MaxHistoryKeep = 100

	MinScratchFiles = 1
	MaxScratchFiles = 1_000_000
	MinScratchBytes = int64(1) << 20 // 1 MiB
	MaxScratchBytes = int64(1) << 40 // 1 TiB
)

var bundleIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,254}$`)

// Validate checks every config rule: version, alias and folder
// patterns, folder uniqueness (case-insensitive, NFC), reserved names
// (reservedFolders from the compat manifest), enum values, app_prefs keys
// against prefTags (compat.Manifest.PrefTags: only "ui_state" and "other"
// keys are allowed; unknown keys and keys tagged "security" or
// "internal_state" are rejected), export/import aliases and categories,
// limits ranges, quit_timeout 5s..10m and late_quit_watch 0..1h, and the
// v0.2 keys (app.busy_stale_after, app.open_file_scan, [online],
// [artifacts], [scratch]). Every problem is reported; the error is a
// *pderr.Error with code Usage.
func (c *Config) Validate(reservedFolders []string, prefTags map[string]string) error {
	v := &validator{}
	if c.Version != SchemaVersion {
		v.add(i18n.ConfigInvalidVersion, SchemaVersion)
	}
	v.app(c.App)
	aliases := v.profiles(c.Profiles, reservedFolders)
	v.sync(c.Sync, aliases, prefTags)
	v.limits(c.Limits)
	v.backups(c.Backups)
	v.online(c.Online)
	v.artifacts(c.Artifacts)
	v.scratch(c.Scratch)
	if len(v.errs) == 0 {
		return nil
	}
	return pderr.NewT(pderr.Usage, "config", i18n.ConfigInvalid, i18n.List(v.errs))
}

type validator struct{ errs []i18n.Text }

func (v *validator) add(id i18n.Msg, args ...any) {
	v.errs = append(v.errs, i18n.Tx(id, args...))
}

func (v *validator) app(a App) {
	if !bundleIDRE.MatchString(a.BundleID) {
		v.add(i18n.ConfigInvalidAppBundleID)
	}
	if a.DataRoot != "~" && !strings.HasPrefix(a.DataRoot, "~/") && !path.IsAbs(a.DataRoot) {
		v.add(i18n.ConfigInvalidAppDataRoot)
	}
	if d := a.QuitTimeout.Duration; d < MinQuitTimeout || d > MaxQuitTimeout {
		v.add(i18n.ConfigInvalidAppQuitTimeout, MinQuitTimeout, MaxQuitTimeout)
	}
	if d := a.LateQuitWatch.Duration; d < 0 || d > MaxLateQuitWatch {
		v.add(i18n.ConfigInvalidAppLateQuitWatch, MaxLateQuitWatch)
	}
	v.durationRange("app.busy_stale_after", a.BusyStaleAfter, MinBusyStaleAfter, MaxBusyStaleAfter)
	switch a.OpenFileScan {
	case OpenFileScanAuto, OpenFileScanNative, OpenFileScanLsof:
	default:
		v.add(i18n.ConfigInvalidAppOpenFileScan, OpenFileScanAuto, OpenFileScanNative, OpenFileScanLsof)
	}
}

// durationRange checks min <= d <= max.
func (v *validator) durationRange(key string, d Duration, min, max time.Duration) {
	if d.Duration < min || d.Duration > max {
		v.add(i18n.ConfigInvalidDurationRange, key, min, max)
	}
}

func (v *validator) online(o Online) {
	switch o.Reconcile {
	case OnlineReconcilePrompt, OnlineReconcileAuto, OnlineReconcileOff:
	default:
		v.add(i18n.ConfigInvalidOnlineReconcile, OnlineReconcilePrompt, OnlineReconcileAuto, OnlineReconcileOff)
	}
	v.durationRange("online.capture_max_age", o.CaptureMaxAge, MinCaptureMaxAge, MaxCaptureMaxAge)
	v.durationRange("online.queue_max_age", o.QueueMaxAge, MinQueueMaxAge, MaxQueueMaxAge)
	v.durationRange("online.lease_ttl", o.LeaseTTL, MinLeaseTTL, MaxLeaseTTL)
	if o.Guard != OnlineGuardArmed && o.Guard != OnlineGuardAlways {
		v.add(i18n.ConfigInvalidOnlineGuard, OnlineGuardArmed, OnlineGuardAlways)
	}
}

func (v *validator) artifacts(a Artifacts) {
	if a.Verify != ArtifactsVerifyFirst && a.Verify != ArtifactsVerifyAll {
		v.add(i18n.ConfigInvalidArtifactsVerify, ArtifactsVerifyFirst, ArtifactsVerifyAll)
	}
	if a.HistoryKeep < MinHistoryKeep || a.HistoryKeep > MaxHistoryKeep {
		v.add(i18n.ConfigInvalidArtifactsHistoryKeep, MinHistoryKeep, MaxHistoryKeep)
	}
}

func (v *validator) scratch(s Scratch) {
	for _, f := range []struct {
		key string
		n   int
	}{{"scratch.max_files_per_workspace", s.MaxFilesPerWorkspace}, {"scratch.max_files_per_run", s.MaxFilesPerRun}} {
		if f.n < MinScratchFiles || f.n > MaxScratchFiles {
			v.add(i18n.ConfigInvalidIntRange, f.key, MinScratchFiles, MaxScratchFiles)
		}
	}
	ws, wsOK := v.scratchSize("scratch.max_workspace_bytes", s.MaxWorkspaceBytes)
	run, runOK := v.scratchSize("scratch.max_run_bytes", s.MaxRunBytes)
	if wsOK && runOK && ws > run {
		v.add(i18n.ConfigInvalidScratchMaxWorkspaceBytes)
	}
	if !(s.MaxDeleteFraction > 0 && s.MaxDeleteFraction <= 1) {
		v.add(i18n.ConfigInvalidScratchMaxDeleteFraction)
	}
	if s.MaxDeleteMin < 0 {
		v.add(i18n.ConfigInvalidScratchMaxDeleteMin)
	}
	for i, g := range s.Exclude {
		if !validExcludeGlob(g) {
			v.add(i18n.ConfigInvalidScratchExclude, i)
		}
	}
	for i, d := range s.CacheDirs {
		if !singleSegment(d) {
			v.add(i18n.ConfigInvalidScratchCacheDirs, i)
		}
	}
}

// scratchSize parses a scratch size and checks 1 MiB..1 TiB.
func (v *validator) scratchSize(key, s string) (int64, bool) {
	n, err := ParseSize(s)
	if err != nil {
		v.add(i18n.ConfigInvalidSize, key, err)
		return 0, false
	}
	if n < MinScratchBytes || n > MaxScratchBytes {
		v.add(i18n.ConfigInvalidSizeRange, key)
		return 0, false
	}
	return n, true
}

// validExcludeGlob accepts a relative, slash-separated glob ("**" allowed)
// without empty, "." or ".." segments.
func validExcludeGlob(g string) bool {
	if g == "" || strings.HasPrefix(g, "/") || strings.ContainsAny(g, "\\\x00") || !utf8.ValidString(g) {
		return false
	}
	for _, seg := range strings.Split(g, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	_, err := path.Match(strings.ReplaceAll(g, "**", "*"), "")
	return err == nil
}

// singleSegment accepts one path segment: no separator, not "." or "..".
func singleSegment(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\\x00") && utf8.ValidString(s)
}

// profiles checks aliases and folders and returns the valid aliases.
func (v *validator) profiles(ps []Profile, reserved []string) map[ids.Alias]bool {
	aliases := map[ids.Alias]bool{}
	folders := map[string]bool{}
	for i, p := range ps {
		if _, err := ids.ParseAlias(string(p.Alias)); err != nil {
			v.add(i18n.ConfigInvalidProfileAlias, i)
		} else if aliases[p.Alias] {
			v.add(i18n.ConfigInvalidProfileAliasTwice, i, p.Alias)
		} else {
			aliases[p.Alias] = true
		}
		v.folder(i, p.ParkedFolder(), reserved, folders)
		if !validLabel(p.Label) {
			v.add(i18n.ConfigInvalidProfileLabel, i, MaxLabelRunes)
		}
	}
	return aliases
}

func (v *validator) folder(i int, name string, reserved []string, seen map[string]bool) {
	folded := ids.FoldName(name)
	switch {
	case folded == ids.FoldName(ids.DefaultFolder):
		v.add(i18n.ConfigInvalidProfileFolderDefault, i, ids.DefaultFolder)
	case ids.ValidateFolder(name, reserved...) != nil:
		v.add(i18n.ConfigInvalidProfileFolderPattern, i)
	case seen[folded]:
		v.add(i18n.ConfigInvalidProfileFolderUsed, i)
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
		v.add(i18n.ConfigInvalidSyncTombstones, TombstonesNewerWins, TombstonesDeleteWins)
	}
	if s.Secrets != SecretsSameBoundary && s.Secrets != SecretsNever {
		v.add(i18n.ConfigInvalidSyncSecrets, SecretsSameBoundary, SecretsNever)
	}
	for i, g := range s.Exclude {
		if _, err := path.Match(g, ""); g == "" || err != nil {
			v.add(i18n.ConfigInvalidSyncExclude, i)
		}
	}
	seen := map[string]bool{}
	for i, k := range s.AppPrefs {
		switch tag, ok := prefTags[k]; {
		case !ok:
			v.add(i18n.ConfigInvalidSyncAppPrefsUnknown, i)
		case tag != prefTagUIState && tag != prefTagOther:
			v.add(i18n.ConfigInvalidSyncAppPrefsTagged, i, tag)
		case seen[k]:
			v.add(i18n.ConfigInvalidSyncAppPrefsTwice, i)
		}
		seen[k] = true
	}
	v.boundary("sync.export", s.Export, aliases)
	v.boundary("sync.import", s.Import, aliases)
	for i, a := range s.SecretsAllow {
		if a == "" || strings.ContainsFunc(a, unicode.IsControl) {
			v.add(i18n.ConfigInvalidSyncSecretsAllow, i)
		}
	}
}

// boundary checks an export or import table: keys are known aliases and
// values known boundary categories (model.BoundaryCategories: every
// --only category plus artifacts).
func (v *validator) boundary(name string, m map[string][]string, aliases map[ids.Alias]bool) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if !aliases[ids.Alias(k)] {
			v.add(i18n.ConfigInvalidBoundaryAlias, name, sanitizeKey(k))
			continue
		}
		for _, c := range m[k] {
			if !slices.Contains(model.BoundaryCategories, model.Category(c)) {
				v.add(i18n.ConfigInvalidBoundaryCategory, name, k, sanitizeKey(c))
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
		v.add(i18n.ConfigInvalidLimitsMaxDeleteFraction)
	}
	if l.MaxDeleteMin < 0 {
		v.add(i18n.ConfigInvalidLimitsMaxDeleteMin)
	}
}

func (v *validator) backups(b Backups) {
	if b.Keep < 1 {
		v.add(i18n.ConfigInvalidBackupsKeep)
	}
	if b.LevelDBClonesKeep < 0 {
		v.add(i18n.ConfigInvalidBackupsLeveldbClonesKeep)
	}
	if _, err := ParseSize(b.MaxSize); err != nil {
		v.add(i18n.ConfigInvalidBackupsMaxSize, err)
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
