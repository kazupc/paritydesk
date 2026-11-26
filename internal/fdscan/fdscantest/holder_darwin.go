//go:build darwin && !pd_nofdscan

package fdscantest

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Environment of the holder child. A package's TestMain runs
// RunHolderChild when EnvHolder is set.
const (
	EnvHolder  = "PARITYDESK_FDSCAN_HOLDER"
	EnvSockDir = "PARITYDESK_FDSCAN_SOCKDIR"
)

// What the child holds, relative to its directory (the socket is in the
// socket directory).
const (
	FileRW    = "rw.txt"     // descriptor, O_RDWR
	FileRO    = "ro.txt"     // descriptor, O_RDONLY
	FileWO    = "wo.txt"     // descriptor, O_WRONLY
	FilePort  = "port.txt"   // fileport of an O_WRONLY descriptor, closed
	FileMap   = "map.bin"    // MAP_SHARED mapping, descriptor closed
	DirThread = "twd"        // a thread's working directory
	Exe       = "bin/holder" // the executable, a copy of the test binary
	SockName  = "s.sock"     // bound Unix socket
)

// sunPathMax is the longest socket path bind accepts (sun_path holds 104
// bytes including the NUL).
const sunPathMax = 103

// mapped keeps the child's mapping referenced for its lifetime.
var mapped []byte

// RunHolderChild is the child side: inside dir it opens rw.txt
// (O_RDWR), ro.txt (O_RDONLY) and wo.txt (O_WRONLY); makes a fileport of
// port.txt (O_WRONLY) and closes the descriptor; maps map.bin (4096
// bytes, PROT_READ, MAP_SHARED) and closes the descriptor; changes its
// working directory to dir; sets one locked thread's working directory to
// dir/twd; binds and listens on a Unix socket at sockDir/s.sock (reported
// as skipped when the sandbox denies it). It then prints
// "ready sock=ok" or "ready sock=skipped" to out and blocks until in
// reaches EOF. It returns the process exit code.
func RunHolderChild(dir, sockDir string, out io.Writer, in io.Reader) int {
	if err := holdFiles(dir); err != nil {
		fmt.Fprintf(out, "error %v\n", err)
		return 1
	}
	sock, err := bindSocket(filepath.Join(sockDir, SockName))
	if err != nil {
		fmt.Fprintf(out, "error %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "ready sock=%s\n", sock)
	_, _ = io.Copy(io.Discard, in)
	return 0
}

// keep holds descriptors open for the child's lifetime.
var keep []*os.File

func holdFiles(dir string) error {
	for _, name := range []string{FileRW, FileRO, FileWO, FilePort} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			return err
		}
	}
	for _, f := range []struct {
		name string
		flag int
	}{{FileRW, os.O_RDWR}, {FileRO, os.O_RDONLY}, {FileWO, os.O_WRONLY}} {
		h, err := os.OpenFile(filepath.Join(dir, f.name), f.flag, 0)
		if err != nil {
			return err
		}
		keep = append(keep, h)
	}
	pf, err := os.OpenFile(filepath.Join(dir, FilePort), os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	if _, err := MakeFileport(int(pf.Fd())); err != nil {
		pf.Close()
		return err
	}
	if err := pf.Close(); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, FileMap), make([]byte, 4096), 0o600); err != nil {
		return err
	}
	mf, err := os.Open(filepath.Join(dir, FileMap))
	if err != nil {
		return err
	}
	m, err := unix.Mmap(int(mf.Fd()), 0, 4096, unix.PROT_READ, unix.MAP_SHARED)
	mf.Close()
	if err != nil {
		return err
	}
	mapped = m
	if err := os.Chdir(dir); err != nil {
		return err
	}
	twd := filepath.Join(dir, DirThread)
	if err := os.MkdirAll(twd, 0o700); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		// The thread stays locked (and alive) for the child's lifetime.
		runtime.LockOSThread()
		done <- unix.PthreadChdir(twd)
		select {}
	}()
	return <-done
}

// bindSocket binds and listens on a Unix socket at path with package
// syscall (no net package). It returns "ok", or "skipped" when the
// sandbox denies local sockets.
func bindSocket(path string) (string, error) {
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if denied(err) {
		return "skipped", nil
	}
	if err != nil {
		return "", err
	}
	if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: path}); err != nil {
		syscall.Close(fd)
		if denied(err) {
			return "skipped", nil
		}
		return "", err
	}
	if err := syscall.Listen(fd, 1); err != nil {
		syscall.Close(fd)
		if denied(err) {
			return "skipped", nil
		}
		return "", err
	}
	return "ok", nil
}

func denied(err error) bool {
	return errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES)
}

// Starter starts the executable at path with args and env, stdin and
// stdout connected to the given files, and returns the child's pid and a
// function that waits for it to exit. Test files implement it with
// os/exec (external commands run through internal/app/exec.go only,
// outside tests).
type Starter func(path string, args, env []string, stdin, stdout *os.File) (pid int, wait func() error, err error)

// StartHolderChild is the parent side: it copies the test binary to
// dir/bin/holder (CopyExecutable) unless it is there already, starts it
// through start with -test.run=^$ and the environment of the holder
// child, and waits up to 10 s for its ready line. sock reports whether
// the socket was bound. The cleanup closes the child's stdin and waits
// for it to exit; no signal is sent.
func StartHolderChild(t testing.TB, dir, sockDir string, start Starter) (pid int, sock bool) {
	t.Helper()
	if l := len(filepath.Join(sockDir, SockName)); l > sunPathMax {
		t.Fatalf("socket path is %d bytes, longer than %d: use a shorter socket directory", l, sunPathMax)
	}
	exe := filepath.Join(dir, Exe)
	if _, err := os.Stat(exe); err != nil {
		exe = CopyExecutable(t, dir)
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), EnvHolder+"="+dir, EnvSockDir+"="+sockDir)
	pid, wait, err := start(exe, []string{"-test.run=^$"}, env, inR, outW)
	inR.Close()
	outW.Close()
	if err != nil {
		inW.Close()
		outR.Close()
		t.Fatalf("start holder child: %v", err)
	}
	t.Cleanup(func() {
		inW.Close()
		done := make(chan error, 1)
		go func() { done <- wait() }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Errorf("holder child %d did not exit after its stdin closed", pid)
		}
		outR.Close()
	})
	lines := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(outR).ReadString('\n')
		lines <- strings.TrimSpace(line)
	}()
	select {
	case line := <-lines:
		switch line {
		case "ready sock=ok":
			return pid, true
		case "ready sock=skipped":
			return pid, false
		}
		t.Fatalf("holder child: %q", line)
	case <-time.After(10 * time.Second):
		t.Fatal("holder child: no ready line within 10s")
	}
	return 0, false
}

// CopyExecutable copies the running test binary (os.Args[0]) to
// dir/bin/holder with mode 0755 and returns its path, so the child maps
// its executable from inside the watched folder, as the Claude Code CLI
// does from the data folder.
func CopyExecutable(t testing.TB, dir string) string {
	t.Helper()
	src, err := os.Executable()
	if err != nil {
		src = os.Args[0]
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, Exe)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		t.Fatal(err)
	}
	return dst
}
