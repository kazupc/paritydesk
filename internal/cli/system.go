package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kazupc/paritydesk/internal/artifacts"
	"github.com/kazupc/paritydesk/internal/clock"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/gen"
	"github.com/kazupc/paritydesk/internal/hook"
	"github.com/kazupc/paritydesk/internal/online"
	"github.com/kazupc/paritydesk/internal/orchestrator"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/state"
)

// generationStore is the part of gen.Store the generations and backups
// commands use.
type generationStore interface {
	List() ([]gen.Info, error)
	Load(n uint64) (*gen.Manifest, error)
	Prune(p gen.PrunePolicy) error
	Purge() error
}

var _ generationStore = (*gen.Store)(nil)

// system is everything the command line takes from the outside world.
// Execute uses realSystem; tests build one over a temporary home with a
// fake orchestrator.Service.
type system struct {
	getenv     func(string) string
	home       func() (string, error)
	executable func() (string, error)
	isTerminal func(f any) bool
	// isTTY reports a real terminal (the terminal driver answers), not
	// just any character device such as /dev/null.
	isTTY func(f any) bool
	now   func() time.Time
	// service builds the orchestrator for one command; ui is the
	// render-backed front end.
	service func(ctx context.Context, e *env, ui orchestrator.UI) (orchestrator.Service, error)
	// gens opens the generation store.
	gens func(ctx context.Context, e *env) (generationStore, error)
	// quiet takes the run lock for a command that deletes paritydesk's own
	// files (backups prune and purge, uninstall) and refuses while a run
	// is open or a profile add waits; release frees the lock.
	quiet func(ctx context.Context, e *env) (release func(), err error)
	// hookDeps builds the SessionStart hook's dependencies.
	hookDeps func(ctx context.Context, e *env, pluginVersion string) (hook.Deps, error)
	// toolDeps builds the PreToolUse and PostToolUse hooks' dependencies.
	toolDeps func(ctx context.Context, e *env) (hook.ToolDeps, error)
	// online builds the environment of the `online` commands.
	online func(ctx context.Context, e *env) (*onlineEnv, error)
	// onlineCancel builds the minimal online service `online cancel`
	// needs: the online folder and its lock, nothing that reads the
	// config, the state or the app (the emergency stop must work when
	// they cannot be read).
	onlineCancel func(ctx context.Context, e *env) (*online.Service, error)
	// rules returns the reserved folder names and preference tags of the
	// embedded compat manifests, for config.Validate.
	rules func(e *env) (reserved []string, prefTags map[string]string, err error)
	// artifacts builds the artifact sync service of the `artifacts`
	// commands.
	artifacts func(ctx context.Context, e *env) (*artifacts.Service, error)
}

// realSystem returns the production system.
func realSystem() *system {
	return &system{
		getenv:       os.Getenv,
		home:         os.UserHomeDir,
		executable:   resolvedExecutable,
		isTerminal:   isTerminal,
		isTTY:        isTTY,
		now:          time.Now,
		service:      realService,
		gens:         realGens,
		quiet:        realQuiet,
		hookDeps:     realHookDeps,
		toolDeps:     realToolDeps,
		online:       realOnline,
		onlineCancel: realOnlineCancel,
		rules:        realRules,
		artifacts:    realArtifacts,
	}
}

// resolvedExecutable returns the absolute, symlink-free path of this
// binary.
func resolvedExecutable() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate executable: %w", err)
	}
	p, err = filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("resolve executable: %w", err)
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("executable path is not absolute")
	}
	return p, nil
}

// isTerminal reports whether f is a character device (a terminal).
func isTerminal(f any) bool {
	file, ok := f.(*os.File)
	if !ok {
		return false
	}
	fi, err := file.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// interactive reports whether prompts can be answered: stdin and stderr
// are terminals.
func (e *env) interactive() bool {
	return e.sys.isTerminal(e.stdin) && e.sys.isTerminal(e.stderr)
}

// terminal is the stricter test of the commands that need a person at a
// terminal (profile adopt --from-legacy --apply --quarantine): stdin and
// stderr are real terminals, so /dev/null or another character device
// does not pass. It is a safeguard against a tool or agent granting
// itself the move, not a guarantee (a pseudo-terminal still passes).
func (e *env) terminal() bool {
	if e.sys.isTTY == nil {
		return false
	}
	return e.interactive() && e.sys.isTTY(e.stdin) && e.sys.isTTY(e.stderr)
}

func realService(ctx context.Context, e *env, ui orchestrator.UI) (orchestrator.Service, error) {
	paths, home, err := e.paths()
	if err != nil {
		return nil, err
	}
	exe, err := e.sys.executable()
	if err != nil {
		return nil, err
	}
	d, err := buildDeps(ctx, e.g, wireInput{
		Home: home, Paths: paths, Getenv: e.sys.getenv, Executable: exe, UI: ui, ReadOnly: e.readOnly(),
	})
	if err != nil {
		return nil, err
	}
	return orchestrator.New(d), nil
}

func realGens(_ context.Context, e *env) (generationStore, error) {
	paths, _, err := e.paths()
	if err != nil {
		return nil, err
	}
	if err := fsx.CheckTestPath(paths.GenDir); err != nil {
		return nil, err
	}
	ops := fsx.Default()
	if e.readOnly() {
		ops = fsx.ReadOnly(ops)
	}
	return gen.Open(paths.GenDir, ops, clock.System{}), nil
}

// realQuiet takes the run lock of the configured data root (the default
// one when the config cannot be read, so uninstall still works) and
// refuses with NeedsRecover while root.json records an open run or a
// pending profile add: deleting a generation or the journal then would
// leave recover nothing to finish or undo the run with.
func realQuiet(ctx context.Context, e *env) (func(), error) {
	paths, home, err := e.paths()
	if err != nil {
		return nil, err
	}
	cfg, _, err := loadConfig(paths.ConfigFile)
	if err != nil {
		cfg = config.Default()
	}
	dataRoot := filepath.Clean(config.ExpandHome(home, cfg.App.DataRoot))
	if err := checkTestPaths(paths.StateDir, paths.SupportDir, dataRoot); err != nil {
		return nil, err
	}
	st, err := state.Open(paths.StateDir, state.LockFile(paths.SupportDir, dataRoot), fsx.Default(), clock.System{})
	if err != nil {
		return nil, err
	}
	l, err := st.Lock(ctx, 0)
	if err != nil {
		return nil, err
	}
	release := func() { _ = l.Release() }
	root, err := st.Load()
	if err != nil {
		release()
		return nil, pderr.Wrap(pderr.Internal, "state", err)
	}
	if err := openRunError(root); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

// openRunError refuses while root records an open run or a pending
// profile add.
func openRunError(root *state.Root) error {
	switch {
	case root == nil:
		return nil
	case root.Run != nil:
		return pderr.New(pderr.NeedsRecover, "state", "a run was interrupted; run paritydesk recover first")
	case root.PendingAdd != nil:
		return pderr.New(pderr.NeedsRecover, "state", "a profile add is waiting for its sign-in; run paritydesk recover first")
	}
	return nil
}

func realRules(e *env) ([]string, map[string]string, error) {
	paths, _, err := e.paths()
	if err != nil {
		return nil, nil, err
	}
	if err := fsx.CheckTestPath(paths.SupportDir); err != nil {
		return nil, nil, err
	}
	ms, err := compat.Load(filepath.Join(paths.SupportDir, "compat.d"))
	if err != nil {
		return nil, nil, err
	}
	reserved, tags := mergeRules(ms)
	return reserved, tags, nil
}

// mergeRules combines the manifests' reserved folders and preference tags.
// A key tagged security or internal_state by any manifest keeps that tag,
// so a preference is allowed only if no manifest forbids it.
func mergeRules(ms []*compat.Manifest) ([]string, map[string]string) {
	var reserved []string
	tags := map[string]string{}
	for _, m := range ms {
		reserved = append(reserved, m.ReservedFolders...)
		for k, t := range m.PrefTags {
			if old, ok := tags[k]; ok && (old == compat.TagSecurity || old == compat.TagInternalState) {
				continue
			}
			tags[k] = t
		}
	}
	return reserved, tags
}

// loadConfig reads the config file, falling back to the defaults when it
// does not exist (exists reports which).
func loadConfig(path string) (cfg *config.Config, exists bool, err error) {
	cfg, err = config.Load(path)
	switch {
	case err == nil:
		return cfg, true, nil
	case err == config.ErrNoConfig:
		return config.Default(), false, nil
	}
	return nil, false, err
}
