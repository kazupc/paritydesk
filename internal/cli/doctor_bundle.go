package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/kazupc/paritydesk/internal/bundle"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/pderr"
)

// The flags of doctor --bundle.
const (
	flagBundle        = "bundle"
	flagBundleLogs    = "bundle-logs"
	defaultBundleLogs = 5
)

// maxOverrideBytes bounds the read of one compat.d file for its hash.
const maxOverrideBytes = 1 << 20

// versionString returns this binary's version: Version, or the module
// version of a go install build.
func versionString() string {
	v := Version
	if bi, ok := debug.ReadBuildInfo(); ok && Version == "0.1.0-dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		v = bi.Main.Version
	}
	return v
}

// checkBundleTarget refuses a support archive path before doctor runs:
// in read-only mode, when the file exists, and inside a Claude data
// folder. It returns the absolute path.
func (e *env) checkBundleTarget(p string, logs int) (string, error) {
	if err := e.refuseReadOnly("doctor --bundle"); err != nil {
		return "", err
	}
	if logs < 0 || logs > bundle.MaxLogs {
		return "", usageError(i18n.UsageOutOfRange, flagBundleLogs, bundle.MaxLogs)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", usageError(i18n.UsageFlagValue, flagBundle, err)
	}
	if _, err := os.Lstat(abs); err == nil {
		return "", pderr.NewT(pderr.Usage, "doctor --bundle", i18n.DoctorBundleExists)
	}
	if err := checkTestPaths(abs); err != nil {
		return "", err
	}
	paths, home, err := e.paths()
	if err != nil {
		return "", err
	}
	cfg, _, err := loadConfig(paths.ConfigFile)
	if err != nil {
		cfg = config.Default()
	}
	dataRoot := filepath.Clean(config.ExpandHome(home, cfg.App.DataRoot))
	if insideDataFolder(dataRoot, abs) || insideDataFolder(resolved(dataRoot), filepath.Join(resolved(filepath.Dir(abs)), filepath.Base(abs))) {
		return "", pderr.NewT(pderr.Usage, "doctor --bundle", i18n.DoctorBundleInData)
	}
	return abs, nil
}

// resolved returns p with symbolic links resolved, or p when that fails
// (for example when p does not exist).
func resolved(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// insideDataFolder reports whether p lies in a Claude or Claude-* folder
// directly under dataRoot. Names compare case-insensitively, as APFS
// compares them, in the data root's own path too.
func insideDataFolder(dataRoot, p string) bool {
	rel, err := filepath.Rel(strings.ToLower(filepath.Clean(dataRoot)), strings.ToLower(filepath.Clean(p)))
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return false
	}
	first := strings.ToLower(strings.SplitN(rel, string(filepath.Separator), 2)[0])
	return first == "claude" || strings.HasPrefix(first, "claude-")
}

// writeBundle gathers paritydesk's own files and writes the support
// archive. It reads only the config file, the compat.d overrides and the
// run logs; doctor's checks come from v. Nothing is read from a Claude
// data folder or from Claude's logs.
func (e *env) writeBundle(abs string, logs int, v doctorView) error {
	paths, home, err := e.paths()
	if err != nil {
		return err
	}
	in := bundle.Input{
		Home:    home,
		Created: e.sys.now(),
		Version: bundle.Version{
			Version: versionString(), Commit: Commit, Date: Date,
			GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH,
		},
		HookBinary: v.HookBinary,
	}
	for _, c := range v.Checks {
		in.Checks = append(in.Checks, bundle.Check{ID: c.ID, Status: c.Status, Detail: c.Detail.English()})
	}
	cfg, exists, err := loadConfig(paths.ConfigFile)
	switch {
	case err != nil:
		in.ConfigExists, in.ConfigProblem = true, true
	case exists:
		in.Config, in.ConfigExists = cfg, true
	}
	in.Manifests, in.Overrides, in.ManifestProblem = gatherManifests(paths.SupportDir)
	if in.Logs, err = gatherLogs(paths.LogDir, logs); err != nil {
		return err
	}
	if err := bundle.Write(abs, in); err != nil {
		if errors.Is(err, bundle.ErrExists) {
			return pderr.NewT(pderr.Usage, "doctor --bundle", i18n.DoctorBundleExists)
		}
		return pderr.Wrap(pderr.Internal, "doctor --bundle", err)
	}
	return nil
}

// gatherManifests returns the manifests in use and the hashes of the
// compat.d override files. problem reports that they could not be loaded.
func gatherManifests(supportDir string) (ms []bundle.Manifest, ovs []bundle.Override, problem bool) {
	if err := checkTestPaths(supportDir); err != nil {
		return nil, nil, true
	}
	dir := filepath.Join(supportDir, "compat.d")
	loaded, err := compat.Load(dir)
	if err != nil {
		problem = true
	}
	for _, m := range loaded {
		ms = append(ms, bundle.Manifest{Name: m.Name, MinVersion: m.MinVersion, MaxVersion: m.MaxVersion,
			Tested: m.Tested, KnownBad: m.KnownBad})
	}
	return ms, overrideHashes(dir), problem
}

// overrideHashes returns the name and sha256 of each regular *.toml file
// in dir.
func overrideHashes(dir string) []bundle.Override {
	entries, err := readDir(dir)
	if err != nil {
		return nil
	}
	var out []bundle.Override
	for _, en := range entries {
		if !en.Type().IsRegular() || !strings.HasSuffix(en.Name(), ".toml") {
			continue
		}
		sum, err := hashFile(filepath.Join(dir, en.Name()))
		if err != nil {
			continue
		}
		out = append(out, bundle.Override{Name: en.Name(), SHA256: sum})
	}
	return out
}

// readDir lists dir; a missing directory has no entries.
func readDir(dir string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return entries, err
}

// hashFile returns the hex sha256 of a regular file (never through a
// symbolic link) of at most maxOverrideBytes.
func hashFile(p string) (string, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxOverrideBytes+1))
	if err != nil {
		return "", err
	}
	if n > maxOverrideBytes {
		return "", errors.New("override file too large")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// gatherLogs returns the n newest run logs.
func gatherLogs(dir string, n int) ([]bundle.Log, error) {
	if n == 0 {
		return nil, nil
	}
	if err := checkTestPaths(dir); err != nil {
		return nil, err
	}
	files, err := listRunLogs(dir)
	if err != nil {
		return nil, nil
	}
	var out []bundle.Log
	for _, f := range files {
		if len(out) == n {
			break
		}
		lines, err := readLogLines(f.Path)
		if err != nil {
			continue
		}
		out = append(out, bundle.Log{ID: f.ID, Lines: lines})
	}
	return out, nil
}
