package compat

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
)

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

// Online validation tables (spec-online 13.3). The online package keeps
// its own copies of the tool enums; these are the manifest's bounds.
var (
	onlineToolRE   = regexp.MustCompile(`^mcp__[a-z0-9_-]+__[a-z0-9_]+$`)
	onlineServerRE = regexp.MustCompile(`^[a-z0-9_-]+$`)
	// onlineAppToolRE matches the app's own tools outside MCP (Artifact).
	onlineAppToolRE = regexp.MustCompile(`^[A-Z][A-Za-z]{1,63}$`)
	// onlineReported are the group-by spellings the tools report.
	onlineReported = []string{"date", "project", "environment", "state", "custom", "none"}
	// onlineWritable is set_view's group_by enum without "custom".
	onlineWritable = []string{"date", "project", "state", "none"}
	onlineSortBy   = []string{"recency", "alpha", "created"}
	onlineClasses  = []string{"sessions", "routines", "pinned", "manual"}
	onlineBuiltin  = []string{"pinned", "routines", "sessions"}
)

// durationIn parses a manifest duration and checks its range.
func durationIn(key, v string, lo, hi time.Duration) error {
	d, err := time.ParseDuration(v)
	if err != nil || d < lo || d > hi {
		return fmt.Errorf("online.%s: must be a duration between %s and %s", key, lo, hi)
	}
	return nil
}

// printable reports a non-empty string of printable characters.
func printable(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// validateOnline checks the [online] table (spec-online 13.3). An absent
// table, or enabled = false, means "online layer off" and is not checked
// further (the pins unit adds the [online.pins] rules).
func (m *Manifest) validateOnline() error {
	o := m.Online
	if !o.Enabled {
		return nil
	}
	for _, d := range []struct {
		key, v string
		lo, hi time.Duration
	}{
		{"settle_after_launch", o.SettleAfterLaunch, 0, 5 * time.Minute},
		{"replica_wait", o.ReplicaWait, 0, 10 * time.Minute},
		{"result_grace", o.ResultGrace, time.Minute, 30 * time.Minute},
		{"unbound_lease_ttl", o.UnboundLeaseTTL, time.Minute, 10 * time.Minute},
		{"read_max_age", o.ReadMaxAge, 30 * time.Second, 10 * time.Minute},
	} {
		if err := durationIn(d.key, d.v, d.lo, d.hi); err != nil {
			return err
		}
	}
	switch {
	case o.BatchMax < 1 || o.BatchMax > 50:
		return fmt.Errorf("online.batch_max: must be between 1 and 50")
	case o.SessionStartMaxShows < 1 || o.SessionStartMaxShows > 9:
		return fmt.Errorf("online.session_start_max_shows: must be between 1 and 9")
	case o.PostMaxPayload < 1<<20 || o.PostMaxPayload > 32<<20:
		return fmt.Errorf("online.post_max_payload: must be between 1 MiB and 32 MiB")
	}
	for _, s := range o.GuardedServersExtra {
		if !onlineServerRE.MatchString(s) {
			return fmt.Errorf("online.guarded_servers_extra: %q is not a server name", s)
		}
	}
	for _, t := range o.AlwaysDeny {
		if !onlineToolRE.MatchString(t) {
			return fmt.Errorf("online.always_deny: %q is not a tool name", t)
		}
	}
	for _, t := range o.GuardedToolsExtra {
		if !onlineToolRE.MatchString(t) && !onlineAppToolRE.MatchString(t) {
			return fmt.Errorf("online.guarded_tools_extra: %q is not a tool name", t)
		}
	}
	if err := validateOnlineSidebar(o.Sidebar); err != nil {
		return err
	}
	if len(o.Responses.ToolsOff) == 0 {
		return fmt.Errorf("online.responses.tools_off: must not be empty")
	}
	for _, t := range o.Responses.ToolsOff {
		if !printable(t) {
			return fmt.Errorf("online.responses.tools_off: entries must be printable text")
		}
	}
	return nil
}

// validateOnlineSidebar checks [online.sidebar].
func validateOnlineSidebar(sb OnlineSidebarSpec) error {
	for _, b := range sb.Builtin {
		if !slices.Contains(onlineBuiltin, b) {
			return fmt.Errorf("online.sidebar.builtin: %q is not a built-in section", b)
		}
	}
	for _, f := range []struct{ key, v string }{
		{"manual_kind", sb.ManualKind}, {"group_by_field", sb.GroupByField}, {"sort_by_field", sb.SortByField},
		{"name_field", sb.NameField}, {"kind_field", sb.KindField},
	} {
		if !onlineServerRE.MatchString(strings.ToLower(f.v)) {
			return fmt.Errorf("online.sidebar.%s: must be a member name", f.key)
		}
	}
	if sb.MaxNameUnits < 1 || sb.MaxNameUnits > 200 {
		return fmt.Errorf("online.sidebar.max_name_units: must be between 1 and 200")
	}
	for _, s := range sb.SortBy {
		if !slices.Contains(onlineSortBy, s) {
			return fmt.Errorf("online.sidebar.sort_by: %q is not a set_view sort", s)
		}
	}
	if len(sb.Refusals) == 0 {
		return fmt.Errorf("online.sidebar.refusals: must not be empty")
	}
	for _, r := range sb.Refusals {
		if !printable(r) {
			return fmt.Errorf("online.sidebar.refusals: entries must be printable text")
		}
	}
	if len(sb.GroupByReport) == 0 {
		return fmt.Errorf("online.sidebar.group_by_report: must not be empty")
	}
	for k, v := range sb.GroupByReport {
		if !printable(k) || !slices.Contains(onlineReported, v) {
			return fmt.Errorf("online.sidebar.group_by_report: %q maps to %q, which is not a reported group_by", k, v)
		}
	}
	if len(sb.GroupBy) != len(onlineClasses) {
		return fmt.Errorf("online.sidebar.group_by: must list exactly the classes %s", strings.Join(onlineClasses, ", "))
	}
	for _, c := range onlineClasses {
		list, ok := sb.GroupBy[c]
		if !ok {
			return fmt.Errorf("online.sidebar.group_by: class %q is missing", c)
		}
		for _, v := range list {
			if !slices.Contains(onlineWritable, v) {
				return fmt.Errorf("online.sidebar.group_by.%s: %q is not a writable set_view group_by", c, v)
			}
		}
	}
	return nil
}

// checkOverrideOnline refuses an override of base that weakens the
// [online] table: it may add always_deny, guarded_servers_extra and
// guarded_tools_extra entries but not remove them (its group_by lists
// are checked by validateOnline like every manifest's).
func checkOverrideOnline(base, over *Manifest) error {
	for _, l := range []struct {
		key        string
		base, next []string
	}{
		{"online.always_deny", base.Online.AlwaysDeny, over.Online.AlwaysDeny},
		{"online.guarded_servers_extra", base.Online.GuardedServersExtra, over.Online.GuardedServersExtra},
		{"online.guarded_tools_extra", base.Online.GuardedToolsExtra, over.Online.GuardedToolsExtra},
	} {
		if missing := removedEntries(l.base, l.next); len(missing) > 0 {
			return fmt.Errorf("%s: removes %s", l.key, strings.Join(missing, ", "))
		}
	}
	return nil
}
