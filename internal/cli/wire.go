package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/app"
	"github.com/kazupc/paritydesk/internal/apply"
	"github.com/kazupc/paritydesk/internal/clock"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/gen"
	"github.com/kazupc/paritydesk/internal/hook"
	"github.com/kazupc/paritydesk/internal/orchestrator"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/profile"
	"github.com/kazupc/paritydesk/internal/rows"
	"github.com/kazupc/paritydesk/internal/runner"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/sources/all"
	"github.com/kazupc/paritydesk/internal/sources/configjson"
	"github.com/kazupc/paritydesk/internal/sources/prefs"
	"github.com/kazupc/paritydesk/internal/sources/worktrees"
	"github.com/kazupc/paritydesk/internal/state"
	"github.com/kazupc/paritydesk/internal/store/kv"
	"github.com/kazupc/paritydesk/internal/store/kv/leveldb"
)

// TempDirName is paritydesk's temporary directory under the data root
// (D30): snapshot clones and atomic-write temp files live there, on the
// same volume as the data folders.
const TempDirName = ".paritydesk-tmp"

// wireInput is what buildDeps needs besides the global flags.
type wireInput struct {
	Home       string
	Paths      config.Paths
	Getenv     func(string) string
	Executable string // absolute path of this binary
	UI         orchestrator.UI
	ReadOnly   bool
	// Exec runs external commands; nil means the real runner, which is
	// refused under `go test`.
	Exec app.Runner
	// Manifests overrides compat.Load (tests); nil loads the embedded
	// manifests plus <SupportDir>/compat.d.
	Manifests []*compat.Manifest
}

// errRealRunnerInTest refuses the real exec runner under `go test`.
var errRealRunnerInTest = errors.New("cli: the real command runner is refused under go test")

// buildDeps constructs the orchestrator's dependencies from the real
// implementations: config.Load/Validate (with the manifest's reserved
// folders and pref tags), state.Open (lock file from state.LockFile keyed
// to the data root), gen.Open, profile.Scanner with configjson.ReadIdentity
// and sources.DiscoverReplicas, profile.Resolve, app.NewDarwin,
// compat.Gate over compat.Load, leveldb.New (always over the unwrapped
// fsx.Default(), confined to its temp root <data_root>/.paritydesk-tmp),
// all.Registry, rows.Registry and rows.PostMerge, plan.Compose and
// plan.Validate, prefs.PermissionAcks, apply.Applier, sources.Directory,
// worktrees.Guard, runner.Detacher (IsApp = app.IsAppProcess bound to the
// located bundle), runner.PlanStore{RunDir}, and the render-backed UI.
//
// In read-only mode (--read-only or PARITYDESK_READONLY=1) the
// orchestrator's FS is fsx.ReadOnly(fsx.Default()), KV is
// kv.ReadOnly(leveldb opener), and Deps.ReadOnly is set, so no lock, state
// commit, config save, plan save or runner submit happens; Local Storage
// snapshots still work.
//
// Under `go test` it refuses real paths (fsx.CheckTestPath) and the real
// exec runner, before anything is read.
func buildDeps(ctx context.Context, g *globalFlags, in wireInput) (orchestrator.Deps, error) {
	_ = ctx
	readOnly := in.ReadOnly || g.ReadOnly
	if err := checkTestPaths(in.Home, in.Paths.ConfigFile, in.Paths.SupportDir, in.Paths.StateDir,
		in.Paths.GenDir, in.Paths.RunDir, in.Paths.LogDir, in.Executable); err != nil {
		return orchestrator.Deps{}, err
	}
	if in.Exec == nil && testing.Testing() {
		return orchestrator.Deps{}, errRealRunnerInTest
	}
	cfg, _, err := loadConfig(in.Paths.ConfigFile)
	if err != nil {
		return orchestrator.Deps{}, err
	}
	dataRoot := filepath.Clean(config.ExpandHome(in.Home, cfg.App.DataRoot))
	if err := checkTestPaths(dataRoot); err != nil {
		return orchestrator.Deps{}, err
	}
	manifests := in.Manifests
	if manifests == nil {
		if manifests, err = compat.Load(filepath.Join(in.Paths.SupportDir, "compat.d")); err != nil {
			return orchestrator.Deps{}, fmt.Errorf("compat manifests: %w", err)
		}
	}
	reserved, tags := mergeRules(manifests)
	if err := cfg.Validate(reserved, tags); err != nil {
		return orchestrator.Deps{}, err
	}

	clk := clock.System{}
	base := fsx.Default()
	ops := base
	if readOnly {
		ops = fsx.ReadOnly(base)
	}
	// The LevelDB opener always wraps the unwrapped platform Ops, confined
	// to its temp root, so snapshots keep working in read-only mode.
	opener, err := leveldb.New(filepath.Join(dataRoot, TempDirName), base)
	if err != nil {
		return orchestrator.Deps{}, err
	}
	var kvo kv.Opener = opener
	if readOnly {
		kvo = kv.ReadOnly(opener)
	}
	st, err := state.Open(in.Paths.StateDir, state.LockFile(in.Paths.SupportDir, dataRoot), ops, clk)
	if err != nil {
		return orchestrator.Deps{}, err
	}
	plat, err := app.NewDarwin(app.DarwinConfig{Home: in.Home, BundleID: cfg.App.BundleID, Exec: in.Exec, Clock: clk})
	if err != nil {
		return orchestrator.Deps{}, err
	}
	isApp := locatedIsApp(plat, cfg.App.BundleID)
	exec := in.Exec
	if exec == nil {
		exec = app.ExecRunner{}
	}
	return orchestrator.Deps{
		Config:   cfg,
		Paths:    in.Paths,
		Home:     in.Home,
		DataRoot: dataRoot,
		Clock:    clk,
		FS:       ops,
		State:    st,
		Gens:     gen.Open(in.Paths.GenDir, ops, clk),
		Folders: profile.Scanner{FS: ops, DataRoot: dataRoot,
			ReadIdentity: configjson.ReadIdentity, ListReplicas: sources.DiscoverReplicas},
		Resolve:        profile.Resolve,
		ReadMarker:     profile.ReadMarker,
		WriteMarker:    profile.WriteMarker,
		App:            plat,
		Compat:         compat.Gate{Manifests: manifests},
		KV:             kvo,
		Adapters:       all.Registry(),
		Rows:           rows.Registry(),
		PostMerge:      rows.PostMerge,
		Compose:        plan.Compose,
		Validate:       plan.Validate,
		PermissionAcks: prefs.PermissionAcks,
		Applier:        &apply.Applier{FS: ops, KV: kvo, Holders: holderCheck(plat), ReadOnly: readOnly},
		Sessions:       sources.Directory{},
		Worktrees:      worktrees.Guard{},
		Detach: runner.Detacher{RunDir: in.Paths.RunDir, LogDir: in.Paths.LogDir, Binary: in.Executable,
			UID: os.Getuid(), Launchd: runner.NewLaunchd(exec), Procs: plat.Procs, IsApp: isApp},
		Plans:    runner.PlanStore{Dir: in.Paths.RunDir},
		UI:       in.UI,
		Crash:    faultInjector(in.Getenv),
		ReadOnly: readOnly,
	}, nil
}

// checkTestPaths applies fsx.CheckTestPath to every non-empty path.
func checkTestPaths(paths ...string) error {
	for _, p := range paths {
		if err := fsx.CheckTestPath(p); err != nil {
			return err
		}
	}
	return nil
}

// locatedIsApp returns app.IsAppProcess bound to the bundle that Locate
// finds on first use. When the bundle cannot be located, no process
// counts as the app's.
func locatedIsApp(plat *app.Platform, bundleID string) func(app.Process) bool {
	var (
		once sync.Once
		path string
	)
	return func(p app.Process) bool {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if id, err := plat.Locate(ctx, bundleID); err == nil {
				path = id.Path
			}
		})
		return path != "" && app.IsAppProcess(p, path)
	}
}

// holderCheck adapts the platform's file-holder scan to apply.HolderCheck.
func holderCheck(plat *app.Platform) apply.HolderCheck {
	return func(ctx context.Context, folder string) (bool, string, error) {
		if plat.Holders == nil {
			return false, "", nil
		}
		hs, err := plat.Holders.Holders(ctx, []string{folder})
		if err != nil {
			return false, "", err
		}
		if len(hs) == 0 {
			return false, "", nil
		}
		return true, fmt.Sprintf("%d open files (first: pid %d)", len(hs), hs[0].PID), nil
	}
}

// faultInjector implements PARITYDESK_FAULT=after:<phase> for the recovery
// drill: the run stops right after that phase's intent, as if killed.
func faultInjector(getenv func(string) string) func(orchestrator.Phase) bool {
	if getenv == nil {
		return nil
	}
	v, ok := strings.CutPrefix(getenv("PARITYDESK_FAULT"), "after:")
	if !ok || v == "" {
		return nil
	}
	phase := orchestrator.Phase(v)
	return func(after orchestrator.Phase) bool { return after == phase }
}

// hookTimeout bounds the SessionStart hook (the plugin allows 5 s).
const hookTimeout = 3 * time.Second

// realHookDeps builds the SessionStart hook's dependencies: state read
// without the lock (Load never writes) through a read-only FS, and the
// inside-the-app check over the process table. Nothing here opens Local
// Storage or scans data folders.
func realHookDeps(_ context.Context, e *env, pluginVersion string) (hook.Deps, error) {
	paths, home, err := e.paths()
	if err != nil {
		return hook.Deps{}, err
	}
	if err := checkTestPaths(home, paths.StateDir, paths.SupportDir); err != nil {
		return hook.Deps{}, err
	}
	cfg, _, err := loadConfig(paths.ConfigFile)
	if err != nil {
		cfg = config.Default()
	}
	dataRoot := filepath.Clean(config.ExpandHome(home, cfg.App.DataRoot))
	st, err := state.Open(paths.StateDir, state.LockFile(paths.SupportDir, dataRoot), fsx.ReadOnly(fsx.Default()), clock.System{})
	if err != nil {
		return hook.Deps{}, err
	}
	d := hook.Deps{State: st, PluginVersion: pluginVersion, BinaryVersion: Version}
	plat, err := app.NewDarwin(app.DarwinConfig{Home: home, BundleID: cfg.App.BundleID})
	if err != nil {
		return d, nil // silent: InsideApp stays nil
	}
	det := runner.Detacher{Procs: plat.Procs, IsApp: locatedIsApp(plat, cfg.App.BundleID)}
	d.InsideApp = det.InsideApp
	return d, nil
}
