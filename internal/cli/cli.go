package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"runtime/debug"
	"sort"
	"strings"

	"github.com/kazupc/paritydesk/internal/pderr"
)

// Version is set at build time with -ldflags "-X .../cli.Version=v0.1.0".
var Version = "0.1.0-dev"

// globalFlags are accepted by every command, before or after its name.
type globalFlags struct {
	JSON, Plain, NoColor, ASCII, ReadOnly bool
	Config, StateDir, AppSupportDir       string
}

func (g *globalFlags) register(fs *flag.FlagSet) {
	fs.BoolVar(&g.JSON, "json", g.JSON, "print one JSON document")
	fs.BoolVar(&g.Plain, "plain", g.Plain, "plain lines, no tables or spinners")
	fs.BoolVar(&g.NoColor, "no-color", g.NoColor, "no colour (also NO_COLOR, TERM=dumb)")
	fs.BoolVar(&g.ASCII, "ascii", g.ASCII, "ASCII only")
	fs.BoolVar(&g.ReadOnly, "read-only", g.ReadOnly, "plan only; never write (also PARITYDESK_READONLY=1)")
	fs.StringVar(&g.Config, "config", g.Config, "config file (also PARITYDESK_CONFIG)")
	fs.StringVar(&g.StateDir, "state-dir", g.StateDir, "state directory (also PARITYDESK_STATE_DIR)")
	fs.StringVar(&g.AppSupportDir, "app-support-dir", g.AppSupportDir, "paritydesk support directory")
}

// env is what a command's run function receives.
type env struct {
	g      *globalFlags
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
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
	flags   func(fs *flag.FlagSet)
	run     func(ctx context.Context, e *env, fs *flag.FlagSet, args []string) error
	sub     []*command
}

// errUsage marks command-line errors (exit 2).
var errUsage = errors.New("usage")

// Execute runs the command line and returns the exit code.
func Execute(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	e := &env{g: &globalFlags{}, stdin: stdin, stdout: stdout, stderr: stderr}
	err := dispatch(ctx, e, root(), args)
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return int(pderr.OK)
	case errors.Is(err, errUsage):
		fmt.Fprintln(stderr, "paritydesk:", err)
		return int(pderr.Usage)
	default:
		fmt.Fprintln(stderr, "paritydesk:", err)
		return int(pderr.CodeOf(err))
	}
}

func dispatch(ctx context.Context, e *env, c *command, args []string) error {
	fs := flag.NewFlagSet(c.name, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	e.g.register(fs)
	if c.flags != nil {
		c.flags(fs)
	}
	fs.Usage = func() { printUsage(e.stderr, c, fs) }
	if len(c.sub) > 0 {
		// Only global flags may precede a subcommand name.
		if err := fs.Parse(args); err != nil {
			return wrapUsage(err)
		}
		rest := fs.Args()
		if len(rest) == 0 {
			printUsage(e.stderr, c, fs)
			return fmt.Errorf("%w: missing command", errUsage)
		}
		for _, s := range c.sub {
			if s.name == rest[0] {
				return dispatch(ctx, e, s, rest[1:])
			}
		}
		return fmt.Errorf("%w: unknown command %q", errUsage, rest[0])
	}
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return wrapUsage(err)
	}
	if len(pos) < c.minArgs || (c.maxArgs >= 0 && len(pos) > c.maxArgs) {
		return fmt.Errorf("%w: paritydesk %s %s", errUsage, c.name, c.args)
	}
	if c.run == nil {
		return pderr.ErrNotImplemented
	}
	return c.run(ctx, e, fs, pos)
}

func wrapUsage(err error) error {
	if errors.Is(err, flag.ErrHelp) {
		return err
	}
	return fmt.Errorf("%w: %v", errUsage, err)
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

func printUsage(w io.Writer, c *command, fs *flag.FlagSet) {
	fmt.Fprintf(w, "usage: paritydesk %s %s\n", c.name, c.args)
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
	fmt.Fprintln(w, "\nflags:")
	fs.PrintDefaults()
}

var categoryNames = []string{"sessions", "environments", "order", "sidebar", "pins", "prefs", "worktrees", "window", "mcp"}

func categoryFlags(fs *flag.FlagSet) {
	fs.String("only", "", "categories to carry, comma-separated: "+strings.Join(categoryNames, ","))
	fs.String("skip", "", "categories to leave alone, comma-separated")
}

func leaf(name, args, short string, min, max int, flags func(*flag.FlagSet)) *command {
	return &command{name: name, args: args, short: short, minArgs: min, maxArgs: max, flags: flags}
}

func group(name, args, short string, hidden bool, sub ...*command) *command {
	return &command{name: name, args: args, short: short, hidden: hidden, sub: sub}
}

func yesFlag(fs *flag.FlagSet) { fs.Bool("yes", false, "do not ask") }

// root returns the command tree. Leaves without a run function are pending
// (WI-8 maps each to its orchestrator, gen or hook call).
func root() *command {
	return group("paritydesk", "<command> [flags]",
		"Switch the Claude desktop app between accounts and keep everything else in step.", false,
		leaf("init", "", "Set up paritydesk for the account in the app now", 0, 0, nil),
		group("profile", "<list|adopt|add|rename|remove>", "Manage profiles", false,
			leaf("list", "", "List profiles", 0, 0, nil),
			leaf("adopt", "<alias> <folder>", "Register a parked folder after checking its identity", 2, 2, nil),
			leaf("add", "<alias>", "Add an account without signing out of any other", 1, 1, func(fs *flag.FlagSet) {
				fs.Bool("managed", false, "the account is managed by an employer or client")
			}),
			leaf("rename", "<alias> <new-alias>", "Rename a profile", 2, 2, nil),
			leaf("remove", "<alias>", "Unregister a profile; its folder is kept", 1, 1, nil),
		),
		leaf("status", "", "Show the active profile and anything pending", 0, 0, func(fs *flag.FlagSet) {
			fs.Bool("short", false, "one line; never opens Local Storage")
		}),
		leaf("plan", "[<alias>]", "Preview what a switch would change (approximate)", 0, 1, categoryFlags),
		leaf("switch", "[<alias>|-]", "Switch to another profile", 0, 1, func(fs *flag.FlagSet) {
			categoryFlags(fs)
			fs.Bool("yes", false, "do not ask (destructive differences after the preview still ask)")
			fs.Bool("no-launch", false, "do not relaunch Claude")
			fs.Bool("no-reopen", false, "do not reopen the invoking session")
			fs.Bool("detach", false, "run as a detached launchd job")
			fs.Bool("json-progress", false, "NDJSON progress on stderr")
			fs.Int("allow-deletions", 0, "allow up to N deletions per category past the breaker")
			fs.Bool("accept-worktree-risk", false, "switch even if a worktree with uncommitted changes loses its registration")
			fs.Bool("allow-update", false, "let a staged Claude update install, then re-check before syncing")
			fs.String("reopen-session", "", "Claude Code session id of the invoking session (set by the plugin)")
			fs.Bool("from-runner", false, "internal: this process is the detached job")
		}),
		leaf("sync", "", "Re-apply the shared state to the active profile", 0, 0, func(fs *flag.FlagSet) {
			yesFlag(fs)
			fs.Int("allow-deletions", 0, "allow up to N deletions per category past the breaker")
		}),
		leaf("verify", "", "Check that the app kept what the last run wrote", 0, 0, nil),
		leaf("recover", "", "Finish or undo an interrupted run", 0, 0, func(fs *flag.FlagSet) {
			fs.Bool("rollback", false, "undo the interrupted run instead of completing it")
			yesFlag(fs)
		}),
		leaf("rollback", "[<n>]", "Restore a generation without overwriting newer work", 0, 1, func(fs *flag.FlagSet) {
			fs.Bool("propagate", false, "carry the restored values to other profiles on the next switch")
		}),
		leaf("rebase", "", "Forget every sync base; the next run is a first sync", 0, 0, nil),
		group("generations", "<list|show>", "List or show backup generations", false,
			leaf("list", "", "List generations", 0, 0, nil),
			leaf("show", "<n>", "Show one generation", 1, 1, nil),
		),
		group("backups", "<prune|purge>", "Prune or purge backup generations", false,
			leaf("prune", "", "Apply the retention policy", 0, 0, nil),
			leaf("purge", "", "Remove every generation", 0, 0, yesFlag),
		),
		leaf("doctor", "", "Check the installation, folders and app compatibility", 0, 0, func(fs *flag.FlagSet) {
			fs.Bool("grant-automation", false, "ask macOS for Automation permission now")
		}),
		leaf("log", "", "Show the log of a run", 0, 0, func(fs *flag.FlagSet) {
			fs.String("run", "", "run id (default: the last run)")
		}),
		leaf("history", "", "List past runs", 0, 0, nil),
		group("config", "<path|show|validate>", "Show, locate or validate the config file", false,
			leaf("path", "", "Print the config file path", 0, 0, nil),
			leaf("show", "", "Print the effective config", 0, 0, nil),
			leaf("validate", "", "Validate the config file", 0, 0, nil),
		),
		leaf("uninstall", "", "Remove paritydesk's state; every data folder is kept", 0, 0, func(fs *flag.FlagSet) {
			fs.Bool("keep-backups", true, "keep backup generations")
			yesFlag(fs)
		}),
		group("hook", "<event>", "Internal: entry point of the plugin's hooks", true,
			leaf("session-start", "", "SessionStart hook", 0, 0, nil),
		),
		&command{name: "version", short: "Print the version", run: runVersion},
	)
}

func runVersion(_ context.Context, e *env, _ *flag.FlagSet, _ []string) error {
	v := Version
	if bi, ok := debug.ReadBuildInfo(); ok && Version == "0.1.0-dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		v = bi.Main.Version
	}
	if e.g.JSON {
		_, err := fmt.Fprintf(e.stdout, "{\"v\":1,\"version\":%q}\n", v)
		return err
	}
	_, err := fmt.Fprintln(e.stdout, "paritydesk", v)
	return err
}
