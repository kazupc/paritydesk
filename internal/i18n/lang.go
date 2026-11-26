package i18n

import (
	"fmt"
	"strings"
)

// Source names where the language came from (doctor shows it).
type Source string

// Sources, highest priority first.
const (
	SourceFlag           Source = "flag"
	SourceEnv            Source = "env"
	SourceConfig         Source = "config"          // [ui] lang
	SourceLocale         Source = "locale"          // LC_ALL, LC_MESSAGES, LANG
	SourceConfigFallback Source = "config-fallback" // [ui] fallback_lang
	SourceDefault        Source = "default"
)

// ParseSetting parses a flag, env or config value: "" and "auto" give
// ok=false (not set); "en"/"ja" in any ASCII case give the language;
// anything else is an error.
func ParseSetting(s string) (l Lang, ok bool, err error) {
	switch strings.ToLower(s) {
	case "", "auto":
		return "", false, nil
	case "en":
		return English, true, nil
	case "ja":
		return Japanese, true, nil
	}
	return "", false, fmt.Errorf("i18n: %q is not auto, en or ja", s)
}

// FromLocale maps one POSIX locale value
// (language[_territory][.codeset][@modifier]): Japanese when the language
// part is "ja" in any case, English otherwise; ok=false for "".
func FromLocale(v string) (l Lang, ok bool) {
	if v == "" {
		return "", false
	}
	lang := v
	if i := strings.IndexAny(lang, "_.@"); i >= 0 {
		lang = lang[:i]
	}
	if strings.EqualFold(lang, "ja") {
		return Japanese, true
	}
	return English, true
}

// Inputs are the language settings of one process.
type Inputs struct {
	Flag, Env, Config, Fallback string
	Getenv                      func(string) string // LC_ALL, LC_MESSAGES, LANG
}

// Resolution is the chosen language and where it came from.
type Resolution struct {
	Lang       Lang
	Source     Source
	EnvInvalid bool // PARITYDESK_LANG held an invalid value (ignored)
}

// localeVars are read in this order; the first non-empty one wins.
var localeVars = []string{"LC_ALL", "LC_MESSAGES", "LANG"}

// Resolve applies the order flag > PARITYDESK_LANG > [ui] lang > locale >
// [ui] fallback_lang > English. An invalid Flag is an error; an invalid
// Env sets EnvInvalid and is skipped; invalid Config/Fallback are skipped
// silently. Fallback is consulted only when LC_ALL, LC_MESSAGES and LANG
// are all "".
func Resolve(in Inputs) (Resolution, error) {
	var res Resolution
	l, ok, err := ParseSetting(in.Flag)
	if err != nil {
		return Resolution{Lang: English, Source: SourceDefault}, err
	}
	if ok {
		res.Lang, res.Source = l, SourceFlag
	}
	if el, eok, eerr := ParseSetting(in.Env); eerr != nil {
		res.EnvInvalid = true
	} else if eok && res.Source == "" {
		res.Lang, res.Source = el, SourceEnv
	}
	if res.Source != "" {
		return res, nil
	}
	if cl, cok, cerr := ParseSetting(in.Config); cerr == nil && cok {
		res.Lang, res.Source = cl, SourceConfig
		return res, nil
	}
	getenv := in.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	for _, k := range localeVars {
		if v := getenv(k); v != "" {
			res.Lang, _ = FromLocale(v)
			res.Source = SourceLocale
			return res, nil
		}
	}
	if fl, fok, ferr := ParseSetting(in.Fallback); ferr == nil && fok {
		res.Lang, res.Source = fl, SourceConfigFallback
		return res, nil
	}
	res.Lang, res.Source = English, SourceDefault
	return res, nil
}
