package app

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kazupc/paritydesk/internal/clock"
	"github.com/kazupc/paritydesk/internal/pderr"
)

// ErrBundleNotFound: no candidate bundle has the requested identifier.
var ErrBundleNotFound = errors.New("app: bundle not found")

const (
	// maxPlistSize bounds Info.plist reads.
	maxPlistSize = 1 << 20
	// updaterStateFile is Squirrel's staged-update state, kept under
	// ~/Library/Caches/<bundle id>.ShipIt/.
	updaterStateFile = "ShipItState.plist"
)

// plistBundle locates the bundle by reading Contents/Info.plist of the
// candidates <dir>/<name>.app, and watches the updater's state file and
// process.
type plistBundle struct {
	run   Runner
	procs ProcessTable
	clock clock.Clock
	home  string
	dirs  []string // application directories, searched in order
	name  string   // bundle file name without ".app"
}

// candidates returns the bundle paths searched by Locate.
func (b *plistBundle) candidates() []string {
	out := make([]string, 0, len(b.dirs))
	for _, d := range b.dirs {
		out = append(out, filepath.Join(d, b.name+".app"))
	}
	return out
}

// Locate finds the first candidate whose CFBundleIdentifier is bundleID
// and returns its identity tuple.
func (b *plistBundle) Locate(ctx context.Context, bundleID string) (BundleIdentity, error) {
	if err := checkBundleID(bundleID); err != nil {
		return BundleIdentity{}, err
	}
	for _, path := range b.candidates() {
		info, err := b.readInfo(ctx, filepath.Join(path, "Contents", "Info.plist"))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return BundleIdentity{}, err
		}
		if info["CFBundleIdentifier"] != bundleID {
			continue
		}
		return identity(path, bundleID, info)
	}
	return BundleIdentity{}, fmt.Errorf("%w: %s", ErrBundleNotFound, bundleID)
}

// identity builds the tuple from Info.plist values and the executable.
func identity(path, bundleID string, info map[string]string) (BundleIdentity, error) {
	exe := info["CFBundleExecutable"]
	if exe == "" || strings.ContainsRune(exe, '/') || exe == "." || exe == ".." {
		return BundleIdentity{}, fmt.Errorf("app: bundle %s: invalid executable name", path)
	}
	fi, err := os.Stat(filepath.Join(path, "Contents", "MacOS", exe))
	if err != nil {
		return BundleIdentity{}, fmt.Errorf("app: bundle executable: %w", err)
	}
	id := BundleIdentity{
		BundleID:  bundleID,
		Path:      path,
		Version:   info["CFBundleShortVersionString"],
		Build:     info["CFBundleVersion"],
		ExecMtime: fi.ModTime().UTC(),
		ExecSize:  fi.Size(),
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		id.ExecIno = st.Ino
	}
	return id, nil
}

// readInfo reads a plist's top-level string values. XML plists are parsed
// directly; binary plists are converted with plutil first.
func (b *plistBundle) readInfo(ctx context.Context, path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxPlistSize+1))
	f.Close()
	if err != nil {
		return nil, fmt.Errorf("app: read %s: %w", path, err)
	}
	if len(data) > maxPlistSize {
		return nil, fmt.Errorf("app: %s is too large", path)
	}
	if bytes.HasPrefix(data, []byte("bplist")) {
		if data, err = b.run.Run(ctx, TmplPlutil, path); err != nil {
			return nil, fmt.Errorf("app: convert %s: %w", path, err)
		}
	}
	info, err := parsePlistStrings(data)
	if err != nil {
		return nil, fmt.Errorf("app: parse %s: %w", path, err)
	}
	return info, nil
}

// parsePlistStrings returns the string values of an XML plist's top-level
// dictionary. Values of other types are skipped.
func parsePlistStrings(data []byte) (map[string]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	if err := findTopDict(dec); err != nil {
		return nil, err
	}
	out := map[string]string{}
	key := ""
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("plist: %w", err)
		}
		switch t := tok.(type) {
		case xml.EndElement:
			return out, nil // end of the top-level dict
		case xml.StartElement:
			if err := plistEntry(dec, t, &key, out); err != nil {
				return nil, err
			}
		}
	}
}

// findTopDict advances dec past <plist><dict>.
func findTopDict(dec *xml.Decoder) error {
	for {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("plist: no dictionary: %w", err)
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "dict" {
			return nil
		}
	}
}

// plistEntry handles one element inside the top-level dict: a <key>
// records the pending key, a <string> stores its value under it, and
// anything else is skipped.
func plistEntry(dec *xml.Decoder, se xml.StartElement, key *string, out map[string]string) error {
	switch se.Name.Local {
	case "key":
		var k string
		if err := dec.DecodeElement(&k, &se); err != nil {
			return fmt.Errorf("plist: %w", err)
		}
		*key = k
	case "string":
		var v string
		if err := dec.DecodeElement(&v, &se); err != nil {
			return fmt.Errorf("plist: %w", err)
		}
		if *key != "" {
			out[*key] = v
		}
		*key = ""
	default:
		if err := dec.Skip(); err != nil {
			return fmt.Errorf("plist: %w", err)
		}
		*key = ""
	}
	return nil
}

// statePath returns the updater state file for bundleID.
func (b *plistBundle) statePath(bundleID string) string {
	return filepath.Join(b.home, "Library", "Caches", bundleID+"."+updaterName, updaterStateFile)
}

// staged reports whether an update waits to be installed. Squirrel
// leaves its state file behind after a successful install, so the file
// alone is not enough: when it is JSON naming a file:// update bundle
// inside the state directory and that bundle is gone, the update was
// installed. Any other content counts as staged.
func (b *plistBundle) staged(bundleID string) (bool, error) {
	path := b.statePath(bundleID)
	f, err := os.Open(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("app: updater state: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxPlistSize))
	if err != nil {
		return false, fmt.Errorf("app: updater state: %w", err)
	}
	bundle, ok := updateBundlePath(data, filepath.Dir(path))
	if !ok {
		return true, nil
	}
	_, err = os.Lstat(bundle)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("app: updater bundle: %w", err)
	}
}

// updateBundlePath returns the update bundle a JSON updater state names,
// when it is a file:// URL inside dir.
func updateBundlePath(data []byte, dir string) (string, bool) {
	var st struct {
		UpdateBundleURL string `json:"updateBundleURL"`
	}
	if json.Unmarshal(data, &st) != nil || st.UpdateBundleURL == "" {
		return "", false
	}
	u, err := url.Parse(st.UpdateBundleURL)
	if err != nil || u.Scheme != "file" || u.Host != "" || !filepath.IsAbs(u.Path) {
		return "", false
	}
	p := filepath.Clean(u.Path)
	rel, err := filepath.Rel(dir, p)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return p, true
}

// updaterRunning reports whether the bundle's updater process runs.
func (b *plistBundle) updaterRunning(ctx context.Context, bundleID string) (bool, error) {
	procs, err := b.procs.Processes(ctx)
	if err != nil {
		return false, err
	}
	paths := append([]string{""}, b.candidates()...)
	for _, p := range procs {
		for _, path := range paths {
			if isUpdater(p, bundleID, path) {
				return true, nil
			}
		}
	}
	return false, nil
}

// UpdateStaged reports a staged update: the updater state file exists or
// the updater runs.
func (b *plistBundle) UpdateStaged(ctx context.Context, bundleID string) (bool, error) {
	if err := checkBundleID(bundleID); err != nil {
		return false, err
	}
	staged, err := b.staged(bundleID)
	if err != nil || staged {
		return staged, err
	}
	return b.updaterRunning(ctx, bundleID)
}

// WaitUpdateInstalled polls every pollInterval, after the app has quit,
// until no updater runs and no staged state remains, up to timeout. A
// timeout gives *pderr.Error UpdatePending.
func (b *plistBundle) WaitUpdateInstalled(ctx context.Context, bundleID string, timeout time.Duration) error {
	if err := checkBundleID(bundleID); err != nil {
		return err
	}
	start := b.clock.Now()
	for {
		running, err := b.updaterRunning(ctx, bundleID)
		if err != nil {
			return err
		}
		staged, err := b.staged(bundleID)
		if err != nil {
			return err
		}
		if !running && !staged {
			return nil
		}
		if b.clock.Now().Sub(start) >= timeout {
			return pderr.New(pderr.UpdatePending, "app/update", "the staged update did not finish installing in time")
		}
		if err := b.clock.Sleep(ctx, pollInterval); err != nil {
			return err
		}
	}
}
