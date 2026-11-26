package e2e

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// envHolder makes the test binary a helper that holds one file open for
// writing (testLeftoverHolder).
const envHolder = "PARITYDESK_E2E_HOLDER"

// TestMain is the package's only TestMain: it runs the holder helper when
// asked to, and the tests otherwise.
func TestMain(m *testing.M) {
	if path := os.Getenv(envHolder); path != "" {
		os.Exit(runHolder(path, os.Stdout, os.Stdin))
	}
	os.Exit(m.Run())
}

// runHolder opens path O_WRONLY|O_CREATE (creating its directory), prints
// "ready" and blocks until in reaches EOF.
func runHolder(path string, out io.Writer, in io.Reader) int {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		fmt.Fprintf(out, "error %v\n", err)
		return 1
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		fmt.Fprintf(out, "error %v\n", err)
		return 1
	}
	defer f.Close()
	fmt.Fprintln(out, "ready")
	_, _ = io.Copy(io.Discard, in)
	return 0
}

// startHolder re-executes the test binary as the holder helper of path and
// waits for it to be ready. The cleanup closes its stdin and waits; no
// signal is sent.
func startHolder(t *testing.T, path string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^$")
	cmd.Env = append(os.Environ(), envHolder+"="+path)
	cmd.Stderr = os.Stderr
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
	t.Cleanup(func() {
		stdin.Close()
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Errorf("holder helper %d did not exit after its stdin closed", cmd.Process.Pid)
		}
	})
	lines := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		lines <- strings.TrimSpace(line)
	}()
	select {
	case line := <-lines:
		if line != "ready" {
			t.Fatalf("holder helper: %q", line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("holder helper: no ready line within 10s")
	}
}

// testLeftoverHolder: a process outside the app keeps a file in the
// active data folder open for writing (as a Claude Code CLI or a VM
// helper that outlived the app would). The release binary's default open
// file scan (auto, native first) must see it: the switch waits out the
// quiescence timeout and stops with nothing written.
func (h *harness) testLeftoverHolder(t *testing.T) {
	h.begin(t)
	w := h.newWorld(t, "leftover-holder", "5s", stubSettings{})
	h.initWorld(t, w)
	h.launchStub(t)
	startHolder(t, filepath.Join(w.data, "Claude", "claude-code", "holder.bin"))
	before := dataTree(t, w)
	r := h.paritydesk(t, w, nil, []string{"switch"}, switchFlags("b")...)
	if r.code != 10 {
		t.Errorf("switch exited %d, want 10 (a file in the data folder is still in use)\n%s", r.code, r.output())
	}
	if !strings.Contains(r.output(), "files in the data folders are still in use") {
		t.Errorf("the output does not name the held files\n%s", r.output())
	}
	if after := dataTree(t, w); !sameTree(before, after) {
		t.Fatal("the data root changed although a file was still held")
	}
	st := h.status(t, w)
	if st.Active != "a" || st.NeedsRecover {
		t.Fatalf("after the stop: active %q, needs_recover %v; want a, false", st.Active, st.NeedsRecover)
	}
}
