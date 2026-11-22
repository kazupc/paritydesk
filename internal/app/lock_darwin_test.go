//go:build darwin

package app

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// lockHelperEnv names the file the helper process locks.
const lockHelperEnv = "PARITYDESK_TEST_LOCK_HELPER"

// TestLockHelperProcess is not a test: re-executed by TestLockProbe, it
// takes an fcntl write lock on the named file, reports "locked" and holds
// the lock until its stdin closes.
func TestLockHelperProcess(t *testing.T) {
	path := os.Getenv(lockHelperEnv)
	if path == "" {
		t.Skip("helper process only")
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		fmt.Println("open:", err)
		os.Exit(2)
	}
	lk := unix.Flock_t{Type: unix.F_WRLCK, Whence: io.SeekStart}
	if err := unix.FcntlFlock(f.Fd(), unix.F_SETLK, &lk); err != nil {
		fmt.Println("lock:", err)
		os.Exit(2)
	}
	fmt.Println("locked")
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func TestLockProbe(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "LOCK")
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	probe := fcntlLocks{}
	if held, err := probe.LockHeld(lock); err != nil || held {
		t.Fatalf("before: held=%v err=%v", held, err)
	}
	if held, err := probe.LockHeld(filepath.Join(dir, "missing")); err != nil || held {
		t.Fatalf("missing: held=%v err=%v", held, err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestLockHelperProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), lockHelperEnv+"="+lock)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = cmd.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatalf("helper: %q %v", line, err)
	}
	if held, err := probe.LockHeld(lock); err != nil || !held {
		t.Fatalf("while held: held=%v err=%v", held, err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper exit: %v", err)
	}
	if held, err := probe.LockHeld(lock); err != nil || held {
		t.Fatalf("after release: held=%v err=%v", held, err)
	}

	if err := os.Symlink(lock, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := probe.LockHeld(filepath.Join(dir, "link")); err == nil {
		t.Fatal("followed a symlink")
	}
}
