package artifacts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// MaxTotalLines bounds a read_artifact summary's total_lines.
const MaxTotalLines = 10_000_000

// Chunk is one parsed read_artifact part (7.3): validated summary fields
// and the exact body between the per-call wrapper tags.
type Chunk struct {
	Slug       Slug      `json:"slug"`
	Version    VersionID `json:"version"`
	Offset     int       `json:"offset"`
	NextOffset int       `json:"next_offset"` // 0 = the last part
	TotalLines int       `json:"total_lines"`
	Lines      int       `json:"lines"`
	Owned      *bool     `json:"owned"`
	FilesMulti bool      `json:"files_multi"`
	Title      string    `json:"title,omitempty"`
	TitleSHA   string    `json:"title_sha,omitempty"`
	Body       string    `json:"body"`
	BodySHA    string    `json:"body_sha256"`
	Form       string    `json:"form"`
	Others     bool      `json:"others"`
}

// ChunkStep is what a chunk must match: the step's slug and offset (0:
// the step had no offset, so the part starts at line 1).
type ChunkStep struct {
	Slug   Slug
	Offset int
}

// Errors of the chunk parser.
var (
	ErrChunk    = errors.New("artifacts: unrecognized read_artifact result")
	ErrNotOwned = errors.New("artifacts: the artifact is not the user's own")
	ErrAssemble = errors.New("artifacts: parts do not assemble")
)

func chunkErr(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrChunk}, args...)...)
}

// lineCount is the number of lines of s: newlines + 1 for a non-empty s,
// 0 for "".
func lineCount(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// ParseChunk parses the joined text of a read_artifact result (7.3).
func ParseChunk(text string, step ChunkStep, k Knobs) (Chunk, error) {
	v, rest, ok := firstValue(text)
	if !ok {
		return Chunk{}, chunkErr("no summary")
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return Chunk{}, chunkErr("summary is not an object")
	}
	rest = strings.TrimLeft(rest, " \t\r\n")
	slug, _, ok := lookupString(obj, k.names("slug"))
	if !ok || Slug(slug) != step.Slug {
		return Chunk{}, chunkErr("slug")
	}
	ver, _, ok := lookupString(obj, k.names("version"))
	if !ok || !k.validVersion(ver) {
		return Chunk{}, chunkErr("version")
	}
	total, _, ok := lookupInt(obj, k.names("total_lines"))
	if !ok || total < 1 || total > MaxTotalLines {
		return Chunk{}, chunkErr("total_lines")
	}
	want := int64(step.Offset)
	if want == 0 {
		want = 1
	}
	off, present, ok := lookupInt(obj, k.names("offset"))
	switch {
	case present && (!ok || off != want):
		return Chunk{}, chunkErr("offset")
	case !present && step.Offset != 0:
		return Chunk{}, chunkErr("offset missing")
	case !present:
		off = 1
	}
	next := int64(0)
	if nv, present := lookup(obj, k.names("next_offset")); present && nv != nil {
		n, _, ok := lookupInt(obj, k.names("next_offset"))
		if !ok || n <= off || n > total {
			return Chunk{}, chunkErr("next_offset")
		}
		next = n
	}
	notice, _, ok := lookupString(obj, k.names("notice"))
	if !ok {
		return Chunk{}, chunkErr("notice")
	}
	tag, n := noticeTag(notice, k.tagRE())
	if n != 1 {
		return Chunk{}, chunkErr("wrapper tag")
	}
	open, closing := "<"+tag+">", "</"+tag+">"
	if !strings.HasPrefix(rest, open) {
		return Chunk{}, chunkErr("body does not start with the wrapper")
	}
	end := strings.LastIndex(rest, closing)
	if end < len(open) {
		return Chunk{}, chunkErr("wrapper not closed")
	}
	inner := rest[len(open):end]
	if !tailOK(rest[end+len(closing):], k.Reminders) {
		return Chunk{}, chunkErr("text after the wrapper")
	}
	if strings.Contains(inner, open) || strings.Contains(inner, closing) {
		return Chunk{}, chunkErr("nested wrapper")
	}
	expect := int(next - off)
	if next == 0 {
		expect = int(total - off + 1)
	}
	body, form, err := matchForm(inner, expect, k.Forms)
	if err != nil {
		return Chunk{}, err
	}
	c := Chunk{Slug: step.Slug, Version: VersionID(ver), Offset: int(off), NextOffset: int(next), TotalLines: int(total),
		Lines: expect, Body: body, Form: form}
	if t, present, isStr := lookupString(obj, k.names("title")); present {
		if !isStr {
			return Chunk{}, chunkErr("title")
		}
		c.TitleSHA = TitleKey(t, tag)
		c.Title = titleValue(t, tag)
	}
	if ov, present := lookup(obj, k.names("owned")); present {
		b, isBool := ov.(bool)
		if !isBool {
			return Chunk{}, chunkErr("owned")
		}
		c.Owned = &b
		if !b {
			return Chunk{}, ErrNotOwned
		}
	}
	if fv, present := lookup(obj, k.names("summary_files")); present && fv != nil {
		c.FilesMulti = !singleEntry(fv, tag)
	}
	max := k.MaxContent
	if max <= 0 {
		max = 16 << 20
	}
	if int64(len(body)) > max {
		return Chunk{}, chunkErr("too large")
	}
	if !utf8.ValidString(body) {
		return Chunk{}, chunkErr("not UTF-8")
	}
	h := sha256.Sum256([]byte(body))
	c.BodySHA = hex.EncodeToString(h[:])
	return c, nil
}

// singleEntry reports a summary file list that names index.html alone:
// ["index.html"], or the wrapped string of it.
func singleEntry(v any, tag string) bool {
	switch x := v.(type) {
	case []any:
		if len(x) != 1 {
			return false
		}
		s, ok := x[0].(string)
		return ok && s == "index.html"
	case string:
		t, ok := unwrapWith(x, tag)
		if !ok {
			return false
		}
		if t == "index.html" {
			return true
		}
		var l []string
		return json.Unmarshal([]byte(t), &l) == nil && len(l) == 1 && l[0] == "index.html"
	}
	return false
}

// matchForm finds the one listed wrapper form whose body has n lines.
func matchForm(inner string, n int, forms []string) (string, string, error) {
	if len(forms) == 0 {
		forms = []string{"bare", "nl"}
	}
	var body, form string
	matches := 0
	for _, f := range forms {
		b, ok := "", false
		switch f {
		case "bare":
			b, ok = inner, lineCount(inner) == n
		case "nl":
			if len(inner) >= 2 && inner[0] == '\n' && inner[len(inner)-1] == '\n' {
				b = inner[1 : len(inner)-1]
				ok = lineCount(b) == n
			}
		case "lead_nl":
			if len(inner) >= 1 && inner[0] == '\n' {
				b = inner[1:]
				ok = lineCount(b) == n
			}
		case "trail_nl":
			if len(inner) >= 1 && inner[len(inner)-1] == '\n' {
				b = inner[:len(inner)-1]
				ok = lineCount(b) == n
			}
		}
		if ok {
			matches++
			body, form = b, f
		}
	}
	if matches != 1 {
		return "", "", chunkErr("line count does not match exactly one form")
	}
	return body, form, nil
}

// Assembled is a whole document from its parts.
type Assembled struct {
	Slug     Slug
	Version  VersionID
	Content  string
	SHA      string
	Lines    int
	Bytes    int64
	Title    string
	TitleSHA string
	Others   bool
	Multi    bool
}

// Assemble joins parts into the document (7.3): consecutive offsets from
// 1, the same slug, version, total and form, the last part without a
// next offset, the exact line count, at most max bytes.
func Assemble(chunks []Chunk, max int64) (Assembled, error) {
	bad := func(why string) (Assembled, error) { return Assembled{}, fmt.Errorf("%w: %s", ErrAssemble, why) }
	if len(chunks) == 0 {
		return bad("no parts")
	}
	cs := append([]Chunk(nil), chunks...)
	sort.Slice(cs, func(i, j int) bool { return cs[i].Offset < cs[j].Offset })
	first := cs[0]
	if first.Offset != 1 {
		return bad("first part does not start at line 1")
	}
	var b strings.Builder
	for i, c := range cs {
		if c.Slug != first.Slug || c.Version != first.Version || c.TotalLines != first.TotalLines || c.Form != first.Form {
			return bad("parts of different documents")
		}
		if i > 0 {
			if cs[i-1].NextOffset != c.Offset {
				return bad("gap or overlap")
			}
			b.WriteByte('\n')
		}
		if i == len(cs)-1 && c.NextOffset != 0 {
			return bad("last part is not the end")
		}
		if i < len(cs)-1 && c.NextOffset == 0 {
			return bad("part after the end")
		}
		b.WriteString(c.Body)
	}
	content := b.String()
	if lineCount(content) != first.TotalLines {
		return bad("line count")
	}
	if max <= 0 {
		max = 16 << 20
	}
	if int64(len(content)) > max {
		return bad("too large")
	}
	h := sha256.Sum256([]byte(content))
	a := Assembled{Slug: first.Slug, Version: first.Version, Content: content, SHA: hex.EncodeToString(h[:]),
		Lines: first.TotalLines, Bytes: int64(len(content))}
	for _, c := range cs {
		if c.TitleSHA != "" && a.TitleSHA == "" {
			a.Title, a.TitleSHA = c.Title, c.TitleSHA
		}
		a.Others = a.Others || c.Others
		a.Multi = a.Multi || c.FilesMulti
	}
	return a, nil
}
