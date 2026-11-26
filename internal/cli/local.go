package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
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
			n := strconv.FormatUint(g.N, 10)
			if g.Pinned {
				// A pinned generation (leftovers moved aside) is never pruned.
				n += " (" + g.Purpose + ")"
			}
			rows[i] = []string{n, g.RunID, string(g.Alias), g.Created.UTC().Format(time.RFC3339),
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
		if m.Pinned {
			p.Line("%s", m.Purpose)
		}
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
	// Pinned counts the generations holding directories profile adopt
	// --from-legacy moved aside (removed by a purge).
	Pinned int `json:"pinned,omitempty"`
}

// pinnedGenerations counts pinned generations (0 when they cannot be
// listed).
func pinnedGenerations(gs generationStore) int {
	list, err := gs.List()
	if err != nil {
		return 0
	}
	n := 0
	for _, g := range list {
		if g.Pinned {
			n++
		}
	}
	return n
}

// warnPinned prints backups.pinned_warn before a question that removes
// generations (text mode; JSON reports the count as "pinned").
func (e *env) warnPinned(n int) {
	if n > 0 && !e.g.JSON {
		e.printer().Warn(i18n.T(i18n.BackupsPinnedWarn, n))
	}
}

// Flags of backups prune.
const (
	flagScratch   = "scratch"
	flagOlderThan = "older-than"
)

func backupsPruneFlags(fs *flag.FlagSet) {
	fs.Bool(flagScratch, false, "remove the kept sets of scratch files removed by earlier switches (asks first)")
	fs.Int(flagOlderThan, 0, "with --scratch: only sets older than this many days")
	yesFlag(fs)
}

func runBackupsPrune(ctx context.Context, e *env, fs *flag.FlagSet, _ []string) error {
	if err := e.refuseReadOnly("backups prune"); err != nil {
		return err
	}
	scratchOnly, days := getBool(fs, flagScratch), getInt(fs, flagOlderThan)
	if days < 0 || (days > 0 && !scratchOnly) {
		return usageErrorf("--%s takes a number of days and needs --%s", flagOlderThan, flagScratch)
	}
	release, err := e.sys.quiet(ctx, e)
	if err != nil {
		return err
	}
	defer release()
	if scratchOnly {
		return pruneScratchKept(ctx, e, fs, days)
	}
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
	gs, err := e.sys.gens(ctx, e)
	if err != nil {
		return err
	}
	pinned := pinnedGenerations(gs)
	e.warnPinned(pinned)
	if err := e.confirmLocal(ctx, getBool(fs, "yes"), i18n.AskPurge); err != nil {
		return err
	}
	if err := gs.Purge(); err != nil {
		return err
	}
	return e.emit(backupsView{OK: true, Purged: true, Pinned: pinned}, func(p *render.Printer) {
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

// scratchKeptStore is the part of gen.Store that keeps quarantined
// scratch files of pruned generations (spec-scratch 9.4).
type scratchKeptStore interface {
	ScratchKept() ([]gen.KeptInfo, error)
	RemoveScratchKept(cutoff time.Time) (int, int64, error)
}

var _ scratchKeptStore = (*gen.Store)(nil)

// scratchPruneView is the --json document of backups prune --scratch.
type scratchPruneView struct {
	Removed int   `json:"scratch_kept_removed"`
	Bytes   int64 `json:"bytes"`
}

// pruneScratchKept removes kept sets of scratch files (all, or those
// older than days) after a confirmation (--yes skips it; declined is
// exit 3).
func pruneScratchKept(ctx context.Context, e *env, fs *flag.FlagSet, days int) error {
	gs, err := e.sys.gens(ctx, e)
	if err != nil {
		return err
	}
	ks, ok := gs.(scratchKeptStore)
	if !ok {
		return pderr.New(pderr.Internal, "backups prune", "the generation store keeps no scratch files")
	}
	var cutoff time.Time
	if days > 0 {
		cutoff = e.sys.now().Add(-time.Duration(days) * 24 * time.Hour)
	}
	sets, err := ks.ScratchKept()
	if err != nil {
		return err
	}
	n, total := 0, int64(0)
	for _, k := range sets {
		if cutoff.IsZero() || k.Created.Before(cutoff) {
			n++
			total += k.Bytes
		}
	}
	if n > 0 {
		if err := e.confirmText(ctx, getBool(fs, "yes"), i18n.T(i18n.BackupsScratchConfirm, n, render.Bytes(total))); err != nil {
			return err
		}
	}
	removed, bytes, err := ks.RemoveScratchKept(cutoff)
	if err != nil {
		return err
	}
	return e.emit(scratchPruneView{Removed: removed, Bytes: bytes}, func(p *render.Printer) {
		p.Line("%s", i18n.T(i18n.BackupsScratchRemoved, removed, render.Bytes(bytes)))
	}, nil)
}

// confirmText asks a question whose text is already formatted, like
// confirmLocal: --yes answers it; a declined question is exit 3.
func (e *env) confirmText(ctx context.Context, yes bool, question string) error {
	if yes {
		return nil
	}
	u := e.newUI(false, false)
	if !u.interactive {
		return errNoTerminal()
	}
	ok, err := u.ask(ctx, question)
	if err != nil {
		return err
	}
	if !ok {
		return pderr.New(pderr.Declined, "confirm", i18n.T(i18n.Declined))
	}
	return nil
}
