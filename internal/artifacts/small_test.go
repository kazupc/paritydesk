package artifacts

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/ids"
)

func TestValidTitle(t *testing.T) {
	long := strings.Repeat("a", 256)
	for title, want := range map[string]bool{
		"Notes":                           true,
		"":                                false,
		long:                              true,
		long + "a":                        false,
		"tab\there":                       false,
		"bidi\u202ehere":                  false,
		"zero\u200bwidth":                 false,
		"line\u2028sep":                   false,
		"has <untrusted-document-ab>":     false,
		"\xff\xfe":                        false,
		"caf\u00e9":                       true,
		strings.Repeat("\U0001F600", 128): true,
		strings.Repeat("\U0001F600", 129): false,
	} {
		if got := ValidTitle(title); got != want {
			t.Errorf("ValidTitle(%q) = %v", title, got)
		}
	}
}

func TestTitleKey(t *testing.T) {
	plain := TitleKey("Notes", "")
	if plain == "" || TitleKey("<"+testTag+">Notes</"+testTag+">", testTag) != plain ||
		TitleKey("<"+testTag+">Notes</"+testTag+">", "") != plain {
		t.Fatal("wrapped and plain titles differ")
	}
	if TitleKey("<untrusted-document-zz>Notes</untrusted-document-zz>", "") == plain {
		t.Fatal("a tag outside the pattern was removed")
	}
	if TitleKey("\x01", "") != "" {
		t.Fatal("invalid title has a key")
	}
	// An NFC-unstable title is never passed in an input (8.8).
	if NFCStable("cafe\u0301") || !NFCStable("caf\u00e9") {
		t.Fatal("NFC check")
	}
}

func TestInClaude(t *testing.T) {
	for env, want := range map[string]bool{"": false, "CLAUDECODE": true, "CLAUDE_CODE_SESSION_ID": true, "CLAUDE_CODE_ENTRYPOINT": true, "OTHER": false} {
		got := InClaude(func(k string) string {
			if k == env {
				return "1"
			}
			return ""
		})
		if got != want {
			t.Errorf("%s: %v", env, got)
		}
	}
	if InClaude(nil) {
		t.Fatal("nil getenv")
	}
}

func TestCompare(t *testing.T) {
	k := KnobsFor(RoleChunk, testSpec(t))
	a := "<!DOCTYPE html>\n<html><head><title>One</title></head><body>x</body></html>"
	cases := map[string]struct {
		back, want string
	}{
		"exact":         {a, ClassExact},
		"wrapped bare":  {"<untrusted-document-ffffffff>" + a + "</untrusted-document-ffffffff>", ClassWrapped},
		"wrapped nl":    {"<untrusted-document-ffffffff>\n" + a + "\n</untrusted-document-ffffffff>", ClassWrapped},
		"wrapped inner": {"<p><untrusted-document-ffffffff>" + a + "</untrusted-document-ffffffff></p>", ClassWrapped},
		"title only":    {strings.Replace(a, "<title>One</title>", "<TITLE>Two</TITLE>", 1), ClassTitleOnly},
		"other":         {a + "x", ClassOther},
	}
	for name, c := range cases {
		if got := Compare(a, c.back, k); got != c.want {
			t.Errorf("%s: %s", name, got)
		}
	}
	// Content that itself contains a wrapper round-trips exactly.
	q := "<p><untrusted-document-00000000>x</untrusted-document-00000000></p>"
	if Compare(q, q, k) != ClassExact {
		t.Fatal("quoted wrapper")
	}
	// The archived content quoting the tag is not double wrapping.
	if Compare(q, "<p>other</p>", k) != ClassOther {
		t.Fatal("other")
	}
}

func testManifest() *Manifest {
	m := NewManifest()
	t0 := time.Date(2026, 11, 26, 10, 20, 0, 0, time.UTC)
	m.Seq, m.UpdatedAt = 17, t0
	m.Gate = Gate{Roundtrip: GateExact, AppVersion: appVer, At: t0}
	m.Profiles["home"] = &ProfileRecord{Replica: repHome, LastInventoryAt: t0, LastSyncAt: t0, LastSyncEpoch: 12}
	sha := strings.Repeat("a", 64)
	m.Pairs[PairIDFor(slugA)] = &Pair{Title: "Quarterly notes", TitleSHA: TitleKey("Quarterly notes", ""), CreatedAt: t0,
		TitleChangedAt: t0,
		Content: &Content{SHA256: sha, Bytes: 52311, Lines: 812, SavedAt: t0, SourceProfile: "work", SourceVersion: "1700000000-0000a",
			ChangedAt: t0, Icon: "notes"},
		Copies: map[ids.Alias]*Copy{
			"work": {Replica: repWork, Slug: slugA, URL: "https://claude.ai/artifact/" + slugA, State: StateLive, Origin: OriginRecorded,
				Read: ReadOwner, SyncedVersion: "1700000000-0000a", SyncedSHA: sha, SeenVersion: "1700000000-0000a",
				SeenTitleSHA: TitleKey("Quarterly notes", ""), SeenAt: t0},
		},
		History: []HistoryEntry{{SHA256: strings.Repeat("b", 64), Title: "Quarterly notes (old)", SavedAt: t0, SourceProfile: "home",
			SourceVersion: "1700000000-0000b", Reason: HistConflict}},
		Flags: Flags{Capabilities: CapFlags{State: CapNone, Names: []string{}}, AllowCreate: []ids.Alias{}, Restore: []ids.Alias{}},
	}
	m.Ignored = map[Slug]*IgnoredItem{slugB: {Alias: "home", Reason: "multi_file", Version: "1700000000-0000c", At: t0}}
	return m
}

func TestManifestRoundTripAndOrder(t *testing.T) {
	m := testManifest()
	if err := Validate(m, compat.ArtifactsSpec{}, 10); err != nil {
		t.Fatal(err)
	}
	b, err := Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "manifest_v3.golden", string(b))
	m2, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := Encode(m2)
	if string(b) != string(b2) {
		t.Fatal("manifest does not round-trip")
	}
	// Example version ids use the fake prefix (M17).
	re := regexp.MustCompile(`"(?:source_version|synced_version|seen_version|version)": "([^"]*)"`)
	for _, mm := range re.FindAllStringSubmatch(string(b), -1) {
		if !strings.HasPrefix(mm[1], "1700000000-") {
			t.Errorf("version id %q", mm[1])
		}
	}
}

func TestManifestValidation(t *testing.T) {
	sp := compat.ArtifactsSpec{}
	cases := map[string]func(m *Manifest){
		"version":  func(m *Manifest) { m.V = 2 },
		"gate":     func(m *Manifest) { m.Gate.Roundtrip = "maybe" },
		"bad enum": func(m *Manifest) { m.Pairs[PairIDFor(slugA)].Copies["work"].State = "gone" },
		"bad url":  func(m *Manifest) { m.Pairs[PairIDFor(slugA)].Copies["work"].URL = "https://example.com/x" },
		"slug twice": func(m *Manifest) {
			p := m.Pairs[PairIDFor(slugA)]
			c := *p.Copies["work"]
			p.Copies["home"] = &c
		},
		"history overflow": func(m *Manifest) {
			p := m.Pairs[PairIDFor(slugA)]
			for i := 0; i < 11; i++ {
				p.History = append(p.History, p.History[0])
			}
		},
		"ignored overflow": func(m *Manifest) {
			for i := 0; i < MaxIgnored+1; i++ {
				m.Ignored[Slug("00000000-0000-4000-8000-"+pad12(i))] = m.Ignored[slugB]
			}
		},
		"local time": func(m *Manifest) { m.UpdatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("x", 3600)) },
		"bad title":  func(m *Manifest) { m.Pairs[PairIDFor(slugA)].Title = "a\x00b" },
	}
	for name, edit := range cases {
		m := testManifest()
		edit(m)
		if err := Validate(m, sp, 10); err == nil {
			t.Errorf("%s: valid", name)
		}
	}
}

func pad12(i int) string { return fmt.Sprintf("%012x", i) }

func TestPairIDFor(t *testing.T) {
	id := PairIDFor(slugA)
	if !ValidPairID(string(id)) || PairIDFor(slugA) != id || PairIDFor(slugB) == id {
		t.Fatal(id)
	}
}

func TestURLID(t *testing.T) {
	for raw, want := range map[string]string{
		"https://claude.ai/artifact/" + slugA:                    slugA,
		"https://claude.ai/code/artifact/my-page-" + slugA:       slugA,
		"https://claude.ai/code/frame/" + strings.ToUpper(slugA): slugA,
		"https://claude.ai/artifact/AbCdEfGhJkLmNpQrStUvWx":      "AbCdEfGhJkLmNpQrStUvWx",
		"http://claude.ai/artifact/" + slugA:                     "",
		"https://claude.ai.example.com/artifact/" + slugA:        "",
		"https://claude.ai/artifact/" + slugA + "?x=1":           "",
	} {
		id, _, ok := URLID(raw)
		if (want == "") == ok || id != want {
			t.Errorf("%s: %q %v", raw, id, ok)
		}
	}
}
