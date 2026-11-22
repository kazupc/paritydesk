package app

import (
	"bytes"
	"encoding/binary"
	"errors"
	"path/filepath"
	"strings"
)

// errProcArgs reports a KERN_PROCARGS2 buffer too short to hold argc and
// the executable path.
var errProcArgs = errors.New("app: malformed process arguments")

// parseProcArgs parses a KERN_PROCARGS2 buffer: a native-endian int32 argc,
// the NUL-terminated executable path, NUL padding, then argc
// NUL-terminated arguments (argv[0] first) followed by the environment,
// which is ignored. A truncated argument list returns the complete
// arguments found; an unterminated trailing argument is dropped.
func parseProcArgs(buf []byte) (exe string, argv []string, err error) {
	if len(buf) < 4 {
		return "", nil, errProcArgs
	}
	argc := int(int32(binary.NativeEndian.Uint32(buf)))
	rest := buf[4:]
	end := bytes.IndexByte(rest, 0)
	if end <= 0 {
		return "", nil, errProcArgs
	}
	exe = string(rest[:end])
	rest = rest[end:]
	// Skip the padding between the executable path and argv[0].
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	for i := 0; i < argc && len(rest) > 0; i++ {
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			break
		}
		argv = append(argv, string(rest[:end]))
		rest = rest[end+1:]
	}
	return exe, argv, nil
}

// contentsRel returns exe's path relative to bundlePath/Contents, also for
// a translocated copy of the same bundle name
// (.../AppTranslocation/<id>/d/<Name>.app/Contents/...).
func contentsRel(exe, bundlePath string) (string, bool) {
	if exe == "" || bundlePath == "" {
		return "", false
	}
	exe = filepath.Clean(exe)
	bundlePath = filepath.Clean(bundlePath)
	if rel, ok := strings.CutPrefix(exe, bundlePath+"/Contents/"); ok {
		return rel, true
	}
	const marker = "/AppTranslocation/"
	i := strings.Index(exe, marker)
	if i < 0 {
		return "", false
	}
	parts := strings.SplitN(exe[i+len(marker):], "/", 4)
	if len(parts) != 4 || parts[1] != "d" || parts[2] != filepath.Base(bundlePath) {
		return "", false
	}
	return strings.CutPrefix(parts[3], "Contents/")
}

// Executables inside the bundle that are not the app (ADR 0003 finding 3).
const (
	nativeHostRel  = "Helpers/chrome-native-host" // a child of Chrome
	updaterName    = "ShipIt"                     // Squirrel's updater
	crashpadHelper = "chrome_crashpad_handler"
)

// IsAppProcess reports whether p belongs to the app's process set: its
// executable lies inside bundlePath/Contents (or a translocated copy of
// the same bundle), and it is not one of the excluded helpers (Chrome's
// native messaging host, the updater, the crash handler). Arguments such
// as --user-data-dir, in either form, do not change membership.
func IsAppProcess(p Process, bundlePath string) bool {
	rel, ok := contentsRel(p.Exe, bundlePath)
	if !ok || rel == "" {
		return false
	}
	switch base := filepath.Base(rel); {
	case rel == nativeHostRel, base == updaterName, base == crashpadHelper:
		return false
	}
	return true
}

// isUpdater reports whether p is the app's updater: an executable named
// ShipIt inside the bundle, or one whose arguments name the bundle's
// updater job (<bundle id>.ShipIt).
func isUpdater(p Process, bundleID, bundlePath string) bool {
	if filepath.Base(p.Exe) != updaterName {
		return false
	}
	if _, ok := contentsRel(p.Exe, bundlePath); ok {
		return true
	}
	if bundleID == "" {
		return false
	}
	for _, a := range p.Args {
		if strings.Contains(a, bundleID+"."+updaterName) {
			return true
		}
	}
	return false
}

// userDataDir returns the value of --user-data-dir in either form
// ("--user-data-dir=X" or "--user-data-dir X").
func userDataDir(args []string) (string, bool) {
	const flag = "--user-data-dir"
	for i, a := range args {
		if v, ok := strings.CutPrefix(a, flag+"="); ok {
			return v, true
		}
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

// usesDataFolder reports whether p was started with --user-data-dir
// pointing at or into one of folders (an instance of the app installed
// elsewhere, or a launcher).
func usesDataFolder(p Process, folders []string) bool {
	dir, ok := userDataDir(p.Args)
	if !ok || dir == "" {
		return false
	}
	return underAny(filepath.Clean(dir), folders)
}

// underAny reports whether path equals or lies under one of prefixes.
func underAny(path string, prefixes []string) bool {
	for _, pre := range prefixes {
		if pre == "" {
			continue
		}
		pre = filepath.Clean(pre)
		if path == pre || strings.HasPrefix(path, pre+"/") {
			return true
		}
	}
	return false
}
