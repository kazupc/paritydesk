package compat

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/store/idb"
)

// IDBSpec is [row.idb]: where the pins row reads its value in the app's
// IndexedDB (a LevelDB store read without the app's lock) and the limits
// of that read (spike S14).
type IDBSpec struct {
	Dir              string   `toml:"dir"`
	BackendPrefix    string   `toml:"backend_prefix"`
	Database         string   `toml:"database"`
	ObjectStore      string   `toml:"object_store"`
	Key              string   `toml:"key"`
	IDsPath          []string `toml:"ids_path"`
	VersionPath      []string `toml:"version_path"`
	UpdatedPath      []string `toml:"updated_path"`
	Versions         []int    `toml:"versions"`
	IDPattern        string   `toml:"id_pattern"`
	BlinkVersions    []int    `toml:"blink_versions"` // [min, max]
	V8Versions       []int    `toml:"v8_versions"`    // [min, max]
	MaxValueBytes    int      `toml:"max_value_bytes"`
	MaxCopyBytes     int64    `toml:"max_copy_bytes"`
	MaxCloneBytes    int64    `toml:"max_clone_bytes"`
	MaxFiles         int      `toml:"max_files"`
	MaxReadMS        int      `toml:"max_read_ms"`
	MaxItems         int      `toml:"max_items"`
	MirrorPrefPath   []string `toml:"mirror_pref_path"`
	MirrorLSKey      string   `toml:"mirror_ls_key"`
	MirrorLSIDsPath  []string `toml:"mirror_ls_ids_path"`
	MirrorLSTimePath []string `toml:"mirror_ls_time_path"`
}

// Reader converts a validated table into the reader's spec.
func (s IDBSpec) Reader() idb.Spec {
	r := func(v []int, i int) uint32 {
		if len(v) == 2 && v[i] >= 0 {
			return uint32(v[i])
		}
		return 0
	}
	return idb.Spec{
		Dir: s.Dir, BackendPrefix: s.BackendPrefix, Database: s.Database, ObjectStore: s.ObjectStore, Key: s.Key,
		BlinkMin: r(s.BlinkVersions, 0), BlinkMax: r(s.BlinkVersions, 1),
		V8Min: r(s.V8Versions, 0), V8Max: r(s.V8Versions, 1),
		MaxValueBytes: s.MaxValueBytes, MaxCopyBytes: s.MaxCopyBytes, MaxCloneBytes: s.MaxCloneBytes,
		MaxFiles: s.MaxFiles, MaxRead: time.Duration(s.MaxReadMS) * time.Millisecond,
	}
}

// FakeLocalID is the fake local session id every id_pattern must match.
const FakeLocalID = "local_00000000-0000-4000-8000-000000000001"

// Bounds of [row.idb] and [online.pins] (spec-pins 5.3).
const (
	minValueBytes  = 4 << 10
	maxValueBytes  = 8 << 20
	maxCopyBytes   = 512 << 20
	maxCloneBytes  = 8 << 30
	maxIDBFiles    = 4096
	maxIDBItems    = 50000
	maxPinsSteps   = 1000
	maxFamilyDepth = 256
)

var familyFieldRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*){0,3}$`)

// asciiName reports printable ASCII of 1..256 bytes.
func asciiName(s string) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// jsonPath reports 1..8 non-empty segments.
func jsonPath(p []string) bool {
	if len(p) == 0 || len(p) > 8 {
		return false
	}
	return !slices.Contains(p, "")
}

// validateRowIDB checks a row's [row.idb] table and, for the pins row
// written online, the [online.pins] table it needs (spec-pins 5.3).
func validateRowIDB(m *Manifest, r RowSpec) error {
	if r.Row != model.RowPins {
		if r.IDB != nil {
			return fmt.Errorf("row %s: [row.idb] is only for row pins", r.Row)
		}
		return nil
	}
	if r.WritePath != PathOn {
		return nil
	}
	if r.IDB == nil {
		return fmt.Errorf("row pins: write_path \"on\" needs [row.idb]")
	}
	if !m.Online.Enabled {
		return fmt.Errorf("row pins: write_path \"on\" needs [online] enabled = true")
	}
	if err := validateIDB(*r.IDB); err != nil {
		return fmt.Errorf("row pins: %w", err)
	}
	if err := validateOnlinePins(m.Online.Pins); err != nil {
		return err
	}
	return validateMirrorSafety(m, *r.IDB)
}

// validateIDB checks one [row.idb] table.
func validateIDB(s IDBSpec) error {
	clean := filepath.ToSlash(filepath.Clean(s.Dir))
	switch {
	case s.Dir == "" || clean != s.Dir || strings.HasPrefix(s.Dir, "/") || slices.Contains(strings.Split(s.Dir, "/"), ".."):
		return fmt.Errorf("idb.dir: must be a clean relative path")
	case !strings.HasPrefix(s.Dir, "IndexedDB/"):
		return fmt.Errorf("idb.dir: must be under IndexedDB/")
	case s.BackendPrefix == "" || !strings.HasPrefix(s.Dir, s.BackendPrefix):
		return fmt.Errorf("idb.backend_prefix: must be a prefix of idb.dir")
	}
	for _, f := range []struct{ key, v string }{
		{"database", s.Database}, {"object_store", s.ObjectStore}, {"key", s.Key}, {"mirror_ls_key", s.MirrorLSKey},
	} {
		if !asciiName(f.v) {
			return fmt.Errorf("idb.%s: must be printable ASCII of 1 to 256 bytes", f.key)
		}
	}
	for _, p := range []struct {
		key string
		v   []string
	}{
		{"ids_path", s.IDsPath}, {"version_path", s.VersionPath}, {"updated_path", s.UpdatedPath},
		{"mirror_pref_path", s.MirrorPrefPath}, {"mirror_ls_ids_path", s.MirrorLSIDsPath}, {"mirror_ls_time_path", s.MirrorLSTimePath},
	} {
		if !jsonPath(p.v) {
			return fmt.Errorf("idb.%s: must have 1 to 8 non-empty segments", p.key)
		}
	}
	if len(s.Versions) == 0 {
		return fmt.Errorf("idb.versions: must not be empty")
	}
	if !strings.HasPrefix(s.IDPattern, "^") || !strings.HasSuffix(s.IDPattern, "$") {
		return fmt.Errorf("idb.id_pattern: must be anchored with ^ and $")
	}
	re, err := regexp.Compile(s.IDPattern)
	if err != nil {
		return fmt.Errorf("idb.id_pattern: does not compile")
	}
	if !re.MatchString(FakeLocalID) {
		return fmt.Errorf("idb.id_pattern: must match a local session id")
	}
	for _, v := range []struct {
		key string
		r   []int
	}{{"blink_versions", s.BlinkVersions}, {"v8_versions", s.V8Versions}} {
		if len(v.r) != 2 || v.r[0] < 1 || v.r[0] > v.r[1] || v.r[1] > 255 {
			return fmt.Errorf("idb.%s: must be [min, max] with 1 <= min <= max <= 255", v.key)
		}
	}
	switch {
	case s.MaxValueBytes < minValueBytes || s.MaxValueBytes > maxValueBytes:
		return fmt.Errorf("idb.max_value_bytes: must be between 4 KiB and 8 MiB")
	case s.MaxCopyBytes < 1<<20 || s.MaxCopyBytes > maxCopyBytes:
		return fmt.Errorf("idb.max_copy_bytes: must be between 1 MiB and 512 MiB")
	case s.MaxCloneBytes < s.MaxCopyBytes || s.MaxCloneBytes > maxCloneBytes:
		return fmt.Errorf("idb.max_clone_bytes: must be at least max_copy_bytes and at most 8 GiB")
	case s.MaxFiles < 8 || s.MaxFiles > maxIDBFiles:
		return fmt.Errorf("idb.max_files: must be between 8 and 4096")
	case s.MaxReadMS < 100 || s.MaxReadMS > 60000:
		return fmt.Errorf("idb.max_read_ms: must be between 100 and 60000")
	case s.MaxItems < 1 || s.MaxItems > maxIDBItems:
		return fmt.Errorf("idb.max_items: must be between 1 and 50000")
	case s.MirrorPrefPath[0] != "preferences":
		return fmt.Errorf("idb.mirror_pref_path: must start with preferences")
	}
	return nil
}

// validateOnlinePins checks [online.pins].
func validateOnlinePins(p OnlinePinsSpec) error {
	switch {
	case p.MaxSteps < 1 || p.MaxSteps > maxPinsSteps:
		return fmt.Errorf("online.pins.max_steps: must be between 1 and 1000")
	case p.FamilyMaxDepth < 1 || p.FamilyMaxDepth > maxFamilyDepth:
		return fmt.Errorf("online.pins.family_max_depth: must be between 1 and 256")
	case len(p.FamilyFields) < 1 || len(p.FamilyFields) > 16:
		return fmt.Errorf("online.pins.family_fields: must list 1 to 16 fields")
	case !asciiName(p.AssignStoreKey):
		return fmt.Errorf("online.pins.assign_store_key: must be printable ASCII of 1 to 256 bytes")
	case !jsonPath(p.AssignPath):
		return fmt.Errorf("online.pins.assign_path: must have 1 to 8 non-empty segments")
	case !asciiName(p.RowKeyPrefix):
		return fmt.Errorf("online.pins.row_key_prefix: must be printable ASCII")
	}
	for _, f := range p.FamilyFields {
		if !familyFieldRE.MatchString(f) {
			return fmt.Errorf("online.pins.family_fields: %q is not a dotted path of 1 to 4 names", f)
		}
	}
	for _, f := range append(append([]string(nil), p.SkipIfTrue...), p.SkipIfPresent...) {
		if !familyFieldRE.MatchString(f) {
			return fmt.Errorf("online.pins: %q is not a record field", f)
		}
	}
	for _, seg := range p.AssignPath {
		if strings.ContainsAny(strings.NewReplacer("{acct}", "", "{org}", "").Replace(seg), "{}") {
			return fmt.Errorf("online.pins.assign_path: only {acct} and {org} may be templated")
		}
	}
	lists := []struct {
		key string
		v   []string
	}{{"ok_pinned", p.OKPinned}, {"ok_unpinned", p.OKUnpinned}, {"refusals_unpin", p.RefusalsUnpin}, {"unknown_outcome", p.UnknownOutcome}}
	for _, l := range lists {
		if len(l.v) == 0 {
			return fmt.Errorf("online.pins.%s: must not be empty", l.key)
		}
		for _, s := range l.v {
			if !printable(s) {
				return fmt.Errorf("online.pins.%s: entries must be printable text", l.key)
			}
		}
	}
	// No entry may be a prefix of an entry of another list: a refusal
	// must never shadow an ok text, and the reverse.
	for i, a := range lists {
		for j, b := range lists {
			if i == j {
				continue
			}
			for _, x := range a.v {
				for _, y := range b.v {
					if strings.HasPrefix(y, x) {
						return fmt.Errorf("online.pins.%s: %q is a prefix of an entry of online.pins.%s", a.key, x, b.key)
					}
				}
			}
		}
	}
	return nil
}

// validateMirrorSafety makes sure no row can write the app's own mirror
// of the pin list: no row's mirror_paths overlaps mirror_pref_path, no
// row names the Local Storage mirror, and an ls_never_touched glob covers
// it.
func validateMirrorSafety(m *Manifest, s IDBSpec) error {
	for _, r := range m.Rows {
		if r.MirrorFile != "" && filepath.Base(r.MirrorFile) != "claude_desktop_config.json" {
			continue
		}
		for _, mp := range r.MirrorPaths {
			if prefixOf(mp, s.MirrorPrefPath) || prefixOf(s.MirrorPrefPath, mp) {
				return fmt.Errorf("row %s: mirror_paths overlap the pins mirror (idb.mirror_pref_path)", r.Row)
			}
		}
		for _, k := range r.LSKeys {
			if strings.EqualFold(k, s.MirrorLSKey) {
				return fmt.Errorf("row %s: ls_keys name the pins mirror (idb.mirror_ls_key)", r.Row)
			}
		}
	}
	covered := false
	for _, g := range m.Classification.LSNeverTouched {
		if ok, err := path.Match(strings.ToLower(g), strings.ToLower(s.MirrorLSKey)); err == nil && ok {
			covered = true
		}
	}
	if !covered {
		return fmt.Errorf("idb.mirror_ls_key: must match an ls_never_touched glob")
	}
	return nil
}

// prefixOf reports whether a is a prefix of b (segment-wise).
func prefixOf(a, b []string) bool {
	if len(a) > len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// pinsRow returns the pins row's spec of m.
func pinsRow(m *Manifest) (RowSpec, bool) {
	for _, r := range m.Rows {
		if r.Row == model.RowPins {
			return r, true
		}
	}
	return RowSpec{}, false
}

// checkOverrideIDB refuses an override of base that weakens [row.idb] or
// [online.pins]: limits may only tighten, refusals_unpin and
// unknown_outcome may only grow, ok texts and family fields may not be
// removed, and cascade_unpin may not be turned off.
func checkOverrideIDB(base, over *Manifest) error {
	bp, over2 := base.Online.Pins, over.Online.Pins
	if over.Online.Enabled && base.Online.Enabled {
		for _, l := range []struct {
			key        string
			base, next []string
		}{
			{"online.pins.ok_pinned", bp.OKPinned, over2.OKPinned},
			{"online.pins.ok_unpinned", bp.OKUnpinned, over2.OKUnpinned},
			{"online.pins.refusals_unpin", bp.RefusalsUnpin, over2.RefusalsUnpin},
			{"online.pins.unknown_outcome", bp.UnknownOutcome, over2.UnknownOutcome},
			{"online.pins.family_fields", bp.FamilyFields, over2.FamilyFields},
			{"online.pins.skip_if_true", bp.SkipIfTrue, over2.SkipIfTrue},
			{"online.pins.skip_if_present", bp.SkipIfPresent, over2.SkipIfPresent},
		} {
			if missing := removedEntries(l.base, l.next); len(missing) > 0 {
				return fmt.Errorf("%s: removes %s", l.key, strings.Join(missing, ", "))
			}
		}
		if bp.CascadeUnpin && !over2.CascadeUnpin {
			return fmt.Errorf("online.pins.cascade_unpin: may not be turned off")
		}
		if over2.MaxSteps > bp.MaxSteps {
			return fmt.Errorf("online.pins.max_steps: may only be lowered")
		}
		if over2.FamilyMaxDepth < bp.FamilyMaxDepth {
			return fmt.Errorf("online.pins.family_max_depth: may only be raised")
		}
	}
	b, okB := pinsRow(base)
	o, okO := pinsRow(over)
	if !okB || !okO || b.IDB == nil || o.IDB == nil {
		return nil
	}
	bi, oi := b.IDB, o.IDB
	for _, c := range []struct {
		key        string
		base, next int64
	}{
		{"idb.max_value_bytes", int64(bi.MaxValueBytes), int64(oi.MaxValueBytes)},
		{"idb.max_copy_bytes", bi.MaxCopyBytes, oi.MaxCopyBytes},
		{"idb.max_clone_bytes", bi.MaxCloneBytes, oi.MaxCloneBytes},
		{"idb.max_files", int64(bi.MaxFiles), int64(oi.MaxFiles)},
		{"idb.max_read_ms", int64(bi.MaxReadMS), int64(oi.MaxReadMS)},
		{"idb.max_items", int64(bi.MaxItems), int64(oi.MaxItems)},
	} {
		if c.next > c.base {
			return fmt.Errorf("%s: may only be lowered", c.key)
		}
	}
	return nil
}
