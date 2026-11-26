package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/orchestrator"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/redact"
	"github.com/kazupc/paritydesk/internal/render"
)

// Version, Commit and Date are set at build time with -ldflags, for
// example "-X github.com/kazupc/paritydesk/internal/cli.Version=0.1.0"
// (see .goreleaser.yaml). Commit and Date stay empty in a plain go build.
// The default ends in "-dev" and carries the in-tree plugin's major and
// minor version (a policy test checks it), so a plain build from a clone
// is not reported as skewed by the SessionStart hook.
var (
	Version = "0.2.0-dev"
	Commit  = ""
	Date    = ""
)

// globalFlags are accepted by every command, before or after its name.
type globalFlags struct {
	JSON, Plain, NoColor, ASCII, ReadOnly bool
	Config, StateDir, AppSupportDir       string
	// Lang is --lang (auto, en or ja); never passed to detached jobs.
	Lang string
}

// flagLang names the message-language flag.
const flagLang = "lang"

// globalNames lists the global flags, for the usage text.
var globalNames = map[string]bool{
	"json": true, "plain": true, "no-color": true, "ascii": true, "read-only": true,
	orchestrator.FlagConfig: true, orchestrator.FlagStateDir: true, orchestrator.FlagAppSupportDir: true,
	flagLang: true,
}

// hiddenFlags are accepted but not listed in the usage text: internal
// flags set by the plugin's shim and by the detached runner.
var hiddenFlags = map[string]bool{
	"plugin-version":            true,
	orchestrator.FlagFromRunner: true,
	orchestrator.FlagLogOffset:  true,
	orchestrator.FlagLogInode:   true,
	orchestrator.FlagAfterRun:   true,
}

// hiddenUsage starts the usage string of a flag that is accepted but not
// listed in usage text, help or completion. Hiding is per flag, not per
// name, so a flag of the same name on another command stays visible.
const hiddenUsage = "(hidden) "

// isHidden reports whether a flag is left out of usage text and
// completion: an internal flag name, or a usage whose English text starts
// with hiddenUsage.
func isHidden(f *flag.Flag) bool {
	return hiddenFlags[f.Name] || strings.HasPrefix(i18n.En(i18n.Msg(f.Usage)), hiddenUsage)
}

// usage is the usage string of a flag: its catalog id, translated when
// the usage text is printed (printFlags, completion).
func usage(id i18n.Msg) string { return string(id) }

// flagUsageArgs supplies the arguments of a flag usage format (today only
// --only lists the category names); help and completion share it.
var flagUsageArgs = map[i18n.Msg]func() []any{
	i18n.FlagOnly: func() []any { return []any{strings.Join(categoryNames(), ",")} },
}

// flagUsageText renders a flag usage id in l.
func flagUsageText(l i18n.Lang, usageID string) string {
	id := i18n.Msg(usageID)
	var args []any
	if f := flagUsageArgs[id]; f != nil {
		args = f()
	}
	return i18n.TIn(l, id, args...)
}

func (g *globalFlags) register(fs *flag.FlagSet) {
	fs.BoolVar(&g.JSON, "json", g.JSON, usage(i18n.FlagJSON))
	fs.BoolVar(&g.Plain, "plain", g.Plain, usage(i18n.FlagPlain))
	fs.BoolVar(&g.NoColor, "no-color", g.NoColor, usage(i18n.FlagNoColor))
	fs.BoolVar(&g.ASCII, "ascii", g.ASCII, usage(i18n.FlagASCII))
	fs.BoolVar(&g.ReadOnly, "read-only", g.ReadOnly, usage(i18n.FlagReadOnly))
	fs.StringVar(&g.Config, orchestrator.FlagConfig, g.Config, usage(i18n.FlagConfig))
	fs.StringVar(&g.StateDir, orchestrator.FlagStateDir, g.StateDir, usage(i18n.FlagStateDir))
	fs.StringVar(&g.AppSupportDir, orchestrator.FlagAppSupportDir, g.AppSupportDir, usage(i18n.FlagAppSupportDir))
	fs.StringVar(&g.Lang, flagLang, g.Lang, usage(i18n.FlagLang))
}

// env is what a command's run function receives.
type env struct {
	g      *globalFlags
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	sys    *system
	in     *bufio.Reader // lazily wraps stdin, shared by every prompt
	ls     langState     // the message language (lang.go)
}

// command is one node of the command tree. The tree is built on the
// standard library's flag package on purpose: popular third-party flag
// libraries import the net package, and the zero-network policy test
// requires that no package in the dependency graph imports it.
type command struct {
	name    string
	args    string   // usage of positional arguments
	short   i18n.Msg // catalog id of the one-line description
	hidden  bool
	minArgs int
	maxArgs int // -1: unlimited
	// lenient commands never fail: parse and run errors exit 0 (the
	// plugin's hook entry point).
	lenient bool
	// failClosed (lenient commands only): a parse error, or a run that
	// returns errFailClosed, exits non-zero without output, so the
	// plugin's shim applies its own armed check (the PreToolUse guard).
	failClosed bool
	// english commands print English only and resolve no language (the
	// hook, completion and the hidden aliases command).
	english bool
	flags   func(fs *flag.FlagSet)
	run     func(ctx context.Context, e *env, fs *flag.FlagSet, args []string) error
	sub     []*command
}

// errUsage marks command-line errors (exit 2).
var errUsage = errors.New("usage")

// usageErr is a command-line error whose text is a catalog message.
type usageErr struct{ t i18n.Text }

func (u *usageErr) Error() string       { return errUsage.Error() + ": " + u.t.English() }
func (u *usageErr) Unwrap() error       { return errUsage }
func (u *usageErr) I18nText() i18n.Text { return u.t }

// usageError returns a command-line error (exit 2) with the catalog text
// id; Go flag parse errors use i18n.UsageFlag with err.Error().
func usageError(id i18n.Msg, args ...any) error { return &usageErr{t: i18n.Tx(id, args...)} }

// quietError carries an exit code whose message was already printed (a
// JSON document with its own code, for example).
type quietError struct{ err error }

func (q *quietError) Error() string { return q.err.Error() }
func (q *quietError) Unwrap() error { return q.err }

// Execute runs the command line and returns the exit code.
func Execute(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return execute(ctx, args, stdin, stdout, stderr, realSystem())
}

// execute runs the command line against sys, which tests replace. Both
// languages start as English and are restored on return, so a test that
// calls execute never leaks a language.
func execute(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, sys *system) int {
	defer i18n.SetLang(i18n.SetLang(i18n.English))
	defer i18n.SetDisplayLang(i18n.SetDisplayLang(i18n.English))
	e := &env{g: &globalFlags{}, stdin: stdin, stdout: stdout, stderr: stderr, sys: sys}
	if englishCommands[commandWord(args)] {
		e.ls.english = true
	} else {
		_ = e.applyLang(nil, nil)
	}
	return e.finish(dispatch(ctx, e, root(), args, "paritydesk"))
}

// finish prints err with its catalog entry and returns the exit code.
func (e *env) finish(err error) int {
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return int(pderr.OK)
	}
	var q *quietError
	if errors.As(err, &q) {
		return int(pderr.CodeOf(q.err))
	}
	if errors.Is(err, errUsage) {
		pe := &pderr.Error{Code: pderr.Usage, Op: "usage", Msg: strings.TrimPrefix(err.Error(), errUsage.Error()+": ")}
		var tx i18n.Texter
		if errors.As(err, &tx) {
			pe.Text = tx.I18nText()
		}
		err = pe
	}
	e.printer().Error(err)
	return int(pderr.CodeOf(err))
}

// printer returns the printer for the flags parsed so far.
func (e *env) printer() *render.Printer {
	return &render.Printer{Out: e.stdout, Err: e.stderr, Mode: e.mode(), Redact: e.redactor().String}
}

// redactor hides home paths and ids in output meant to be shared (JSON
// error documents and doctor). Without a home directory only ids and
// other users' home paths are hidden.
func (e *env) redactor() redact.Redactor {
	home, err := e.sys.home()
	if err != nil {
		home = ""
	}
	return redact.Redactor{Home: home}
}

// readOnly reports read-only mode (--read-only or PARITYDESK_READONLY=1).
func (e *env) readOnly() bool {
	return e.g.ReadOnly || e.sys.getenv("PARITYDESK_READONLY") == "1"
}

// paths resolves paritydesk's own locations from flags and environment.
func (e *env) paths() (config.Paths, string, error) {
	home, err := e.sys.home()
	if err != nil {
		return config.Paths{}, "", fmt.Errorf("home directory: %w", err)
	}
	return config.ResolvePaths(home, e.sys.getenv, config.Overrides{
		Config: e.g.Config, StateDir: e.g.StateDir, AppSupportDir: e.g.AppSupportDir,
	}), home, nil
}

func dispatch(ctx context.Context, e *env, c *command, args []string, path string) error {
	fs := flag.NewFlagSet(path, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	e.g.register(fs)
	if c.flags != nil {
		c.flags(fs)
	}
	if c.english {
		e.ls.english = true
	}
	if len(c.sub) > 0 {
		// Only global flags may precede a subcommand name.
		if err := fs.Parse(args); err != nil {
			return e.parseError(c, fs, path, args, err)
		}
		if err := e.applyLang(fs, c); err != nil {
			return err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			printUsage(e.stderr, c, fs, path)
			return usageError(i18n.UsageMissingCommand, strings.TrimPrefix(path, "paritydesk"))
		}
		for _, s := range c.sub {
			if s.name == rest[0] {
				return dispatch(ctx, e, s, rest[1:], path+" "+s.name)
			}
		}
		return usageError(i18n.UsageUnknownCommand, rest[0])
	}
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		if c.lenient && !errors.Is(err, flag.ErrHelp) {
			return c.lenientFailure(path)
		}
		return e.parseError(c, fs, path, args, err)
	}
	if err := e.applyLang(fs, c); err != nil {
		return err
	}
	if len(pos) < c.minArgs || (c.maxArgs >= 0 && len(pos) > c.maxArgs) {
		if c.lenient {
			return c.lenientFailure(path)
		}
		return usageError(i18n.UsageSyntax, path, c.args)
	}
	if c.run == nil {
		return pderr.Bug(path, "command has no run function")
	}
	err = c.run(ctx, e, fs, pos)
	if c.lenient {
		if errors.Is(err, errFailClosed) {
			return c.lenientFailure(path)
		}
		return nil
	}
	return err
}

// errFailClosed is returned by a failClosed command's run when it could
// not decide (its dependencies or a panic).
var errFailClosed = errors.New("could not decide")

// lenientFailure is a lenient command's answer to a failure: exit 0, or
// for a failClosed one a silent non-zero exit.
func (c *command) lenientFailure(path string) error {
	if !c.failClosed {
		return nil
	}
	return &quietError{pderr.Wrap(pderr.Internal, path, errFailClosed)}
}

// parseError prints the usage for --help (exit 0) and turns other parse
// errors into usage errors.
func (e *env) parseError(c *command, fs *flag.FlagSet, path string, args []string, err error) error {
	if errors.Is(err, flag.ErrHelp) {
		e.applyLangHelp(fs, c, args)
		printUsage(e.stdout, c, fs, path)
		return err
	}
	return usageError(i18n.UsageFlag, err.Error())
}

// parseInterleaved accepts flags before and after positional arguments.
// "--" ends flag parsing.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		if n := len(args) - len(rest); n > 0 && args[n-1] == "--" {
			return append(pos, rest...), nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

func printUsage(w io.Writer, c *command, fs *flag.FlagSet, path string) {
	args := c.args
	if len(c.sub) == 0 {
		args = strings.TrimSpace(args + " [flags]")
	}
	fmt.Fprintln(w, i18n.T(i18n.HelpUsage, path, args))
	if c.short != "" {
		fmt.Fprintf(w, "\n%s\n", i18n.T(c.short))
	}
	if len(c.sub) > 0 {
		fmt.Fprintf(w, "\n%s\n", i18n.T(i18n.HelpCommands))
		subs := append([]*command(nil), c.sub...)
		sort.Slice(subs, func(i, j int) bool { return subs[i].name < subs[j].name })
		// The name column fits the longest visible name (names are
		// ASCII), and is at least 12 wide.
		width := 12
		for _, s := range subs {
			if !s.hidden && len(s.name) > width {
				width = len(s.name)
			}
		}
		for _, s := range subs {
			if !s.hidden {
				fmt.Fprintf(w, "  %-*s %s\n", width, s.name, i18n.T(s.short))
			}
		}
	}
	printFlags(w, fs, i18n.T(i18n.HelpFlags), func(name string) bool { return !globalNames[name] })
	printFlags(w, fs, i18n.T(i18n.HelpGlobalFlags), func(name string) bool { return globalNames[name] })
}

// printFlags lists the visible flags that keep selects.
func printFlags(w io.Writer, fs *flag.FlagSet, title string, keep func(string) bool) {
	var lines []string
	fs.VisitAll(func(f *flag.Flag) {
		if !keep(f.Name) || isHidden(f) {
			return
		}
		typ, _ := flag.UnquoteUsage(f)
		text := flagUsageText(i18n.Current(), f.Usage)
		name := "--" + f.Name
		if typ != "" {
			name += " " + typ
		}
		if d := f.DefValue; d != "" && d != "false" && d != "0" {
			text += " " + i18n.T(i18n.HelpDefault, d)
		}
		lines = append(lines, fmt.Sprintf("  %-28s %s", name, text))
	})
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s:\n%s\n", title, strings.Join(lines, "\n"))
}

func categoryFlags(fs *flag.FlagSet) {
	fs.String(orchestrator.FlagOnly, "", usage(i18n.FlagOnly))
	fs.String(orchestrator.FlagSkip, "", usage(i18n.FlagSkip))
}

// planFlags registers the plan flags.
func planFlags(fs *flag.FlagSet) {
	categoryFlags(fs)
	fs.String(flagInvokingSession, "", usage(i18n.FlagInvokingSession))
}

// flagInvokingSession names the Claude Code session that runs plan (set
// by the plugin's skill).
const flagInvokingSession = "invoking-session"

func leaf(name, args string, short i18n.Msg, min, max int, flags func(*flag.FlagSet), run func(context.Context, *env, *flag.FlagSet, []string) error) *command {
	return &command{name: name, args: args, short: short, minArgs: min, maxArgs: max, flags: flags, run: run}
}

func group(name, args string, short i18n.Msg, hidden bool, sub ...*command) *command {
	return &command{name: name, args: args, short: short, hidden: hidden, sub: sub}
}

// englishOnly marks c and its subcommands as English-only.
func englishOnly(c *command) *command {
	c.english = true
	for _, s := range c.sub {
		englishOnly(s)
	}
	return c
}

func yesFlag(fs *flag.FlagSet) { fs.Bool(orchestrator.FlagYes, false, usage(i18n.FlagYes)) }

// switchFlags registers the switch flags under the names the orchestrator
// uses to build the detached job's argv (orchestrator.JobArgs).
func switchFlags(fs *flag.FlagSet) {
	categoryFlags(fs)
	fs.Bool(orchestrator.FlagYes, false, usage(i18n.FlagSwitchYes))
	fs.Bool(orchestrator.FlagNoLaunch, false, usage(i18n.FlagNoLaunch))
	fs.Bool(orchestrator.FlagNoReopen, false, usage(i18n.FlagNoReopen))
	fs.Bool(orchestrator.FlagDetach, false, usage(i18n.FlagDetach))
	fs.Bool(flagJSONProgress, false, usage(i18n.FlagJSONProgress))
	fs.Int(orchestrator.FlagAllowDeletions, 0, usage(i18n.FlagAllowDeletions))
	fs.Bool(orchestrator.FlagAcceptWorktreeRisk, false, usage(i18n.FlagAcceptWorktreeRisk))
	fs.String(orchestrator.FlagReopenSession, "", usage(i18n.FlagReopenSession))
	fs.String(orchestrator.FlagConfirmedPlan, "", usage(i18n.FlagConfirmedPlan))
	fs.Bool(orchestrator.FlagConfirmFirstSync, false, usage(i18n.FlagConfirmFirstSync))
	fs.Int64(orchestrator.FlagLogOffset, -1, usage(i18n.FlagLogOffset))
	fs.Uint64(orchestrator.FlagLogInode, 0, usage(i18n.FlagLogInode))
	fs.Bool(orchestrator.FlagFromRunner, false, usage(i18n.FlagFromRunner))
}

// managedFlags registers --managed and --unmanaged. Without either, a
// terminal asks and a run without one refuses (see managedChoice).
func managedFlags(fs *flag.FlagSet) {
	fs.Bool("managed", false, usage(i18n.FlagManaged))
	fs.Bool("unmanaged", false, usage(i18n.FlagUnmanaged))
}

// Hidden flags of profile adopt --from-legacy (spec-legacy-adopt 2.1).
const (
	flagFromLegacy    = "from-legacy"
	flagLegacyApply   = "apply"
	flagQuarantine    = "quarantine"
	flagLegacyState   = "legacy-state"
	flagLegacyAlias   = "legacy-alias"
	flagLegacyExclude = "legacy-exclude"
	flagLegacyManaged = "legacy-managed"
	flagLegacyPlan    = "legacy-plan"
	flagLegacyNoSeed  = "legacy-no-seed"
)

// adoptFlags registers --managed, --unmanaged and the hidden flags of the
// legacy mode.
func adoptFlags(fs *flag.FlagSet) {
	managedFlags(fs)
	fs.Bool(flagFromLegacy, false, usage(i18n.FlagFromLegacy))
	fs.Bool(flagLegacyApply, false, usage(i18n.FlagProfileAdoptApply))
	fs.Bool(flagQuarantine, false, usage(i18n.FlagQuarantine))
	fs.String(flagLegacyState, "", usage(i18n.FlagLegacyState))
	fs.Var(&multiFlag{}, flagLegacyAlias, usage(i18n.FlagLegacyAlias))
	fs.Var(&multiFlag{}, flagLegacyExclude, usage(i18n.FlagLegacyExclude))
	fs.String(flagLegacyManaged, "", usage(i18n.FlagLegacyManaged))
	fs.String(flagLegacyPlan, "", usage(i18n.FlagLegacyPlan))
	fs.Bool(flagLegacyNoSeed, false, usage(i18n.FlagLegacyNoSeed))
}

// flagJSONProgress selects NDJSON progress events on stderr.
const flagJSONProgress = "json-progress"

// root returns the command tree. Every leaf maps to one orchestrator, gen,
// config, log or hook call.
func root() *command {
	return group("paritydesk", "<command> [flags]",
		i18n.HelpRoot, false,
		leaf("init", "", i18n.HelpInit, 0, 0, func(fs *flag.FlagSet) {
			fs.String("alias", "", usage(i18n.FlagAlias))
			fs.String("label", "", usage(i18n.FlagLabel))
			managedFlags(fs)
		}, runInit),
		group("profile", "<list|adopt|add|rename|remove>", i18n.HelpProfile, false,
			leaf("list", "", i18n.HelpProfileList, 0, 0, nil, runProfileList),
			leaf("adopt", "<alias> <folder>", i18n.HelpProfileAdopt, 0, 2, adoptFlags, runProfileAdopt),
			leaf("add", "<alias>", i18n.HelpProfileAdd, 1, 1, managedFlags, runProfileAdd),
			leaf("rename", "<alias> <new-alias>", i18n.HelpProfileRename, 2, 2, nil, runProfileRename),
			leaf("remove", "<alias>", i18n.HelpProfileRemove, 1, 1, nil, runProfileRemove),
		),
		leaf("status", "", i18n.HelpStatus, 0, 0, func(fs *flag.FlagSet) {
			fs.Bool("short", false, usage(i18n.FlagShort))
		}, runStatus),
		leaf("plan", "[<alias>|-]", i18n.HelpPlan, 0, 1, planFlags, runPlan),
		leaf("switch", "[<alias>|-]", i18n.HelpSwitch, 0, 1, switchFlags, runSwitch),
		leaf("sync", "", i18n.HelpSync, 0, 0, func(fs *flag.FlagSet) {
			yesFlag(fs)
			fs.Int(orchestrator.FlagAllowDeletions, 0, usage(i18n.FlagAllowDeletions))
			fs.Bool(orchestrator.FlagNoLaunch, false, usage(i18n.FlagNoLaunch))
			fs.Bool(flagJSONProgress, false, usage(i18n.FlagJSONProgress))
		}, runSync),
		leaf("verify", "", i18n.HelpVerify, 0, 0, func(fs *flag.FlagSet) {
			fs.String(orchestrator.FlagAfterRun, "", usage(i18n.FlagAfterRun))
		}, runVerify),
		leaf("recover", "", i18n.HelpRecover, 0, 0, func(fs *flag.FlagSet) {
			fs.Bool("rollback", false, usage(i18n.FlagRollback))
			yesFlag(fs)
		}, runRecover),
		leaf("rollback", "[<n>]", i18n.HelpRollback, 0, 1, func(fs *flag.FlagSet) {
			fs.Bool("propagate", false, usage(i18n.FlagPropagate))
			yesFlag(fs)
		}, runRollback),
		leaf("rebase", "", i18n.HelpRebase, 0, 0, nil, runRebase),
		group("generations", "<list|show>", i18n.HelpGenerations, false,
			leaf("list", "", i18n.HelpGenerationsList, 0, 0, nil, runGenerationsList),
			leaf("show", "<n>", i18n.HelpGenerationsShow, 1, 1, nil, runGenerationsShow),
		),
		group("backups", "<prune|purge>", i18n.HelpBackups, false,
			leaf("prune", "", i18n.HelpBackupsPrune, 0, 0, backupsPruneFlags, runBackupsPrune),
			leaf("purge", "", i18n.HelpBackupsPurge, 0, 0, yesFlag, runBackupsPurge),
		),
		leaf("doctor", "", i18n.HelpDoctor, 0, 0, func(fs *flag.FlagSet) {
			fs.Bool("grant-automation", false, usage(i18n.FlagGrantAutomation))
			fs.String(flagBundle, "", usage(i18n.FlagBundle))
			fs.Int(flagBundleLogs, defaultBundleLogs, usage(i18n.FlagBundleLogs))
		}, runDoctor),
		leaf("log", "", i18n.HelpLog, 0, 0, func(fs *flag.FlagSet) {
			fs.String("run", "", usage(i18n.FlagRun))
		}, runLog),
		leaf("history", "", i18n.HelpHistory, 0, 0, nil, runHistory),
		group("config", "<path|show|validate>", i18n.HelpConfig, false,
			leaf("path", "", i18n.HelpConfigPath, 0, 0, nil, runConfigPath),
			leaf("show", "", i18n.HelpConfigShow, 0, 0, nil, runConfigShow),
			leaf("validate", "", i18n.HelpConfigValidate, 0, 0, nil, runConfigValidate),
		),
		onlineGroup(),

		artifactsGroup(),

		leaf("uninstall", "", i18n.HelpUninstall, 0, 0, func(fs *flag.FlagSet) {
			fs.Bool("keep-backups", true, usage(i18n.FlagKeepBackups))
			fs.Bool("keep-artifacts", true, usage(i18n.FlagKeepArtifacts))
			fs.Bool("keep-state", false, usage(i18n.FlagKeepState))
			yesFlag(fs)
		}, runUninstall),
		englishOnly(group("hook", "<event>", i18n.HelpHook, true,
			&command{name: "session-start", short: i18n.HelpHookSessionStart, maxArgs: -1, lenient: true,
				flags: func(fs *flag.FlagSet) {
					fs.String("plugin-version", "", usage(i18n.FlagHookSessionStartPluginVersion))
				}, run: runHookSessionStart},
			&command{name: "pre-tool-use", short: i18n.HelpHookPreToolUse, maxArgs: -1, lenient: true, failClosed: true,
				flags: func(fs *flag.FlagSet) {
					fs.String("plugin-version", "", usage(i18n.FlagPluginVersion))
				}, run: runHookPreToolUse},
			&command{name: "post-tool-use", short: i18n.HelpHookPostToolUse, maxArgs: -1, lenient: true,
				flags: func(fs *flag.FlagSet) {
					fs.String("plugin-version", "", usage(i18n.FlagPluginVersion))
				}, run: runHookPostToolUse},
		)),
		englishOnly(leaf("completion", "<bash|zsh|fish>", i18n.HelpCompletion, 1, 1, nil, runCompletion)),
		&command{name: aliasesCommand, short: i18n.HelpAliases, hidden: true, lenient: true, english: true,
			run: runAliases},
		&command{name: "help", args: "[<command>...]", short: i18n.HelpHelp, maxArgs: -1, run: runHelp},
		&command{name: "version", short: i18n.HelpVersion, run: runVersion},
	)
}

// runHelp prints the usage of the named command.
func runHelp(_ context.Context, e *env, _ *flag.FlagSet, args []string) error {
	c, path := root(), "paritydesk"
	for _, a := range args {
		var next *command
		for _, s := range c.sub {
			if s.name == a {
				next = s
			}
		}
		if next == nil {
			return usageError(i18n.UsageUnknownCommand, a)
		}
		c, path = next, path+" "+a
	}
	fs := flag.NewFlagSet(path, flag.ContinueOnError)
	(&globalFlags{}).register(fs)
	if c.flags != nil {
		c.flags(fs)
	}
	if e.g.JSON {
		var b strings.Builder
		printUsage(&b, c, fs, path)
		return e.printer().JSON(struct {
			Command string `json:"command"`
			Usage   string `json:"usage"`
		}{path, b.String()})
	}
	printUsage(e.stdout, c, fs, path)
	return nil
}

func runVersion(_ context.Context, e *env, _ *flag.FlagSet, _ []string) error {
	v := versionString()
	if e.g.JSON {
		return e.printer().JSON(struct {
			Version string `json:"version"`
			Commit  string `json:"commit,omitempty"`
			Date    string `json:"date,omitempty"`
		}{v, Commit, Date})
	}
	_, err := fmt.Fprintln(e.stdout, "paritydesk", v)
	return err
}
