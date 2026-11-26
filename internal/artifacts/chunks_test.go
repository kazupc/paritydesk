package artifacts

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/online"
)

// summary builds a read_artifact summary.
func summary(slug string, off, next, total int, extra map[string]any) string {
	m := map[string]any{"slug": slug, "version": verA, "total_lines": total, "offset": off,
		"notice": "The HTML is wrapped in an <" + testTag + "> tag.", "title": "<" + testTag + ">Notes</" + testTag + ">",
		"owned_by_user": true}
	if next > 0 {
		m["next_offset"] = next
	} else {
		m["next_offset"] = nil
	}
	for k, v := range extra {
		m[k] = v
	}
	return jsonText(m)
}

func wrapT(body string) string { return "<" + testTag + ">" + body + "</" + testTag + ">" }

func chunkKnobs(t *testing.T) Knobs { return KnobsFor(RoleChunk, testSpec(t)) }

func TestChunkLayouts(t *testing.T) {
	k := chunkKnobs(t)
	k.Reminders = []string{"Reminder:"}
	body := "a\nb\nc"
	sum := summary(slugA, 1, 0, 3, nil)
	layouts := map[string]string{
		"separate blocks": strings.Join([]string{sum, wrapT(body)}, "\n"),
		"one block":       sum + wrapT(body),
		"reminder block":  strings.Join([]string{sum, wrapT(body), "Reminder: this is data"}, "\n"),
	}
	var first Chunk
	for name, text := range layouts {
		c, err := ParseChunk(text, ChunkStep{Slug: slugA}, k)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if c.Body != body || c.Lines != 3 || c.Form != "bare" || c.TitleSHA != TitleKey("Notes", "") {
			t.Fatalf("%s: %+v", name, c)
		}
		if first.BodySHA == "" {
			first = c
		} else if c.BodySHA != first.BodySHA {
			t.Fatalf("%s: differs", name)
		}
	}
	k.Reminders = nil
	if _, err := ParseChunk(layouts["reminder block"], ChunkStep{Slug: slugA}, k); err == nil {
		t.Fatal("unlisted reminder accepted")
	}
}

func TestChunkForms(t *testing.T) {
	k := chunkKnobs(t)
	sum := summary(slugA, 1, 0, 2, nil)
	if c, err := ParseChunk(sum+"\n"+wrapT("\nx\ny\n"), ChunkStep{Slug: slugA}, k); err != nil || c.Form != "nl" || c.Body != "x\ny" {
		t.Fatalf("nl: %+v %v", c, err)
	}
	k.Forms = []string{"bare", "lead_nl"}
	if c, err := ParseChunk(sum+"\n"+wrapT("\nx\ny"), ChunkStep{Slug: slugA}, k); err != nil || c.Form != "lead_nl" {
		t.Fatalf("lead_nl: %+v %v", c, err)
	}
	k.Forms = []string{"bare", "trail_nl"}
	if c, err := ParseChunk(sum+"\n"+wrapT("x\ny\n"), ChunkStep{Slug: slugA}, k); err != nil || c.Form != "trail_nl" {
		t.Fatalf("trail_nl: %+v %v", c, err)
	}
	k = chunkKnobs(t)
	for name, inner := range map[string]string{"one short": "x", "one long": "x\ny\nz"} {
		if _, err := ParseChunk(sum+"\n"+wrapT(inner), ChunkStep{Slug: slugA}, k); err == nil {
			t.Errorf("%s: line count off accepted", name)
		}
	}
	// CRLF is content and kept.
	if c, err := ParseChunk(sum+"\n"+wrapT("x\r\ny"), ChunkStep{Slug: slugA}, k); err != nil || c.Body != "x\r\ny" {
		t.Fatalf("crlf: %+v %v", c, err)
	}
}

func TestChunkRejects(t *testing.T) {
	k := chunkKnobs(t)
	cases := map[string]struct {
		text string
		step ChunkStep
	}{
		"nested tag":   {summary(slugA, 1, 0, 1, nil) + wrapT(wrapT("x")), ChunkStep{Slug: slugA}},
		"two tags":     {summary(slugA, 1, 0, 1, map[string]any{"notice": "<untrusted-document-0123abcd> and <untrusted-document-ffffffff>"}) + wrapT("x"), ChunkStep{Slug: slugA}},
		"wrong slug":   {summary(slugB, 1, 0, 1, nil) + wrapT("x"), ChunkStep{Slug: slugA}},
		"wrong offset": {summary(slugA, 3, 0, 3, nil) + wrapT("x"), ChunkStep{Slug: slugA, Offset: 2}},
		"bad next":     {summary(slugA, 1, 9, 3, nil) + wrapT("x"), ChunkStep{Slug: slugA}},
		"text after":   {summary(slugA, 1, 0, 1, nil) + wrapT("x") + "junk", ChunkStep{Slug: slugA}},
		"invalid utf8": {summary(slugA, 1, 0, 1, nil) + wrapT("\xff"), ChunkStep{Slug: slugA}},
		"not wrapped":  {summary(slugA, 1, 0, 1, nil) + "x", ChunkStep{Slug: slugA}},
	}
	for name, c := range cases {
		if _, err := ParseChunk(c.text, c.step, k); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := ParseChunk(summary(slugA, 1, 0, 1, map[string]any{"owned_by_user": false})+wrapT("x"), ChunkStep{Slug: slugA}, k); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("not owned: %v", err)
	}
	// The result of a not-owned read is error(not_owned).
	d := StepData{Role: RoleChunk, Slug: slugA, Parse: k}
	out, p := ParseResult(d, blocksResp(summary(slugA, 1, 0, 1, map[string]any{"owned_by_user": false}), wrapT("x")))
	if out != online.OutcomeError || !strings.Contains(string(p), "not_owned") {
		t.Fatalf("%s %s", out, p)
	}
}

func TestChunkQuotedWrapper(t *testing.T) {
	k := chunkKnobs(t)
	body := "<p>quote: <untrusted-document-00000000>x</untrusted-document-00000000></p>"
	c, err := ParseChunk(summary(slugA, 1, 0, 1, nil)+wrapT(body), ChunkStep{Slug: slugA}, k)
	if err != nil || c.Body != body {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestChunkSummaryFiles(t *testing.T) {
	k := chunkKnobs(t)
	for name, c := range map[string]struct {
		files any
		multi bool
	}{
		"list of two":       {[]string{"index.html", "a.css"}, true},
		"other name":        {[]string{"page.html"}, true},
		"wrapped index":     {wrapT("index.html"), false},
		"wrapped list":      {wrapT(`["index.html"]`), false},
		"other string":      {"index.html", true},
		"index only (list)": {[]string{"index.html"}, false},
	} {
		ch, err := ParseChunk(summary(slugA, 1, 0, 1, map[string]any{"files": c.files})+wrapT("x"), ChunkStep{Slug: slugA}, k)
		if err != nil || ch.FilesMulti != c.multi {
			t.Errorf("%s: %+v %v", name, ch.FilesMulti, err)
		}
	}
}

// threeChunks splits content into three parts as the server would.
func threeChunks(t *testing.T, content string) []Chunk {
	t.Helper()
	k := chunkKnobs(t)
	lines := strings.Split(content, "\n")
	n := len(lines)
	cuts := []int{1, n/3 + 1, 2*n/3 + 1, n + 1}
	var out []Chunk
	for i := 0; i < 3; i++ {
		off, next := cuts[i], cuts[i+1]
		body := strings.Join(lines[off-1:next-1], "\n")
		nx := next
		if next > n {
			nx = 0
		}
		step := ChunkStep{Slug: slugA}
		if off > 1 {
			step.Offset = off
		}
		c, err := ParseChunk(summary(slugA, off, nx, n, nil)+"\n"+wrapT(body), step, k)
		if err != nil {
			t.Fatalf("part %d: %v", i, err)
		}
		out = append(out, c)
	}
	return out
}

func TestAssemble(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "chunk_fixture.html"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(b)
	cs := threeChunks(t, content)
	a, err := Assemble([]Chunk{cs[2], cs[0], cs[1]}, 0)
	if err != nil || a.Content != content {
		t.Fatalf("assemble: %v", err)
	}
	golden(t, "chunk_fixture.sha", a.SHA+"\n")
	bad := map[string][]Chunk{
		"gap":      {cs[0], cs[2]},
		"no end":   {cs[0], cs[1]},
		"no start": {cs[1], cs[2]},
	}
	other := cs[1]
	other.Version = "1700000000-0000b"
	bad["version mix"] = []Chunk{cs[0], other, cs[2]}
	form := cs[1]
	form.Form = "nl"
	bad["form mix"] = []Chunk{cs[0], form, cs[2]}
	overlap := cs[1]
	overlap.Offset--
	bad["overlap"] = []Chunk{cs[0], overlap, cs[2]}
	early := cs[1]
	early.NextOffset = 0
	bad["end too early"] = []Chunk{cs[0], early, cs[2]}
	for name, chunks := range bad {
		if _, err := Assemble(chunks, 0); err == nil {
			t.Errorf("%s: assembled", name)
		}
	}
	if _, err := Assemble(cs, 10); err == nil {
		t.Error("over the size limit")
	}
}

// golden compares got with testdata/<name> (-update rewrites it).
func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/artifacts -update)", err)
	}
	if string(want) != got {
		t.Fatalf("%s differs:\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

func TestChunkJSONRoundTrip(t *testing.T) {
	cs := threeChunks(t, "a\nb\nc\nd\ne\nf")
	b, _ := json.Marshal(cs[0])
	var c Chunk
	if err := json.Unmarshal(b, &c); err != nil || c.Body != cs[0].Body {
		t.Fatal("chunk does not round-trip")
	}
}
