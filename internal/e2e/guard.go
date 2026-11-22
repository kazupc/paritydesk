package e2e

import (
	"errors"
	"fmt"
	"os/user"
	"path/filepath"
	"strings"
)

// RealBundleID is the desktop app's bundle id. The e2e tests never target
// it.
const RealBundleID = "com.anthropic.claudefordesktop"

// realVendorPrefix covers every bundle id of the app's vendor, so a
// renamed or future bundle is refused too.
const realVendorPrefix = "com.anthropic."

// CheckTarget returns an error when the e2e tests must not run against
// bundleID with home as $HOME: the bundle id is empty or belongs to the
// real app's vendor, home is not an absolute path, the real home is
// unknown, or home is the real home, a parent of it, or a path under the
// real home's Library folder.
func CheckTarget(bundleID, home, realHome string) error {
	switch {
	case bundleID == "":
		return errors.New("e2e: no bundle id")
	case bundleID == RealBundleID || strings.HasPrefix(strings.ToLower(bundleID), realVendorPrefix):
		return fmt.Errorf("e2e: refusing the real app's bundle id %q", bundleID)
	case home == "" || !filepath.IsAbs(home):
		return errors.New("e2e: HOME must be an absolute path")
	case realHome == "" || !filepath.IsAbs(realHome):
		return errors.New("e2e: the real home directory is unknown")
	}
	h := filepath.Clean(home)
	r := filepath.Clean(realHome)
	switch {
	case h == r:
		return errors.New("e2e: HOME is the real user's home; run with a temporary HOME")
	case h == "/" || strings.HasPrefix(r, h+string(filepath.Separator)):
		return errors.New("e2e: HOME is a parent of the real user's home")
	case h == filepath.Join(r, "Library") || strings.HasPrefix(h, filepath.Join(r, "Library")+string(filepath.Separator)):
		return errors.New("e2e: HOME lies under the real user's Library folder")
	}
	return nil
}

// RealHome returns the current user's home directory from the user
// database. It ignores $HOME, so a test run with a temporary HOME still
// sees the real one.
func RealHome() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("e2e: current user: %w", err)
	}
	if u.HomeDir == "" {
		return "", errors.New("e2e: the user database has no home directory")
	}
	return u.HomeDir, nil
}
