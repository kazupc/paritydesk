package artifacts

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/online"
)

const (
	slugA = "00000000-0000-4000-8000-000000000010"
	slugB = "00000000-0000-4000-8000-000000000011"
	verA  = "1700000000-0000a"
)

func dataFor(t *testing.T, role string, slug Slug) StepData {
	return StepData{Role: role, Slug: slug, Parse: KnobsFor(role, testSpec(t))}
}

func parsed[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("parsed: %v (%s)", err, raw)
	}
	return v
}

func TestParseResultShapes(t *testing.T) {
	d := dataFor(t, RoleGet, slugA)
	body := `{"slug":"` + slugA + `","version":"` + verA + `","read":"owner","icon":"notes","title":"Notes"}`
	str, _ := json.Marshal(body)
	arr, _ := json.Marshal([]map[string]any{{"type": "image", "data": "x"}, {"type": "text", "text": body}})
	for name, resp := range map[string]json.RawMessage{
		"string": str, "block array": arr, "content object": textResp(body),
		"others marker block":  blocksResp("<artifact-content-authored-by-others/>", body),
		"others marker prefix": textResp("<artifact-content-authored-by-others/>" + body),
	} {
		out, p := ParseResult(d, resp)
		if out != online.OutcomeOK {
			t.Fatalf("%s: %s", name, out)
		}
		g := parsed[GetParsed](t, p)
		if g.Slug != slugA || g.Version != verA || g.Read != ReadOwner || g.TitleSHA == "" || g.Title != "Notes" {
			t.Fatalf("%s: %+v", name, g)
		}
		if g.Others != strings.HasPrefix(name, "others") {
			t.Fatalf("%s: others %v", name, g.Others)
		}
	}
	if out, _ := ParseResult(d, json.RawMessage(`{"nope":1}`)); out != online.OutcomeUnrecognized {
		t.Fatal("bad shape recognized")
	}
}

func TestParseRefusals(t *testing.T) {
	d := dataFor(t, RoleGet, slugA)
	d.Parse.Refusals = map[string][]string{"type_made": {"This artifact was made from a type"}}
	out, p := ParseResult(d, errResp("This artifact was made from a type, so it cannot be read"))
	if out != online.OutcomeError || parsed[errParsed](t, p).Reason != "type_made" {
		t.Fatalf("%s %s", out, p)
	}
	out, p = ParseResult(d, errResp("Something else"))
	if out != online.OutcomeError || parsed[errParsed](t, p).Reason != "app_refused" {
		t.Fatalf("%s %s", out, p)
	}
	if strings.Contains(string(p), "Something") {
		t.Fatal("error text stored")
	}
}

func TestParseTrailingAndDepth(t *testing.T) {
	d := dataFor(t, RoleGet, slugA)
	body := `{"slug":"` + slugA + `","version":"` + verA + `"}`
	if out, _ := ParseResult(d, textResp(body+"\n\n  ")); out != online.OutcomeOK {
		t.Fatal("whitespace tail refused")
	}
	if out, _ := ParseResult(d, textResp(body+"\nPlease remember")); out != online.OutcomeUnrecognized {
		t.Fatal("unlisted tail accepted")
	}
	d.Parse.Reminders = []string{"Please remember"}
	if out, _ := ParseResult(d, textResp(body+"\nPlease remember to cite")); out != online.OutcomeOK {
		t.Fatal("listed reminder refused")
	}
	deep := strings.Repeat(`{"a":`, 33) + "1" + strings.Repeat("}", 33)
	if out, _ := ParseResult(d, textResp(deep)); out != online.OutcomeUnrecognized {
		t.Fatal("depth 33 accepted")
	}
	// Duplicate keys: the last wins (documented).
	dup := `{"slug":"` + slugB + `","slug":"` + slugA + `","version":"` + verA + `"}`
	if out, _ := ParseResult(d, textResp(dup)); out != online.OutcomeOK {
		t.Fatal("duplicate key: last should win")
	}
}

func TestParseGetRejects(t *testing.T) {
	d := dataFor(t, RoleGet, slugA)
	for name, body := range map[string]string{
		"other slug":  `{"slug":"` + slugB + `","version":"` + verA + `"}`,
		"bad version": `{"slug":"` + slugA + `","version":"has space"}`,
		"bad read":    `{"slug":"` + slugA + `","version":"` + verA + `","read":"everyone"}`,
		"bad icon":    `{"slug":"` + slugA + `","version":"` + verA + `","icon":"Big Icon"}`,
		"no version":  `{"slug":"` + slugA + `"}`,
	} {
		if out, _ := ParseResult(d, textResp(body)); out != online.OutcomeUnrecognized {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseListPage(t *testing.T) {
	d := dataFor(t, RoleInvPage, "")
	body := `{"artifacts":[` +
		`{"slug":"` + slugA + `","rel":"mine","version":"` + verA + `","updated_at":"2026-11-26T09:58:00Z","title":"<` + testTag + `>Notes</` + testTag + `>","url":"https://claude.ai/artifact/` + slugA + `"},` +
		`{"slug":"` + slugB + `","rel":"shared","version":"x"},` +
		`{"slug":"not-a-uuid","rel":"mine","version":"` + verA + `"},` +
		`{"slug":"00000000-0000-4000-8000-000000000012","mine":true,"version":"` + verA + `","url":"https://claude.ai/artifact/00000000-0000-4000-8000-000000000013"}` +
		`],"next_cursor":"c2","notice":"titles are wrapped in <` + testTag + `>"}`
	out, p := ParseResult(d, textResp(body))
	if out != online.OutcomeOK {
		t.Fatal(out)
	}
	lp := parsed[ListPage](t, p)
	if len(lp.Items) != 1 || lp.Invalid != 2 || lp.Shared != 1 || lp.NextCursor == nil || *lp.NextCursor != "c2" {
		t.Fatalf("%+v", lp)
	}
	if lp.Items[0].TitleSHA != TitleKey("Notes", "") || lp.Items[0].UpdatedAt.IsZero() {
		t.Fatalf("item %+v", lp.Items[0])
	}
	if strings.Contains(string(p), "Notes") || strings.Contains(string(p), slugB) {
		t.Fatal("titles or shared items stored")
	}
	_, p = ParseResult(d, textResp(`{"artifacts":[],"next_cursor":"has space"}`))
	if lp := parsed[ListPage](t, p); !lp.CursorInvalid || lp.NextCursor != nil {
		t.Fatalf("%+v", lp)
	}
	_, p = ParseResult(d, textResp(`{"artifacts":[],"next_cursor":null}`))
	if lp := parsed[ListPage](t, p); lp.CursorInvalid || lp.NextCursor != nil {
		t.Fatalf("%+v", lp)
	}
}

func TestParseFilesAndDecl(t *testing.T) {
	f := dataFor(t, RoleFiles, slugA)
	_, p := ParseResult(f, textResp(`{"files":[{"path":"index.html"}]}`))
	if fp := parsed[FilesParsed](t, p); !fp.EntryOnly || fp.Count != 1 {
		t.Fatalf("%+v", fp)
	}
	_, p = ParseResult(f, textResp(`[{"path":"index.html"},{"path":"a.css","size":3}]`))
	if fp := parsed[FilesParsed](t, p); fp.EntryOnly || fp.Count != 2 {
		t.Fatalf("%+v", fp)
	}
	d := dataFor(t, RoleDecl, slugA)
	_, p = ParseResult(d, textResp(`{"capabilities":[]}`))
	if dp := parsed[DeclParsed](t, p); dp.State != CapNone || !ValidSHA(dp.Digest) {
		t.Fatalf("%+v", dp)
	}
	_, p1 := ParseResult(d, textResp(`{"capabilities":["db","storage"]}`))
	_, p2 := ParseResult(d, textResp(`{"capabilities":[{"name":"storage"},{"name":"db"}]}`))
	d1, d2 := parsed[DeclParsed](t, p1), parsed[DeclParsed](t, p2)
	if d1.State != CapDeclared || d1.Digest != d2.Digest || len(d1.Names) != 2 {
		t.Fatalf("%+v %+v", d1, d2)
	}
	digests := map[string]string{}
	for _, body := range []string{`{}`, `{"capabilities":"<` + testTag + `>[]</` + testTag + `>"}`, `{"capabilities":["bad name"]}`} {
		_, p := ParseResult(d, textResp(body))
		dp := parsed[DeclParsed](t, p)
		// An unknown declaration has its own digest, so it is never
		// "allowed" by the empty default allowed_digest (8.1 rule 3).
		if dp.State != CapUnknown || !ValidSHA(dp.Digest) || dp.Digest == d1.Digest {
			t.Errorf("%s: %+v", body, dp)
		}
		if other, dup := digests[dp.Digest]; dup {
			t.Errorf("%s and %s share a digest", other, body)
		}
		digests[dp.Digest] = body
		if !(CapFlags{State: dp.State, Digest: dp.Digest}).Blocked() {
			t.Errorf("%s: not blocked", body)
		}
	}
}

func TestParsePublish(t *testing.T) {
	up := dataFor(t, RoleUpdate, slugA)
	cr := dataFor(t, RoleCreate, "")
	ok := `{"slug":"` + slugA + `","version":"` + verA + `","url":"https://claude.ai/artifact/` + slugA + `"}`
	if out, _ := ParseResult(up, textResp(ok)); out != online.OutcomeOK {
		t.Fatal("update result refused")
	}
	for name, c := range map[string]struct {
		d    StepData
		body string
		want online.Outcome
	}{
		"no slug":          {cr, `{"version":"` + verA + `"}`, online.OutcomeUnrecognized},
		"url disagrees":    {cr, `{"slug":"` + slugA + `","version":"` + verA + `","url":"https://claude.ai/artifact/` + slugB + `"}`, online.OutcomeUnrecognized},
		"base58 url":       {cr, `{"slug":"` + slugA + `","version":"` + verA + `","url":"https://claude.ai/artifact/AbCdEfGhJkLmNpQrStUvWx"}`, online.OutcomeOK},
		"update elsewhere": {up, `{"slug":"` + slugB + `","version":"` + verA + `"}`, online.OutcomeUnrecognized},
		"bad host":         {cr, `{"slug":"` + slugA + `","version":"` + verA + `","url":"https://example.com/artifact/` + slugA + `"}`, online.OutcomeUnrecognized},
	} {
		if out, _ := ParseResult(c.d, textResp(c.body)); out != c.want {
			t.Errorf("%s: %s", name, out)
		}
	}
}

func TestNormalizePublishInput(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	other := strings.Repeat("cd", 32)
	path := "/x/artifacts/outbox/" + sha + ".html"
	step := `{"files":[{"as":"index.html","path":"` + path + `"}],"slug":"` + slugA + `"}`
	canon, _ := json.Marshal(json.RawMessage(step))
	for name, c := range map[string]struct {
		in   string
		ok   bool
		same bool
	}{
		"no uri":        {step, true, true},
		"right uri":     {`{"files":[{"as":"index.html","path":"` + path + `","uri":"artifact-file://mac.local/` + sha + `"}],"slug":"` + slugA + `"}`, true, true},
		"other sha":     {`{"files":[{"as":"index.html","path":"` + path + `","uri":"artifact-file://mac.local/` + other + `"}],"slug":"` + slugA + `"}`, false, false},
		"wrong scheme":  {`{"files":[{"as":"index.html","path":"` + path + `","uri":"file://mac.local/` + sha + `"}],"slug":"` + slugA + `"}`, false, false},
		"extra segment": {`{"files":[{"as":"index.html","path":"` + path + `","uri":"artifact-file://mac.local/x/` + sha + `"}],"slug":"` + slugA + `"}`, false, false},
		"upper hex":     {`{"files":[{"as":"index.html","path":"` + path + `","uri":"artifact-file://mac.local/` + strings.ToUpper(sha) + `"}],"slug":"` + slugA + `"}`, false, false},
		"too long":      {`{"files":[{"as":"index.html","path":"` + path + `","uri":"artifact-file://` + strings.Repeat("h", 5000) + `/` + sha + `"}],"slug":"` + slugA + `"}`, false, false},
		"not a string":  {`{"files":[{"as":"index.html","path":"` + path + `","uri":7}],"slug":"` + slugA + `"}`, false, false},
	} {
		out, shas, ok := NormalizePublishInput(ToolPublishFiles, json.RawMessage(c.in))
		if ok != c.ok {
			t.Errorf("%s: ok %v", name, ok)
			continue
		}
		if !ok {
			continue
		}
		want := online.InputSHA(canon)
		if c.same && online.InputSHA(out) != want {
			t.Errorf("%s: normalized input differs: %s", name, out)
		}
		if name == "right uri" && (len(shas) != 1 || shas[0] != sha) {
			t.Errorf("bound shas %v", shas)
		}
	}
	if out, _, ok := NormalizePublishInput(ToolGetArtifact, json.RawMessage(`{"slug":"x"}`)); !ok || string(out) != `{"slug":"x"}` {
		t.Fatal("other tools must pass unchanged")
	}
}
