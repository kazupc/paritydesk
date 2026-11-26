package compat

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

// validateArtifacts checks the [artifacts] table. The artifacts unit owns
// its rules (spec-artifacts 11.3); an absent table decodes to the zero
// value, which means "artifact sync off".
func (m *Manifest) validateArtifacts() error { return nil }

// checkOverrideArtifacts refuses an override of base that weakens the
// [artifacts] table. The artifacts unit owns its rules (spec-artifacts
// 11.3).
func checkOverrideArtifacts(base, over *Manifest) error { return nil }
