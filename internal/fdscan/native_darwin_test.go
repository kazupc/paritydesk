//go:build darwin && !pd_nofdscan

package fdscan

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/fdscan/fdscantest"
)

// fullScanAllowed reports whether the tests that read every same-user
// process may run: on GitHub Actions or when asked for explicitly. CI=true
// alone is not enough (many developer tools set it), so a local go test
// never reads the real app's tables.
func fullScanAllowed() bool {
	return os.Getenv("GITHUB_ACTIONS") == "true" || os.Getenv("PARITYDESK_FDSCAN_FULL") == "1"
}

// sockTempDir returns a short directory for the socket (sun_path holds
// 104 bytes; t.TempDir() paths come close with long test names).
func sockTempDir(t testing.TB) string {
	t.Helper()
	d, err := os.MkdirTemp("", "pd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

// prefixMatcher matches paths at or under the prefixes and their resolved
// forms (the kernel reports /private/var for /var; socket paths are as
// bound).
func prefixMatcher(t testing.TB, prefixes ...string) func(string) bool {
	t.Helper()
	var all []string
	for _, p := range prefixes {
		all = append(all, filepath.Clean(p))
		if r, err := filepath.EvalSymlinks(p); err == nil && r != p {
			all = append(all, r)
		}
	}
	return func(path string) bool {
		for _, p := range all {
			if path == p || strings.HasPrefix(path, p+"/") {
				return true
			}
		}
		return false
	}
}

func resolved(t testing.TB, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// held is a record without pid, fd, identity and start time.
type held struct {
	Kind   Kind
	Access Access
	Path   string
}

// startChild starts the holder child in dir and returns its pid and the
// expected records.
func startChild(t *testing.T, dir, sockDir string) (int, []held) {
	t.Helper()
	pid, sock := fdscantest.StartHolderChild(t, dir, sockDir, execStart)
	rd := resolved(t, dir)
	want := []held{
		{KindFD, AccessReadWrite, filepath.Join(rd, fdscantest.FileRW)},
		{KindFD, AccessRead, filepath.Join(rd, fdscantest.FileRO)},
		{KindFD, AccessWrite, filepath.Join(rd, fdscantest.FileWO)},
		{KindFileport, AccessWrite, filepath.Join(rd, fdscantest.FilePort)},
		{KindCwd, AccessNone, rd},
		{KindThreadCwd, AccessNone, filepath.Join(rd, fdscantest.DirThread)},
		{KindMapped, AccessNone, filepath.Join(rd, fdscantest.Exe)},
		{KindMapped, AccessNone, filepath.Join(rd, fdscantest.FileMap)},
	}
	if sock {
		// The socket's name is the path as given to bind, not resolved.
		want = append(want, held{KindSocket, AccessReadWrite, filepath.Join(sockDir, fdscantest.SockName)})
	} else {
		t.Log("socket skipped: the sandbox denies local sockets")
	}
	slices.SortFunc(want, cmpHeld)
	return pid, want
}

func cmpHeld(a, b held) int {
	if a.Kind != b.Kind {
		return int(a.Kind) - int(b.Kind)
	}
	return strings.Compare(a.Path, b.Path)
}

func helds(rs []Record) []held {
	out := make([]held, len(rs))
	for i, r := range rs {
		out[i] = held{r.Kind, r.Access, r.Path}
	}
	slices.SortFunc(out, cmpHeld)
	return out
}

func TestScanChild(t *testing.T) {
	dir, sockDir := t.TempDir(), sockTempDir(t)
	pid, want := startChild(t, dir, sockDir)
	res, err := System().Scan(t.Context(), Options{PIDs: []int{pid}, Match: prefixMatcher(t, dir, sockDir)})
	if err != nil {
		t.Fatal(err)
	}
	if got := helds(res.Records); !slices.Equal(got, want) {
		t.Fatalf("records\n%+v\nwant\n%+v", got, want)
	}
	ki, st := sysKernel{}.kinfo(pid)
	if st != kOK {
		t.Fatalf("kinfo: %d", st)
	}
	for _, r := range res.Records {
		if r.PID != pid || !r.Started.Equal(ki.Started) {
			t.Fatalf("record %+v: want pid %d started %v", r, pid, ki.Started)
		}
	}
	if res.Stats.Partial != 0 || res.Stats.Unreadable != 0 || res.Stats.Scanned != 1 {
		t.Fatalf("stats %+v", res.Stats)
	}
}

func TestScanChildAfterExit(t *testing.T) {
	dir, sockDir := t.TempDir(), sockTempDir(t)
	var pid int
	t.Run("child", func(t *testing.T) {
		// The subtest's cleanup closes the child's stdin and waits.
		pid, _ = startChild(t, dir, sockDir)
	})
	res, err := System().Scan(t.Context(), Options{PIDs: []int{pid}, Match: prefixMatcher(t, dir, sockDir)})
	if err != nil || len(res.Records) != 0 || res.Stats.Exited != 1 {
		t.Fatalf("after exit: %+v %+v %v", res.Records, res.Stats, err)
	}
}

func TestSelfCheck(t *testing.T) {
	flavor, err := System().SelfCheck(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if flavor != flavorRegionPath2 {
		t.Logf("region flavor %d (the private flavor 22 was not usable)", flavor)
	}
	t.Logf("socket flavor verified: %v (false is expected where the sandbox denies socketpair)", system.socketOK)
}

func TestSelfCheckUnderSandbox(t *testing.T) {
	const sandboxExec = "/usr/bin/sandbox-exec"
	if _, err := os.Stat(sandboxExec); err != nil {
		t.Skip("no sandbox-exec")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range []string{
		"(version 1)(allow default)(deny process-info-pidfdinfo)",
		"(version 1)(allow default)(deny process-info-pidinfo)",
	} {
		t.Run(profile, func(t *testing.T) {
			cmd := exec.Command(sandboxExec, "-p", profile, exe, "-test.run=^$")
			cmd.Env = append(os.Environ(), envSelfCheck+"=1")
			out, err := cmd.CombinedOutput()
			var ee *exec.ExitError
			if errors.As(err, &ee) && ee.ExitCode() == 71 && strings.Contains(string(out), "sandbox_apply: Operation not permitted") {
				t.Skip("already inside a sandbox: sandbox-exec cannot nest")
			}
			if err != nil {
				t.Fatalf("child: %v\n%s", err, out)
			}
			if got := strings.TrimSpace(string(out)); got != "blind" {
				t.Fatalf("self-check under %s: %q, want blind", profile, got)
			}
		})
	}
}

func TestTrampolinesLinked(t *testing.T) {
	if !trampolinesLinked() {
		t.Fatal("a trampoline address is zero: is the .s file of this architecture missing?")
	}
}

func TestOtherUserDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	if _, err := System().SelfCheck(t.Context()); err != nil {
		t.Fatal(err)
	}
	res, err := System().Scan(t.Context(), Options{PIDs: []int{1}, Match: func(string) bool { return true }})
	if err != nil || len(res.Records) != 0 || res.Stats.Denied != 1 || res.Stats.Calls != 0 {
		t.Fatalf("pid 1: %+v %v", res.Stats, err)
	}
}

func TestScanAllFindsChild(t *testing.T) {
	if !fullScanAllowed() {
		t.Skip("reads every same-user process: only on GitHub Actions or with PARITYDESK_FDSCAN_FULL=1")
	}
	dir, sockDir := t.TempDir(), sockTempDir(t)
	pid, want := startChild(t, dir, sockDir)
	res, err := System().Scan(t.Context(), Options{Match: prefixMatcher(t, dir, sockDir)})
	t.Logf("stats: listed %d scanned %d denied %d unreadable %d partial %d exited %d reused %d calls %d elapsed %v",
		res.Stats.Listed, res.Stats.Scanned, res.Stats.Denied, res.Stats.Unreadable, res.Stats.Partial,
		res.Stats.Exited, res.Stats.Reused, res.Stats.Calls, res.Stats.Elapsed)
	if err != nil {
		t.Fatal(err)
	}
	var mine []Record
	for _, r := range res.Records {
		if r.PID == pid {
			mine = append(mine, r)
		} else {
			t.Logf("other holder: pid %d kind %s", r.PID, r.Kind)
		}
	}
	if got := helds(mine); !slices.Equal(got, want) {
		t.Fatalf("child records\n%+v\nwant\n%+v", got, want)
	}
}

func TestTimingAgainstLsof(t *testing.T) {
	if !fullScanAllowed() {
		t.Skip("reads every same-user process: only on GitHub Actions or with PARITYDESK_FDSCAN_FULL=1")
	}
	if _, err := os.Stat("/usr/sbin/lsof"); err != nil {
		t.Skip("no lsof")
	}
	start := time.Now()
	if _, err := System().Scan(t.Context(), Options{Match: func(string) bool { return false }}); err != nil {
		t.Logf("native scan: %v", err)
	}
	native := time.Since(start)
	start = time.Now()
	_ = exec.Command("/usr/sbin/lsof", "-nP", "-w", "-F", "pfan").Run()
	lsof := time.Since(start)
	t.Logf("native %v, lsof %v (not gating)", native, lsof)
}

func BenchmarkScanChild(b *testing.B) {
	dir := b.TempDir()
	sockDir, err := os.MkdirTemp("", "pd")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { os.RemoveAll(sockDir) })
	pid, _ := fdscantest.StartHolderChild(b, dir, sockDir, execStart)
	m := prefixMatcher(b, dir, sockDir)
	for b.Loop() {
		if _, err := System().Scan(b.Context(), Options{PIDs: []int{pid}, Match: m}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkScanAll(b *testing.B) {
	if !fullScanAllowed() {
		b.Skip("reads every same-user process: only on GitHub Actions or with PARITYDESK_FDSCAN_FULL=1")
	}
	for b.Loop() {
		// Not gating: a runner's unreadable processes are logged.
		if res, err := System().Scan(b.Context(), Options{Match: func(string) bool { return false }}); err != nil {
			b.Logf("full scan: %v (unreadable %d, partial %d)", err, res.Stats.Unreadable, res.Stats.Partial)
		}
	}
}
