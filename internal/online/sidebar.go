package online

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"

	"golang.org/x/text/unicode/norm"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/model"
)

// Sidebar tools.
const (
	ToolSetView       = "mcp__ccd_sidebar__set_view"
	ToolCreateSection = "mcp__ccd_sidebar__create_section"
	ToolListSections  = "mcp__ccd_sidebar__list_sections"
)

// Section classes.
const (
	ClassSessions = "sessions"
	ClassRoutines = "routines"
	ClassPinned   = "pinned"
	ClassManual   = "manual"
)

// sidebarName is the sidebar provider's name.
const sidebarName = "sidebar"

// sidebar item key prefix of the sidebar-view row.
const sectionItemPrefix = "section:"

// FieldVals are the two sidebar fields a step writes, in reported (tool)
// spelling. nil means no intent (Intended) or unknown (Prior).
type FieldVals struct {
	GroupBy *string `json:"group_by,omitempty"`
	SortBy  *string `json:"sort_by,omitempty"`
}

// SidebarData is the data of a sidebar or section_create step.
type SidebarData struct {
	// Key is builtin:<id> or manual:<NameSHA>.
	Key      string    `json:"key"`
	Class    string    `json:"class"`
	Intended FieldVals `json:"intended"`
	Prior    FieldVals `json:"prior"`
	// Name is the manual section's own name, kept only when sections may
	// be created (6.4); it is passed to the model only inside the
	// create_section input.
	Name string `json:"name,omitempty"`
	// NameInvalid: sections may be created, but the name fails 6.4, so
	// the step has no create step and a section missing live is
	// skipped(not_writable) rather than not_found.
	NameInvalid bool `json:"name_invalid,omitempty"`
}

// unmapped marks a prior stored value that has no reported spelling: it
// never equals a live value, so such a field is only ever found equal or
// skipped, never written blind.
const unmapped = "?unmapped"

var (
	sidebarKeyRE = regexp.MustCompile(`^(builtin:[a-z_]{1,32}|manual:[0-9a-f]{32})$`)
	// toolGroupBy is set_view's group_by enum (A3); toolSortBy its sort.
	toolGroupBy = []string{"date", "project", "state", "custom", "none"}
	toolSortBy  = []string{"recency", "alpha", "created"}
)

// sidebarProvider writes whole-list and section group-by and sort through
// set_view, after one list_sections read (6.3).
type sidebarProvider struct{}

// SidebarProvider returns the sidebar provider.
func SidebarProvider() Provider { return sidebarProvider{} }

func (sidebarProvider) Name() string { return sidebarName }

func (sidebarProvider) Kinds() []Kind { return []Kind{KindSidebar, KindSectionCreate} }

func (sidebarProvider) Tools() ToolPolicy {
	return ToolPolicy{
		Kinds:        []Kind{KindRead, KindSidebar, KindSectionCreate},
		Exact:        []string{ToolSetView, ToolCreateSection},
		Reads:        []string{ToolListSections},
		ValidateData: validateSidebarData,
	}
}

func (sidebarProvider) Questions(*Queue) []Question { return nil }

func (sidebarProvider) Interpret(s Step, r Result) (StepStatus, string) {
	return InterpretBuiltin(s, r)
}

// sectionFields reads the members of a stored section object.
type storedSection struct {
	id, kind, name  string
	groupBy, sortBy *string
	hasName         bool
}

func readStored(key string, v json.RawMessage, sp compat.OnlineSidebarSpec) (storedSection, bool) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(v, &obj) != nil {
		return storedSection{}, false
	}
	str := func(field string) (*string, bool) {
		raw, ok := obj[field]
		if !ok {
			return nil, false
		}
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return nil, true
		}
		return &s, true
	}
	s := storedSection{id: strings.TrimPrefix(key, sectionItemPrefix)}
	if k, _ := str(sp.KindField); k != nil {
		s.kind = *k
	}
	if n, ok := str(sp.NameField); ok && n != nil {
		s.name, s.hasName = *n, true
	}
	s.groupBy, _ = str(sp.GroupByField)
	s.sortBy, _ = str(sp.SortByField)
	return s, true
}

// classify returns the section class and step key of a stored section.
func classify(s storedSection, sp compat.OnlineSidebarSpec) (class, key string, ok bool) {
	switch {
	case slices.Contains(sp.Builtin, s.kind) && s.id == s.kind:
		return s.kind, "builtin:" + s.id, true
	case s.kind != "" && s.kind == sp.ManualKind && s.hasName:
		return ClassManual, "manual:" + NameSHA(s.name), true
	}
	return "", "", false
}

// sections returns the classified sections of a sidebar-view snapshot by
// step key; keys held by two manual sections are listed in dup.
func sections(snap *model.RowSnapshot, sp compat.OnlineSidebarSpec) (map[string]storedSection, map[string]string, map[string]bool) {
	out, classes, dup := map[string]storedSection{}, map[string]string{}, map[string]bool{}
	if snap == nil {
		return out, classes, dup
	}
	keys := make([]string, 0, len(snap.Items))
	for k := range snap.Items {
		if strings.HasPrefix(k, sectionItemPrefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		s, ok := readStored(k, snap.Items[k].Value, sp)
		if !ok {
			continue
		}
		class, key, ok := classify(s, sp)
		if !ok {
			continue
		}
		if _, seen := out[key]; seen {
			dup[key] = true
			continue
		}
		out[key], classes[key] = s, class
	}
	return out, classes, dup
}

// ValidSectionName applies 6.4: NFC, trimmed, 1..max UTF-16 code units,
// no control, format, bidi or line/paragraph separator characters.
func ValidSectionName(name string, maxUnits int) (string, bool) {
	n := norm.NFC.String(strings.TrimSpace(name))
	units := len(utf16.Encode([]rune(n)))
	if units < 1 || units > maxUnits {
		return "", false
	}
	for _, r := range n {
		if unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Bidi_Control, r) ||
			r == '\u2028' || r == '\u2029' || unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r) || unicode.Is(unicode.Variation_Selector, r) {
			return "", false
		}
	}
	return n, true
}

// Build implements Provider (6.3 steps 1-3).
func (sidebarProvider) Build(in BuildInput) ([]Step, []Warning, error) {
	sp := in.Spec.Sidebar
	if !in.Spec.Enabled || in.Config.Reconcile == config.OnlineReconcileOff {
		return nil, nil, nil
	}
	out := in.Outputs[model.RowSidebarView]
	if out == nil || out.Desired == nil {
		return nil, nil, nil
	}
	desired, classes, dup := sections(out.Desired, sp)
	var current map[string]storedSection
	if cur := in.Current[model.RowSidebarView]; cur != nil {
		current, _, _ = sections(cur, sp)
	}
	keys := make([]string, 0, len(desired))
	for k := range desired {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var steps []Step
	notWritable := 0
	read := Step{ID: "r001", Kind: KindRead, Tool: ToolListSections, Input: json.RawMessage(`{}`), Status: StatusPending,
		Needs: []string{NeedSettled}, After: []StepID{}, Expect: Expect{Format: FormatJSONArray}}
	for _, key := range keys {
		if dup[key] {
			continue // two manual sections with one name: no step (ambiguous_name)
		}
		d, class := desired[key], classes[key]
		data := SidebarData{Key: key, Class: class}
		sectionNW := false
		if d.groupBy != nil {
			rep, ok := sp.GroupByReport[*d.groupBy]
			if ok && slices.Contains(sp.GroupBy[class], rep) {
				data.Intended.GroupBy = &rep
				data.Prior.GroupBy = priorGroupBy(current, key, class, sp, in.Current[model.RowSidebarView] != nil)
			} else {
				sectionNW = true
			}
		}
		if d.sortBy != nil {
			if slices.Contains(sp.SortBy, *d.sortBy) && slices.Contains(toolSortBy, *d.sortBy) {
				v := *d.sortBy
				data.Intended.SortBy = &v
				data.Prior.SortBy = priorSortBy(current, key, sp)
			} else {
				sectionNW = true
			}
		}
		if sectionNW {
			notWritable++
		}
		// A field whose known prior already equals the intent cannot
		// change anything: the live check would find it equal (done) or
		// changed elsewhere (skipped). Dropping it keeps a switch without
		// sidebar changes from publishing a queue (ADR 0005).
		if data.Intended.GroupBy != nil && data.Prior.GroupBy != nil && *data.Prior.GroupBy == *data.Intended.GroupBy {
			data.Intended.GroupBy, data.Prior.GroupBy = nil, nil
		}
		if data.Intended.SortBy != nil && data.Prior.SortBy != nil && *data.Prior.SortBy == *data.Intended.SortBy {
			data.Intended.SortBy, data.Prior.SortBy = nil, nil
		}
		if data.Intended.GroupBy == nil && data.Intended.SortBy == nil {
			continue
		}
		local := StepID(fmt.Sprintf("s%03d", len(steps)))
		st := Step{ID: local, Kind: KindSidebar, Tool: ToolSetView, Templated: true, Status: StatusPending,
			Needs: []string{NeedSettled, NeedReplica, needRead("r001")}, After: []StepID{"r001"},
			Expect: Expect{Format: FormatJSONObject, Refusals: slices.Clone(sp.Refusals)}}
		var create *Step
		if class == ClassManual && in.Config.CreateSections {
			if name, ok := ValidSectionName(d.name, sp.MaxNameUnits); ok {
				data.Name = name
				input, err := json.Marshal(map[string]string{"name": name})
				if err != nil {
					return nil, nil, err
				}
				cid := StepID(fmt.Sprintf("n%03d", len(steps)))
				cdata, err := json.Marshal(SidebarData{Key: key, Class: class})
				if err != nil {
					return nil, nil, err
				}
				create = &Step{ID: cid, Kind: KindSectionCreate, Tool: ToolCreateSection, Input: input, Status: StatusDormant,
					Needs: []string{NeedSettled, NeedReplica, needRead("r001")}, After: []StepID{"r001"},
					Expect: Expect{Format: FormatJSONObject}, Data: cdata}
				st.After = append(st.After, cid)
			} else {
				data.NameInvalid = true
				notWritable++
			}
		}
		b, err := json.Marshal(data)
		if err != nil {
			return nil, nil, err
		}
		st.Data = b
		if create != nil {
			steps = append(steps, *create)
		}
		steps = append(steps, st)
	}
	var warns []Warning
	if notWritable > 0 {
		warns = append(warns, Warning{Msg: string(i18n.OnlineWarnSidebarNotWritable), Text: i18n.T(i18n.OnlineWarnSidebarNotWritable, notWritable)})
	}
	if len(steps) == 0 {
		return nil, warns, nil
	}
	return append([]Step{read}, steps...), warns, nil
}

// priorGroupBy is T's value of a section's group_by in reported spelling:
// absent in T is "none" for manual, routines and pinned (what
// list_sections reports) and unknown for sessions; T unknown is unknown.
func priorGroupBy(cur map[string]storedSection, key, class string, sp compat.OnlineSidebarSpec, known bool) *string {
	if !known {
		return nil
	}
	s, ok := cur[key]
	if !ok {
		return nil
	}
	if s.groupBy == nil {
		if class == ClassSessions {
			return nil
		}
		v := "none"
		return &v
	}
	rep, ok := sp.GroupByReport[*s.groupBy]
	if !ok {
		v := unmapped
		return &v
	}
	return &rep
}

// priorSortBy is T's sort_by of a section, nil when unknown.
func priorSortBy(cur map[string]storedSection, key string, sp compat.OnlineSidebarSpec) *string {
	s, ok := cur[key]
	if !ok || s.sortBy == nil {
		return nil
	}
	if !slices.Contains(sp.SortBy, *s.sortBy) {
		v := unmapped
		return &v
	}
	v := *s.sortBy
	return &v
}

// validateSidebarData checks the data of sidebar and section_create steps.
func validateSidebarData(s Step) error {
	if s.Kind == KindRead {
		if len(s.Data) > 0 {
			return fmt.Errorf("read step with data")
		}
		return nil
	}
	var d SidebarData
	if err := json.Unmarshal(s.Data, &d); err != nil {
		return err
	}
	if !sidebarKeyRE.MatchString(d.Key) {
		return fmt.Errorf("key")
	}
	if !slices.Contains([]string{ClassSessions, ClassRoutines, ClassPinned, ClassManual}, d.Class) {
		return fmt.Errorf("class")
	}
	if strings.HasPrefix(d.Key, "manual:") != (d.Class == ClassManual) {
		return fmt.Errorf("key and class disagree")
	}
	if d.Name != "" {
		if n, ok := ValidSectionName(d.Name, 200); !ok || n != d.Name || NameSHA(n) != strings.TrimPrefix(d.Key, "manual:") {
			return fmt.Errorf("name")
		}
	}
	if d.NameInvalid && (d.Name != "" || d.Class != ClassManual || s.Kind != KindSidebar) {
		return fmt.Errorf("name_invalid")
	}
	if s.Kind == KindSectionCreate {
		return nil
	}
	check := func(v *string, set []string, prior bool) error {
		if v == nil {
			return nil
		}
		if prior && (*v == unmapped || reportedGroupBy[*v] || reportedSortBy[*v]) {
			return nil
		}
		if !slices.Contains(set, *v) {
			return fmt.Errorf("field value")
		}
		return nil
	}
	gb := []string{"date", "project", "state", "none"}
	for _, e := range []error{check(d.Intended.GroupBy, gb, false), check(d.Intended.SortBy, toolSortBy, false),
		check(d.Prior.GroupBy, gb, true), check(d.Prior.SortBy, toolSortBy, true)} {
		if e != nil {
			return e
		}
	}
	if d.Intended.GroupBy == nil && d.Intended.SortBy == nil {
		return fmt.Errorf("no intended field")
	}
	return nil
}

// sidebarSteps returns the read step and the sidebar and create steps of
// q (all of this provider's steps).
func sidebarRead(q *Queue) *Step {
	for i := range q.Steps {
		if s := &q.Steps[i]; s.Kind == KindRead && s.Tool == ToolListSections {
			return s
		}
	}
	return nil
}

// Advance implements Provider (6.3 step 4).
func (sidebarProvider) Advance(q *Queue, results map[StepID]Result, _ time.Time) error {
	read := sidebarRead(q)
	if read == nil {
		return nil
	}
	switch read.Status {
	case StatusFailed, StatusSkipped:
		for i := range q.Steps {
			s := &q.Steps[i]
			if (s.Kind == KindSidebar || s.Kind == KindSectionCreate) && (s.Status == StatusPending || s.Status == StatusDormant) {
				s.Status, s.Reason = StatusSkipped, ReasonNotFound
			}
		}
		return nil
	case StatusDone:
	default:
		return nil
	}
	var live SectionsParsed
	if r, ok := results[read.ID]; !ok || r.Outcome != OutcomeOK || json.Unmarshal(r.Parsed, &live) != nil {
		return nil
	}
	for i := range q.Steps {
		s := &q.Steps[i]
		if s.Kind != KindSidebar || (s.Status != StatusPending && s.Status != StatusDormant) {
			continue
		}
		var d SidebarData
		if json.Unmarshal(s.Data, &d) != nil {
			s.Status, s.Reason = StatusSkipped, ReasonNotWritable
			continue
		}
		advanceSidebarStep(q, s, d, live.Sections, results)
	}
	return nil
}

// createOf returns the section_create step a sidebar step waits for.
func createOf(q *Queue, s *Step) *Step {
	for _, a := range s.After {
		if c := q.Step(a); c != nil && c.Kind == KindSectionCreate {
			return c
		}
	}
	return nil
}

func advanceSidebarStep(q *Queue, s *Step, d SidebarData, live []LiveSection, results map[StepID]Result) {
	create := createOf(q, s)
	var found []LiveSection
	for _, ls := range live {
		switch {
		case strings.HasPrefix(d.Key, "builtin:") && ls.ID == strings.TrimPrefix(d.Key, "builtin:"):
			found = append(found, ls)
		case strings.HasPrefix(d.Key, "manual:") && ls.Kind == ClassManual && ls.NameSHA == strings.TrimPrefix(d.Key, "manual:"):
			found = append(found, ls)
		}
	}
	if len(found) > 1 {
		s.Status, s.Reason = StatusSkipped, ReasonAmbiguousName
		if create != nil && create.Status == StatusDormant {
			create.Status, create.Reason = StatusSkipped, ""
		}
		return
	}
	if len(found) == 0 {
		if create == nil {
			s.Status, s.Reason = StatusSkipped, ReasonNotFound
			if d.NameInvalid {
				s.Reason = ReasonNotWritable // 6.4: the name cannot be created
			}
			return
		}
		switch create.Status {
		case StatusDormant:
			create.Status = StatusPending
			return
		case StatusPending, StatusOffered:
			return
		case StatusDone:
			var p IDParsed
			if r, ok := results[create.ID]; !ok || json.Unmarshal(r.Parsed, &p) != nil || !sectionIDRE.MatchString(p.ID) {
				s.Status, s.Reason = StatusSkipped, ReasonNotFound
				return
			}
			fillSetView(s, d, p.ID, d.Intended)
			return
		default:
			s.Status, s.Reason = StatusSkipped, ReasonNotFound
			return
		}
	}
	ls := found[0]
	if create != nil && create.Status == StatusDormant {
		create.Status, create.Reason = StatusSkipped, "" // retired
	}
	var write FieldVals
	equal, changed, notWritable := 0, 0, 0
	decide := func(intended, prior, liveV *string, sortField bool) *string {
		if intended == nil {
			return nil
		}
		switch {
		case liveV == nil:
			changed++
		case sortField && *liveV == userOrder:
			notWritable++
		case *liveV == *intended:
			equal++
		case prior == nil || *liveV == *prior:
			v := *intended
			return &v
		default:
			changed++
		}
		return nil
	}
	write.GroupBy = decide(d.Intended.GroupBy, d.Prior.GroupBy, ls.GroupBy, false)
	write.SortBy = decide(d.Intended.SortBy, d.Prior.SortBy, ls.SortBy, true)
	if write.GroupBy == nil && write.SortBy == nil {
		switch {
		case changed > 0:
			s.Status, s.Reason = StatusSkipped, ReasonChangedElsewhere
		case notWritable > 0:
			s.Status, s.Reason = StatusSkipped, ReasonNotWritable
		default:
			s.Status, s.Reason = StatusDone, ""
		}
		return
	}
	fillSetView(s, d, ls.ID, write)
}

// fillSetView sets a sidebar step's input: the whole list (sessions)
// without section_id, other sections with it; only the fields to write.
func fillSetView(s *Step, d SidebarData, liveID string, w FieldVals) {
	in := map[string]string{}
	if d.Class != ClassSessions {
		in["section_id"] = liveID
	}
	if w.GroupBy != nil {
		in["group_by"] = *w.GroupBy
	}
	if w.SortBy != nil {
		in["sort_by"] = *w.SortBy
	}
	b, err := json.Marshal(in)
	if err != nil {
		return
	}
	c, err := model.Canonical(b)
	if err != nil {
		return
	}
	s.Input = c
	if s.Status == StatusDormant {
		s.Status = StatusPending
	}
}

// SidebarMap renders the stored -> reported -> writable table per class
// of a manifest's [online.sidebar] (the sidebar_map golden).
func SidebarMap(sp compat.OnlineSidebarSpec) string {
	stored := make([]string, 0, len(sp.GroupByReport))
	for k := range sp.GroupByReport {
		stored = append(stored, k)
	}
	sort.Strings(stored)
	classes := []string{ClassSessions, ClassRoutines, ClassPinned, ClassManual}
	var b strings.Builder
	fmt.Fprintf(&b, "%-18s %-12s %s\n", "stored", "reported", strings.Join(classes, " "))
	for _, st := range stored {
		rep := sp.GroupByReport[st]
		var cols []string
		for _, c := range classes {
			w := "-"
			if slices.Contains(sp.GroupBy[c], rep) && slices.Contains(toolGroupBy, rep) && rep != "custom" {
				w = "write"
			}
			cols = append(cols, fmt.Sprintf("%-*s", len(c), w))
		}
		fmt.Fprintf(&b, "%-18s %-12s %s\n", st, rep, strings.TrimRight(strings.Join(cols, " "), " "))
	}
	return b.String()
}
