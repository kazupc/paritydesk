//go:build unix

package state

import (
	"bufio"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/clock"
	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/fsx/fsxtest"
	"github.com/kazupc/paritydesk/internal/pderr"
)

const (
	helperLockEnv = "PARITYDESK_TEST_LOCK_FILE"
	helperHoldEnv = "PARITYDESK_TEST_LOCK_HOLD"
	helperReady   = "PARITYDESK-LOCK-HELD"
)

// mkdirOps is the minimal fsx.Ops the helper process needs: its lock file
// lives in the parent test's temp tree, outside its own fsxtest root.
type mkdirOps struct{ fsx.Ops }

func (mkdirOps) MkdirAll(p string, perm fs.FileMode) error { return os.MkdirAll(p, perm) }

// TestLockHelperProcess is not a test: it is the lock holder started by
// startHolder. It takes the lock, announces it, holds it for the given
// duration (or until stdin closes) and exits.
func TestLockHelperProcess(t *testing.T) {
	lockFile := os.Getenv(helperLockEnv)
	if lockFile == "" {
		t.Skip("helper process only")
	}
	hold, err := time.ParseDuration(os.Getenv(helperHoldEnv))
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(filepath.Join(filepath.Dir(lockFile), "unused-state"), lockFile, mkdirOps{}, clock.System{})
	if err != nil {
		t.Fatal(err)
	}
	l, err := s.Lock(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout.WriteString(helperReady + "\n")
	done := make(chan struct{})
	go func() {
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
		close(done)
	}()
	select {
	case <-time.After(hold):
	case <-done:
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
}

// startHolder starts a helper process holding lockFile for hold and waits
// until it has the lock.
func startHolder(t *testing.T, lockFile string, hold time.Duration) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockHelperProcess$", "-test.v")
	cmd.Env = append(os.Environ(), helperLockEnv+"="+lockFile, helperHoldEnv+"="+hold.String())
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stdin.Close()
		_ = cmd.Wait()
	})
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		if strings.Contains(sc.Text(), helperReady) {
			go func() {
				for sc.Scan() {
				}
			}()
			return cmd
		}
	}
	t.Fatalf("helper did not take the lock: %v", sc.Err())
	return nil
}

func TestLockBusyAcrossProcesses(t *testing.T) {
	fsys := fsxtest.New(t)
	base := t.TempDir()
	lockFile := LockFile(filepath.Join(base, "support"), "/data")

	t.Run("busy with wait 0", func(t *testing.T) {
		startHolder(t, lockFile, time.Minute)
		s, err := Open(filepath.Join(base, "state"), lockFile, fsys, clock.NewFake(t0))
		if err != nil {
			t.Fatal(err)
		}
		l, err := s.Lock(context.Background(), 0)
		if pderr.CodeOf(err) != pderr.RunLockBusy || l != nil {
			t.Fatalf("Lock = %v, %v; want code 11", l, err)
		}
		fi, err := os.Stat(lockFile)
		if err != nil || fi.Mode().Perm() != filePerm {
			t.Fatalf("lock file mode: %v, %v", fi, err)
		}
		di, err := os.Stat(filepath.Dir(lockFile))
		if err != nil || di.Mode().Perm() != dirPerm {
			t.Fatalf("lock dir mode: %v, %v", di, err)
		}
	})

	t.Run("holder releases within wait", func(t *testing.T) {
		startHolder(t, lockFile, 300*time.Millisecond)
		s, err := Open(filepath.Join(base, "state"), lockFile, fsys, clock.System{})
		if err != nil {
			t.Fatal(err)
		}
		l, err := s.Lock(context.Background(), 10*time.Second)
		if err != nil {
			t.Fatalf("Lock = %v", err)
		}
		if err := l.Release(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestLockRetriesOnClock(t *testing.T) {
	fsys := fsxtest.New(t)
	base := t.TempDir()
	lockFile := LockFile(filepath.Join(base, "support"), "/data")
	clk := clock.NewFake(t0)
	a, _ := Open(filepath.Join(base, "a"), lockFile, fsys, clk)
	held, err := a.Lock(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	b, _ := Open(filepath.Join(base, "b"), lockFile, fsys, clk)
	if _, err := b.Lock(context.Background(), 2*time.Second); pderr.CodeOf(err) != pderr.RunLockBusy {
		t.Fatalf("Lock = %v", err)
	}
	if got := clk.Now().Sub(t0); got < 2*time.Second || got > 2*time.Second+lockRetry {
		t.Fatalf("waited %v on the clock, want about 2s", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.Lock(ctx, time.Second); err == nil || pderr.CodeOf(err) == pderr.RunLockBusy {
		t.Fatalf("cancelled Lock = %v", err)
	}
}

func TestLockKeyedToDataRootNotStateDir(t *testing.T) {
	fsys := fsxtest.New(t)
	base := t.TempDir()
	support := filepath.Join(base, "support")
	clk := clock.NewFake(t0)
	a, _ := Open(filepath.Join(base, "state-a"), LockFile(support, "/data"), fsys, clk)
	b, _ := Open(filepath.Join(base, "state-b"), LockFile(support, "/data/"), fsys, clk)
	other, _ := Open(filepath.Join(base, "state-c"), LockFile(support, "/other"), fsys, clk)

	la, err := a.Lock(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Lock(context.Background(), 0); pderr.CodeOf(err) != pderr.RunLockBusy {
		t.Fatalf("second store on the same data root: %v", err)
	}
	lo, err := other.Lock(context.Background(), 0)
	if err != nil {
		t.Fatalf("other data root blocked: %v", err)
	}
	_ = lo.Release()
	if err := la.Release(); err != nil {
		t.Fatal(err)
	}
	if err := la.Release(); err != nil {
		t.Fatal("second release:", err)
	}
	lb, err := b.Lock(context.Background(), 0)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	_ = lb.Release()
	if _, err := os.Stat(a.Dir()); err == nil {
		t.Fatal("Lock created the state directory")
	}
}
