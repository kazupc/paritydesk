//go:build darwin && !pd_nofdscan

package fdscan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/kazupc/paritydesk/internal/fdscan/fdscantest"
)

// envSelfCheck makes the test binary run only the self-check and print
// its outcome (TestSelfCheckUnderSandbox).
const envSelfCheck = "PARITYDESK_FDSCAN_SELFCHECK"

// TestMain is the package's only TestMain. It runs the holder child
// (fdscantest) or the sandboxed self-check child when asked to, and the
// tests otherwise.
func TestMain(m *testing.M) {
	if dir := os.Getenv(fdscantest.EnvHolder); dir != "" {
		os.Exit(fdscantest.RunHolderChild(dir, os.Getenv(fdscantest.EnvSockDir), os.Stdout, os.Stdin))
	}
	if os.Getenv(envSelfCheck) == "1" {
		_, err := System().SelfCheck(context.Background())
		switch {
		case err == nil:
			fmt.Println("ok")
		case errors.Is(err, ErrBlind):
			fmt.Println("blind")
		case errors.Is(err, ErrLayout):
			fmt.Println("layout")
		default:
			fmt.Println("other")
		}
		os.Exit(0)
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
