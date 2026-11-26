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
	"github.com/kazupc/paritydesk/internal/orchestrator"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/redact"
	"github.com/kazupc/paritydesk/internal/render"
)

// Version, Commit and Date are set at build time with -ldflags, for
// example "-X github.com/kazupc/paritydesk/internal/cli.Version=0.1.0"
// (see .goreleaser.yaml). Commit and Date stay empty in a plain go build.
var (
	Version = "0.1.0-dev"
	Commit  = ""
	Date    = ""
)

// globalFlags are accepted by every command, before or after its name.
type globalFlags struct {
	JSON, Plain, NoColor, ASCII, ReadOnly bool
	Config, StateDir, AppSupportDir       string
}

// globalNames lists the global flags, for the usage text.
var globalNames = map[string]bool{
	"json": true, "plain": true, "no-color": true, "ascii": true, "read-only": true,
	orchestrator.FlagConfig: true, orchestrator.FlagStateDir: true, orchestrator.FlagAppSupportDir: true,
}

// hiddenFlags are accepted but not listed in the usage text: internal
// flags set by the plugin's shim and by the detached runner.
var hiddenFlags = map[string]bool{
	"plugin-version":            true,
	orchestrator.FlagFromRunner: true,
	orchestrator.FlagLogOffset:  true,
	orchestrator.FlagAfterRun:   true,
}

func (g *globalFlags) register(fs *flag.FlagSet) {
	fs.BoolVar(&g.JSON, "json", g.JSON, "print one JSON document")
	fs.BoolVar(&g.Plain, "plain", g.Plain, "plain lines, no tables or spinners")
	fs.BoolVar(&g.NoColor, "no-color", g.NoColor, "no colour (also NO_COLOR, TERM=dumb)")
	fs.BoolVar(&g.ASCII, "ascii", g.ASCII, "ASCII only")
	fs.BoolVar(&g.ReadOnly, "read-only", g.ReadOnly, "plan only; never write (also PARITYDESK_READONLY=1)")
	fs.StringVar(&g.Config, orchestrator.FlagConfig, g.Config, "config file (also PARITYDESK_CONFIG)")
	fs.StringVar(&g.StateDir, orchestrator.FlagStateDir, g.StateDir, "state directory (also PARITYDESK_STATE_DIR)")
	fs.StringVar(&g.AppSupportDir, orchestrator.FlagAppSupportDir, g.AppSupportDir, "paritydesk support directory")
}

// env is what a command's run function receives.
type env struct {
	g      *globalFlags
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	sys    *system
	in     *bufio.Reader // lazily wraps stdin, shared by every prompt
}

// command is one node of the command tree. The tree is built on the
// standard library's flag package on purpose: popular third-party flag
// libraries import the net package, and the zero-network policy test
// requires that no package in the dependency graph imports it.
type command struct {
	name    string
	args    string // usage of positional arguments
	short   string
	hidden  bool
	minArgs int
	maxArgs int // -1: unlimited
	// lenient commands never fail: parse and run errors exit 0 (the
	// plugin's hook entry point).
	lenient bool
	flags   func(fs *flag.FlagSet)
	run     func(ctx context.Context, e *env, fs *flag.FlagSet, args []string) error
	sub     []*command
}

// errUsage marks command-line errors (exit 2).
var errUsage = errors.New("usage")

// usageErrorf returns a command-line error.
func usageErrorf(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{errUsage}, args...)...)
}

// quietError carries an exit code whose message was already printed (a
// JSON document with its own code, for example).
type quietError struct{ err error }

func (q *quietError) Error() string { return q.err.Error() }
func (q *quietError) Unwrap() error { return q.err }

// Execute runs the command line and returns the exit code.
func Execute(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return execute(ctx, args, stdin, stdout, stderr, realSystem())
}

// execute runs the command line against sys, which tests replace.
func execute(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, sys *system) int {
	e := &env{g: &globalFlags{}, stdin: stdin, stdout: stdout, stderr: stderr, sys: sys}
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
		err = &pderr.Error{Code: pderr.Usage, Op: "usage", Msg: strings.TrimPrefix(err.Error(), errUsage.Error()+": ")}
	}
	e.printer().Error(err)
	return int(pderr.CodeOf(err))
}

// printer returns the printer for the flags parsed so far.
func (e *env) printer() *render.Printer {
	m := render.DetectMode(render.Mode{
		JSON: e.g.JSON, Plain: e.g.Plain, NoColor: e.g.NoColor, ASCII: e.g.ASCII,
	}, e.sys.getenv, e.sys.isTerminal(e.stdout))
	return &render.Printer{Out: e.stdout, Err: e.stderr, Mode: m, Redact: e.redactor().String}
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
	if len(c.sub) > 0 {
		// Only global flags may precede a subcommand name.
		if err := fs.Parse(args); err != nil {
			return e.parseError(c, fs, path, err)
		}
		rest := fs.Args()
		if len(rest) == 0 {
			printUsage(e.stderr, c, fs, path)
			return usageErrorf("missing command; run paritydesk help %s", strings.TrimPrefix(path, "paritydesk"))
		}
		for _, s := range c.sub {
			if s.name == rest[0] {
				return dispatch(ctx, e, s, rest[1:], path+" "+s.name)
			}
		}
		return usageErrorf("unknown command %q", rest[0])
	}
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		if c.lenient && !errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return e.parseError(c, fs, path, err)
	}
	if len(pos) < c.minArgs || (c.maxArgs >= 0 && len(pos) > c.maxArgs) {
		if c.lenient {
			return nil
		}
		return usageErrorf("%s %s", path, c.args)
	}
	if c.run == nil {
		return pderr.New(pderr.Internal, path, "command has no run function")
	}
	err = c.run(ctx, e, fs, pos)
	if c.lenient {
		return nil
	}
	return err
}

// parseError prints the usage for --help (exit 0) and turns other parse
// errors into usage errors.
func (e *env) parseError(c *command, fs *flag.FlagSet, path string, err error) error {
	if errors.Is(err, flag.ErrHelp) {
		printUsage(e.stdout, c, fs, path)
		return err
	}
	return usageErrorf("%v", err)
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
	fmt.Fprintf(w, "usage: %s %s\n", path, args)
	if c.short != "" {
		fmt.Fprintf(w, "\n%s\n", c.short)
	}
	if len(c.sub) > 0 {
		fmt.Fprintln(w, "\ncommands:")
		subs := append([]*command(nil), c.sub...)
		sort.Slice(subs, func(i, j int) bool { return subs[i].name < subs[j].name })
		for _, s := range subs {
			if !s.hidden {
				fmt.Fprintf(w, "  %-12s %s\n", s.name, s.short)
			}
		}
	}
	printFlags(w, fs, "flags", func(name string) bool { return !globalNames[name] })
	printFlags(w, fs, "global flags", func(name string) bool { return globalNames[name] })
}

// printFlags lists the visible flags that keep selects.
func printFlags(w io.Writer, fs *flag.FlagSet, title string, keep func(string) bool) {
	var lines []string
	fs.VisitAll(func(f *flag.Flag) {
		if !keep(f.Name) || hiddenFlags[f.Name] {
			return
		}
		typ, usage := flag.UnquoteUsage(f)
		name := "--" + f.Name
		if typ != "" {
			name += " " + typ
		}
		if d := f.DefValue; d != "" && d != "false" && d != "0" {
			usage += fmt.Sprintf(" (default %s)", d)
		}
		lines = append(lines, fmt.Sprintf("  %-28s %s", name, usage))
	})
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s:\n%s\n", title, strings.Join(lines, "\n"))
}

func categoryFlags(fs *flag.FlagSet) {
	fs.String(orchestrator.FlagOnly, "", "categories to carry, comma-separated: "+strings.Join(categoryNames(), ","))
	fs.String(orchestrator.FlagSkip, "", "categories to leave alone, comma-separated")
}

func leaf(name, args, short string, min, max int, flags func(*flag.FlagSet), run func(context.Context, *env, *flag.FlagSet, []string) error) *command {
	return &command{name: name, args: args, short: short, minArgs: min, maxArgs: max, flags: flags, run: run}
}

func group(name, args, short string, hidden bool, sub ...*command) *command {
	return &command{name: name, args: args, short: short, hidden: hidden, sub: sub}
}

func yesFlag(fs *flag.FlagSet) { fs.Bool(orchestrator.FlagYes, false, "do not ask") }

// switchFlags registers the switch flags under the names the orchestrator
// uses to build the detached job's argv (orchestrator.JobArgs).
func switchFlags(fs *flag.FlagSet) {
	categoryFlags(fs)
	fs.Bool(orchestrator.FlagYes, false, "do not ask (destructive differences after the preview still ask)")
	fs.Bool(orchestrator.FlagNoLaunch, false, "do not relaunch Claude")
	fs.Bool(orchestrator.FlagNoReopen, false, "do not reopen the invoking session")
	fs.Bool(orchestrator.FlagDetach, false, "run as a detached launchd job")
	fs.Bool(flagJSONProgress, false, "NDJSON progress on stderr")
	fs.Int(orchestrator.FlagAllowDeletions, 0, "allow up to N deletions per category past the breaker")
	fs.Bool(orchestrator.FlagAcceptWorktreeRisk, false, "switch even if a worktree with uncommitted changes loses its registration")
	fs.String(orchestrator.FlagReopenSession, "", "Claude Code session id of the invoking session (set by the plugin; empty: no reopen)")
	fs.String(orchestrator.FlagConfirmedPlan, "", "hash of the plan you confirmed (printed by paritydesk plan --json)")
	fs.Bool(orchestrator.FlagConfirmFirstSync, false, "confirm the first-sync union that the confirmed plan showed")
	fs.Int64(orchestrator.FlagLogOffset, -1, "internal: main.log offset recorded before the hand-off")
	fs.Bool(orchestrator.FlagFromRunner, false, "internal: this process is the detached job")
}

// managedFlags registers --managed and --unmanaged. Without either, a
// terminal asks and a run without one refuses (see managedChoice).
func managedFlags(fs *flag.FlagSet) {
	fs.Bool("managed", false, "the account is managed by an employer or client")
	fs.Bool("unmanaged", false, "the account is not managed by an employer or client")
}

// flagJSONProgress selects NDJSON progress events on stderr.
const flagJSONProgress = "json-progress"

// root returns the command tree. Every leaf maps to one orchestrator, gen,
// config, log or hook call.
func root() *command {
	return group("paritydesk", "<command> [flags]",
		"Switch the Claude desktop app between accounts and keep everything else in step.", false,
		leaf("init", "", "Set up paritydesk for the account in the app now", 0, 0, func(fs *flag.FlagSet) {
			fs.String("alias", "", "alias for the account in Claude now (asked when missing)")
			fs.String("label", "", "display label for the profile")
			managedFlags(fs)
		}, runInit),
		group("profile", "<list|adopt|add|rename|remove>", "Manage profiles", false,
			leaf("list", "", "List profiles", 0, 0, nil, runProfileList),
			leaf("adopt", "<alias> <folder>", "Register a parked folder after checking its identity", 2, 2, managedFlags, runProfileAdopt),
			leaf("add", "<alias>", "Add an account without signing out of any other", 1, 1, managedFlags, runProfileAdd),
			leaf("rename", "<alias> <new-alias>", "Rename a profile", 2, 2, nil, runProfileRename),
			leaf("remove", "<alias>", "Unregister a profile; its folder is kept", 1, 1, nil, runProfileRemove),
		),
		leaf("status", "", "Show the active profile and anything pending", 0, 0, func(fs *flag.FlagSet) {
			fs.Bool("short", false, "one line; never opens Local Storage")
		}, runStatus),
		leaf("plan", "[<alias>|-]", "Preview what a switch would change (approximate)", 0, 1, categoryFlags, runPlan),
		leaf("switch", "[<alias>|-]", "Switch to another profile", 0, 1, switchFlags, runSwitch),
		leaf("sync", "", "Re-apply the shared state to the active profile", 0, 0, func(fs *flag.FlagSet) {
			yesFlag(fs)
			fs.Int(orchestrator.FlagAllowDeletions, 0, "allow up to N deletions per category past the breaker")
			fs.Bool(orchestrator.FlagNoLaunch, false, "do not relaunch Claude")
			fs.Bool(flagJSONProgress, false, "NDJSON progress on stderr")
		}, runSync),
		leaf("verify", "", "Check that the app kept what the last run wrote", 0, 0, func(fs *flag.FlagSet) {
			fs.String(orchestrator.FlagAfterRun, "", "run id a background check verifies (set by paritydesk)")
		}, runVerify),
		leaf("recover", "", "Finish or undo an interrupted run", 0, 0, func(fs *flag.FlagSet) {
			fs.Bool("rollback", false, "undo the interrupted run instead of completing it")
			yesFlag(fs)
		}, runRecover),
		leaf("rollback", "[<n>]", "Restore a generation without overwriting newer work", 0, 1, func(fs *flag.FlagSet) {
			fs.Bool("propagate", false, "carry the restored values to other profiles on the next switch")
			yesFlag(fs)
		}, runRollback),
		leaf("rebase", "", "Forget every sync base; the next run is a first sync", 0, 0, nil, runRebase),
		group("generations", "<list|show>", "List or show backup generations", false,
			leaf("list", "", "List generations", 0, 0, nil, runGenerationsList),
			leaf("show", "<n>", "Show one generation", 1, 1, nil, runGenerationsShow),
		),
		group("backups", "<prune|purge>", "Prune or purge backup generations", false,
			leaf("prune", "", "Apply the retention policy", 0, 0, backupsPruneFlags, runBackupsPrune),
			leaf("purge", "", "Remove every generation", 0, 0, yesFlag, runBackupsPurge),
		),
		leaf("doctor", "", "Check the installation, folders and app compatibility", 0, 0, func(fs *flag.FlagSet) {
			fs.Bool("grant-automation", false, "ask macOS for Automation permission now")
			fs.String(flagBundle, "", "write a redacted support archive (tar.gz) to this new file")
			fs.Int(flagBundleLogs, defaultBundleLogs, "newest run logs to put in the archive")
		}, runDoctor),
		leaf("log", "", "Show the log of a run", 0, 0, func(fs *flag.FlagSet) {
			fs.String("run", "", "run id (default: the last run)")
		}, runLog),
		leaf("history", "", "List past runs", 0, 0, nil, runHistory),
		group("config", "<path|show|validate>", "Show, locate or validate the config file", false,
			leaf("path", "", "Print the config file path", 0, 0, nil, runConfigPath),
			leaf("show", "", "Print the effective config", 0, 0, nil, runConfigShow),
			leaf("validate", "", "Validate the config file", 0, 0, nil, runConfigValidate),
		),
		// v0.2 anchor: commands online

		// v0.2 anchor: commands artifacts

		leaf("uninstall", "", "Remove paritydesk's state; every data folder is kept", 0, 0, func(fs *flag.FlagSet) {
			fs.Bool("keep-backups", true, "keep backup generations")
			fs.Bool("keep-state", false, "keep the state directory as well")
			yesFlag(fs)
		}, runUninstall),
		group("hook", "<event>", "Internal: entry point of the plugin's hooks", true,
			&command{name: "session-start", short: "SessionStart hook", maxArgs: -1, lenient: true,
				flags: func(fs *flag.FlagSet) {
					fs.String("plugin-version", "", "version of the calling plugin (silent on version skew)")
				}, run: runHookSessionStart},
		),
		leaf("completion", "<bash|zsh|fish>", "Print a shell completion script", 1, 1, nil, runCompletion),
		&command{name: aliasesCommand, short: "Internal: list profile aliases for completion", hidden: true, lenient: true,
			run: runAliases},
		&command{name: "help", args: "[<command>...]", short: "Show help for a command", maxArgs: -1, run: runHelp},
		&command{name: "version", short: "Print the version", run: runVersion},
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
			return usageErrorf("unknown command %q", a)
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
