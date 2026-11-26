package cli

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/orchestrator"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/state"
)

// jaEnv puts the harness into a Japanese UTF-8 terminal.
func jaEnv(h *harness) { h.env["LANG"] = "ja_JP.UTF-8" }

// C1: every short text and flag usage is a catalog id, with the naming
// rule flag.<name> (flag.<command path>.<name> only for a different text).
func TestHelpTextsAreMessages(t *testing.T) {
	texts := map[string]map[string]bool{} // flag name -> English texts
	var walk func(c *command, path []string)
	walk = func(c *command, path []string) {
		if c.short == "" || !i18n.Has(c.short) {
			t.Errorf("%v: short %q is not a catalog id", path, c.short)
		}
		fs := flag.NewFlagSet("x", flag.ContinueOnError)
		(&globalFlags{}).register(fs)
		if c.flags != nil {
			c.flags(fs)
		}
		fs.VisitAll(func(f *flag.Flag) {
			id := i18n.Msg(f.Usage)
			if !i18n.Has(id) {
				t.Errorf("%v --%s: usage %q is not a catalog id", path, f.Name, f.Usage)
				return
			}
			if texts[f.Name] == nil {
				texts[f.Name] = map[string]bool{}
			}
			texts[f.Name][i18n.En(id)] = true
			want := "flag." + f.Name
			if string(id) != want && string(id) != "flag."+strings.Join(path, ".")+"."+f.Name {
				t.Errorf("%v --%s: id %s is neither %s nor qualified by the command path", path, f.Name, id, want)
			}
		})
		for _, s := range c.sub {
			walk(s, append(append([]string(nil), path...), s.name))
		}
	}
	walk(root(), nil)
	for id := range i18n.Catalog() {
		s := string(id)
		if !strings.HasPrefix(s, "flag.") {
			continue
		}
		parts := strings.Split(s, ".")
		name := parts[len(parts)-1]
		if texts[name] == nil {
			t.Errorf("%s names no registered flag", s)
			continue
		}
		if !i18n.Has(i18n.Msg("flag." + name)) {
			t.Errorf("flag.%s is missing", name)
		}
		if len(parts) > 2 && len(texts[name]) < 2 {
			t.Errorf("%s is qualified, but every command registers --%s with the same text", s, name)
		}
	}
}

// C2 (Japanese): the usage of every command path with --lang ja.
func TestUsageGoldenJapanese(t *testing.T) {
	for _, path := range commandPaths(root(), nil) {
		name := "ja/usage/" + strings.Join(append([]string{"paritydesk"}, path...), "_")
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			jaEnv(h)
			code, out, errOut := h.run("", append(append([]string(nil), path...), "--help", "--lang", "ja")...)
			if code != 0 || errOut != "" {
				t.Fatalf("code %d stderr %q", code, errOut)
			}
			golden(t, name, out)
		})
	}
}

// stripDisplay removes the language members of a decoded document.
func stripDisplay(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			if k == "lang" || strings.HasPrefix(k, "display_") {
				continue
			}
			out[k] = stripDisplay(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = stripDisplay(e)
		}
		return out
	}
	return v
}

// runJSON runs one jsonCases entry and decodes its document.
func runJSON(t *testing.T, args []string, setup func(*harness), ja bool) (map[string]any, string) {
	t.Helper()
	h := newHarness(t)
	if setup != nil {
		setup(h)
	}
	argv := append([]string{"--json"}, args...)
	if ja {
		jaEnv(h)
		argv = append([]string{"--lang", "ja"}, argv...)
	}
	_, out, _ := h.run("", argv...)
	out = h.normalize(out)
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return doc, out
}

// C3: --lang ja changes no JSON member but adds lang and display_lines
// (and the question's display members); every document with lines gets a
// Japanese golden (7.4 item 5).
func TestJSONLanguageInvariant(t *testing.T) {
	for _, tc := range jsonCases {
		t.Run(tc.name, func(t *testing.T) {
			en, _ := runJSON(t, tc.args, tc.setup, false)
			ja, raw := runJSON(t, tc.args, tc.setup, true)
			if _, ok := en["display_lines"]; ok {
				t.Fatal("the English document has display_lines")
			}
			if _, ok := en["lang"]; ok {
				t.Fatal("the English document has lang")
			}
			if checks, ok := ja["checks"].([]any); ok {
				for _, c := range checks {
					if m := c.(map[string]any); m["id"] == "lang" {
						delete(m, "detail")
					}
				}
				for _, c := range en["checks"].([]any) {
					if m := c.(map[string]any); m["id"] == "lang" {
						delete(m, "detail")
					}
				}
			}
			if !reflect.DeepEqual(stripDisplay(en), stripDisplay(ja)) {
				t.Fatalf("the documents differ beyond the language members:\nen %v\nja %v", en, ja)
			}
			lines, ok := en["lines"].([]any)
			if !ok || len(lines) == 0 {
				return
			}
			dl, _ := ja["display_lines"].([]any)
			if ja["lang"] != "ja" || len(dl) != len(lines) || reflect.DeepEqual(dl, lines) {
				t.Fatalf("display_lines %v for lines %v (lang %v)", dl, lines, ja["lang"])
			}
			if s, _ := lines[len(lines)-1].(string); strings.HasPrefix(s, "DESTRUCTIVE: ") {
				if d, _ := dl[len(dl)-1].(string); !strings.HasPrefix(d, "DESTRUCTIVE: ") {
					t.Fatalf("the last display line is %q", d)
				}
			}
			golden(t, "ja/json/"+tc.name, raw)
		})
	}
}

// C5: Japanese human output.
func TestHumanGoldenJapanese(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		stdin string
		setup func(h *harness)
	}{
		{name: "status", args: []string{"status"}},
		{name: "profile_list", args: []string{"profile", "list"}},
		{name: "plan", args: []string{"plan", "home"}},
		{name: "switch_confirm", args: []string{"switch", "home"}, stdin: "\uff59\n", setup: func(h *harness) {
			h.tty = true
			s := fakePlan().Summarize()
			h.svc.confirm = &orchestrator.Confirmation{Kind: orchestrator.ConfirmPlan, Plan: fakePlan(), Summary: &s}
		}},
		{name: "error_breaker", args: []string{"switch", "home"}, setup: func(h *harness) {
			h.svc.errs["switch"] = pderr.NewT(pderr.DeletionBreaker, "switch/P5", i18n.ErrValidateBreaker, 5, "sessions", 3, 8)
		}},
		{name: "doctor", args: []string{"doctor"}},
		{name: "help", args: []string{"--help"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			jaEnv(h)
			if tc.setup != nil {
				tc.setup(h)
			}
			code, out, errOut := h.run(tc.stdin, append([]string{"--lang", "ja", "--no-color"}, tc.args...)...)
			got := h.normalize(out + "--- stderr\n" + errOut)
			if tc.name == "switch_confirm" && (code != 0 || len(h.svc.calls) == 0) {
				t.Fatalf("the full-width y was not accepted: code %d\n%s", code, got)
			}
			golden(t, "ja/human/"+tc.name, got)
		})
	}
}

// C6: when English is forced, and the warnings.
func TestLangForcedEnglish(t *testing.T) {
	h := newHarness(t)
	jaEnv(h)
	_, out, errOut := h.run("", "--lang", "ja", "--ascii", "status")
	if !strings.Contains(out, "Active profile: ") || strings.Count(errOut, i18n.En(i18n.LangNeedsUTF8)) != 1 {
		t.Errorf("--ascii: out %q err %q", out, errOut)
	}
	h = newHarness(t)
	h.env["LANG"] = "ja_JP.eucJP"
	_, out, errOut = h.run("", "status")
	if !strings.Contains(out, "Active profile: ") || errOut != "" {
		t.Errorf("eucJP: out %q err %q", out, errOut)
	}
	h = newHarness(t)
	h.env["PARITYDESK_LANG"] = "xx"
	code, out, errOut := h.run("", "status")
	if code != 0 || !strings.Contains(out, "Active profile: ") || strings.Count(errOut, "PARITYDESK_LANG=xx is not") != 1 {
		t.Errorf("invalid env: %d %q %q", code, out, errOut)
	}
	h = newHarness(t)
	h.env["PARITYDESK_LANG"] = strings.Repeat("a", 40)
	_, _, errOut = h.run("", "status")
	if strings.Contains(errOut, strings.Repeat("a", 17)) || !strings.Contains(errOut, strings.Repeat("a", 15)) {
		t.Errorf("the echoed value is not bounded: %q", errOut)
	}
	h = newHarness(t)
	code, _, errOut = h.run("", "--lang", "xx", "status")
	if code != 2 || !strings.Contains(errOut, i18n.En(i18n.UsageLang)) {
		t.Errorf("--lang xx: %d %q", code, errOut)
	}
	h = newHarness(t)
	jaEnv(h)
	_, out, errOut = h.run("", "--lang", "ja", "plan", "--json", "--", "home")
	var v runViewProbe
	if err := json.Unmarshal([]byte(out), &v); err != nil || errOut != "" || len(v.DisplayLines) == 0 || v.Lines[0] == v.DisplayLines[0] ||
		!strings.HasPrefix(v.Lines[0], "Plan: ") {
		t.Errorf("--lang ja --json: %v %q %q", err, out, errOut)
	}
	h = newHarness(t)
	h.env = map[string]string{}
	writeFile(h, ".config/paritydesk/config.toml", "[ui]\nlang = \"ja\"\n")
	_, out, _ = h.run("", "plan", "home")
	if !strings.Contains(out, "Plan: ") || strings.Contains(out, "?") {
		t.Errorf("config lang without a locale: %q", out)
	}
	h = newHarness(t)
	h.env["PARITYDESK_LANG"] = "ja"
	h.root = state.NewRoot("/data")
	h.root.Run = &state.Run{ID: "run-0001"}
	_, out, _ = h.run(`{"session_id":"00000000-0000-4000-8000-000000000001","hook_event_name":"SessionStart"}`, "hook", "session-start")
	if !strings.Contains(out, "interrupted") {
		t.Errorf("hook line is not English: %q", out)
	}
	en := newHarness(t)
	_, enOut, _ := en.run("", "completion", "zsh")
	h = newHarness(t)
	jaEnv(h)
	_, jaOut, _ := h.run("", "completion", "zsh", "--lang", "ja")
	if enOut != jaOut {
		t.Error("completion differs with --lang ja")
	}
	h = newHarness(t)
	jaEnv(h)
	_, _, errOut = h.run("", "switch", "--from-runner", "--lang", "ja", "--yes", "home")
	if strings.ContainsFunc(errOut, func(r rune) bool { return r > 0x2fff }) {
		t.Errorf("the detached job printed Japanese: %q", errOut)
	}
}

// runViewProbe decodes the language members of a plan document.
type runViewProbe struct {
	Lines        []string `json:"lines"`
	DisplayLines []string `json:"display_lines"`
	Lang         string   `json:"lang"`
}

// C7: --lang counts before or after the command and around --help.
func TestLangFlagPosition(t *testing.T) {
	jaActive := i18n.TIn(i18n.Japanese, i18n.StatusActive, "work")
	jaUsage := strings.SplitN(i18n.TIn(i18n.Japanese, i18n.HelpUsage, "x", "y"), " ", 2)[0]
	for _, args := range [][]string{
		{"--lang", "ja", "status"}, {"status", "--lang", "ja"},
		{"--lang", "ja", "--help"}, {"status", "--help", "--lang", "ja"}, {"status", "--lang=ja", "-h"},
	} {
		h := newHarness(t)
		jaEnv(h)
		_, out, _ := h.run("", args...)
		help := strings.Contains(strings.Join(args, " "), "-h")
		if (help && !strings.HasPrefix(out, jaUsage)) || (!help && !strings.Contains(out, jaActive)) {
			t.Errorf("%v: not Japanese:\n%s", args, out)
		}
	}
	// On the help path an invalid --lang is ignored wherever it stands:
	// help prints, exit 0, in the language resolved without it.
	enUsage := strings.SplitN(i18n.TIn(i18n.English, i18n.HelpUsage, "x", "y"), " ", 2)[0]
	for _, tc := range []struct {
		locale string
		args   []string
		wantJA bool
	}{
		{"ja_JP.UTF-8", []string{"status", "--help", "--lang", "xx"}, true},
		{"ja_JP.UTF-8", []string{"status", "--lang", "xx", "--help"}, true},
		{"ja_JP.UTF-8", []string{"--lang", "xx", "--help"}, true},
		{"ja_JP.UTF-8", []string{"--lang=xx", "-h"}, true},
		{"en_US.UTF-8", []string{"status", "--lang", "ja", "--lang", "xx", "--help"}, true},
		{"en_US.UTF-8", []string{"--lang", "ja", "status", "--lang", "xx", "--help"}, true},
		{"en_US.UTF-8", []string{"--lang", "xx", "--help"}, false},
		{"ja_JP.UTF-8", []string{"status", "--lang", "xx", "--help", "--lang", "en"}, false},
	} {
		h := newHarness(t)
		h.env["LANG"] = tc.locale
		code, out, errOut := h.run("", tc.args...)
		want := enUsage
		if tc.wantJA {
			want = jaUsage
		}
		if code != 0 || !strings.HasPrefix(out, want) || errOut != "" {
			t.Errorf("LANG=%s %v: exit %d, stderr %q, want %q first:\n%s", tc.locale, tc.args, code, errOut, want, out)
		}
	}
}

// C8: [ui] keys decide display_lines where no locale is set.
func TestConfigLangUsedWithoutLocale(t *testing.T) {
	for _, tc := range []struct {
		cfg, lang string
		wantJA    bool
	}{
		{"[ui]\nfallback_lang = \"ja\"\n", "", true},
		{"[ui]\nlang = \"ja\"\n", "", true},
		{"[ui]\nfallback_lang = \"ja\"\n", "en_US.UTF-8", false},
	} {
		h := newHarness(t)
		h.env = map[string]string{}
		if tc.lang != "" {
			h.env["LANG"] = tc.lang
		}
		writeFile(h, ".config/paritydesk/config.toml", tc.cfg)
		_, out, _ := h.run("", "plan", "--json", "--", "home")
		var v runViewProbe
		if err := json.Unmarshal([]byte(out), &v); err != nil {
			t.Fatal(err)
		}
		if got := v.Lang == "ja" && len(v.DisplayLines) == len(v.Lines); got != tc.wantJA || !strings.HasPrefix(v.Lines[0], "Plan: ") {
			t.Errorf("%q LANG=%q: %+v", tc.cfg, tc.lang, v)
		}
	}
}

// C9: init records the fallback language for a locale or env choice and
// hints for a flag; the orchestrator writes it (TestInitFallbackLang).
func TestInitRecordsJapanese(t *testing.T) {
	cases := []struct {
		env       map[string]string
		args      []string
		want      i18n.Lang
		saved     bool
		hint      bool
		configTOM string
	}{
		{env: map[string]string{"LANG": "ja_JP.UTF-8"}, want: i18n.Japanese, saved: true},
		{env: map[string]string{"LANG": "en_US.UTF-8", "PARITYDESK_LANG": "ja"}, want: i18n.Japanese, saved: true},
		{env: map[string]string{"LANG": "en_US.UTF-8"}, args: []string{"--lang", "ja"}, hint: true},
		{env: map[string]string{"LANG": "en_US.UTF-8"}},
		{env: map[string]string{"LANG": "ja_JP.UTF-8"}, want: i18n.Japanese, configTOM: "[ui]\nfallback_lang = \"en\"\n"},
	}
	for _, tc := range cases {
		h := newHarness(t)
		h.env = tc.env
		if tc.configTOM != "" {
			writeFile(h, ".config/paritydesk/config.toml", tc.configTOM)
		}
		code, out, errOut := h.run("", append([]string{"init", "--alias", "work", "--unmanaged"}, tc.args...)...)
		if code != 0 {
			t.Fatalf("%v: code %d %s", tc.env, code, errOut)
		}
		if h.svc.initOpts.FallbackLang != tc.want {
			t.Errorf("%v %v: FallbackLang %q", tc.env, tc.args, h.svc.initOpts.FallbackLang)
		}
		saved := strings.Contains(out, i18n.TIn(i18n.Japanese, i18n.InitLangSaved))
		hint := strings.Contains(out, i18n.TIn(i18n.Japanese, i18n.InitLangHint))
		if saved != tc.saved || hint != tc.hint {
			t.Errorf("%v %v: saved %v hint %v\n%s", tc.env, tc.args, saved, hint, out)
		}
	}
}

// C10: answers typed with a Japanese input method are folded.
func TestAnswerFolding(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"y\n", true}, {"Y\n", true}, {"yes\n", true}, {"\uff59\n", true}, {"\uff59\uff45\uff53\n", true}, {"y\u3000\n", true},
		{"\u306f\u3044\n", false}, {"n\n", false}, {"\n", false}, {"", false},
	} {
		h := newHarness(t)
		h.tty = true
		u := (&env{g: &globalFlags{}, stdin: strings.NewReader(tc.in), stdout: &strings.Builder{}, stderr: &strings.Builder{}, sys: h.sys}).newUI(false, false)
		ok, err := u.ask(t.Context(), "q? ")
		if err != nil || ok != tc.want {
			t.Errorf("%q: %v %v", tc.in, ok, err)
		}
	}
}

// commandRE finds "paritydesk <words>" in a text.
var commandRE = regexp.MustCompile(`paritydesk((?: [a-z][a-z-]*)+)`)

// C11: every "paritydesk <command>" of the English text appears verbatim
// in the Japanese text (the longest command path of root()).
func TestJapaneseKeepsCommands(t *testing.T) {
	paths := map[string]bool{}
	for _, p := range commandPaths(root(), nil) {
		if len(p) > 0 {
			paths[strings.Join(p, " ")] = true
		}
	}
	ja := i18n.CatalogIn(i18n.Japanese)
	ids := make([]string, 0, len(ja))
	for id := range ja {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	for _, s := range ids {
		id := i18n.Msg(s)
		en := i18n.En(id)
		for _, m := range commandRE.FindAllStringSubmatch(en, -1) {
			words := strings.Fields(m[1])
			best := ""
			for n := len(words); n > 0; n-- {
				if p := strings.Join(words[:n], " "); paths[p] {
					best = p
					break
				}
			}
			if best == "" {
				continue
			}
			if !strings.Contains(ja[id], "paritydesk "+best) {
				t.Errorf("%s: %q is missing in the Japanese text", id, "paritydesk "+best)
			}
		}
	}
}

// C12: execute restores both languages.
func TestExecuteRestoresLang(t *testing.T) {
	before, beforeDisplay := i18n.Current(), i18n.DisplayLang()
	h := newHarness(t)
	jaEnv(h)
	h.run("", "--lang", "ja", "status")
	if i18n.Current() != before || i18n.DisplayLang() != beforeDisplay {
		t.Errorf("languages leaked: %s %s", i18n.Current(), i18n.DisplayLang())
	}
}

// Under go test the language step applies buildDeps' guard: with the
// real system and a home that fsx.CheckTestPath refuses (the real home),
// it reads no config, so a test of Execute never opens the developer's
// own config. Here the "real" home is a temporary one holding a config
// with [ui] lang = "ja"; the error must stay English.
func TestLangStepReadsNoRealConfigUnderTest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG", "PARITYDESK_LANG", "PARITYDESK_CONFIG", "XDG_CONFIG_HOME"} {
		t.Setenv(k, "")
	}
	t.Setenv("LC_CTYPE", "en_US.UTF-8")
	cfg := filepath.Join(home, ".config", "paritydesk", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("[ui]\nlang = \"ja\"\nfallback_lang = \"ja\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := &env{g: &globalFlags{}, sys: realSystem()}
	if lang, fallback := e.readUI(); lang != "" || fallback != "" {
		t.Errorf("readUI read the config under the real home: %q %q", lang, fallback)
	}
	var out, errb strings.Builder
	if code := Execute(t.Context(), []string{"nope"}, strings.NewReader(""), &out, &errb); code != 2 {
		t.Fatalf("exit %d", code)
	}
	if !strings.HasPrefix(errb.String(), "paritydesk: "+i18n.TIn(i18n.English, pderr.TitleID(pderr.Usage))) {
		t.Errorf("the language step read the config under the real home:\n%s", errb.String())
	}
}

// C13: the hook reads no config for the language; commandWord skips the
// values of global flags.
func TestHookSkipsLangResolution(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--config", "x", "hook", "session-start"}, "hook"},
		{[]string{"--lang=ja", "status"}, "status"},
		{[]string{"--json", "--state-dir", "/s", "plan", "home"}, "plan"},
		{[]string{"--", "completion"}, "completion"},
	} {
		if got := commandWord(tc.args); got != tc.want {
			t.Errorf("commandWord(%v) = %q", tc.args, got)
		}
	}
	h := newHarness(t)
	dir := filepath.Join(h.home, "unreadable")
	if err := os.MkdirAll(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	h.env["PARITYDESK_CONFIG"] = filepath.Join(dir, "config.toml")
	h.env["PARITYDESK_LANG"] = "ja"
	h.root = state.NewRoot("/data")
	code, out, errOut := h.run("", "hook", "session-start")
	if code != 0 || errOut != "" || out != "" {
		t.Errorf("hook: %d %q %q", code, out, errOut)
	}
	e := &env{g: &globalFlags{}, sys: h.sys}
	if english := englishCommands[commandWord([]string{"--config", "x", "hook"})]; !english {
		t.Error("hook is not an English command")
	}
	_ = e
	_ = config.LangAuto
}

// TestOnlineArtifactsJapanese: the online and artifacts documents gain
// lang, display_lines and the question's display members with --lang ja,
// and nothing else changes (7.4 item 5); one Japanese golden each.
func TestOnlineArtifactsJapanese(t *testing.T) {
	legacy, err := filepath.Abs(filepath.Join("..", "artifacts", "testdata", "legacy_v2"))
	if err != nil {
		t.Fatal(err)
	}
	type docCase struct {
		name  string
		args  []string
		setup func(h *harness)
	}
	var cases []docCase
	for _, c := range onlineCases {
		cases = append(cases, docCase{c.name, c.args, c.setup})
	}
	for _, c := range artifactsCases {
		cases = append(cases, docCase{c.name, c.args, c.setup})
	}
	run := func(t *testing.T, tc docCase, ja bool) (map[string]any, string) {
		h := newHarness(t)
		if tc.setup != nil {
			tc.setup(h)
		}
		args := append([]string{"--json"}, tc.args...)
		for i, a := range args {
			if a == "LEGACY" {
				args[i] = legacy
			}
		}
		if ja {
			jaEnv(h)
			args = append([]string{"--lang", "ja"}, args...)
		}
		_, out, _ := h.run("", args...)
		out = h.normalize(out)
		var doc map[string]any
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return doc, out
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			en, _ := run(t, tc, false)
			ja, raw := run(t, tc, true)
			if !reflect.DeepEqual(stripDisplay(en), stripDisplay(ja)) {
				t.Fatalf("the documents differ beyond the language members:\nen %v\nja %v", en, ja)
			}
			if q, ok := ja["question"].(map[string]any); ok && q != nil {
				if q["display_header"] == nil || q["display_header"] == q["header"] {
					t.Fatalf("question without a Japanese display_header: %v", q)
				}
				for _, o := range q["options"].([]any) {
					if o.(map[string]any)["display_label"] == nil {
						t.Fatalf("option without display_label: %v", o)
					}
				}
			}
			if lines, _ := en["lines"].([]any); len(lines) > 0 {
				if dl, _ := ja["display_lines"].([]any); len(dl) != len(lines) || ja["lang"] != "ja" {
					t.Fatalf("display_lines %v for %v", dl, lines)
				}
			}
			golden(t, "ja/json/"+tc.name, raw)
		})
	}
}
