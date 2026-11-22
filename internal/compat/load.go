package compat

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/kazupc/paritydesk/internal/model"
)

// maxManifestSize bounds an override file.
const maxManifestSize = 1 << 20

// loadEmbedded decodes every embedded manifest, keyed by name, in file
// order.
func loadEmbedded() (map[string]*Manifest, []string, error) {
	ents, err := fs.ReadDir(embedded, "manifests")
	if err != nil {
		return nil, nil, fmt.Errorf("compat: list embedded manifests: %w", err)
	}
	byName := map[string]*Manifest{}
	var order []string
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		data, err := fs.ReadFile(embedded, "manifests/"+e.Name())
		if err != nil {
			return nil, nil, fmt.Errorf("compat: read embedded %s: %w", e.Name(), err)
		}
		m, err := Decode(data, e.Name())
		if err != nil {
			return nil, nil, err
		}
		if _, dup := byName[m.Name]; dup {
			return nil, nil, fmt.Errorf("compat: embedded manifest name %q is used twice", m.Name)
		}
		byName[m.Name] = m
		order = append(order, m.Name)
	}
	return byName, order, nil
}

// applyOverrides decodes every *.toml in dir and replaces (or adds) the
// manifest of the same name. A missing dir is no override.
func applyOverrides(dir string, byName map[string]*Manifest, order *[]string) error {
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("compat: list override directory: %w", err)
	}
	embeddedByName := make(map[string]*Manifest, len(byName))
	for k, v := range byName {
		embeddedByName[k] = v
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".toml") || !e.Type().IsRegular() {
			continue
		}
		m, err := readOverride(filepath.Join(dir, e.Name()), e.Name())
		if err != nil {
			return err
		}
		if err := checkOverride(e.Name(), m, embeddedByName); err != nil {
			return err
		}
		if _, ok := byName[m.Name]; !ok {
			*order = append(*order, m.Name)
		}
		byName[m.Name] = m
	}
	return nil
}

// checkOverride refuses an override that weakens any embedded manifest
// it could replace: one of the same name, or one whose version range
// overlaps the override's (an override of another name with a lower
// min_version would otherwise be selected instead). Such an override may
// add never_touched, never_until_classified and ls_never_touched entries
// but not remove them, and its synced globs must all be synced globs of
// those embedded manifests.
func checkOverride(file string, m *Manifest, embedded map[string]*Manifest) error {
	names := make([]string, 0, len(embedded))
	for n := range embedded {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		base := embedded[n]
		if n != m.Name && !rangesOverlap(base, m) {
			continue
		}
		for _, l := range []struct {
			key        string
			base, next []string
		}{
			{"never_touched", base.Classification.NeverTouched, m.Classification.NeverTouched},
			{"never_until_classified", base.Classification.NeverUntilClassified, m.Classification.NeverUntilClassified},
			{"ls_never_touched", base.Classification.LSNeverTouched, m.Classification.LSNeverTouched},
		} {
			if missing := removedEntries(l.base, l.next); len(missing) > 0 {
				return fmt.Errorf("compat: override %s removes %s entries of manifest %q: %s",
					file, l.key, base.Name, strings.Join(missing, ", "))
			}
		}
		if extra := removedEntries(m.Classification.Synced, base.Classification.Synced); len(extra) > 0 {
			return fmt.Errorf("compat: override %s widens synced beyond manifest %q: %s",
				file, base.Name, strings.Join(extra, ", "))
		}
	}
	return nil
}

// rangesOverlap reports whether two manifests' version ranges overlap.
func rangesOverlap(a, b *Manifest) bool {
	return compareVersions(a.MinVersion, b.MaxVersion) <= 0 && compareVersions(b.MinVersion, a.MaxVersion) <= 0
}

func readOverride(p, name string) (*Manifest, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return nil, fmt.Errorf("compat: override %s: %w", name, err)
	}
	if fi.Size() > maxManifestSize {
		return nil, fmt.Errorf("compat: override %s is too large", name)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("compat: read override %s: %w", name, err)
	}
	return Decode(data, name)
}

// removedEntries returns the entries of base missing from next.
func removedEntries(base, next []string) []string {
	have := map[string]bool{}
	for _, s := range next {
		have[s] = true
	}
	var out []string
	for _, s := range base {
		if !have[s] {
			out = append(out, s)
		}
	}
	return out
}

// Decode parses one manifest strictly (unknown keys are errors) and
// validates it. label names the file in errors.
func Decode(data []byte, label string) (*Manifest, error) {
	var m Manifest
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("compat: %s: %w", label, err)
	}
	if err := m.validate(); err != nil {
		return nil, fmt.Errorf("compat: %s: %w", label, err)
	}
	return &m, nil
}

var (
	writePaths = map[WritePath]bool{PathOff: true, PathOffVerify: true, PathOn: true, PathCap: true, PathNone: true}
	probeKinds = map[ProbeKind]bool{ProbeFile: true, ProbeDir: true, ProbeJSONShape: true, ProbeLSKey: true,
		ProbeLSShape: true, ProbePersistVersion: true, ProbeIdxVersion: true, ProbeJSONVersion: true}
	jsonTypes = map[string]bool{"": true, "object": true, "array": true, "string": true, "number": true, "bool": true}
	prefTags  = map[string]bool{TagUIState: true, TagOther: true, TagSecurity: true, TagInternalState: true}
)

func knownRow(r model.RowID) bool {
	for _, x := range model.AllRows {
		if x == r {
			return true
		}
	}
	return false
}

func (m *Manifest) validate() error {
	if m.Name == "" {
		return errors.New("name is empty")
	}
	if err := m.validateVersions(); err != nil {
		return err
	}
	if m.LSBackend != "leveldb" {
		return errors.New("ls_backend must be leveldb")
	}
	probes, err := m.validateProbes()
	if err != nil {
		return err
	}
	if err := m.validateRows(probes); err != nil {
		return err
	}
	for k, v := range m.PrefTags {
		if !prefTags[v] {
			return fmt.Errorf("pref_tags.%s: unknown tag", k)
		}
	}
	for _, g := range append(append([]string(nil), m.TmpRecoveryGlobs...), m.allClassGlobs()...) {
		if _, err := path.Match(g, ""); err != nil || !relativeClean(strings.ReplaceAll(g, "**", "x")) {
			return fmt.Errorf("glob %q is malformed", g)
		}
	}
	for _, g := range m.Classification.LSNeverTouched {
		if _, err := path.Match(g, ""); err != nil || g == "" {
			return fmt.Errorf("ls_never_touched glob %q is malformed", g)
		}
	}
	return m.Signals.validate()
}

func (m *Manifest) allClassGlobs() []string {
	c := m.Classification
	var out []string
	for _, l := range [][]string{c.NeverTouched, c.NeverUntilClassified, c.Cache, c.Synced, c.Other} {
		out = append(out, l...)
	}
	return out
}

func (m *Manifest) validateVersions() error {
	for _, v := range append([]string{m.MinVersion, m.MaxVersion}, append(m.Tested, m.KnownBad...)...) {
		if _, err := parseVersion(v); err != nil {
			return err
		}
	}
	if compareVersions(m.MinVersion, m.MaxVersion) > 0 {
		return errors.New("min_version is above max_version")
	}
	return nil
}

func (m *Manifest) validateProbes() (map[string]bool, error) {
	ids := map[string]bool{}
	for _, p := range m.Probes {
		switch {
		case p.ID == "" || ids[p.ID]:
			return nil, fmt.Errorf("probe id %q is empty or used twice", p.ID)
		case !probeKinds[p.Kind]:
			return nil, fmt.Errorf("probe %s: unknown kind", p.ID)
		case p.Row != "" && !knownRow(p.Row):
			return nil, fmt.Errorf("probe %s: unknown row", p.ID)
		case !jsonTypes[p.Type]:
			return nil, fmt.Errorf("probe %s: unknown type", p.ID)
		case p.Target == "":
			return nil, fmt.Errorf("probe %s: empty target", p.ID)
		case p.Kind == ProbeJSONVersion && (len(p.Path) == 0 || p.Version <= 0):
			return nil, fmt.Errorf("probe %s: json-version needs a path and a positive version", p.ID)
		}
		if p.Kind != ProbeLSKey && p.Kind != ProbeLSShape && p.Kind != ProbePersistVersion && !relativeClean(p.Target) {
			return nil, fmt.Errorf("probe %s: target must be a clean relative path", p.ID)
		}
		ids[p.ID] = true
	}
	return ids, nil
}

func (m *Manifest) validateRows(probes map[string]bool) error {
	seen := map[model.RowID]bool{}
	for _, r := range m.Rows {
		switch {
		case !knownRow(r.Row) || seen[r.Row]:
			return fmt.Errorf("row %q is unknown or declared twice", r.Row)
		case !writePaths[r.WritePath]:
			return fmt.Errorf("row %s: unknown write_path", r.Row)
		case len(r.MirrorPaths) > 0 && r.MirrorFile == "":
			return fmt.Errorf("row %s: mirror_paths without mirror_file", r.Row)
		}
		for _, p := range r.Probes {
			if !probes[p] {
				return fmt.Errorf("row %s: unknown probe %q", r.Row, p)
			}
		}
		for _, f := range r.Files {
			if !relativeClean(f) {
				return fmt.Errorf("row %s: file %q must be a clean relative path", r.Row, f)
			}
		}
		seen[r.Row] = true
	}
	return nil
}

// relativeClean reports whether p is a clean, relative, slash-separated
// path without "..".
func relativeClean(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || path.Clean(p) != p {
		return false
	}
	for _, s := range strings.Split(p, "/") {
		if s == ".." {
			return false
		}
	}
	return true
}

// sampleSession is a well-formed session id used to check {session}
// patterns.
const sampleSession = "local_00000000-0000-4000-8000-000000000000"

func (s Signals) validate() error {
	for name, p := range map[string]string{
		"quit_vetoed": s.QuitVetoed, "turn_started": s.TurnStarted, "turn_ended": s.TurnEnded,
		"app_started": s.AppStarted, "focused_session": s.FocusedSession, "bridge_connected": s.BridgeConnected,
		"pairing_disabled": s.PairingDisabled, "unclean_exit": s.UncleanExit, "login_required": s.LoginRequired,
		"active_org": s.ActiveOrg, "native_multi_account": s.NativeMultiAccount,
	} {
		if p == "" {
			continue
		}
		if _, err := regexp.Compile(strings.ReplaceAll(p, "{session}", regexp.QuoteMeta(sampleSession))); err != nil {
			return fmt.Errorf("signals.%s: %w", name, err)
		}
	}
	return nil
}

// parseVersion splits a dotted numeric version.
func parseVersion(v string) ([]int, error) {
	parts := strings.Split(v, ".")
	if v == "" || len(parts) > 4 {
		return nil, fmt.Errorf("compat: version %q is malformed", v)
	}
	out := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p != strconv.Itoa(n) {
			return nil, fmt.Errorf("compat: version %q is malformed", v)
		}
		out[i] = n
	}
	return out, nil
}

// CompareVersions compares two app versions numerically per component
// (missing components are 0): -1, 0 or 1. Malformed versions compare as
// strings after every valid one.
func CompareVersions(a, b string) int { return compareVersions(a, b) }

// compareVersions compares numerically per component (missing components
// are 0). Malformed versions compare as strings after every valid one.
func compareVersions(a, b string) int {
	va, ea := parseVersion(a)
	vb, eb := parseVersion(b)
	switch {
	case ea != nil || eb != nil:
		if (ea == nil) != (eb == nil) {
			if ea == nil {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	}
	for i := 0; i < len(va) || i < len(vb); i++ {
		x, y := at(va, i), at(vb, i)
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func at(v []int, i int) int {
	if i < len(v) {
		return v[i]
	}
	return 0
}
