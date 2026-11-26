package artifacts

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/online"
)

func fuzzSpec() compat.ArtifactsSpec {
	ms, err := compat.Load("")
	if err != nil {
		panic(err)
	}
	for _, m := range ms {
		if m.Name == "7.x" {
			return m.Artifacts
		}
	}
	panic("no 7.x")
}

func FuzzParseChunk(f *testing.F) {
	k := KnobsFor(RoleChunk, fuzzSpec())
	f.Add(summary(slugA, 1, 0, 2, nil)+"\n"+wrapT("a\nb"), 0)
	f.Add(summary(slugA, 1, 2, 3, nil)+wrapT("a"), 0)
	f.Add(summary(slugA, 2, 0, 3, nil)+wrapT("b\nc"), 2)
	f.Fuzz(func(t *testing.T, text string, off int) {
		c, err := ParseChunk(text, ChunkStep{Slug: slugA, Offset: off}, k)
		if err != nil {
			return
		}
		if c.Slug != slugA || lineCount(c.Body) != c.Lines || strings.Contains(c.Body, "</"+testTag+">") {
			t.Fatalf("accepted an inconsistent chunk %+v", c)
		}
	})
}

func FuzzParseListPage(f *testing.F) {
	d := StepData{Role: RoleInvPage, Parse: KnobsFor(RoleInvPage, fuzzSpec())}
	f.Add(`{"artifacts":[{"slug":"` + slugA + `","rel":"mine","version":"` + verA + `"}],"next_cursor":null}`)
	f.Add(`{"artifacts":[],"next_cursor":"c2"}`)
	f.Fuzz(func(t *testing.T, text string) {
		out, p := ParseResult(d, textResp(text))
		if out != online.OutcomeOK {
			return
		}
		var lp ListPage
		if json.Unmarshal(p, &lp) != nil {
			t.Fatal("unparsable parsed")
		}
		for _, it := range lp.Items {
			if !ValidSlug(string(it.Slug)) || !it.Mine {
				t.Fatalf("invalid item kept %+v", it)
			}
		}
	})
}

func FuzzParseDeclaration(f *testing.F) {
	d := StepData{Role: RoleDecl, Parse: KnobsFor(RoleDecl, fuzzSpec())}
	f.Add(`{"capabilities":["db"]}`)
	f.Add(`{"capabilities":{"db":{}}}`)
	f.Fuzz(func(t *testing.T, text string) {
		out, p := ParseResult(d, textResp(text))
		if out != online.OutcomeOK {
			return
		}
		var dp DeclParsed
		if json.Unmarshal(p, &dp) != nil || (dp.State != CapNone && dp.State != CapDeclared && dp.State != CapUnknown) {
			t.Fatalf("%s", p)
		}
	})
}

func FuzzParsePublishResult(f *testing.F) {
	d := StepData{Role: RoleCreate, Parse: KnobsFor(RoleCreate, fuzzSpec())}
	f.Add(`{"slug":"` + slugA + `","version":"` + verA + `","url":"https://claude.ai/artifact/` + slugA + `"}`)
	f.Fuzz(func(t *testing.T, text string) {
		out, p := ParseResult(d, textResp(text))
		if out != online.OutcomeOK {
			return
		}
		var pp PublishParsed
		if json.Unmarshal(p, &pp) != nil || !ValidSlug(string(pp.Slug)) || (pp.URL != "" && !ValidURL(pp.URL)) {
			t.Fatalf("%s", p)
		}
	})
}

func FuzzNormalizePublishInput(f *testing.F) {
	s := strings.Repeat("ab", 32)
	f.Add(`{"files":[{"as":"index.html","path":"/o/` + s + `.html","uri":"artifact-file://h/` + s + `"}],"slug":"` + slugA + `"}`)
	f.Add(`{"files":7}`)
	f.Fuzz(func(t *testing.T, in string) {
		out, shas, ok := NormalizePublishInput(ToolPublishFiles, json.RawMessage(in))
		if !ok || len(shas) == 0 {
			return
		}
		if strings.Contains(string(out), `"uri"`) {
			t.Fatalf("uri kept: %s", out)
		}
		for _, h := range shas {
			if !ValidSHA(h) {
				t.Fatalf("bound sha %q", h)
			}
		}
	})
}

func FuzzTitleKey(f *testing.F) {
	f.Add("Notes", "")
	f.Add("<"+testTag+">Notes</"+testTag+">", testTag)
	f.Fuzz(func(t *testing.T, raw, tag string) {
		k := TitleKey(raw, tag)
		if k != "" && !ValidSHA(k) {
			t.Fatalf("key %q", k)
		}
	})
}

func FuzzDecodeLegacy(f *testing.F) {
	f.Add(`{"schema":2,"artifacts":{"a":{"title":"T","copies":{}}}}`)
	f.Add(`{"artifacts":{"x":{"owner":"` + acctHome + `","mirrors":{}}}}`)
	f.Fuzz(func(t *testing.T, in string) {
		lm, err := DecodeLegacy([]byte(in))
		if err == nil && lm.Format != 1 && lm.Format != 2 {
			t.Fatalf("format %d", lm.Format)
		}
	})
}

func FuzzManifestValidate(f *testing.F) {
	b, _ := Encode(testManifest())
	f.Add(string(b))
	f.Add(`{"v":3,"seq":1,"gate":{"roundtrip":"unverified"},"profiles":{},"pairs":{}}`)
	f.Fuzz(func(t *testing.T, in string) {
		m, err := Decode([]byte(in))
		if err != nil {
			return
		}
		_ = Validate(m, compat.ArtifactsSpec{}, 10)
	})
}
