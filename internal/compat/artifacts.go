package compat

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// ArtifactsSpec is the [artifacts] table: artifact sync through the
// claude-artifacts MCP tools (beta, ADR 0006).
type ArtifactsSpec struct {
	Enabled            bool                `toml:"enabled"`
	Server             string              `toml:"server"`
	PublishVia         string              `toml:"publish_via"`
	ReadMode           string              `toml:"read_mode"`
	EntryPath          string              `toml:"entry_path"`
	BlobSuffix         string              `toml:"blob_suffix"`
	OutboxSuffix       string              `toml:"outbox_suffix"`
	DefaultIcon        string              `toml:"default_icon"`
	MaxContentBytes    int64               `toml:"max_content_bytes"`
	VersionMaxBytes    int64               `toml:"version_max_bytes"`
	MaxTitleUnits      int                 `toml:"max_title_units"`
	ListPageLimit      int                 `toml:"list_page_limit"`
	MaxListPages       int                 `toml:"max_list_pages"`
	ReadLimitLines     int                 `toml:"read_limit_lines"`
	MaxChunks          int                 `toml:"max_chunks"`
	FirstChunkEstimate int64               `toml:"first_chunk_estimate"`
	RoundBytes         int64               `toml:"round_bytes"`
	InspectPerRound    int                 `toml:"inspect_per_round"`
	ReadPerRound       int                 `toml:"read_per_round"`
	PublishPerRound    int                 `toml:"publish_per_round"`
	BatchMax           int                 `toml:"batch_max"`
	ResultGraceReads   string              `toml:"result_grace_reads"`
	MaxRounds          int                 `toml:"max_rounds"`
	OthersMarker       string              `toml:"others_marker"`
	WrapperTagPattern  string              `toml:"wrapper_tag_pattern"`
	WrapperForms       []string            `toml:"wrapper_forms"`
	ReminderPrefixes   []string            `toml:"reminder_prefixes"`
	VersionPattern     string              `toml:"version_pattern"`
	VersionTimePrefix  string              `toml:"version_time_prefix"`
	Fields             ArtifactsFields     `toml:"fields"`
	Refusals           map[string][]string `toml:"refusals"` // keys exactly the six refusal reasons
}

// ArtifactsFields are the result field names of the artifact tools: one
// list of accepted names per field, the first present wins.
type ArtifactsFields struct {
	ListItems    []string `toml:"list_items"`
	NextCursor   []string `toml:"next_cursor"`
	Slug         []string `toml:"slug"`
	URL          []string `toml:"url"`
	Version      []string `toml:"version"`
	UpdatedAt    []string `toml:"updated_at"`
	SoftDeleted  []string `toml:"soft_deleted"`
	Owner        []string `toml:"owner"`
	OwnerMine    string   `toml:"owner_mine"`
	MineBool     []string `toml:"mine_bool"`
	Title        []string `toml:"title"`
	Read         []string `toml:"read"`
	Icon         []string `toml:"icon"`
	TotalLines   []string `toml:"total_lines"`
	Offset       []string `toml:"offset"`
	NextOffset   []string `toml:"next_offset"`
	Notice       []string `toml:"notice"`
	SummaryFiles []string `toml:"summary_files"`
	Owned        []string `toml:"owned"`
	FilesList    []string `toml:"files_list"`
	FilePath     []string `toml:"file_path"`
	Capabilities []string `toml:"capabilities"`
}

// Lists returns every field list with its key, in a fixed order.
func (f ArtifactsFields) Lists() []NamedList {
	return []NamedList{
		{"list_items", f.ListItems}, {"next_cursor", f.NextCursor}, {"slug", f.Slug}, {"url", f.URL},
		{"version", f.Version}, {"updated_at", f.UpdatedAt}, {"soft_deleted", f.SoftDeleted}, {"owner", f.Owner},
		{"mine_bool", f.MineBool}, {"title", f.Title}, {"read", f.Read}, {"icon", f.Icon},
		{"total_lines", f.TotalLines}, {"offset", f.Offset}, {"next_offset", f.NextOffset}, {"notice", f.Notice},
		{"summary_files", f.SummaryFiles}, {"owned", f.Owned}, {"files_list", f.FilesList},
		{"file_path", f.FilePath}, {"capabilities", f.Capabilities},
	}
}

// NamedList is one field list of ArtifactsFields.
type NamedList struct {
	Key   string
	Names []string
}

// The values the artifacts table may take.
const (
	ArtifactsServer       = "claude-artifacts"
	ArtifactsPublishFiles = "files"
	ArtifactsPublishVer   = "version"
	ArtifactsPublishOff   = "off"
	ArtifactsReadChunks   = "chunks"
)

// ArtifactRefusalReasons are the keys of [artifacts.refusals].
var ArtifactRefusalReasons = []string{"type_made", "live_doc", "version_conflict", "not_found", "client_refused", "too_large"}

// ArtifactWrapperForms are the accepted wrapper forms of a chunk body.
var ArtifactWrapperForms = []string{"bare", "nl", "lead_nl", "trail_nl"}

var (
	artSuffixRE = regexp.MustCompile(`^\.[a-z.]{1,16}$`)
	artIconRE   = regexp.MustCompile(`^[a-z]{1,24}$`)
	artFieldRE  = regexp.MustCompile(`^[a-z_]{1,32}$`)
)

// validateArtifacts checks the [artifacts] table (spec-artifacts 11.3).
// An absent or disabled table means "artifact sync off" and is not
// checked further.
func (m *Manifest) validateArtifacts() error {
	a := m.Artifacts
	if !a.Enabled {
		return nil
	}
	return ValidateArtifacts(a)
}

// ValidateArtifacts checks an enabled [artifacts] table.
func ValidateArtifacts(a ArtifactsSpec) error {
	if a.Server != ArtifactsServer {
		return fmt.Errorf("artifacts.server: must be %s (the guarded server)", ArtifactsServer)
	}
	if !slices.Contains([]string{ArtifactsPublishFiles, ArtifactsPublishVer, ArtifactsPublishOff}, a.PublishVia) {
		return fmt.Errorf("artifacts.publish_via: must be files, version or off")
	}
	if a.ReadMode != ArtifactsReadChunks {
		return fmt.Errorf("artifacts.read_mode: must be chunks")
	}
	if a.EntryPath == "" || strings.ContainsRune(a.EntryPath, 0) || strings.HasPrefix(a.EntryPath, "/") ||
		path.Clean(a.EntryPath) != a.EntryPath || slices.Contains(strings.Split(a.EntryPath, "/"), "..") {
		return fmt.Errorf("artifacts.entry_path: must be a clean relative path")
	}
	if !artSuffixRE.MatchString(a.BlobSuffix) || !artSuffixRE.MatchString(a.OutboxSuffix) {
		return fmt.Errorf("artifacts.blob_suffix and outbox_suffix: must match %s", artSuffixRE)
	}
	if !artIconRE.MatchString(a.DefaultIcon) {
		return fmt.Errorf("artifacts.default_icon: must match %s", artIconRE)
	}
	ranges := []struct {
		key          string
		v, low, high int64
	}{
		{"max_content_bytes", a.MaxContentBytes, 1 << 10, 32 << 20},
		{"version_max_bytes", a.VersionMaxBytes, 1 << 10, 1 << 20},
		{"max_title_units", int64(a.MaxTitleUnits), 1, 1024},
		{"list_page_limit", int64(a.ListPageLimit), 1, 100},
		{"max_list_pages", int64(a.MaxListPages), 1, 100},
		{"read_limit_lines", int64(a.ReadLimitLines), 0, 100000},
		{"max_chunks", int64(a.MaxChunks), 1, 256},
		{"first_chunk_estimate", a.FirstChunkEstimate, 1 << 10, 4 << 20},
		{"round_bytes", a.RoundBytes, 64 << 10, 8 << 20},
		{"inspect_per_round", int64(a.InspectPerRound), 1, 300},
		{"read_per_round", int64(a.ReadPerRound), 1, 100},
		{"publish_per_round", int64(a.PublishPerRound), 1, 50},
		{"batch_max", int64(a.BatchMax), 1, 20},
		{"max_rounds", int64(a.MaxRounds), 1, 99},
	}
	for _, r := range ranges {
		if r.v < r.low || r.v > r.high {
			return fmt.Errorf("artifacts.%s: must be between %d and %d", r.key, r.low, r.high)
		}
	}
	// Step ids a000-a999 cap one round at 1000 artifact steps.
	if a.MaxListPages+3*a.InspectPerRound >= 999 || a.ReadPerRound >= 999 || a.PublishPerRound+1 >= 999 {
		return fmt.Errorf("artifacts: a round could hold more than 999 steps")
	}
	g, err := time.ParseDuration(a.ResultGraceReads)
	if err != nil || g < 10*time.Second || g > 10*time.Minute {
		return fmt.Errorf("artifacts.result_grace_reads: must be a duration between 10s and 10m")
	}
	if !printable(a.OthersMarker) {
		return fmt.Errorf("artifacts.others_marker: must be printable text")
	}
	for _, p := range []struct{ key, v string }{
		{"wrapper_tag_pattern", a.WrapperTagPattern}, {"version_pattern", a.VersionPattern},
		{"version_time_prefix", a.VersionTimePrefix},
	} {
		if !strings.HasPrefix(p.v, "^") {
			return fmt.Errorf("artifacts.%s: must be anchored at the start", p.key)
		}
		if _, err := regexp.Compile(p.v); err != nil {
			return fmt.Errorf("artifacts.%s: %v", p.key, err)
		}
	}
	if !strings.HasSuffix(a.WrapperTagPattern, "$") || !strings.HasSuffix(a.VersionPattern, "$") {
		return fmt.Errorf("artifacts.wrapper_tag_pattern and version_pattern: must be anchored at both ends")
	}
	if re := regexp.MustCompile(a.VersionTimePrefix); re.NumSubexp() != 1 {
		return fmt.Errorf("artifacts.version_time_prefix: must have exactly one group")
	}
	if err := validateWrapperForms(a.WrapperForms); err != nil {
		return err
	}
	for _, p := range a.ReminderPrefixes {
		if !printable(p) || len(p) < 4 || len(p) > 64 {
			return fmt.Errorf("artifacts.reminder_prefixes: entries must be printable, 4 to 64 bytes")
		}
	}
	for _, l := range a.Fields.Lists() {
		if len(l.Names) == 0 {
			return fmt.Errorf("artifacts.fields.%s: must not be empty", l.Key)
		}
		for _, n := range l.Names {
			if !artFieldRE.MatchString(n) {
				return fmt.Errorf("artifacts.fields.%s: %q is not a field name", l.Key, n)
			}
		}
	}
	if !artFieldRE.MatchString(a.Fields.OwnerMine) {
		return fmt.Errorf("artifacts.fields.owner_mine: must be a word")
	}
	keys := make([]string, 0, len(a.Refusals))
	for k := range a.Refusals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := append([]string(nil), ArtifactRefusalReasons...)
	sort.Strings(want)
	if !slices.Equal(keys, want) {
		return fmt.Errorf("artifacts.refusals: must list exactly %s", strings.Join(ArtifactRefusalReasons, ", "))
	}
	for _, k := range keys {
		for _, p := range a.Refusals[k] {
			if !printable(p) || len(p) > 200 {
				return fmt.Errorf("artifacts.refusals.%s: entries must be printable, at most 200 bytes", k)
			}
		}
	}
	return nil
}

// validateWrapperForms checks artifacts.wrapper_forms.
func validateWrapperForms(forms []string) error {
	if len(forms) == 0 {
		return fmt.Errorf("artifacts.wrapper_forms: must not be empty")
	}
	for _, f := range forms {
		if !slices.Contains(ArtifactWrapperForms, f) {
			return fmt.Errorf("artifacts.wrapper_forms: %q is not a form", f)
		}
	}
	lead, trail, nl := slices.Contains(forms, "lead_nl"), slices.Contains(forms, "trail_nl"), slices.Contains(forms, "nl")
	if lead && trail {
		return fmt.Errorf("artifacts.wrapper_forms: lead_nl and trail_nl cannot both be listed")
	}
	if nl && (lead || trail) {
		return fmt.Errorf("artifacts.wrapper_forms: nl cannot be listed with lead_nl or trail_nl")
	}
	return nil
}

// checkOverrideArtifacts refuses an override of base that weakens the
// [artifacts] table (spec-artifacts 11.3): it may lower limits, add
// refusal prefixes, reminder prefixes, wrapper forms and field names, and
// choose the publish transport (files, version or off: the embedded
// manifest ships off until release step R-AR-4 has passed, and that step
// is run with an override); it may not enable artifact sync
// where the embedded manifest does not, change the server, or change the
// fixed names and patterns.
func checkOverrideArtifacts(base, over *Manifest) error {
	b, o := base.Artifacts, over.Artifacts
	if !o.Enabled {
		return nil
	}
	if !b.Enabled {
		return fmt.Errorf("artifacts.enabled: the embedded manifest does not enable artifact sync")
	}
	if o.Server != b.Server {
		return fmt.Errorf("artifacts.server: cannot be changed")
	}
	for _, f := range []struct{ key, b, o string }{
		{"read_mode", b.ReadMode, o.ReadMode}, {"entry_path", b.EntryPath, o.EntryPath},
		{"blob_suffix", b.BlobSuffix, o.BlobSuffix}, {"outbox_suffix", b.OutboxSuffix, o.OutboxSuffix},
		{"others_marker", b.OthersMarker, o.OthersMarker}, {"wrapper_tag_pattern", b.WrapperTagPattern, o.WrapperTagPattern},
		{"version_pattern", b.VersionPattern, o.VersionPattern}, {"version_time_prefix", b.VersionTimePrefix, o.VersionTimePrefix},
		{"fields.owner_mine", b.Fields.OwnerMine, o.Fields.OwnerMine},
	} {
		if f.b != f.o {
			return fmt.Errorf("artifacts.%s: cannot be changed", f.key)
		}
	}
	for _, l := range []struct {
		key  string
		b, o int64
	}{
		{"max_content_bytes", b.MaxContentBytes, o.MaxContentBytes}, {"version_max_bytes", b.VersionMaxBytes, o.VersionMaxBytes},
		{"max_title_units", int64(b.MaxTitleUnits), int64(o.MaxTitleUnits)}, {"list_page_limit", int64(b.ListPageLimit), int64(o.ListPageLimit)},
		{"max_list_pages", int64(b.MaxListPages), int64(o.MaxListPages)}, {"read_limit_lines", int64(b.ReadLimitLines), int64(o.ReadLimitLines)},
		{"max_chunks", int64(b.MaxChunks), int64(o.MaxChunks)}, {"first_chunk_estimate", b.FirstChunkEstimate, o.FirstChunkEstimate},
		{"round_bytes", b.RoundBytes, o.RoundBytes}, {"inspect_per_round", int64(b.InspectPerRound), int64(o.InspectPerRound)},
		{"read_per_round", int64(b.ReadPerRound), int64(o.ReadPerRound)}, {"publish_per_round", int64(b.PublishPerRound), int64(o.PublishPerRound)},
		{"batch_max", int64(b.BatchMax), int64(o.BatchMax)}, {"max_rounds", int64(b.MaxRounds), int64(o.MaxRounds)},
	} {
		if l.o > l.b {
			return fmt.Errorf("artifacts.%s: an override may only lower it", l.key)
		}
	}
	if missing := removedEntries(b.WrapperForms, o.WrapperForms); len(missing) > 0 {
		return fmt.Errorf("artifacts.wrapper_forms: removes %s", strings.Join(missing, ", "))
	}
	if missing := removedEntries(b.ReminderPrefixes, o.ReminderPrefixes); len(missing) > 0 {
		return fmt.Errorf("artifacts.reminder_prefixes: removes %s", strings.Join(missing, ", "))
	}
	bl, ol := b.Fields.Lists(), o.Fields.Lists()
	for i := range bl {
		if missing := removedEntries(bl[i].Names, ol[i].Names); len(missing) > 0 {
			return fmt.Errorf("artifacts.fields.%s: removes %s", bl[i].Key, strings.Join(missing, ", "))
		}
	}
	for _, k := range ArtifactRefusalReasons {
		if missing := removedEntries(b.Refusals[k], o.Refusals[k]); len(missing) > 0 {
			return fmt.Errorf("artifacts.refusals.%s: removes %s", k, strings.Join(missing, ", "))
		}
	}
	return nil
}
