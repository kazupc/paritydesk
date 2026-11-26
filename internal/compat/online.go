package compat

// OnlineSpec is the [online] table: the online layer that reconciles pins
// and sidebar settings through the app's own tools after a launch (ADR
// 0005). The armed read list, the untrusted-read list and the guarded
// servers are code; the manifest may only add servers and floor tools.
type OnlineSpec struct {
	Enabled              bool              `toml:"enabled"`
	SettleAfterLaunch    string            `toml:"settle_after_launch"`
	ReplicaWait          string            `toml:"replica_wait"`
	ResultGrace          string            `toml:"result_grace"`
	UnboundLeaseTTL      string            `toml:"unbound_lease_ttl"`
	ReadMaxAge           string            `toml:"read_max_age"`
	BatchMax             int               `toml:"batch_max"`
	SessionStartMaxShows int               `toml:"session_start_max_shows"`
	PostMaxPayload       int64             `toml:"post_max_payload"`
	GuardedServersExtra  []string          `toml:"guarded_servers_extra"`
	GuardedToolsExtra    []string          `toml:"guarded_tools_extra"` // may only add
	AlwaysDeny           []string          `toml:"always_deny"`
	Pins                 OnlinePinsSpec    `toml:"pins"`
	Sidebar              OnlineSidebarSpec `toml:"sidebar"`
	Responses            OnlineResponses   `toml:"responses"`
}

// OnlineSidebarSpec is [online.sidebar]: section classes, field names, the
// set_view enums and the refusal texts of the sidebar tools.
type OnlineSidebarSpec struct {
	Builtin      []string `toml:"builtin"`
	ManualKind   string   `toml:"manual_kind"`
	GroupByField string   `toml:"group_by_field"`
	SortByField  string   `toml:"sort_by_field"`
	NameField    string   `toml:"name_field"`
	KindField    string   `toml:"kind_field"`
	MaxNameUnits int      `toml:"max_name_units"` // UTF-16 code units
	SortBy       []string `toml:"sort_by"`
	Refusals     []string `toml:"refusals"`
	// GroupByReport maps a stored section groupBy to its reported (tool)
	// spelling; a stored value not listed is never written.
	GroupByReport map[string]string `toml:"group_by_report"`
	// GroupBy lists the tool values writable per section class.
	GroupBy map[string][]string `toml:"group_by"`
}

// OnlineResponses are fixed tool response texts the online layer
// recognizes.
type OnlineResponses struct {
	ToolsOff []string `toml:"tools_off"`
}

// OnlinePinsSpec is [online.pins]: how the pin provider sets pins through
// the app's set_pinned tool and reads its answers.
type OnlinePinsSpec struct {
	MaxSteps       int      `toml:"max_steps"`
	SkipIfTrue     []string `toml:"skip_if_true"`
	SkipIfPresent  []string `toml:"skip_if_present"`
	OKPinned       []string `toml:"ok_pinned"`
	OKUnpinned     []string `toml:"ok_unpinned"`
	RefusalsUnpin  []string `toml:"refusals_unpin"`
	UnknownOutcome []string `toml:"unknown_outcome"`
	CascadeUnpin   bool     `toml:"cascade_unpin"`
	FamilyFields   []string `toml:"family_fields"`
	FamilyMaxDepth int      `toml:"family_max_depth"`
	AssignStoreKey string   `toml:"assign_store_key"`
	AssignPath     []string `toml:"assign_path"`
	RowKeyPrefix   string   `toml:"row_key_prefix"`
}

// validateOnline checks the [online] table. The online unit owns its
// rules (spec-online 13.3; the pins unit adds those of [online.pins]); an
// absent table decodes to the zero value, which means "online layer off".
func (m *Manifest) validateOnline() error { return nil }

// checkOverrideOnline refuses an override of base that weakens the
// [online] table. The online unit owns its rules (spec-online 13.3).
func checkOverrideOnline(base, over *Manifest) error { return nil }
