package app

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/kazupc/paritydesk/internal/clock"
	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/pderr"
)

// defaultAppName is the bundle's file name without ".app".
const defaultAppName = "Claude"

// systemAppDir is the system-wide application directory.
const systemAppDir = "/Applications"

// NewDarwin returns the macOS implementation: processes from sysctl,
// holders from lsof, fcntl lock probes, the bundle from Info.plist, the
// controller through osascript and open, and main.log under
// Home/Library/Logs/Claude, all over cfg.Exec and cfg.Clock. Under
// `go test` it refuses a nil Exec (the real runner), an empty Home and any
// Home or application directory that fsx.CheckTestPath rejects, so no
// test can drive or read the real app.
func NewDarwin(cfg DarwinConfig) (*Platform, error) {
	run, err := runnerFor(cfg.Exec)
	if err != nil {
		return nil, err
	}
	home, err := homeFor(cfg.Home)
	if err != nil {
		return nil, err
	}
	if cfg.BundleID != "" {
		if err := checkBundleID(cfg.BundleID); err != nil {
			return nil, pderr.Wrap(pderr.Internal, "app", err)
		}
	}
	dirs, err := appDirsFor(cfg.AppDirs, home)
	if err != nil {
		return nil, err
	}
	clk := cfg.Clock
	if clk == nil {
		clk = clock.System{}
	}
	name := cfg.AppName
	if name == "" {
		name = defaultAppName
	}
	if name != filepath.Base(name) || name == "." || name == ".." {
		return nil, pderr.New(pderr.Internal, "app", "invalid app name")
	}
	procs := sysctlProcs{}
	logPath := filepath.Join(home, "Library", "Logs", defaultAppName, "main.log")
	if err := fsx.CheckTestPath(logPath); err != nil {
		return nil, pderr.Wrap(pderr.Internal, "app", err)
	}
	return &Platform{
		Procs:   procs,
		Holders: lsofHolders{run: run, procs: procs},
		Locks:   fcntlLocks{},
		Bundle:  &plistBundle{run: run, procs: procs, clock: clk, home: home, dirs: dirs, name: name},
		Control: osaController{run: run},
		Log:     fileLog{path: logPath},
		Git:     gitPorcelain{run: run},
		Clock:   clk,
	}, nil
}

// runnerFor returns exec, or the real runner outside tests.
func runnerFor(exec Runner) (Runner, error) {
	if exec != nil {
		return exec, nil
	}
	if testing.Testing() {
		return nil, pderr.New(pderr.Internal, "app", "the real command runner is not allowed under go test")
	}
	return ExecRunner{}, nil
}

// homeFor returns an absolute, clean home directory and checks it.
func homeFor(home string) (string, error) {
	if home == "" {
		if testing.Testing() {
			return "", pderr.New(pderr.Internal, "app", "a test must pass a temporary home")
		}
		h, err := os.UserHomeDir()
		if err != nil {
			return "", pderr.Wrap(pderr.Internal, "app", fmt.Errorf("home directory: %w", err))
		}
		home = h
	}
	if !filepath.IsAbs(home) {
		return "", pderr.New(pderr.Internal, "app", "home must be an absolute path")
	}
	home = filepath.Clean(home)
	if err := fsx.CheckTestPath(home); err != nil {
		return "", pderr.Wrap(pderr.Internal, "app", err)
	}
	return home, nil
}

// appDirsFor returns the bundle search directories.
func appDirsFor(dirs []string, home string) ([]string, error) {
	if dirs == nil {
		dirs = []string{filepath.Join(home, "Applications")}
		if !testing.Testing() {
			dirs = append([]string{systemAppDir}, dirs...)
		}
	}
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if !filepath.IsAbs(d) {
			return nil, pderr.New(pderr.Internal, "app", "application directories must be absolute")
		}
		d = filepath.Clean(d)
		if err := fsx.CheckTestPath(d); err != nil {
			return nil, pderr.Wrap(pderr.Internal, "app", err)
		}
		out = append(out, d)
	}
	return out, nil
}
