//go:build darwin && !pd_nofdscan

package app

import (
	"os"
	"os/exec"
	"testing"

	"github.com/kazupc/paritydesk/internal/fdscan/fdscantest"
)

// TestMain is the package's only TestMain: it runs the holder child of
// TestNativeMatchesLsof when asked to, and the tests otherwise.
func TestMain(m *testing.M) {
	if dir := os.Getenv(fdscantest.EnvHolder); dir != "" {
		os.Exit(fdscantest.RunHolderChild(dir, os.Getenv(fdscantest.EnvSockDir), os.Stdout, os.Stdin))
	}
	os.Exit(m.Run())
}

// execStart starts a child with os/exec (allowed in test files only).
func execStart(path string, args, env []string, stdin, stdout *os.File) (int, func() error, error) {
	cmd := exec.Command(path, args...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return 0, nil, err
	}
	return cmd.Process.Pid, cmd.Wait, nil
}
