package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/gen"
	"github.com/kazupc/paritydesk/internal/hook"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/render"
)

// This file holds the commands the CLI runs itself, without the
// orchestrator: the hook binary record, generations and backups, config,
// log and history, uninstall and the SessionStart hook.

// HookBinaryFile is the file under the support directory that records
// this binary's absolute path for the plugin's shim.
const HookBinaryFile = "hook-binary"

// recordHookBinary writes <SupportDir>/hook-binary (the resolved absolute
// path of this binary, mode 0600) and returns the path it recorded. In
// read-only mode it writes nothing and returns "".
func (e *env) recordHookBinary() (string, error) {
	if e.readOnly() {
		return "", nil
	}
	paths, _, err := e.paths()
	if err != nil {
		return "", err
	}
	exe, err := e.sys.executable()
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(exe) || filepath.Clean(exe) != exe {
		return "", pderr.New(pderr.Internal, "hook-binary", "executable path is not absolute")
	}
	if err := checkTestPaths(paths.SupportDir); err != nil {
		return "", err
	}
	if err := writeFileAtomic(filepath.Join(paths.SupportDir, HookBinaryFile), []byte(exe+"\n"), 0o600); err != nil {
		return "", err
	}
	return exe, nil
}

// writeFileAtomic replaces path with data (temporary file in the same
// directory, sync, rename). The directory is created 0700 when missing.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Base(dir), err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op after the rename
	_, werr := tmp.Write(data)
	if werr == nil {
		werr = tmp.Chmod(perm)
	}
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), werr)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	return nil
}

// refuseReadOnly refuses a command that writes in read-only mode.
func (e *env) refuseReadOnly(op string) error {
	if e.readOnly() {
		return pderr.New(pderr.Usage, op, i18n.T(i18n.ReadOnlyRefused))
	}
	return nil
}

// generationsView is the --json document of generations list.
type generationsView struct {
	Generations []gen.Info `json:"generations"`
}

func runGenerationsList(ctx context.Context, e *env, _ *flag.FlagSet, _ []string) error {
	gs, err := e.sys.gens(ctx, e)
	if err != nil {
		return err
	}
	list, err := gs.List()
	if err != nil {
		return err
	}
	if list == nil {
		list = []gen.Info{}
	}
	return e.emit(generationsView{Generations: list}, func(p *render.Printer) {
		if len(list) == 0 {
			p.Line("%s", i18n.T(i18n.GenerationsNone))
			return
		}
		rows := make([][]string, len(list))
		for i, g := range list {
			rows[i] = []string{strconv.FormatUint(g.N, 10), g.RunID, string(g.Alias), g.Created.UTC().Format(time.RFC3339),
				strconv.Itoa(g.Entries), yesNo(g.LevelDB), yesNo(g.Complete), humanBytes(g.Bytes)}
		}
		p.Table([]string{i18n.T(i18n.ColN), i18n.T(i18n.ColRun), i18n.T(i18n.ColAlias), i18n.T(i18n.ColCreated),
			i18n.T(i18n.ColEntries), i18n.T(i18n.ColLevelDB), i18n.T(i18n.ColComplete), i18n.T(i18n.ColSize)}, rows)
	}, nil)
}

// generationView is the --json document of generations show.
type generationView struct {
	Generation *gen.Manifest `json:"generation"`
}

func runGenerationsShow(ctx context.Context, e *env, _ *flag.FlagSet, args []string) error {
	n, err := parseGeneration(args)
	if err != nil {
		return err
	}
	gs, err := e.sys.gens(ctx, e)
	if err != nil {
		return err
	}
	m, err := gs.Load(n)
	if err != nil {
		return err
	}
	if m == nil {
		return pderr.New(pderr.Internal, "generations show", "no manifest")
	}
	return e.emit(generationView{Generation: m}, func(p *render.Printer) {
		p.Line(i18n.T(i18n.GenerationShow, m.N, "%s", "%s", "%s", yesNo(m.Complete)),
			m.RunID, string(m.Alias), m.Created.UTC().Format(time.RFC3339))
		rows := make([][]string, len(m.Entries))
		for i, en := range m.Entries {
			item := en.Path
			if item == "" {
				item = en.Key
			}
			rows[i] = []string{string(en.Kind), string(en.Row), item}
		}
		if len(rows) > 0 {
			p.Table([]string{i18n.T(i18n.ColKind), i18n.T(i18n.ColRow), i18n.T(i18n.ColItem)}, rows)
		}
	}, nil)
}

func yesNo(b bool) string {
	if b {
		return i18n.T(i18n.Yes)
	}
	return i18n.T(i18n.No)
}

// humanBytes formats a byte count with binary units.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// backupsView is the --json document of backups prune and purge.
type backupsView struct {
	OK     bool  `json:"ok"`
	Keep   int   `json:"keep,omitempty"`
	Purged bool  `json:"purged,omitempty"`
	Max    int64 `json:"max_bytes,omitempty"`
}

func runBackupsPrune(ctx context.Context, e *env, _ *flag.FlagSet, _ []string) error {
	if err := e.refuseReadOnly("backups prune"); err != nil {
		return err
	}
	release, err := e.sys.quiet(ctx, e)
	if err != nil {
		return err
	}
	defer release()
	paths, _, err := e.paths()
	if err != nil {
		return err
	}
	cfg, _, err := loadConfig(paths.ConfigFile)
	if err != nil {
		return err
	}
	maxBytes, err := config.ParseSize(cfg.Backups.MaxSize)
	if err != nil {
		return pderr.Wrap(pderr.Usage, "config", err)
	}
	gs, err := e.sys.gens(ctx, e)
	if err != nil {
		return err
	}
	pol := gen.PrunePolicy{Keep: cfg.Backups.Keep, LevelDBClones: cfg.Backups.LevelDBClonesKeep, MaxBytes: maxBytes}
	if err := gs.Prune(pol); err != nil {
		return err
	}
	return e.emit(backupsView{OK: true, Keep: pol.Keep, Max: maxBytes}, func(p *render.Printer) {
		p.Line(i18n.T(i18n.BackupsPruned, pol.Keep))
	}, nil)
}

func runBackupsPurge(ctx context.Context, e *env, fs *flag.FlagSet, _ []string) error {
	if err := e.refuseReadOnly("backups purge"); err != nil {
		return err
	}
	release, err := e.sys.quiet(ctx, e)
	if err != nil {
		return err
	}
	defer release()
	if err := e.confirmLocal(ctx, getBool(fs, "yes"), i18n.AskPurge); err != nil {
		return err
	}
	gs, err := e.sys.gens(ctx, e)
	if err != nil {
		return err
	}
	if err := gs.Purge(); err != nil {
		return err
	}
	return e.emit(backupsView{OK: true, Purged: true}, func(p *render.Printer) {
		p.Line("%s", i18n.T(i18n.BackupsPurged))
	}, nil)
}

// configView is the --json document of config path, show and validate.
type configView struct {
	Path   string         `json:"path"`
	Exists bool           `json:"exists"`
	Valid  *bool          `json:"valid,omitempty"`
	Config *config.Config `json:"config,omitempty"`
}

func runConfigPath(_ context.Context, e *env, _ *flag.FlagSet, _ []string) error {
	paths, _, err := e.paths()
	if err != nil {
		return err
	}
	_, statErr := os.Stat(paths.ConfigFile)
	return e.emit(configView{Path: paths.ConfigFile, Exists: statErr == nil}, func(p *render.Printer) {
		p.Line(i18n.T(i18n.ConfigPath, "%s"), paths.ConfigFile)
	}, nil)
}

func runConfigShow(_ context.Context, e *env, _ *flag.FlagSet, _ []string) error {
	paths, _, err := e.paths()
	if err != nil {
		return err
	}
	cfg, exists, err := loadConfig(paths.ConfigFile)
	if err != nil {
		return err
	}
	return e.emit(configView{Path: paths.ConfigFile, Exists: exists, Config: cfg}, func(p *render.Printer) {
		if !exists {
			p.Warn(i18n.T(i18n.ConfigNone))
		}
		var buf bytes.Buffer
		enc := toml.NewEncoder(&buf)
		enc.SetIndentTables(true)
		if err := enc.Encode(cfg); err == nil {
			for _, l := range bytes.Split(bytes.TrimRight(buf.Bytes(), "\n"), []byte("\n")) {
				p.Line("%s", string(l))
			}
		}
	}, nil)
}

func runConfigValidate(_ context.Context, e *env, _ *flag.FlagSet, _ []string) error {
	paths, _, err := e.paths()
	if err != nil {
		return err
	}
	cfg, err := config.Load(paths.ConfigFile)
	if errors.Is(err, config.ErrNoConfig) {
		return pderr.Wrap(pderr.Usage, "config", err)
	}
	if err != nil {
		return err
	}
	reserved, tags, err := e.sys.rules(e)
	if err != nil {
		return err
	}
	if err := cfg.Validate(reserved, tags); err != nil {
		return err
	}
	valid := true
	return e.emit(configView{Path: paths.ConfigFile, Exists: true, Valid: &valid}, func(p *render.Printer) {
		p.Line(i18n.T(i18n.ConfigValid, "%s"), paths.ConfigFile)
	}, nil)
}

// runHookSessionStart is the plugin's SessionStart entry point. It always
// exits 0 and prints at most one fixed line.
func runHookSessionStart(ctx context.Context, e *env, fs *flag.FlagSet, _ []string) (err error) {
	defer func() {
		if recover() != nil {
			err = nil
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, hookTimeout)
	defer cancel()
	d, err := e.sys.hookDeps(ctx, e, getString(fs, "plugin-version"))
	if err != nil {
		return nil
	}
	_ = hook.SessionStart(ctx, e.stdin, e.stdout, d)
	return nil
}

// Budgets of the tool hooks: PreToolUse has its own 2 s deadline inside
// (hook.PreDeadline) and the process returns within 2.5 s; PostToolUse
// gets 3 s.
const (
	preToolTimeout  = 2500 * time.Millisecond
	postToolTimeout = 3 * time.Second
)

// runHookPreToolUse is the plugin's PreToolUse entry point (the guard).
// It writes at most one deny document and exits 0 once it has decided;
// on a failure while armed it denies (hook.PreToolUse). When it cannot
// get as far as deciding (its dependencies fail, a panic outside the
// guard, a bad command line), it exits non-zero without output and the
// shim denies if the armed marker is live. The plugin version is
// ignored: staying silent on skew would fail open.
func runHookPreToolUse(ctx context.Context, e *env, _ *flag.FlagSet, _ []string) (err error) {
	defer func() {
		if recover() != nil {
			err = errFailClosed
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, preToolTimeout)
	defer cancel()
	d, err := e.sys.toolDeps(ctx, e)
	if err != nil {
		return errFailClosed
	}
	_ = hook.PreToolUse(ctx, e.stdin, e.stdout, d)
	return nil
}

// runHookPostToolUse is the plugin's PostToolUse entry point (the
// recorder). It always exits 0 and prints nothing.
func runHookPostToolUse(ctx context.Context, e *env, _ *flag.FlagSet, _ []string) (err error) {
	defer func() {
		if recover() != nil {
			err = nil
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, postToolTimeout)
	defer cancel()
	d, err := e.sys.toolDeps(ctx, e)
	if err != nil {
		return nil
	}
	_ = hook.PostToolUse(ctx, e.stdin, io.Discard, d)
	return nil
}

// checkRemovable guards uninstall's removals: p must be absolute and
// clean, pass fsx.CheckRemovable (never a data folder, the home directory
// or one of its ancestors) and, under go test, fsx.CheckTestPath.
func checkRemovable(p string) error {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return fsx.ErrNotRemovable
	}
	if _, err := fsx.CheckRemovable(filepath.Dir(p), filepath.Base(p)); err != nil {
		return err
	}
	return fsx.CheckTestPath(p)
}
