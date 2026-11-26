package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/app"
	"github.com/kazupc/paritydesk/internal/apply"
	"github.com/kazupc/paritydesk/internal/artifacts"
	"github.com/kazupc/paritydesk/internal/clock"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/e2e"
	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/gen"
	"github.com/kazupc/paritydesk/internal/hook"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/online"
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
	"github.com/kazupc/paritydesk/internal/store/idb"
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
// all.RegistryWith (row pins over idb.Cloner in the same temp root),
// rows.Registry and rows.PostMerge, plan.Compose and
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
	// PARITYDESK_FAULT is checked before anything is opened, so a refused
	// fault leaves no trace.
	flt, err := parseFaults(faultEnv{getenv: in.Getenv, home: in.Home, realHome: e2e.RealHome,
		bundleID: cfg.App.BundleID, dataRoot: dataRoot, ui: in.UI, sleep: time.Sleep, exit: os.Exit})
	if err != nil {
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
	// The pins row reads IndexedDB from a private per-file clone in the
	// same temporary root (spec-pins 4.2); leftovers of a crash go first.
	cloner, err := idb.NewCloner(filepath.Join(dataRoot, TempDirName), base)
	if err != nil {
		return orchestrator.Deps{}, err
	}
	_ = cloner.SweepTemp()
	var kvo kv.Opener = opener
	if readOnly {
		kvo = kv.ReadOnly(opener)
	}
	st, err := state.Open(in.Paths.StateDir, state.LockFile(in.Paths.SupportDir, dataRoot), ops, clk)
	if err != nil {
		return orchestrator.Deps{}, err
	}
	plat, err := app.NewDarwin(app.DarwinConfig{Home: in.Home, BundleID: cfg.App.BundleID, Exec: in.Exec, Clock: clk,
		OpenFileScan: app.ScanMode(cfg.App.OpenFileScan)})
	if err != nil {
		return orchestrator.Deps{}, err
	}
	isApp := locatedIsApp(plat, cfg.App.BundleID)
	var ol orchestrator.Online
	var osvc *online.Service
	if cfg.Online.Reconcile != config.OnlineReconcileOff {
		osvc = onlineService(in.Paths.SupportDir, ops, cfg, compat.OnlineSpec{})
		ol = osvc
	}
	// The archive view is always wired: profile rename and remove keep the
	// archive's aliases right even while artifact sync is off (an archive
	// left under an old alias would make every later sync stop as
	// foreign). Plan lines, due.json and the doctor check are gated on
	// artifact sync being on (orchestrator.artifactsActive).
	art := archiveView(in.Paths.SupportDir, ops, cfg, osvc, dataRoot, readOnly, manifests, plat)
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
		Adapters:       all.RegistryWith(all.Options{IDB: cloner.Read}),
		Rows:           rows.Registry(),
		PostMerge:      rows.PostMerge,
		Compose:        plan.Compose,
		Validate:       plan.Validate,
		PermissionAcks: prefs.PermissionAcks,
		Applier: &apply.Applier{FS: ops, KV: kvo, Holders: holderCheck(plat), ReadOnly: readOnly,
			AfterFirstWrite: flt.afterFirstWrite},
		Sessions:  sources.Directory{},
		Worktrees: worktrees.Guard{},
		Detach: runner.Detacher{RunDir: in.Paths.RunDir, LogDir: in.Paths.LogDir, Binary: in.Executable,
			UID: os.Getuid(), Launchd: runner.NewLaunchd(exec), Procs: plat.Procs, IsApp: isApp},
		Plans:     runner.PlanStore{Dir: in.Paths.RunDir},
		UI:        in.UI,
		Crash:     flt.crash,
		Online:    ol,
		Artifacts: art,
		ReadOnly:  readOnly,
	}, nil
}

// archiveView is the orchestrator's view of the artifact archive (plan
// lines, due.json at P10, profile rename and remove, doctor). The
// installed Claude's [artifacts] table and version are looked up once,
// on first use.
func archiveView(supportDir string, ops fsx.Ops, cfg *config.Config, osvc *online.Service, dataRoot string, readOnly bool,
	manifests []*compat.Manifest, plat *app.Platform) *lazyArchive {
	return &lazyArchive{build: func() *artifacts.Service {
		arch := artifacts.NewArchive(supportDir, ops, compat.ArtifactsSpec{})
		arch.Lock = artifactsLocker(supportDir)
		svc := &artifacts.Service{Archive: arch, Online: osvc, Config: cfg, ReadOnly: readOnly, Clock: clock.System{},
			SupportDir: supportDir, DataRoot: dataRoot}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if b, err := plat.Locate(ctx, cfg.App.BundleID); err == nil {
			if m, _, err := (compat.Gate{Manifests: manifests}).Manifest(ctx, b.Version); err == nil && m != nil {
				svc.Spec, svc.OnlineSpec, svc.AppVersion = m.Artifacts, m.Online, b.Version
				arch.Spec = m.Artifacts
			}
		}
		return svc
	}}
}

// lazyArchive builds the archive view on first use, so commands that
// never touch artifacts do not look up the installed Claude for it.
type lazyArchive struct {
	once  sync.Once
	build func() *artifacts.Service
	svc   *artifacts.Service
}

func (l *lazyArchive) get() *artifacts.Service {
	l.once.Do(func() { l.svc = l.build() })
	return l.svc
}

func (l *lazyArchive) Due(a ids.Alias, rep model.Replica) (int, error) { return l.get().Due(a, rep) }
func (l *lazyArchive) LastSyncEpoch(a ids.Alias) (uint64, bool, error) {
	return l.get().LastSyncEpoch(a)
}
func (l *lazyArchive) WriteDue(ctx context.Context, a ids.Alias, rep model.Replica, epoch uint64) error {
	return l.get().WriteDue(ctx, a, rep, epoch)
}
func (l *lazyArchive) RenameProfile(ctx context.Context, from, to ids.Alias) error {
	return l.get().RenameProfile(ctx, from, to)
}
func (l *lazyArchive) RemoveProfile(ctx context.Context, a ids.Alias) error {
	return l.get().RemoveProfile(ctx, a)
}

// Doctor is the doctor view (orchestrator's artifactsDoctor).
func (l *lazyArchive) Doctor() (int, string, error) { return l.get().Doctor() }

// onlineService returns the online layer over <supportDir>/online, with
// the online lock in <supportDir>/locks and the built-in providers
// (probe, sidebar; the pins and artifacts units add theirs). spec is the
// manifest's [online] table when known (the orchestrator passes its own
// to Build and Publish).
func onlineService(supportDir string, ops fsx.Ops, cfg *config.Config, spec compat.OnlineSpec) *online.Service {
	store := online.NewStore(supportDir, ops)
	store.Lock = onlineLocker(supportDir)
	return &online.Service{Store: store, Config: cfg.Online, Clock: clock.System{}, Mono: online.SystemMono{},
		Providers: onlineRegistry(compat.ArtifactsSpec{}), Settings: online.NewSettings(spec, cfg.Online)}
}

// onlineRegistry is the online layer's provider registry: the built-in
// providers and, appended (A10), the artifacts provider in its guard
// form (every artifact transport's tools are declared, so a queue of
// either transport validates; calls still pass only as exact offered
// steps). In a switch's P5 Build it yields no steps.
func onlineRegistry(spec compat.ArtifactsSpec) []online.Provider {
	return append(online.Registry(), artifacts.Provider{Spec: spec, Guard: true})
}

// onlineLocker takes <supportDir>/locks/online.lock (flock), never the
// run lock. It always uses the platform's own Ops: the lock file is not
// state, and the commands that lock refuse read-only mode first.
func onlineLocker(supportDir string) func(ctx context.Context, wait time.Duration) (func(), error) {
	return func(ctx context.Context, wait time.Duration) (func(), error) {
		dir := filepath.Join(supportDir, "locks")
		ls, err := state.Open(dir, filepath.Join(dir, online.LockName), fsx.Default(), clock.System{})
		if err != nil {
			return nil, err
		}
		l, err := ls.Lock(ctx, wait)
		if err != nil {
			return nil, err
		}
		return func() { _ = l.Release() }, nil
	}
}

// defaultSupportDir is the support directory the plugin's shim and hooks
// know.
func defaultSupportDir(home string) string {
	return filepath.Join(home, "Library", "Application Support", "paritydesk")
}

// realToolDeps builds the PreToolUse and PostToolUse hooks' dependencies
// for the default support directory only (the shim knows only that one):
// the online folder through a create-only FS, the guard mode from the
// config (defaults when it cannot be read) and the providers' policies.
// No process scan, no state, no manifest, no main.log.
func realToolDeps(_ context.Context, e *env) (hook.ToolDeps, error) {
	home, err := e.sys.home()
	if err != nil {
		return hook.ToolDeps{}, err
	}
	paths := config.ResolvePaths(home, e.sys.getenv, config.Overrides{})
	sd := defaultSupportDir(home)
	if err := checkTestPaths(home, sd); err != nil {
		return hook.ToolDeps{}, err
	}
	cfg, _, err := loadConfig(paths.ConfigFile)
	if err != nil {
		cfg = config.Default()
	}
	dir := filepath.Join(sd, online.DirName)
	store := online.NewStore(sd, online.CreateOnly(fsx.Default(), dir))
	return hook.ToolDeps{Store: store, Mode: online.GuardMode(cfg.Online.Guard), Mono: online.SystemMono{},
		Clock: clock.System{}, Providers: onlineRegistry(compat.ArtifactsSpec{})}, nil
}

// realOnline builds the `online` commands' environment: config, a
// lock-free view of root.json, the online service, and the installed
// Claude's manifest, main.log and process table.
func realOnline(ctx context.Context, e *env) (*onlineEnv, error) {
	paths, home, err := e.paths()
	if err != nil {
		return nil, err
	}
	if err := checkTestPaths(home, paths.StateDir, paths.SupportDir); err != nil {
		return nil, err
	}
	cfg, exists, err := loadConfig(paths.ConfigFile)
	if err != nil {
		return nil, err
	}
	dataRoot := filepath.Clean(config.ExpandHome(home, cfg.App.DataRoot))
	st, err := state.Open(paths.StateDir, state.LockFile(paths.SupportDir, dataRoot), fsx.ReadOnly(fsx.Default()), clock.System{})
	if err != nil {
		return nil, err
	}
	ops := fsx.Default()
	if e.readOnly() {
		ops = fsx.ReadOnly(ops)
	}
	o := &onlineEnv{root: st.Load, defaultDir: filepath.Clean(paths.SupportDir) == defaultSupportDir(home),
		insideApp: func(context.Context) (bool, error) { return false, nil },
		replica:   func(context.Context, time.Time) online.ReplicaObs { return online.ReplicaObs{} },
		activeOrg: func(context.Context, ids.AccountID) (ids.OrgID, bool) { return "", false }}
	if exists {
		o.cfg = cfg
	}
	var sig compat.Signals
	if plat, perr := app.NewDarwin(app.DarwinConfig{Home: home, BundleID: cfg.App.BundleID, OpenFileScan: app.ScanMode(cfg.App.OpenFileScan)}); perr == nil {
		lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if b, lerr := plat.Locate(lctx, cfg.App.BundleID); lerr == nil {
			if ms, merr := compat.Load(filepath.Join(paths.SupportDir, "compat.d")); merr == nil {
				if m, _, serr := (compat.Gate{Manifests: ms}).Manifest(lctx, b.Version); serr == nil && m != nil {
					o.spec, sig = m.Online, m.Signals
					o.art, o.appVersion = m.Artifacts, b.Version
				}
			}
		}
		cancel()
		det := runner.Detacher{Procs: plat.Procs, IsApp: locatedIsApp(plat, cfg.App.BundleID)}
		o.insideApp = det.InsideApp
		o.replica = func(ctx context.Context, since time.Time) online.ReplicaObs {
			rep, known, err := plat.LastReplica(ctx, sig, since)
			if err != nil || !known {
				return online.ReplicaObs{}
			}
			return online.ReplicaObs{Known: true, Replica: rep}
		}
		o.activeOrg = func(ctx context.Context, a ids.AccountID) (ids.OrgID, bool) {
			org, ok, err := plat.ActiveOrg(ctx, sig, a, time.Time{})
			return org, ok && err == nil
		}
	}
	o.dataRoot = dataRoot
	o.svc = onlineService(paths.SupportDir, ops, cfg, o.spec)
	o.svc.Providers = onlineRegistry(o.art)
	return o, nil
}

// realOnlineCancel builds the online service of `online cancel` from the
// resolved paths only: no config, state, manifest or process scan, so
// the emergency stop works with a broken config file.
func realOnlineCancel(_ context.Context, e *env) (*online.Service, error) {
	paths, home, err := e.paths()
	if err != nil {
		return nil, err
	}
	if err := checkTestPaths(home, paths.SupportDir); err != nil {
		return nil, err
	}
	store := online.NewStore(paths.SupportDir, fsx.Default())
	store.Lock = onlineLocker(paths.SupportDir)
	return &online.Service{Store: store, Clock: clock.System{}, Mono: online.SystemMono{}}, nil
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

// artifactsDueReader reads the artifact archive's due.json (at most 4
// KiB, read-only; malformed or oversized means none).
func artifactsDueReader(supportDir string) func() (hook.ArtifactsDue, bool) {
	return func() (hook.ArtifactsDue, bool) {
		d := artifacts.NewArchive(supportDir, fsx.ReadOnly(fsx.Default()), compat.ArtifactsSpec{}).LoadDue()
		if d == nil {
			return hook.ArtifactsDue{}, false
		}
		return hook.ArtifactsDue{Seq: d.Seq, Profile: string(d.Profile), Epoch: d.Epoch, Count: d.Count}, true
	}
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
	d := hook.Deps{State: st, PluginVersion: pluginVersion, BinaryVersion: versionString(), Mode: cfg.Online.Reconcile,
		ArtifactsOn: cfg.Sync.Artifacts}
	if paths.SupportDir == defaultSupportDir(home) {
		dir := filepath.Join(paths.SupportDir, online.DirName)
		d.Online = online.NewStore(paths.SupportDir, online.CreateOnly(fsx.Default(), dir))
		d.ArtifactsDue = artifactsDueReader(paths.SupportDir)
	}
	plat, err := app.NewDarwin(app.DarwinConfig{Home: home, BundleID: cfg.App.BundleID, OpenFileScan: app.ScanMode(cfg.App.OpenFileScan)})
	if err != nil {
		return d, nil // silent: InsideApp stays nil
	}
	det := runner.Detacher{Procs: plat.Procs, IsApp: locatedIsApp(plat, cfg.App.BundleID)}
	d.InsideApp = det.InsideApp
	return d, nil
}
