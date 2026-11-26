package online

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// maxJSONDepth bounds the nesting of every decoded tool response.
const maxJSONDepth = 32

// maxSections bounds the sections kept from a list_sections answer.
const maxSections = 500

// ExtractBlocks returns the text blocks of an MCP tool result and whether
// it was marked as an error. Accepted shapes: a JSON string; an array of
// blocks [{"type":"text","text":...}]; an object with a "content" array
// of such blocks (and an optional "isError"). Non-text blocks are
// ignored. Any other shape returns ok=false. The "_meta" member (it holds
// the session title) is never read.
func ExtractBlocks(resp json.RawMessage) (blocks []string, isError bool, ok bool) {
	resp = bytes.TrimSpace(resp)
	if len(resp) == 0 || depthExceeded(resp) {
		return nil, false, false
	}
	switch resp[0] {
	case '"':
		var s string
		if json.Unmarshal(resp, &s) != nil {
			return nil, false, false
		}
		return []string{s}, false, true
	case '[':
		bs, ok := textBlocks(resp)
		return bs, false, ok
	case '{':
		var obj struct {
			Content json.RawMessage `json:"content"`
			IsError *bool           `json:"isError"`
		}
		if json.Unmarshal(resp, &obj) != nil || len(obj.Content) == 0 {
			return nil, false, false
		}
		bs, ok := textBlocks(obj.Content)
		if !ok {
			return nil, false, false
		}
		return bs, obj.IsError != nil && *obj.IsError, true
	}
	return nil, false, false
}

// textBlocks decodes an array of content blocks and keeps the text ones.
func textBlocks(raw json.RawMessage) ([]string, bool) {
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) != nil {
		return nil, false
	}
	var out []string
	for _, e := range arr {
		var b struct {
			Type string  `json:"type"`
			Text *string `json:"text"`
		}
		if json.Unmarshal(e, &b) != nil {
			return nil, false
		}
		if b.Type == "text" && b.Text != nil {
			out = append(out, *b.Text)
		}
	}
	return out, true
}

// ExtractText returns the concatenated text blocks of a tool result
// (ExtractBlocks joined).
func ExtractText(resp json.RawMessage) (text string, isError bool, ok bool) {
	bs, isErr, ok := ExtractBlocks(resp)
	if !ok {
		return "", false, false
	}
	return strings.Join(bs, ""), isErr, true
}

// depthExceeded reports JSON nesting deeper than maxJSONDepth (or bytes
// that are not one JSON value).
func depthExceeded(b []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(b))
	depth := 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return false
		}
		if err != nil {
			return true
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{', '[':
				depth++
				if depth > maxJSONDepth {
					return true
				}
			default:
				depth--
			}
		}
	}
}

// firstJSON decodes the first JSON value of text with UseNumber. Bytes
// after it must be empty or start with "\n\n" (the app appends a note).
func firstJSON(text string) (any, bool) {
	trimmed := strings.TrimLeft(text, " \t\r\n")
	if trimmed == "" {
		return nil, false
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	rest := trimmed[dec.InputOffset():]
	if rest != "" && !strings.HasPrefix(rest, "\n\n") {
		return nil, false
	}
	if depthExceeded([]byte(trimmed[:dec.InputOffset()])) {
		return nil, false
	}
	return v, true
}

// Reported group-by and sort spellings a tool may answer with.
var (
	reportedGroupBy = map[string]bool{"date": true, "project": true, "environment": true, "state": true, "custom": true, "none": true}
	reportedSortBy  = map[string]bool{"recency": true, "alpha": true, "created": true, userOrder: true}
	sectionIDRE     = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	sectionKindRE   = regexp.MustCompile(`^[a-z_]{1,32}$`)
	notFoundRE      = regexp.MustCompile(`^Session "local_[0-9a-f-]{36}" not found\.`)
)

// userOrder is the sort_by list_sections reports for hand-ordered rows.
const userOrder = "user_order"

// NameSHA is the stored form of a section name: hex sha256 of the
// trimmed NFC name, first 32 hex digits.
func NameSHA(name string) string {
	return hashHex([]byte(norm.NFC.String(strings.TrimSpace(name))))[:32]
}

// Tool name suffixes with a built-in parser.
const (
	toolSetPinned     = "set_pinned"
	toolSetView       = "set_view"
	toolCreateSection = "create_section"
	toolListSections  = "list_sections"
	toolGetSession    = "get_session"
)

// toolSuffix returns the name after the server ("set_view").
func toolSuffix(tool string) string {
	if i := strings.LastIndex(tool, "__"); i >= 0 {
		return tool[i+2:]
	}
	return tool
}

// RecordInput is what Recognize needs besides the response.
type RecordInput struct {
	Step Step
	// ToolsOff are the texts that mean "sidebar tools are off"
	// (manifest online.responses.tools_off, copied into the queue).
	ToolsOff []string
}

// Recognize turns a tool response into an outcome, an optional verdict
// from a fixed text (9.3 rules 1-3) and validated parsed fields (9.3).
// Nothing of the raw text is kept.
func Recognize(in RecordInput, resp json.RawMessage) (Outcome, *Verdict, json.RawMessage) {
	text, isErr, ok := ExtractText(resp)
	if !ok {
		return OutcomeUnrecognized, nil, nil
	}
	for _, t := range in.ToolsOff {
		if t != "" && strings.HasPrefix(text, t) {
			return OutcomeError, &Verdict{StatusFailed, ReasonToolsOff}, nil
		}
	}
	for _, p := range in.Step.Expect.Unknown {
		if p != "" && strings.HasPrefix(text, p) {
			return OutcomeError, &Verdict{StatusFailed, ReasonUnknownOutcome}, nil
		}
	}
	for _, p := range in.Step.Expect.Refusals {
		if p != "" && strings.HasPrefix(text, p) {
			return OutcomeError, &Verdict{StatusSkipped, ReasonNotWritable}, nil
		}
	}
	// The app's not-found, archived and scheduled-run answers are plain
	// error texts. A JSON answer is never checked for them: it can hold
	// text the user controls (a session title, a section name).
	if _, isJSON := firstJSON(text); isErr || !isJSON {
		switch {
		case notFoundRE.MatchString(text):
			return OutcomeError, &Verdict{StatusSkipped, ReasonNotFound}, nil
		case strings.Contains(text, "is archived;") || strings.Contains(text, "is a scheduled task run."):
			return OutcomeError, &Verdict{StatusSkipped, ReasonNotWritable}, nil
		}
	}
	if isErr {
		return OutcomeError, nil, nil
	}
	if in.Step.Expect.Format == FormatText {
		for i, okText := range in.Step.Expect.OK {
			if text == okText {
				return OutcomeOK, nil, mustJSON(map[string]int{"matched": i})
			}
		}
		return OutcomeUnrecognized, nil, nil
	}
	var parsed json.RawMessage
	switch toolSuffix(in.Step.Tool) {
	case toolSetView:
		parsed, ok = parseSetView(text)
	case toolCreateSection:
		parsed, ok = parseCreateSection(text)
	case toolListSections:
		parsed, ok = parseListSections(text)
	case toolGetSession:
		parsed, ok = parseGetSession(text)
	default:
		ok = false
	}
	if !ok {
		return OutcomeUnrecognized, nil, nil
	}
	return OutcomeOK, nil, parsed
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// SetViewParsed is the validated part of a set_view answer.
type SetViewParsed struct {
	Changed      int    `json:"changed"`
	GroupBy      string `json:"group_by,omitempty"`
	SortBy       string `json:"sort_by,omitempty"`
	SectionShown *bool  `json:"section_shown,omitempty"`
}

func parseSetView(text string) (json.RawMessage, bool) {
	v, ok := firstJSON(text)
	obj, isObj := v.(map[string]any)
	if !ok || !isObj {
		return nil, false
	}
	view, ok := obj["view"].(map[string]any)
	if !ok {
		return nil, false
	}
	var p SetViewParsed
	if n, ok := obj["changed"].(json.Number); ok {
		if i, err := n.Int64(); err == nil && i >= 0 && i < 1<<31 {
			p.Changed = int(i)
		}
	}
	if g, ok := view["group_by"].(string); ok && reportedGroupBy[g] {
		p.GroupBy = g
	}
	if s, ok := view["sort_by"].(string); ok && reportedSortBy[s] {
		p.SortBy = s
	}
	if b, ok := obj["section_shown"].(bool); ok {
		p.SectionShown = &b
	}
	return mustJSON(p), true
}

// IDParsed is the validated part of create_section and get_session.
type IDParsed struct {
	ID string `json:"id"`
}

func parseCreateSection(text string) (json.RawMessage, bool) {
	v, ok := firstJSON(text)
	obj, isObj := v.(map[string]any)
	if !ok || !isObj {
		return nil, false
	}
	id, _ := obj["id"].(string)
	kind, _ := obj["kind"].(string)
	if !sectionIDRE.MatchString(id) || kind != "manual" {
		return nil, false
	}
	return mustJSON(IDParsed{ID: id}), true
}

func parseGetSession(text string) (json.RawMessage, bool) {
	v, ok := firstJSON(text)
	obj, isObj := v.(map[string]any)
	if !ok || !isObj {
		return nil, false
	}
	id, _ := obj["sessionId"].(string)
	if !ValidLocalSession(id) {
		return nil, false
	}
	return mustJSON(IDParsed{ID: id}), true
}

// LiveSection is one validated list_sections entry.
type LiveSection struct {
	ID      string  `json:"id"`
	Kind    string  `json:"kind"`
	NameSHA string  `json:"name_sha,omitempty"`
	GroupBy *string `json:"group_by"`
	SortBy  *string `json:"sort_by"`
	Shown   *bool   `json:"shown,omitempty"`
}

// SectionsParsed is the validated part of a list_sections answer.
type SectionsParsed struct {
	Sections []LiveSection `json:"sections"`
}

func parseListSections(text string) (json.RawMessage, bool) {
	v, ok := firstJSON(text)
	arr, isArr := v.([]any)
	if !ok || !isArr {
		return nil, false
	}
	out := SectionsParsed{Sections: []LiveSection{}}
	for _, e := range arr {
		if len(out.Sections) >= maxSections {
			break
		}
		obj, ok := e.(map[string]any)
		if !ok {
			continue
		}
		id, _ := obj["id"].(string)
		kind, _ := obj["kind"].(string)
		if !sectionIDRE.MatchString(id) || !sectionKindRE.MatchString(kind) {
			continue
		}
		ls := LiveSection{ID: id, Kind: kind}
		if name, ok := obj["name"].(string); ok && kind == "manual" {
			ls.NameSHA = NameSHA(name)
		}
		if g, ok := obj["group_by"].(string); ok && reportedGroupBy[g] {
			ls.GroupBy = &g
		}
		if s, ok := obj["sort_by"].(string); ok && reportedSortBy[s] {
			ls.SortBy = &s
		}
		if b, ok := obj["shown"].(bool); ok {
			ls.Shown = &b
		}
		out.Sections = append(out.Sections, ls)
	}
	return mustJSON(out), true
}

// InterpretBuiltin turns an ok result of a built-in tool into the step's
// status: set_view must report every expected field; every other
// recognised answer is done.
func InterpretBuiltin(s Step, r Result) (StepStatus, string) {
	if r.Outcome != OutcomeOK {
		return StatusFailed, ReasonAppRefused
	}
	if toolSuffix(s.Tool) != toolSetView {
		return StatusDone, ""
	}
	var p SetViewParsed
	if json.Unmarshal(r.Parsed, &p) != nil {
		return StatusFailed, ReasonMismatch
	}
	want := map[string]string{}
	for k, v := range s.Expect.Fields {
		want[k] = v
	}
	// A templated set_view step expects what its input writes (the
	// expectation is derived, so the immutable projection stays fixed).
	var in map[string]string
	if json.Unmarshal(s.Input, &in) == nil {
		if v, ok := in["group_by"]; ok {
			want["view.group_by"] = v
		}
		if v, ok := in["sort_by"]; ok {
			want["view.sort_by"] = v
		}
	}
	for path, want := range want {
		got := ""
		switch path {
		case "view.group_by":
			got = p.GroupBy
		case "view.sort_by":
			got = p.SortBy
		}
		if got != want {
			return StatusFailed, ReasonMismatch
		}
	}
	return StatusDone, ""
}
