package artifacts

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/online"
)

// Step roles (5.3).
const (
	RoleInvPage = "inv_page"
	RoleGet     = "get"
	RoleFiles   = "files"
	RoleDecl    = "decl"
	RoleChunk   = "chunk"
	RoleGrant   = "grant"
	RoleUpdate  = "update"
	RoleCreate  = "create"
	RoleUpdateV = "update_v"
	RoleCreateV = "create_v"
)

// publishRoles are the roles that change an account.
var publishRoles = map[string]bool{RoleGrant: true, RoleUpdate: true, RoleCreate: true, RoleUpdateV: true, RoleCreateV: true}

// maxDepth bounds the nesting of every decoded result.
const maxDepth = 32

// maxListItems bounds the entries of one list page.
const maxListItems = 500

// cursorRE validates a list cursor (at most 1024 bytes, validCursor).
var cursorRE = regexp.MustCompile(`^[A-Za-z0-9._~:=+/-]+$`)

// validCursor reports a list cursor of the accepted form.
func validCursor(s string) bool { return len(s) <= 1024 && cursorRE.MatchString(s) }

// Knobs are the manifest values a role's parser needs (5.5). They are
// copied into each step's data, so hooks never load a manifest.
type Knobs struct {
	Fields         map[string][]string `json:"fields,omitempty"`
	OwnerMine      string              `json:"owner_mine,omitempty"`
	Refusals       map[string][]string `json:"refusals,omitempty"`
	OthersMarker   string              `json:"others_marker,omitempty"`
	TagPattern     string              `json:"wrapper_tag_pattern,omitempty"`
	Forms          []string            `json:"wrapper_forms,omitempty"`
	Reminders      []string            `json:"reminder_prefixes,omitempty"`
	VersionPattern string              `json:"version_pattern,omitempty"`
	MaxContent     int64               `json:"max_content_bytes,omitempty"`
}

// roleFields are the field keys each role reads.
var roleFields = map[string][]string{
	RoleInvPage: {"list_items", "next_cursor", "slug", "version", "updated_at", "soft_deleted", "owner", "mine_bool", "title", "url", "notice"},
	RoleGet:     {"slug", "version", "read", "icon", "title", "notice"},
	RoleFiles:   {"files_list", "file_path"},
	RoleDecl:    {"capabilities"},
	RoleChunk:   {"slug", "version", "total_lines", "offset", "next_offset", "notice", "title", "owned", "summary_files"},
	RoleUpdate:  {"slug", "url", "version"},
	RoleCreate:  {"slug", "url", "version"},
	RoleUpdateV: {"slug", "url", "version"},
	RoleCreateV: {"slug", "url", "version"},
}

// KnobsFor returns the knobs of role from the manifest table.
func KnobsFor(role string, spec compat.ArtifactsSpec) Knobs {
	k := Knobs{OthersMarker: spec.OthersMarker, VersionPattern: spec.VersionPattern, Reminders: spec.ReminderPrefixes}
	if len(spec.Refusals) > 0 {
		k.Refusals = map[string][]string{}
		for r, l := range spec.Refusals {
			if len(l) > 0 {
				k.Refusals[r] = append([]string(nil), l...)
			}
		}
		if len(k.Refusals) == 0 {
			k.Refusals = nil
		}
	}
	all := map[string][]string{}
	for _, l := range spec.Fields.Lists() {
		all[l.Key] = l.Names
	}
	if keys := roleFields[role]; len(keys) > 0 {
		k.Fields = map[string][]string{}
		for _, key := range keys {
			k.Fields[key] = append([]string(nil), all[key]...)
		}
	}
	if role == RoleInvPage {
		k.OwnerMine = spec.Fields.OwnerMine
	}
	if role == RoleChunk || role == RoleInvPage || role == RoleGet {
		k.TagPattern = spec.WrapperTagPattern
	}
	if role == RoleChunk {
		k.Forms = append([]string(nil), spec.WrapperForms...)
		k.MaxContent = spec.MaxContentBytes
	}
	return k
}

func (k Knobs) names(key string) []string { return k.Fields[key] }

func (k Knobs) versionRE() *regexp.Regexp {
	if k.VersionPattern != "" {
		if r, err := regexp.Compile(k.VersionPattern); err == nil {
			return r
		}
	}
	return defaultVersionRE
}

func (k Knobs) validVersion(s string) bool {
	return len(s) <= 128 && s != "" && k.versionRE().MatchString(s)
}

func (k Knobs) tagRE() *regexp.Regexp {
	if k.TagPattern != "" {
		if r, err := regexp.Compile(k.TagPattern); err == nil {
			return r
		}
	}
	return defaultTagRE
}

// StepData is the data of an artifact step (5.5): role facts and parse
// knobs. It is part of the immutable projection.
type StepData struct {
	Role     string `json:"role"`
	Pair     PairID `json:"pair,omitempty"`
	Slug     Slug   `json:"slug,omitempty"`
	Page     int    `json:"page,omitempty"`
	Limit    int    `json:"limit,omitempty"`
	Offset   int    `json:"offset,omitempty"`
	BlobSHA  string `json:"blob_sha,omitempty"`
	Verify   bool   `json:"verify,omitempty"`
	Conflict bool   `json:"conflict,omitempty"`
	Title    bool   `json:"title,omitempty"`
	Parse    Knobs  `json:"parse"`
}

// DecodeStepData decodes a step's data.
func DecodeStepData(raw json.RawMessage) (StepData, error) {
	var d StepData
	if len(raw) == 0 {
		return d, errors.New("artifacts: step without data")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return d, err
	}
	return d, nil
}

// lookup returns the first of names present in obj.
func lookup(obj map[string]any, names []string) (any, bool) {
	for _, n := range names {
		if v, ok := obj[n]; ok {
			return v, true
		}
	}
	return nil, false
}

// lookupString returns a string field; present reports the key.
func lookupString(obj map[string]any, names []string) (s string, present, ok bool) {
	v, present := lookup(obj, names)
	if !present {
		return "", false, false
	}
	s, ok = v.(string)
	return s, true, ok
}

// lookupInt returns an integer field.
func lookupInt(obj map[string]any, names []string) (n int64, present, ok bool) {
	v, present := lookup(obj, names)
	if !present {
		return 0, false, false
	}
	if v == nil {
		return 0, true, false
	}
	num, isNum := v.(json.Number)
	if !isNum {
		return 0, true, false
	}
	n, err := num.Int64()
	return n, true, err == nil
}

// depthOK reports JSON nesting of at most maxDepth.
func depthOK(b []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(b))
	depth := 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return true
		}
		if err != nil {
			return false
		}
		if d, ok := tok.(json.Delim); ok {
			if d == '{' || d == '[' {
				depth++
				if depth > maxDepth {
					return false
				}
			} else {
				depth--
			}
		}
	}
}

// firstValue decodes the first JSON value of text (UseNumber, depth at
// most 32) and returns the rest after it.
func firstValue(text string) (any, string, bool) {
	trimmed := strings.TrimLeft(text, " \t\r\n")
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return nil, "", false
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, "", false
	}
	off := dec.InputOffset()
	if !depthOK([]byte(trimmed[:off])) {
		return nil, "", false
	}
	return v, trimmed[off:], true
}

// tailOK reports a tail that is whitespace, or whitespace followed by a
// listed reminder.
func tailOK(rest string, reminders []string) bool {
	rest = strings.TrimLeft(rest, " \t\r\n")
	if rest == "" {
		return true
	}
	for _, p := range reminders {
		if p != "" && strings.HasPrefix(rest, p) {
			return true
		}
	}
	return false
}

// decodeObject decodes the first JSON object of text with a valid tail.
func decodeObject(text string, k Knobs) (map[string]any, bool) {
	v, rest, ok := firstValue(text)
	if !ok || !tailOK(rest, k.Reminders) {
		return nil, false
	}
	obj, ok := v.(map[string]any)
	return obj, ok
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// errParsed is the parsed form of a tool error.
type errParsed struct {
	Reason string `json:"reason"`
}

// ParseResult turns a tool response into a validated parsed result for
// a step of role d.Role (5.6). Raw text is never stored.
func ParseResult(d StepData, resp json.RawMessage) (online.Outcome, json.RawMessage) {
	blocks, isError, ok := online.ExtractBlocks(resp)
	if !ok {
		return online.OutcomeUnrecognized, nil
	}
	others := false
	if len(blocks) > 0 && d.Parse.OthersMarker != "" && strings.HasPrefix(blocks[0], d.Parse.OthersMarker) {
		others = true
		blocks[0] = strings.TrimPrefix(blocks[0], d.Parse.OthersMarker)
		if strings.TrimSpace(blocks[0]) == "" {
			blocks = blocks[1:]
		}
	}
	text := strings.Join(blocks, "\n")
	if isError {
		return online.OutcomeError, mustJSON(errParsed{Reason: refusalReason(text, d.Parse.Refusals)})
	}
	var parsed any
	switch d.Role {
	case RoleInvPage:
		parsed, ok = parseListPage(text, d.Parse)
	case RoleGet:
		parsed, ok = parseGet(text, d, others)
	case RoleFiles:
		parsed, ok = parseFiles(text, d.Parse)
	case RoleDecl:
		parsed, ok = parseDecl(text, d.Parse, others)
	case RoleChunk:
		c, err := ParseChunk(text, ChunkStep{Slug: d.Slug, Offset: d.Offset}, d.Parse)
		if err != nil {
			if errors.Is(err, ErrNotOwned) {
				return online.OutcomeError, mustJSON(errParsed{Reason: "not_owned"})
			}
			return online.OutcomeUnrecognized, nil
		}
		c.Others = others
		parsed, ok = c, true
	case RoleGrant:
		parsed, ok = struct{}{}, true
	case RoleUpdate, RoleCreate, RoleUpdateV, RoleCreateV:
		parsed, ok = parsePublish(text, d)
	default:
		ok = false
	}
	if !ok {
		return online.OutcomeUnrecognized, nil
	}
	return online.OutcomeOK, mustJSON(parsed)
}

// refusalReason maps an error text to its reason: the first listed
// prefix that matches, else app_refused.
func refusalReason(text string, refusals map[string][]string) string {
	t := strings.TrimLeft(text, " \t\r\n")
	for _, r := range compat.ArtifactRefusalReasons {
		for _, p := range refusals[r] {
			if p != "" && strings.HasPrefix(t, p) {
				return r
			}
		}
	}
	return "app_refused"
}

// ListPage is the parsed form of a list_artifacts page.
type ListPage struct {
	Items         []InvItem `json:"items"`
	NextCursor    *string   `json:"next_cursor"`
	Invalid       int       `json:"invalid"`
	CursorInvalid bool      `json:"cursor_invalid,omitempty"`
	Shared        int       `json:"shared,omitempty"`
}

// noticeTag returns the one wrapper tag a notice names ("" when none or
// several).
func noticeTag(notice string, re *regexp.Regexp) (string, int) {
	found := map[string]bool{}
	for _, m := range tagCandidateRE.FindAllString(notice, -1) {
		if re.MatchString(m) {
			found[m] = true
		}
	}
	if len(found) != 1 {
		return "", len(found)
	}
	for t := range found {
		return t, 1
	}
	return "", 0
}

// tagCandidateRE finds wrapper-like names in a notice.
var tagCandidateRE = regexp.MustCompile(`untrusted-document-[0-9A-Za-z]+`)

func parseListPage(text string, k Knobs) (ListPage, bool) {
	obj, ok := decodeObject(text, k)
	if !ok {
		return ListPage{}, false
	}
	raw, present := lookup(obj, k.names("list_items"))
	arr, isArr := raw.([]any)
	if !present || !isArr || len(arr) > maxListItems {
		return ListPage{}, false
	}
	tag := ""
	if n, present, ok := lookupString(obj, k.names("notice")); present && ok {
		tag, _ = noticeTag(n, k.tagRE())
	}
	page := ListPage{Items: []InvItem{}}
	for _, e := range arr {
		it, mine, ok := parseListItem(e, k, tag)
		switch {
		case !ok:
			page.Invalid++
		case !mine:
			page.Shared++
		default:
			page.Items = append(page.Items, it)
		}
	}
	if v, present := lookup(obj, k.names("next_cursor")); present && v != nil {
		s, isStr := v.(string)
		if isStr && validCursor(s) {
			page.NextCursor = &s
		} else {
			page.CursorInvalid = true
		}
	}
	return page, true
}

func parseListItem(e any, k Knobs, tag string) (InvItem, bool, bool) {
	obj, ok := e.(map[string]any)
	if !ok {
		return InvItem{}, false, false
	}
	slug, _, ok := lookupString(obj, k.names("slug"))
	if !ok || !ValidSlug(slug) {
		return InvItem{}, false, false
	}
	mine := false
	if owner, present, isStr := lookupString(obj, k.names("owner")); present {
		if !isStr {
			return InvItem{}, false, false
		}
		mine = owner == k.OwnerMine
	} else if v, present := lookup(obj, k.names("mine_bool")); present {
		b, isBool := v.(bool)
		if !isBool {
			return InvItem{}, false, false
		}
		mine = b
	} else {
		return InvItem{}, false, false
	}
	if !mine {
		return InvItem{}, false, true
	}
	it := InvItem{Slug: Slug(slug), Mine: true}
	v, _, ok := lookupString(obj, k.names("version"))
	if !ok || !k.validVersion(v) {
		return InvItem{}, false, false
	}
	it.Version = VersionID(v)
	if s, present, isStr := lookupString(obj, k.names("updated_at")); present && s != "" {
		t, err := time.Parse(time.RFC3339Nano, s)
		if !isStr || err != nil {
			return InvItem{}, false, false
		}
		it.UpdatedAt = t.UTC()
	}
	if v, present := lookup(obj, k.names("soft_deleted")); present && v != nil {
		b, isBool := v.(bool)
		if !isBool {
			return InvItem{}, false, false
		}
		it.SoftDeleted = b
	}
	if s, present, isStr := lookupString(obj, k.names("title")); present {
		if !isStr {
			return InvItem{}, false, false
		}
		it.TitleSHA = TitleKey(s, tag)
	}
	if s, present, isStr := lookupString(obj, k.names("url")); present && s != "" {
		if !isStr || !ValidURL(s) {
			return InvItem{}, false, false
		}
		if id, isUUID, _ := URLID(s); isUUID && id != slug {
			return InvItem{}, false, false
		}
		it.URL = s
	}
	return it, true, true
}

// GetParsed is the parsed form of get_artifact.
type GetParsed struct {
	Slug     Slug      `json:"slug"`
	Version  VersionID `json:"version"`
	Read     string    `json:"read,omitempty"`
	Icon     string    `json:"icon,omitempty"`
	TitleSHA string    `json:"title_sha,omitempty"`
	Title    string    `json:"title,omitempty"`
	Others   bool      `json:"others"`
}

func parseGet(text string, d StepData, others bool) (GetParsed, bool) {
	k := d.Parse
	obj, ok := decodeObject(text, k)
	if !ok {
		return GetParsed{}, false
	}
	slug, _, ok := lookupString(obj, k.names("slug"))
	if !ok || Slug(slug) != d.Slug {
		return GetParsed{}, false
	}
	v, _, ok := lookupString(obj, k.names("version"))
	if !ok || !k.validVersion(v) {
		return GetParsed{}, false
	}
	g := GetParsed{Slug: d.Slug, Version: VersionID(v), Others: others}
	if r, present, isStr := lookupString(obj, k.names("read")); present {
		if !isStr || (r != ReadOwner && r != ReadUsers && r != ReadOrg && r != ReadPublic) {
			return GetParsed{}, false
		}
		g.Read = r
	}
	if ic, present, isStr := lookupString(obj, k.names("icon")); present && ic != "" {
		if !isStr || !ValidIcon(ic) {
			return GetParsed{}, false
		}
		g.Icon = ic
	}
	if t, present, isStr := lookupString(obj, k.names("title")); present {
		if !isStr {
			return GetParsed{}, false
		}
		tag := ""
		if n, present, ok := lookupString(obj, k.names("notice")); present && ok {
			tag, _ = noticeTag(n, k.tagRE())
		}
		g.TitleSHA = TitleKey(t, tag)
		g.Title = titleValue(t, tag)
	}
	return g, true
}

// FilesParsed is the parsed form of list_files.
type FilesParsed struct {
	Count     int  `json:"count"`
	EntryOnly bool `json:"entry_only"`
}

func parseFiles(text string, k Knobs) (FilesParsed, bool) {
	v, rest, ok := firstValue(text)
	if !ok || !tailOK(rest, k.Reminders) {
		return FilesParsed{}, false
	}
	var arr []any
	switch x := v.(type) {
	case []any:
		arr = x
	case map[string]any:
		l, present := lookup(x, k.names("files_list"))
		a, isArr := l.([]any)
		if !present || !isArr {
			return FilesParsed{}, false
		}
		arr = a
	default:
		return FilesParsed{}, false
	}
	if len(arr) > maxListItems {
		return FilesParsed{}, false
	}
	entry := 0
	for _, e := range arr {
		obj, ok := e.(map[string]any)
		if !ok {
			return FilesParsed{}, false
		}
		p, _, ok := lookupString(obj, k.names("file_path"))
		if !ok || p == "" {
			return FilesParsed{}, false
		}
		if p == "index.html" {
			entry++
		}
	}
	return FilesParsed{Count: len(arr), EntryOnly: len(arr) == 1 && entry == 1}, true
}

// DeclParsed is the parsed form of read_declaration.
type DeclParsed struct {
	State  string   `json:"state"`
	Names  []string `json:"names"`
	Digest string   `json:"digest"`
	Others bool     `json:"others"`
}

func parseDecl(text string, k Knobs, others bool) (DeclParsed, bool) {
	obj, ok := decodeObject(text, k)
	if !ok {
		return DeclParsed{}, false
	}
	raw, present := lookup(obj, k.names("capabilities"))
	// An unreadable declaration still gets a digest (of its raw value), so
	// `allow --capabilities` can allow exactly this declaration and a later
	// change makes the pair skipped again (8.1 rule 3).
	unknown := DeclParsed{State: CapUnknown, Names: []string{}, Digest: unknownCapDigest(raw), Others: others}
	if !present || raw == nil {
		return unknown, true
	}
	var names []string
	switch x := raw.(type) {
	case []any:
		for _, e := range x {
			switch y := e.(type) {
			case string:
				names = append(names, y)
			case map[string]any:
				n, _, ok := lookupString(y, []string{"name"})
				if !ok {
					return unknown, true
				}
				names = append(names, n)
			default:
				return unknown, true
			}
		}
	case map[string]any:
		for n := range x {
			names = append(names, n)
		}
	default:
		return unknown, true
	}
	for _, n := range names {
		if !capNameRE.MatchString(n) {
			return unknown, true
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return DeclParsed{State: CapNone, Names: []string{}, Digest: capDigest(nil), Others: others}, true
	}
	return DeclParsed{State: CapDeclared, Names: names, Digest: capDigest(names), Others: others}, true
}

// unknownCapDigest is the digest of a declaration that could not be read:
// the sha256 of a fixed prefix and the canonical JSON of its raw value
// (map keys sorted by encoding/json). It never equals a capDigest.
func unknownCapDigest(raw any) string {
	b, err := json.Marshal(raw)
	if err != nil {
		b = []byte("unencodable")
	}
	h := sha256.Sum256(append([]byte("paritydesk-capabilities-unknown\x00"), b...))
	return hex.EncodeToString(h[:])
}

// capDigest is the sha256 of the canonical sorted name list.
func capDigest(names []string) string {
	if names == nil {
		names = []string{}
	}
	b, _ := json.Marshal(names)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// PublishParsed is the parsed form of a publish result.
type PublishParsed struct {
	Slug    Slug      `json:"slug"`
	URL     string    `json:"url,omitempty"`
	Version VersionID `json:"version"`
}

func parsePublish(text string, d StepData) (PublishParsed, bool) {
	k := d.Parse
	obj, ok := decodeObject(text, k)
	if !ok {
		return PublishParsed{}, false
	}
	slug, _, ok := lookupString(obj, k.names("slug"))
	if !ok || !ValidSlug(slug) {
		return PublishParsed{}, false
	}
	if (d.Role == RoleUpdate || d.Role == RoleUpdateV) && Slug(slug) != d.Slug {
		return PublishParsed{}, false
	}
	v, _, ok := lookupString(obj, k.names("version"))
	if !ok || !k.validVersion(v) {
		return PublishParsed{}, false
	}
	p := PublishParsed{Slug: Slug(slug), Version: VersionID(v)}
	if u, present, isStr := lookupString(obj, k.names("url")); present && u != "" {
		if !isStr || !ValidURL(u) {
			return PublishParsed{}, false
		}
		if id, isUUID, _ := URLID(u); isUUID && id != slug {
			return PublishParsed{}, false
		}
		p.URL = u
	}
	return p, true
}

// uriRE is a client-filled publish_files uri (B11).
var uriRE = regexp.MustCompile(`^artifact-file://[^/\s]{1,255}/([0-9a-f]{64})$`)

// ToolPublishFiles is the publish tool of transport files.
const ToolPublishFiles = "mcp__claude-artifacts__publish_files"

// NormalizePublishInput removes from a publish_files input the uri the
// client fills in (A13): a string of at most 4 KiB of the form
// artifact-file://<host>/<sha>, whose sha is the content-addressed name
// of the same entry's path (<sha><suffix>, so it equals the step's blob
// sha). Any other uri leaves ok false: the call then matches no step.
// Inputs of other tools are returned unchanged.
func NormalizePublishInput(tool string, in json.RawMessage) (json.RawMessage, []string, bool) {
	if tool != ToolPublishFiles {
		return in, nil, true
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(in, &obj) != nil {
		return in, nil, true
	}
	raw, ok := obj["files"]
	if !ok {
		return in, nil, true
	}
	var files []map[string]json.RawMessage
	if json.Unmarshal(raw, &files) != nil {
		return in, nil, true
	}
	var shas []string
	changed := false
	for _, f := range files {
		u, has := f["uri"]
		if !has {
			continue
		}
		var s string
		if len(u) > 4<<10+2 || json.Unmarshal(u, &s) != nil || len(s) > 4<<10 {
			return nil, nil, false
		}
		m := uriRE.FindStringSubmatch(s)
		if m == nil {
			return nil, nil, false
		}
		var p string
		if json.Unmarshal(f["path"], &p) != nil || !strings.HasPrefix(path.Base(p), m[1]+".") {
			return nil, nil, false
		}
		shas = append(shas, m[1])
		delete(f, "uri")
		changed = true
	}
	if !changed {
		return in, nil, true
	}
	fb, err := json.Marshal(files)
	if err != nil {
		return nil, nil, false
	}
	obj["files"] = fb
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, nil, false
	}
	c, err := model.Canonical(out)
	if err != nil {
		return nil, nil, false
	}
	return c, shas, true
}

// validAlias reports a profile alias.
func validAlias(a ids.Alias) bool {
	_, err := ids.ParseAlias(string(a))
	return err == nil
}
