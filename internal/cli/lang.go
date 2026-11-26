package cli

import (
	"flag"
	"strings"

	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/orchestrator"
	"github.com/kazupc/paritydesk/internal/render"
)

// envLang is the environment variable of the message language.
const envLang = "PARITYDESK_LANG"

// langState is the language part of env.
type langState struct {
	lang i18n.Resolution // last resolution
	// validFlag is the last --lang value that resolved without error.
	validFlag string
	// forced: the resolved language is not English, but ASCII mode
	// forces English human output.
	forced bool
	// english: the command is English-only (hook, completion, the hidden
	// aliases command); nothing is resolved and no config is read.
	english bool
	// warnedEnv and warnedUTF8: lang.invalid_env and lang.needs_utf8
	// were already printed.
	warnedEnv, warnedUTF8 bool
	// uiPath, uiLang and uiFallback cache config.ReadUI for one path.
	uiPath, uiLang, uiFallback string
	uiRead                     bool
}

// englishCommands are the top-level command words whose output is
// English only (model-facing hook text, generated completion scripts).
var englishCommands = map[string]bool{"hook": true, "completion": true, aliasesCommand: true}

// valueFlags are the global flags that take a value, for commandWord.
var valueGlobalFlags = map[string]bool{
	orchestrator.FlagConfig: true, orchestrator.FlagStateDir: true, orchestrator.FlagAppSupportDir: true, flagLang: true,
}

// commandWord returns the first argument that is not a flag and not the
// value of a value-taking global flag (--config, --state-dir,
// --app-support-dir, --lang).
func commandWord(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			return a
		}
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue
		}
		if valueGlobalFlags[name] {
			i++
		}
	}
	return ""
}

// scanLangs returns the --lang values in a command's raw arguments, in
// order (up to a "--" terminator): --lang v, --lang=v, -lang v, -lang=v.
func scanLangs(args []string) []string {
	var vs []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		switch {
		case a == "--lang" || a == "-lang":
			if i+1 < len(args) {
				vs = append(vs, args[i+1])
				i++
			}
		case strings.HasPrefix(a, "--lang="):
			vs = append(vs, strings.TrimPrefix(a, "--lang="))
		case strings.HasPrefix(a, "-lang="):
			vs = append(vs, strings.TrimPrefix(a, "-lang="))
		}
	}
	return vs
}

// mode returns the output mode for the flags parsed so far.
func (e *env) mode() render.Mode {
	return render.DetectMode(render.Mode{
		JSON: e.g.JSON, Plain: e.g.Plain, NoColor: e.g.NoColor, ASCII: e.g.ASCII,
	}, e.sys.getenv, e.sys.isTerminal(e.stdout))
}

// readUI reads the [ui] keys of the resolved config file (cached per
// path); nothing when the home directory is unknown. Under `go test` it
// applies the same guard as buildDeps before reading: a test that runs
// with the real home (or a refused config path) reads nothing, so it
// never opens the developer's own config.
func (e *env) readUI() (lang, fallback string) {
	paths, home, err := e.paths()
	if err != nil {
		return "", ""
	}
	if !e.ls.uiRead || e.ls.uiPath != paths.ConfigFile {
		e.ls.uiPath, e.ls.uiRead = paths.ConfigFile, true
		e.ls.uiLang, e.ls.uiFallback = "", ""
		if checkTestPaths(home, paths.ConfigFile) == nil {
			e.ls.uiLang, e.ls.uiFallback = config.ReadUI(paths.ConfigFile)
		}
	}
	return e.ls.uiLang, e.ls.uiFallback
}

// applyLang resolves the language from the parsed flags, the
// environment, the config ([ui], read tolerantly) and the locale, and
// sets the display and process languages: the process language is
// English under --json, in ASCII mode, and for the detached job and the
// background check. It returns a usage error for an invalid --lang. fs
// and c may be nil (before dispatch). It is a no-op for English-only
// commands. Warnings are printed only for a leaf command (final).
func (e *env) applyLang(fs *flag.FlagSet, c *command) error {
	if c != nil && c.english {
		e.ls.english = true
	}
	if e.ls.english {
		return nil
	}
	cfgLang, cfgFallback := e.readUI()
	res, err := i18n.Resolve(i18n.Inputs{Flag: e.g.Lang, Env: e.sys.getenv(envLang),
		Config: cfgLang, Fallback: cfgFallback, Getenv: e.sys.getenv})
	if err != nil {
		e.setLangs(i18n.Resolution{Lang: i18n.English, Source: i18n.SourceDefault}, false)
		return usageError(i18n.UsageLang)
	}
	e.ls.validFlag = e.g.Lang
	m := e.mode()
	jobOutput := fs != nil && (flagTrue(fs, orchestrator.FlagFromRunner) || flagValue(fs, orchestrator.FlagAfterRun) != "")
	e.setLangs(res, m.ASCII || jobOutput)
	if c == nil || len(c.sub) > 0 || m.JSON || jobOutput {
		return nil
	}
	p := e.printer()
	if res.EnvInvalid && !e.ls.warnedEnv {
		e.ls.warnedEnv = true
		p.WarnText(i18n.Tx(i18n.LangInvalidEnv, render.Sanitize(e.sys.getenv(envLang), 16)))
	}
	if res.Source == i18n.SourceFlag && res.Lang != i18n.English && m.ASCII && !e.ls.warnedUTF8 {
		e.ls.warnedUTF8 = true
		p.WarnText(i18n.Tx(i18n.LangNeedsUTF8))
	}
	return nil
}

// setLangs records res and sets the display and process languages;
// english forces the process language to English.
func (e *env) setLangs(res i18n.Resolution, english bool) {
	e.ls.lang = res
	process := res.Lang
	if english || e.g.JSON {
		process = i18n.English
	}
	e.ls.forced = res.Lang != i18n.English && process == i18n.English && !e.g.JSON
	i18n.SetDisplayLang(res.Lang)
	i18n.SetLang(process)
}

// applyLangHelp is applyLang for the --help path: Go's flag package
// stops at --help before later flags are parsed, so a --lang after it is
// found by scanning the raw arguments. Every invalid --lang value is
// ignored (help still prints, in the language resolved without it): one
// the scan finds, and one the flag package stored before --help. The
// last valid value wins, as with the flag package; without one in args,
// the flag value of an earlier (root or group) parse stays.
func (e *env) applyLangHelp(fs *flag.FlagSet, c *command, args []string) {
	lang := e.g.Lang
	if _, _, err := i18n.ParseSetting(lang); err != nil {
		lang = e.ls.validFlag
	}
	for _, v := range scanLangs(args) {
		if _, _, err := i18n.ParseSetting(v); err == nil {
			lang = v
		}
	}
	e.g.Lang = lang
	saved := e.ls.warnedEnv
	e.ls.warnedEnv = true // help prints no language warnings
	_ = e.applyLang(fs, nil)
	e.ls.warnedEnv = saved
	_ = c
}

// flagTrue reports a parsed boolean flag of fs (false when absent).
func flagTrue(fs *flag.FlagSet, name string) bool {
	return flagValue(fs, name) == "true"
}

// flagValue returns a parsed flag's value as a string ("" when absent).
func flagValue(fs *flag.FlagSet, name string) string {
	if f := fs.Lookup(name); f != nil {
		return f.Value.String()
	}
	return ""
}
