package cli

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"

	"github.com/kazupc/paritydesk/internal/artifacts"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/online"
	"github.com/kazupc/paritydesk/internal/orchestrator"
	"github.com/kazupc/paritydesk/internal/render"
)

// uninstallView is the --json document of uninstall.
type uninstallView struct {
	Removed  []string `json:"removed"`
	Kept     []string `json:"kept"`
	Move     string   `json:"move,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	// Pinned counts the pinned generations removed with the backups.
	Pinned int `json:"pinned,omitempty"`
}

// runUninstall removes paritydesk's own files. Data folders are never
// touched, and nothing is removed while a run is open or the run lock is
// held elsewhere. When no profile is active it prints the single mv that makes a
// parked profile active again. State is removed unless --keep-state;
// backups are kept unless --keep-backups=false, and so is the artifact
// archive unless --keep-artifacts=false; the config file is kept.
func runUninstall(ctx context.Context, e *env, fs *flag.FlagSet, _ []string) error {
	if err := e.refuseReadOnly("uninstall"); err != nil {
		return err
	}
	paths, home, err := e.paths()
	if err != nil {
		return err
	}
	// The run lock is held until everything is removed, and an open run
	// or a pending profile add refuses: the journal and the generations
	// are what recover needs to finish or undo it.
	release, err := e.sys.quiet(ctx, e)
	if err != nil {
		return err
	}
	defer release()
	pinned := 0
	if !getBool(fs, "keep-backups") {
		if gs, err := e.sys.gens(ctx, e); err == nil {
			pinned = pinnedGenerations(gs)
		}
	}
	e.warnPinned(pinned)
	if err := e.confirmLocal(ctx, getBool(fs, "yes"), i18n.AskUninstall); err != nil {
		return err
	}
	v := uninstallView{Removed: []string{}, Kept: []string{}, Pinned: pinned}
	// Which profile is active decides the mv hint; status is read before
	// the state is removed.
	if svc, err := e.service(ctx, e.newUI(false, false)); err == nil {
		if st, err := svc.Status(ctx, true); err == nil && st != nil {
			v.Move = moveHint(paths, home, st)
		} else {
			v.Warnings = append(v.Warnings, i18n.T(i18n.UninstallNoStatus))
		}
	} else {
		v.Warnings = append(v.Warnings, i18n.T(i18n.UninstallNoStatus))
	}

	// fixed marks the folders paritydesk owns by their exact place under
	// the support directory (online queue, artifact archive): they are
	// removed whatever they hold. Every other folder must pass ownedDir,
	// so a mistyped --state-dir cannot remove a user's own folder.
	type target struct {
		path  string
		keep  bool
		fixed bool
	}
	targets := []target{
		{filepath.Join(paths.SupportDir, HookBinaryFile), false, false},
		{filepath.Join(paths.SupportDir, online.DirName), false, true},
		{paths.RunDir, false, false},
		{filepath.Join(paths.SupportDir, "locks"), false, false},
		{paths.StateDir, getBool(fs, "keep-state"), false},
		{paths.GenDir, getBool(fs, "keep-backups"), false},
		{filepath.Join(paths.SupportDir, artifacts.DirName), getBool(fs, "keep-artifacts"), true},
		{paths.LogDir, getBool(fs, "keep-state"), false},
	}
	for _, t := range targets {
		if _, err := os.Lstat(t.path); err != nil {
			continue
		}
		if t.keep {
			v.Kept = append(v.Kept, t.path)
			continue
		}
		if err := removeOwned(t.path, t.fixed); err != nil {
			return err
		}
		v.Removed = append(v.Removed, t.path)
	}
	if _, err := os.Stat(paths.ConfigFile); err == nil {
		v.Kept = append(v.Kept, paths.ConfigFile)
	}
	// The support directory goes too once it is empty.
	if err := os.Remove(paths.SupportDir); err == nil {
		v.Removed = append(v.Removed, paths.SupportDir)
	}
	return e.emit(v, func(p *render.Printer) {
		if len(v.Removed) == 0 {
			p.Line("%s", i18n.T(i18n.UninstallNothing))
		}
		for _, r := range v.Removed {
			p.Line(i18n.T(i18n.UninstallRemoved, "%s"), r)
		}
		for _, k := range v.Kept {
			p.Line(i18n.T(i18n.UninstallKept, "%s"), k)
		}
		p.Line("%s", i18n.T(i18n.UninstallFolders))
		if v.Move != "" {
			p.Line("%s", i18n.T(i18n.UninstallMoveHint))
			p.Line("  %s", v.Move)
		}
		p.Line("%s", i18n.T(i18n.UninstallPlugin))
		warn(p, v.Warnings)
	}, nil)
}

// moveHint returns the mv command that activates a parked profile when no
// profile is active: the previous profile's folder, else the first one.
func moveHint(paths config.Paths, home string, st *orchestrator.Status) string {
	if st.Active != "" || len(st.Profiles) == 0 {
		return ""
	}
	cfg, _, err := loadConfig(paths.ConfigFile)
	if err != nil {
		cfg = config.Default()
	}
	dataRoot := filepath.Clean(config.ExpandHome(home, cfg.App.DataRoot))
	pick := st.Profiles[0]
	for _, p := range st.Profiles {
		if p.Alias == st.Previous {
			pick = p
		}
	}
	folder := pick.Folder
	if ids.ValidateFolder(folder) != nil {
		folder = ids.DefaultParkedFolder(pick.Alias)
		if ids.ValidateFolder(folder) != nil {
			return ""
		}
	}
	return "mv " + shellQuote(filepath.Join(dataRoot, folder)) + " " + shellQuote(filepath.Join(dataRoot, ids.DefaultFolder))
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// errNotOwned refuses to remove a directory that does not look like one
// of paritydesk's own.
var errNotOwned = errors.New("cli: refusing to remove a directory paritydesk does not own")

// removeOwned removes one of paritydesk's own files or directories after
// checkRemovable. A state directory named by --state-dir must hold
// root.json, or be empty, so a mistyped flag cannot remove anything else.
// fixed is true only for a folder at its exact place under the support
// directory (never one a flag or variable names): it is paritydesk's by
// where it is, not by its name.
func removeOwned(p string, fixed bool) error {
	if err := checkRemovable(p); err != nil {
		return err
	}
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return os.Remove(p)
	}
	if fi.IsDir() && !fixed && !ownedDir(p) {
		return errNotOwned
	}
	return os.RemoveAll(p)
}

// ownedDir reports whether dir looks like a paritydesk directory: one of
// the fixed names, or a directory holding root.json, or an empty one.
func ownedDir(dir string) bool {
	switch filepath.Base(dir) {
	case "run", "locks", "generations", "paritydesk":
		return true
	}
	if _, err := os.Stat(filepath.Join(dir, "root.json")); err == nil {
		return true
	}
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) == 0
}
