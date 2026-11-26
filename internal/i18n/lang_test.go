package i18n

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

// T8: the language order flag > PARITYDESK_LANG > [ui] lang > locale >
// [ui] fallback_lang > English.
func TestResolve(t *testing.T) {
	type in struct{ flag, env, cfg, lcAll, lcMessages, lang, fallback string }
	type want struct {
		lang       Lang
		src        Source
		envInvalid bool
		err        bool
	}
	tests := []struct {
		in   in
		want want
	}{
		{in{"", "", "", "", "", "ja_JP.UTF-8", ""}, want{Japanese, SourceLocale, false, false}},
		{in{"", "", "", "C", "", "ja_JP.UTF-8", ""}, want{English, SourceLocale, false, false}},
		{in{"", "", "", "", "ja_JP.UTF-8", "en_US.UTF-8", ""}, want{Japanese, SourceLocale, false, false}},
		{in{"", "", "", "", "", "", ""}, want{English, SourceDefault, false, false}},
		{in{"", "", "ja", "", "", "en_US.UTF-8", ""}, want{Japanese, SourceConfig, false, false}},
		{in{"", "", "", "", "", "en_US.UTF-8", "ja"}, want{English, SourceLocale, false, false}},
		{in{"", "", "", "", "", "C", "ja"}, want{English, SourceLocale, false, false}},
		{in{"", "", "", "", "", "", "ja"}, want{Japanese, SourceConfigFallback, false, false}},
		{in{"", "", "en", "", "", "", "ja"}, want{English, SourceConfig, false, false}},
		{in{"", "en", "ja", "", "", "", ""}, want{English, SourceEnv, false, false}},
		{in{"ja", "en", "en", "", "", "", ""}, want{Japanese, SourceFlag, false, false}},
		{in{"auto", "ja", "", "", "", "", ""}, want{Japanese, SourceEnv, false, false}},
		{in{"JA", "", "", "", "", "", ""}, want{Japanese, SourceFlag, false, false}},
		{in{"jp", "", "", "", "", "", ""}, want{err: true}},
		{in{"", "JP", "", "", "", "ja_JP.UTF-8", ""}, want{Japanese, SourceLocale, true, false}},
		{in{"", "", "bogus", "", "", "", ""}, want{English, SourceDefault, false, false}},
		{in{"", "", "", "", "", "", "bogus"}, want{English, SourceDefault, false, false}},
		{in{"", "", "", "", "", "jav_ID.UTF-8", ""}, want{English, SourceLocale, false, false}},
		{in{"", "", "", "", "", "ja", ""}, want{Japanese, SourceLocale, false, false}},
		{in{"", "", "", "", "", "JA_jp.utf8", ""}, want{Japanese, SourceLocale, false, false}},
		{in{"", "", "", "", "", "ja_JP.eucJP", ""}, want{Japanese, SourceLocale, false, false}},
	}
	for _, tc := range tests {
		env := map[string]string{"LC_ALL": tc.in.lcAll, "LC_MESSAGES": tc.in.lcMessages, "LANG": tc.in.lang}
		res, err := Resolve(Inputs{Flag: tc.in.flag, Env: tc.in.env, Config: tc.in.cfg, Fallback: tc.in.fallback,
			Getenv: func(k string) string { return env[k] }})
		if tc.want.err {
			if err == nil {
				t.Errorf("%+v: no error", tc.in)
			}
			continue
		}
		if err != nil || res.Lang != tc.want.lang || res.Source != tc.want.src || res.EnvInvalid != tc.want.envInvalid {
			t.Errorf("%+v: got %+v %v, want %+v", tc.in, res, err, tc.want)
		}
	}
}

func TestParseSettingAndLocale(t *testing.T) {
	for _, s := range []string{"", "auto", "AUTO"} {
		if _, ok, err := ParseSetting(s); ok || err != nil {
			t.Errorf("ParseSetting(%q) = %v %v", s, ok, err)
		}
	}
	if l, ok, err := ParseSetting("En"); l != English || !ok || err != nil {
		t.Errorf("ParseSetting(En) = %v %v %v", l, ok, err)
	}
	if _, ok := FromLocale(""); ok {
		t.Error("an empty locale is a value")
	}
	for _, v := range []string{"C", "POSIX", "en_US.UTF-8"} {
		if l, _ := FromLocale(v); l != English {
			t.Errorf("FromLocale(%q) = %v", v, l)
		}
	}
}

// T10: the fallbacks of TIn.
func TestTInFallbacks(t *testing.T) {
	Register(map[Msg]string{
		"test.only_en": "only %s", "test.bad_verb": "count %d", "test.dropped": "a %s b %s",
		"test.extra": "x %s", "test.data": "data %s", "hook.test_line": "model text",
	})
	RegisterIn(Japanese, map[Msg]string{
		"test.bad_verb": "\u4ef6\u6570 %s", "test.dropped": "a %s", "test.extra": "x %s %s",
		"test.data": "\u30c7\u30fc\u30bf %s", "hook.test_line": "\u30e2\u30c7\u30eb",
	})
	tests := []struct {
		id   Msg
		args []any
		want string
	}{
		{"test.only_en", []any{"v"}, "only v"},
		{"no.such.id", nil, "no.such.id"},
		{"hook.test_line", nil, "model text"},
		{"test.bad_verb", []any{3}, "count 3"},
		{"test.dropped", []any{"1", "2"}, "a 1 b 2"},
		{"test.extra", []any{"1"}, "x 1"},
		{"test.data", []any{"%!x"}, "\u30c7\u30fc\u30bf %!x"},
	}
	for _, tc := range tests {
		if got := TIn(Japanese, tc.id, tc.args...); got != tc.want {
			t.Errorf("TIn(ja, %s) = %q, want %q", tc.id, got, tc.want)
		}
	}
	defer SetLang(SetLang(Japanese))
	if got := T("test.data", "v"); got != "\u30c7\u30fc\u30bf v" {
		t.Errorf("T in the process language = %q", got)
	}
}

type alias string

type point struct{ X, Y int }

// T11: Text marshals to English, unmarshals as Raw, lists join per
// language, MapStrings reaches nested arguments, Tx normalizes.
func TestTextJSON(t *testing.T) {
	Register(map[Msg]string{"test.text": "t %s"})
	RegisterIn(Japanese, map[Msg]string{"test.text": "\u30c6 %s"})
	tx := Tx("test.text", "a")
	b, err := json.Marshal(tx)
	if err != nil || string(b) != `"t a"` {
		t.Fatalf("marshal = %s %v", b, err)
	}
	var back Text
	if err := json.Unmarshal([]byte(`"t <b>"`), &back); err != nil || back.In(Japanese) != "t <b>" || back.ID != "" {
		t.Fatalf("unmarshal = %+v %v", back, err)
	}
	if b, _ := json.Marshal(Raw("<a&b>")); string(b) != "\"\\u003ca\\u0026b\\u003e\"" {
		t.Errorf("json.Marshal applies its HTML escaping: %s", b)
	}
	l := List{Raw("x"), Tx("test.text", "y")}
	if got := l.In(English); got != "x; t y" {
		t.Errorf("English list = %q", got)
	}
	if got := l.In(Japanese); got != "x / \u30c6 y" {
		t.Errorf("Japanese list = %q", got)
	}
	nested := Tx("test.text", Tx("test.text", "in")).MapStrings(strings.ToUpper)
	if got := nested.English(); got != "t t IN" {
		t.Errorf("MapStrings nested = %q", got)
	}
	if got := Raw("raw").MapStrings(strings.ToUpper).English(); got != "RAW" {
		t.Errorf("MapStrings raw = %q", got)
	}
	n := Tx("test.text", alias("a"), int32(4), point{1, 2})
	if _, ok := n.Args[0].(string); !ok {
		t.Errorf("named string kept its type: %T", n.Args[0])
	}
	if _, ok := n.Args[1].(int64); !ok {
		t.Errorf("int32 became %T", n.Args[1])
	}
	if s, ok := n.Args[2].(string); !ok || s != "{1 2}" {
		t.Errorf("struct became %T %v", n.Args[2], n.Args[2])
	}
	if got := Tx("test.text", "long text").Cut(4).English(); got != "t l\u2026" {
		t.Errorf("Cut = %q", got)
	}
}

// T14: Error renders English, unwraps to its base and is a Texter.
func TestErrorType(t *testing.T) {
	Register(map[Msg]string{"test.err": "something %s"})
	e := NewErr("test.err", "broke")
	if e.Error() != "something broke" {
		t.Fatalf("Error() = %q", e.Error())
	}
	base := errors.New("base")
	w := fmt.Errorf("ctx: %w", WrapErr(base, Tx("test.err", "x")))
	if !errors.Is(w, base) {
		t.Error("errors.Is lost the base")
	}
	var tx Texter
	if !errors.As(w, &tx) || tx.I18nText().ID != "test.err" {
		t.Error("errors.As found no Texter")
	}
}

func TestVerbs(t *testing.T) {
	v, explicit, err := Verbs("a %-13s %d %% %[1]q")
	if err == nil {
		t.Errorf("an argument used twice is no error: %v %v", v, explicit)
	}
	v, explicit, err = Verbs("%[2]s %[1]d")
	if err != nil || !explicit || v[1].Spec != "d" || v[2].Spec != "s" {
		t.Errorf("explicit = %v %v %v", v, explicit, err)
	}
	for _, bad := range []string{"%", "%*d", "%.*d", "%[3]s", "%y", "%[0]s"} {
		if _, _, err := Verbs(bad); err == nil {
			t.Errorf("Verbs(%q) is no error", bad)
		}
	}
	if !Compatible("%s has %d", "%[2]d \u4ef6 %[1]s") {
		t.Error("reordering is compatible")
	}
	if Compatible("%[1]s", "%s") {
		t.Error("an English format with an explicit index is not compatible")
	}
	if Compatible("%-13s", "%s") {
		t.Error("a different width is not compatible")
	}
}

// T12 (property): every string argument consumed by %s appears verbatim,
// and "%!" appears only inside argument text, for every id in both
// languages.
func TestFormatProperty(t *testing.T) {
	ids := msgConstants(t)
	for _, l := range Langs {
		cat := CatalogIn(English)
		for _, id := range ids {
			f := cat[id]
			if l != English {
				if ja, ok := CatalogIn(l)[id]; ok {
					f = ja
				}
			}
			verbs, _, err := Verbs(f)
			if err != nil || len(verbs) == 0 {
				continue
			}
			rapid.Check(t, func(rt *rapid.T) {
				args := make([]any, len(verbs))
				var strs []string
				bang := 0
				for i := 1; i <= len(verbs); i++ {
					spec := verbs[i].Spec
					switch spec[len(spec)-1] {
					case 'd':
						args[i-1] = rapid.Int64().Draw(rt, "n")
					case 's', 'v':
						s := rapid.StringOf(rapid.SampledFrom([]rune("ab%!\x1b \u6f22\u30a2"))).Filter(func(s string) bool {
							return !strings.HasSuffix(s, "%")
						}).Draw(rt, "s")
						args[i-1] = s
						if spec == "s" || spec == "v" {
							strs = append(strs, s)
						}
						bang += strings.Count(s, "%!")
					default:
						s := rapid.StringOf(rapid.SampledFrom([]rune("abc"))).Draw(rt, "q")
						args[i-1] = s
					}
				}
				out := TIn(l, id, args...)
				for _, s := range strs {
					if !strings.Contains(out, s) {
						rt.Fatalf("%s (%s): %q lost argument %q", id, l, out, s)
					}
				}
				if got := strings.Count(out, "%!"); got != bang {
					rt.Fatalf("%s (%s): %q has %d %%! but the arguments %d", id, l, out, got, bang)
				}
			})
		}
	}
}

func TestLangsAreRestored(t *testing.T) {
	prev := SetLang(Japanese)
	if Current() != Japanese {
		t.Fatal("SetLang did not set")
	}
	SetLang(prev)
	if SetDisplayLang(Lang("xx")); DisplayLang() != English {
		t.Error("an unsupported language is not English")
	}
	SetDisplayLang(English)
}
