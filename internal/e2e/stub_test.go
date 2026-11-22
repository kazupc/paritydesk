package e2e

// End-to-end tests: the real paritydesk binary against the stub app
// (testdata/stubapp) in a temporary HOME. CI only; see doc.go and
// scripts/ci/e2e.sh. Every world (data root, config file and paritydesk
// support directory) lives under that HOME, and the stub is installed
// once at Applications/Claude.app inside it.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/sources/configjson"
	"github.com/kazupc/paritydesk/internal/testkit"
)

// Environment of the e2e job.
const (
	envE2E      = "PARITYDESK_E2E"
	envBundleID = "PARITYDESK_E2E_BUNDLE_ID"
	envBinary   = "PARITYDESK_E2E_BINARY"
)

// lsregister registers the stub with Launch Services, so paritydesk's
// "open -b" finds it.
const lsregister = "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"

// The main.log line the stub writes for a vetoed quit
// (internal/compat/manifests/6.x.toml, signals.quit_vetoed).
const vetoLine = "beforeQuit: vetoed by before-quit interceptor"

// accounts maps the aliases the tests use to the fake accounts of their
// folders: a starts in Claude (testkit replica 1), b in Claude-b
// (replica 2).
var accounts = map[string]ids.AccountID{
	"a": testkit.Replica(1).Account,
	"b": testkit.Replica(2).Account,
}

// harness is the shared setup: one temporary HOME, one installed stub.
type harness struct {
	home     string
	bin      string // paritydesk binary
	bundleID string
	app      string // Claude.app in the Applications folder of HOME
	conf     string // stubapp.conf next to the bundle
	logDir   string // HOME/Library/Logs/Claude (main.log)
	stateDir string // the stub's pid and events files
	// stubData is the data_dir the stub's config names (writeStubConf):
	// the folder every stub launched from now on must report.
	stubData string
}

// stubSettings are the stub's per-world settings.
type stubSettings struct {
	vetoSeconds int
	vetoMode    string // "later" or "cancel"
	session     string
}

// world is one data root with its own paritydesk config and support
// directory.
type world struct {
	name    string
	data    string
	config  string
	support string
	stub    stubSettings
}

// TestStubApp runs the scenarios in order on one installed stub. Every
// subtest starts with no stub process running (begin) and its cleanup
// stops the stub again, so no subtest sees an instance an earlier one
// left behind. That matters because paritydesk relaunches the app with
// open -b and, in recover, rollback and abort, returns without waiting
// for it to finish launching: a stub still starting when a subtest ends
// has no pid file yet, and if it survived, the next subtest's open -b
// would only bring that instance (still on the earlier world's data
// folder) forward, so no launch would ever be recorded.
func TestStubApp(t *testing.T) {
	h := newHarness(t)
	t.Run("switch a to b to a", h.testSwitchABA)
	t.Run("recover after a killed run", h.testRecover)
	t.Run("kill inside P7, then recover", h.testKillInP7)
	t.Run("quit veto wait", h.testVetoWait)
	t.Run("quit veto timeout writes nothing", h.testVetoTimeout)
}

// newHarness checks every guard, registers the stub and proves that the
// stub can be quit by an Apple Event on this machine.
func newHarness(t *testing.T) *harness {
	t.Helper()
	if os.Getenv(envE2E) != "1" {
		t.Skipf("set %s=1 to run the stub-app tests (CI e2e job only)", envE2E)
	}
	bundleID := os.Getenv(envBundleID)
	if bundleID == "" {
		t.Skipf("set %s to the stub app's bundle id", envBundleID)
	}
	if runtime.GOOS != "darwin" {
		t.Skip("the stub-app tests need macOS")
	}
	home := os.Getenv("HOME")
	realHome, err := RealHome()
	if err != nil {
		t.Fatalf("refusing to run: %v", err)
	}
	if err := CheckTarget(bundleID, home, realHome); err != nil {
		t.Fatalf("refusing to run: %v", err)
	}
	home = filepath.Clean(home)
	if r, err := filepath.EvalSymlinks(home); err != nil || r != home {
		t.Fatalf("refusing to run: HOME must exist and hold no symlinks (paritydesk matches process paths literally)")
	}
	bin := os.Getenv(envBinary)
	if !filepath.IsAbs(bin) {
		t.Fatalf("%s must name the paritydesk binary by absolute path", envBinary)
	}
	if fi, err := os.Stat(bin); err != nil || fi.Mode()&0o111 == 0 {
		t.Fatalf("%s is not an executable file", envBinary)
	}
	h := &harness{
		home:     home,
		bin:      bin,
		bundleID: bundleID,
		app:      filepath.Join(home, "Applications", "Claude.app"),
		conf:     filepath.Join(home, "Applications", "stubapp.conf"),
		logDir:   filepath.Join(home, "Library", "Logs", "Claude"),
		stateDir: filepath.Join(home, "stubapp-state"),
	}
	h.checkStubBundle(t)
	for _, d := range []string{h.logDir, h.stateDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(lsregister); err == nil {
		h.command(t, time.Minute, lsregister, "-f", h.app)
		t.Cleanup(func() { _ = exec.Command(lsregister, "-u", h.app).Run() })
	}
	t.Cleanup(func() { h.stopStub(t) })
	h.probeAppleEvents(t)
	return h
}

var (
	plistIDRE  = regexp.MustCompile(`<key>CFBundleIdentifier</key>\s*<string>([^<]*)</string>`)
	plistExeRE = regexp.MustCompile(`<key>CFBundleExecutable</key>\s*<string>([^<]*)</string>`)
)

// checkStubBundle requires the installed bundle to be the stub: its
// bundle id is the one under test and its executable is "stubapp".
func (h *harness) checkStubBundle(t *testing.T) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(h.app, "Contents", "Info.plist"))
	if err != nil {
		t.Fatalf("the stub is not installed as Claude.app in the Applications folder of HOME (scripts/ci/build-stubapp.sh): %v", err)
	}
	id := plistIDRE.FindSubmatch(b)
	exe := plistExeRE.FindSubmatch(b)
	if id == nil || string(id[1]) != h.bundleID || exe == nil || string(exe[1]) != "stubapp" {
		t.Fatalf("refusing to run: Claude.app in the Applications folder of HOME is not the stub app with bundle id %s", h.bundleID)
	}
}

// probeAppleEvents launches the stub and quits it with an Apple Event. A
// runner that does not allow Apple Events cannot run these tests, which
// is a property of the runner, not of paritydesk: the test is skipped.
func (h *harness) probeAppleEvents(t *testing.T) {
	t.Helper()
	data := filepath.Join(h.home, "worlds", "probe", "data")
	h.writeStubConf(t, data, stubSettings{})
	pid := h.launchStub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	out, err := h.osaQuit(ctx)
	if err != nil {
		// scripts/ci/e2e.sh names the sandbox the tests run in; its
		// Apple Events diagnostic tells a runner that blocks Apple Events
		// apart from a sandbox that does.
		sandbox := os.Getenv("PARITYDESK_E2E_SANDBOX")
		if sandbox == "" {
			sandbox = "unknown"
		}
		t.Skipf("Apple Events to the stub are not allowed here (sandbox: %s; %v: %s); the stub-app tests cannot run, see the Apple Events diagnostic of the e2e job", sandbox, err, bytes.TrimSpace(out))
	}
	h.waitExited(t, pid, 30*time.Second)
}

// ---------------------------------------------------------------- scenarios

func (h *harness) testSwitchABA(t *testing.T) {
	h.begin(t)
	w := h.newWorld(t, "switch", "60s", stubSettings{})
	h.initWorld(t, w)
	pid := h.launchStub(t)

	from, logStart := h.eventCount(t), h.mainLogSize()
	h.mustSwitch(t, w, "b", nil)
	h.waitExited(t, pid, 10*time.Second)
	pid = h.waitLaunch(t, from, pid, "the switch to b did not relaunch the stub")
	h.requireAppAccount(t, logStart, "b", "after the switch to b")
	h.checkConsistent(t, w, "b")
	// Session 11 exists only in a's folder; the switch carries it to b.
	h.requireSession(t, w, 11)

	from, logStart = h.eventCount(t), h.mainLogSize()
	h.mustSwitch(t, w, "a", nil)
	h.waitExited(t, pid, 10*time.Second)
	h.waitLaunch(t, from, pid, "the switch to a did not relaunch the stub")
	h.requireAppAccount(t, logStart, "a", "after the switch to a")
	h.checkConsistent(t, w, "a")
	// Session 12 exists only in b's folder.
	h.requireSession(t, w, 12)
}

func (h *harness) testRecover(t *testing.T) {
	h.begin(t)
	w := h.newWorld(t, "recover", "60s", stubSettings{})
	h.initWorld(t, w)
	// Claude (the stub) runs before every fault switch, so P3 really
	// quits it and the run records quit_by_us; recover then closes the
	// run and relaunches it, which the test waits for and checks (it
	// becomes the running stub of the next case). Without a running stub
	// the switches would skip the quit and recover would relaunch
	// nothing, and the test would pass without exercising either. The
	// subtest takes only a few seconds: every switch here stops before
	// P10, so none of them waits in P12 for the relaunched app.
	pid := h.launchStub(t)
	// PARITYDESK_FAULT=after:<phase> stops the run right after that phase's
	// first intent is recorded, as if the process had been killed there.
	// The injected stop is not a paritydesk error with a code of its own,
	// so the switch exits 70 (internal error, "run paritydesk status"),
	// as designed. What proves that the fault, and not another internal
	// error, stopped the run is the journal: the open run's last intent is
	// the phase's own and not done. P10 and P11 have no intents (P10
	// closes the run in one commit), so paritydesk refuses them (exit 2).
	//
	// recover rolls the first three back (Claude still holds a) and
	// finishes the last, so every switch here starts from a with the same
	// non-empty plan and runs P6 and P7. The P8 fault fires right after
	// the swapping intent, before the swap is tried, so before any
	// fallback is chosen. P9 is skipped after the fallback swap (not used
	// on APFS); the fault would then stop the run just before P10 with a
	// done intent, and this test would fail on it.
	cases := []struct {
		phase  string
		intent string // the last intent's expected name
		active string // after recover
	}{
		{"P6", "backed_up", "a"},
		{"P7", "applied", "a"},
		{"P8", "swapping", "a"},
		{"P9", "parked", "b"},
	}
	for _, c := range cases {
		h.requireStub(t, pid, "fault after "+c.phase+": before the switch")
		r := h.paritydesk(t, w, []string{"PARITYDESK_FAULT=after:" + c.phase}, []string{"switch"}, switchFlags("b")...)
		if r.code != 70 {
			t.Fatalf("fault after %s: switch exited %d, want 70\n%s", c.phase, r.code, r.output())
		}
		run := h.journal(t, w).Run
		if run == nil || run.From != "a" || run.To != "b" {
			t.Fatalf("fault after %s: no open switch from a to b in the journal: %+v", c.phase, run)
		}
		if name, done := run.last(); name != c.intent || done {
			t.Fatalf("fault after %s: last intent %q (done %v), want %s, not done", c.phase, name, done, c.intent)
		}
		if !run.QuitByUs {
			t.Fatalf("fault after %s: the run does not record that paritydesk quit Claude", c.phase)
		}
		h.waitExited(t, pid, 10*time.Second)
		h.requireNoStub(t, "fault after "+c.phase+": after the switch quit the stub")
		if !h.status(t, w).NeedsRecover {
			t.Fatalf("fault after %s: status does not report the interrupted run", c.phase)
		}
		from, logStart := h.eventCount(t), h.mainLogSize()
		h.recover(t, w)
		pid = h.waitLaunch(t, from, pid, "fault after "+c.phase+": recover did not relaunch the stub it found quit by paritydesk")
		h.requireAppAccount(t, logStart, c.active, "fault after "+c.phase+": after recover")
		h.checkConsistent(t, w, c.active)
		if run := h.journal(t, w).Run; run != nil {
			t.Fatalf("fault after %s: recover left the run open: %+v", c.phase, run)
		}
		// One line per case, so the job log shows how long each took.
		t.Logf("fault after %s: switch exited 70, recover relaunched the stub as pid %d on %s", c.phase, pid, c.active)
	}
	for _, phase := range []string{"P10", "P11"} {
		r := h.paritydesk(t, w, []string{"PARITYDESK_FAULT=after:" + phase}, []string{"switch"}, switchFlags("a")...)
		if r.code != 2 {
			t.Fatalf("fault after %s: switch exited %d, want 2 (refused: no intent to stop after)\n%s", phase, r.code, r.output())
		}
		// Refused before the run: Claude was not touched.
		h.requireStub(t, pid, "fault after "+phase+": after the refused switch")
	}
	h.checkConsistent(t, w, "b")
}

// testKillInP7 kills paritydesk with SIGKILL inside P7, after the first
// write has landed in the entering folder: the crash window where that
// folder is half written. PARITYDESK_FAULT=pause:P7 (honoured only with
// PARITYDESK_E2E=1 inside the e2e guard) holds the run there, so the kill
// cannot miss. recover must then roll the entering folder back: Claude
// still holds a, so the run is undone, and both folders, Local Storage
// included, read as before the switch.
func (h *harness) testKillInP7(t *testing.T) {
	h.begin(t)
	w := h.newWorld(t, "kill-in-p7", "60s", stubSettings{})
	h.initWorld(t, w)
	pid := h.launchStub(t)
	folders := []string{ids.DefaultFolder, "Claude-b"}
	before := map[string]folderContent{}
	for _, f := range folders {
		before[f] = readFolder(t, filepath.Join(w.data, f))
	}

	p := h.startPausedSwitch(t, w, "b")
	p.waitPaused(t, 3*time.Minute)
	// Paused: the run is open at P7 and the entering folder already holds
	// at least one planned write.
	run := h.journal(t, w).Run
	if run == nil || run.From != "a" || run.To != "b" {
		t.Fatalf("paused: no open switch from a to b in the journal: %+v", run)
	}
	if name, done := run.last(); name != "applied" || done {
		t.Fatalf("paused: last intent %q (done %v), want applied, not done", name, done)
	}
	if !run.QuitByUs {
		t.Fatal("paused: the run does not record that paritydesk quit Claude")
	}
	if sameTree(before["Claude-b"].files, readFiles(t, filepath.Join(w.data, "Claude-b"))) {
		t.Fatal("paused: no write has landed in the entering folder yet")
	}
	// P3 quit Claude before the pause, so the kill lands with the app
	// stopped, as in a real crash inside P7.
	h.waitExited(t, pid, 10*time.Second)
	h.requireNoStub(t, "paused")
	p.kill(t)

	if run := h.journal(t, w).Run; run == nil {
		t.Fatal("killed: the journal has no open run")
	} else if name, done := run.last(); name != "applied" || done {
		t.Fatalf("killed: last intent %q (done %v), want applied, not done", name, done)
	}
	if !h.status(t, w).NeedsRecover {
		t.Fatal("killed: status does not report the interrupted run")
	}
	from, logStart := h.eventCount(t), h.mainLogSize()
	h.recover(t, w)
	// recover relaunches Claude because paritydesk had quit it, and
	// returns without waiting for the launch to finish. Wait for it here,
	// before the folders are read and before the cleanup quits it.
	h.waitLaunch(t, from, pid, "recover did not relaunch the stub it found quit by paritydesk")
	h.requireAppAccount(t, logStart, "a", "after recover")
	h.checkConsistent(t, w, "a")
	if run := h.journal(t, w).Run; run != nil {
		t.Fatalf("recover left the run open: %+v", run)
	}
	for _, f := range folders {
		after := readFolder(t, filepath.Join(w.data, f))
		if d := diffKeys(before[f].files, after.files); len(d) > 0 {
			t.Errorf("%s: files differ from before the switch: %v", f, d)
		}
		if d := diffKeys(before[f].ls, after.ls); len(d) > 0 {
			t.Errorf("%s: Local Storage items differ from before the switch: %v", f, d)
		}
	}
}

func (h *harness) testVetoWait(t *testing.T) {
	const veto = 6
	h.begin(t)
	w := h.newWorld(t, "veto", "60s", stubSettings{vetoSeconds: veto, vetoMode: "later", session: testkit.FakeSession(1)})
	h.initWorld(t, w)
	pid := h.launchStub(t)
	logStart := h.mainLogSize()
	from := h.eventCount(t)
	start := time.Now()
	h.mustSwitch(t, w, "b", nil)
	if elapsed := time.Since(start); elapsed < veto*time.Second {
		t.Errorf("the switch took %s, shorter than the %ds veto", elapsed, veto)
	}
	h.requireEvent(t, fmt.Sprintf("quit-request veto=%d mode=later pid=%d", veto, pid))
	h.requireEvent(t, fmt.Sprintf("veto-ended pid=%d", pid))
	if !strings.Contains(readFrom(t, filepath.Join(h.logDir, "main.log"), logStart), vetoLine) {
		t.Error("main.log has no quit-vetoed line")
	}
	h.waitExited(t, pid, 10*time.Second)
	// P11 relaunches the stub (paritydesk quit it); P12 waited for it.
	h.waitLaunch(t, from, pid, "the switch did not relaunch the stub after the veto")
	h.requireAppAccount(t, logStart, "b", "after the vetoed switch")
	h.checkConsistent(t, w, "b")
}

func (h *harness) testVetoTimeout(t *testing.T) {
	// The stub refuses the quit and keeps running far past quit_timeout
	// (5s, the minimum); late_quit_watch is 0. Its terminateCancel reply
	// should make osascript fail with -128 (userCanceledErr), which is a
	// veto (exit 10), never missing Automation (exit 15, -1743 and -1744
	// only). The runner has not confirmed that number yet; see the exit
	// code check below.
	h.begin(t)
	w := h.newWorld(t, "veto-timeout", "5s", stubSettings{vetoSeconds: 300, vetoMode: "cancel"})
	h.initWorld(t, w)
	pid := h.launchStub(t)
	logStart := h.mainLogSize()
	before := dataTree(t, w)
	r := h.paritydesk(t, w, nil, []string{"switch"}, switchFlags("b")...)
	// The exit code is how paritydesk names the refusal; it depends on
	// the error number osascript reports for the stub's reply, which this
	// job has not yet recorded with the current classification (8e761d4).
	// It is checked with Errorf, so the safety checks below still run and
	// a wrong message is not mistaken for a broken guarantee. The output
	// is logged in full, so the job log shows paritydesk's message for
	// whatever osascript reported.
	if r.code != 10 {
		t.Errorf("switch exited %d, want 10 (the quit was vetoed)\n%s", r.code, r.output())
	}
	h.requireEvent(t, fmt.Sprintf("quit-request veto=300 mode=cancel pid=%d", pid))
	if !strings.Contains(readFrom(t, filepath.Join(h.logDir, "main.log"), logStart), vetoLine) {
		t.Error("main.log has no quit-vetoed line")
	}
	// The same stub still runs: paritydesk never forces a quit, and
	// nothing relaunched it.
	h.requireStub(t, pid, "after the timed-out quit")
	if after := dataTree(t, w); !sameTree(before, after) {
		t.Fatal("the data root changed although the quit timed out")
	}
	st := h.status(t, w)
	if st.Active != "a" || st.NeedsRecover {
		t.Fatalf("after the timeout: active %q, needs_recover %v; want a, false", st.Active, st.NeedsRecover)
	}
}

// ---------------------------------------------------------------- worlds

// newWorld creates a data root with a (Claude) and b (Claude-b), a config
// that points paritydesk at the stub and the data root, and the stub's
// settings for it.
func (h *harness) newWorld(t *testing.T, name, quitTimeout string, stub stubSettings) *world {
	t.Helper()
	dir := filepath.Join(h.home, "worlds", name)
	if _, err := os.Stat(dir); err == nil {
		t.Fatalf("world %s exists already", name)
	}
	w := &world{
		name:    name,
		data:    filepath.Join(dir, "data"),
		config:  filepath.Join(dir, "config.toml"),
		support: filepath.Join(dir, "support"),
		stub:    stub,
	}
	dr := &testkit.DataRoot{Dir: w.data}
	dr.AddFolder(t, testkit.Rich("Claude", 1))
	dr.AddFolder(t, testkit.Rich("Claude-b", 2))
	cfg := fmt.Sprintf("version = 1\n\n[app]\nbundle_id = %q\ndata_root = %q\nquit_timeout = %q\nlate_quit_watch = \"0s\"\n",
		h.bundleID, w.data, quitTimeout)
	if err := os.WriteFile(w.config, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	h.writeStubConf(t, w.data, stub)
	return w
}

// initWorld registers a (the account in Claude) and adopts b. Both are
// unmanaged: there is no terminal to ask.
func (h *harness) initWorld(t *testing.T, w *world) {
	t.Helper()
	h.mustRun(t, w, []string{"init"}, "--unmanaged", "--alias", "a")
	h.mustRun(t, w, []string{"profile", "adopt"}, "--unmanaged", "b", "Claude-b")
	h.checkConsistent(t, w, "a")
}

// switchFlags are the flags of every switch. The e2e job tests app
// control, the folder swap and recovery; the merge rules, the deletion
// breaker and the worktree guard have unit tests, so they are not allowed
// to stop these runs.
func switchFlags(target string) []string {
	return []string{"--yes", "--confirm-first-sync", "--no-reopen", "--accept-worktree-risk", "--allow-deletions", "100", target}
}

func (h *harness) mustSwitch(t *testing.T, w *world, target string, env []string) result {
	t.Helper()
	r := h.paritydesk(t, w, env, []string{"switch"}, switchFlags(target)...)
	if r.code != 0 {
		t.Fatalf("switch to %s exited %d\n%s", target, r.code, r.output())
	}
	return r
}

// recover runs paritydesk recover, which must succeed at once. Every
// caller has checked that the stub is stopped (the interrupted switch
// quit it), so recover has no reason to ask for a quit (exit 41); a
// retry after quitting the stub here would hide a stub that something
// else relaunched.
func (h *harness) recover(t *testing.T, w *world) {
	t.Helper()
	r := h.paritydesk(t, w, nil, []string{"recover"}, "--yes")
	if r.code != 0 {
		t.Fatalf("recover exited %d\n%s", r.code, r.output())
	}
}

// progressEvent is one NDJSON line of --json-progress.
type progressEvent struct {
	Phase  string `json:"phase"`
	Status string `json:"status"`
}

// pausedRun is a switch started with PARITYDESK_FAULT=pause:P7 and
// --json-progress, whose stderr events are read as they come.
type pausedRun struct {
	cmd    *exec.Cmd
	events chan progressEvent // closed at the end of stderr
	seen   []progressEvent
	once   sync.Once
	err    error // cmd.Wait's result, once waited
}

// startPausedSwitch starts the switch. Only this run gets the e2e marker
// and the pause fault. The test's cleanup kills the process if the test
// ends while it still runs, so no paused paritydesk is left behind.
func (h *harness) startPausedSwitch(t *testing.T, w *world, target string) *pausedRun {
	t.Helper()
	args := append(h.argv(w, []string{"switch"}), append([]string{"--json-progress"}, switchFlags(target)...)...)
	cmd := exec.Command(h.bin, args...)
	cmd.Env = h.env([]string{envE2E + "=1", "PARITYDESK_FAULT=pause:P7"})
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &pausedRun{cmd: cmd, events: make(chan progressEvent, 1024)}
	go func() {
		defer close(p.events)
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			var ev progressEvent
			if json.Unmarshal(sc.Bytes(), &ev) == nil && ev.Phase != "" {
				p.events <- ev
			}
		}
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGKILL) // paritydesk's own pid only
		p.wait()
	})
	return p
}

// wait drains stderr (bounded) and reaps the process, once.
func (p *pausedRun) wait() error {
	p.once.Do(func() {
		timeout := time.After(time.Minute)
	drain:
		for {
			select {
			case ev, ok := <-p.events:
				if !ok {
					break drain
				}
				p.seen = append(p.seen, ev)
			case <-timeout:
				break drain
			}
		}
		p.err = p.cmd.Wait()
	})
	return p.err
}

func (p *pausedRun) trace() string {
	s := make([]string, len(p.seen))
	for i, ev := range p.seen {
		s[i] = ev.Phase + "/" + ev.Status
	}
	return strings.Join(s, " ")
}

// waitPaused waits for the pause event. It fails when the run ends, fails
// or goes past P7 first, or when P7 did not start before the pause.
func (p *pausedRun) waitPaused(t *testing.T, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	p7 := false
	for {
		select {
		case ev, ok := <-p.events:
			if !ok {
				t.Fatalf("the switch ended without pausing inside P7 (exit %v); events: %s", p.wait(), p.trace())
			}
			p.seen = append(p.seen, ev)
			switch {
			case ev.Phase == "P7" && ev.Status == "start":
				p7 = true
			case ev.Phase == "P7" && ev.Status == "paused":
				if !p7 {
					t.Fatalf("paused before P7 started; events: %s", p.trace())
				}
				return
			case ev.Status == "fail" || (p7 && ev.Phase != "P7"):
				t.Fatalf("the switch failed or went past P7 without pausing; events: %s", p.trace())
			}
		case <-deadline:
			t.Fatalf("no pause inside P7 within %s; events: %s", timeout, p.trace())
		}
	}
}

// kill sends SIGKILL to the paused paritydesk process (never to the app)
// and requires that the signal, not the run itself, ended it, with no
// progress after the pause.
func (p *pausedRun) kill(t *testing.T) {
	t.Helper()
	n := len(p.seen)
	if err := p.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL to the paused switch: %v", err)
	}
	err := p.wait()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("the paused switch was not killed (wait: %v); events: %s", err, p.trace())
	}
	if ws, ok := ee.ProcessState.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Fatalf("the paused switch ended by itself (%v), not by SIGKILL; events: %s", ee, p.trace())
	}
	if len(p.seen) > n {
		t.Fatalf("progress after the pause: the kill did not land inside P7; events: %s", p.trace())
	}
}

// journal is the part of the state's root.json the tests read.
type journal struct {
	Run *journalRun `json:"run"`
}

type journalRun struct {
	From string `json:"from"`
	To   string `json:"to"`
	// QuitByUs is set when paritydesk quit the app for this run, so an
	// abort or recover relaunches it.
	QuitByUs bool `json:"quit_by_us"`
	Intents  []struct {
		Name string `json:"name"`
		Done bool   `json:"done"`
	} `json:"intents"`
}

// last returns the last intent's name and done flag.
func (r *journalRun) last() (string, bool) {
	if len(r.Intents) == 0 {
		return "", false
	}
	l := r.Intents[len(r.Intents)-1]
	return l.Name, l.Done
}

// journal reads the world's root.json (state/ in the support directory).
func (h *harness) journal(t *testing.T, w *world) journal {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(w.support, "state", "root.json"))
	if err != nil {
		t.Fatal(err)
	}
	var j journal
	if err := json.Unmarshal(b, &j); err != nil {
		t.Fatalf("root.json: %v", err)
	}
	return j
}

// folderContent is what a data folder holds: its regular files (sha256
// by relative path) without the Local Storage database's own files and
// the stub's run log, and its Local Storage items as decoded text.
type folderContent struct {
	files map[string]string
	ls    map[string]string
}

// readFolder reads a folder's content. Reading Local Storage opens a copy
// of the database, so a database that does not open or decode fails the
// test.
func readFolder(t *testing.T, dir string) folderContent {
	t.Helper()
	return folderContent{files: readFiles(t, dir), ls: testkit.ReadLS(t, dir)}
}

func readFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := testkit.TreeOf(t, dir)
	for k := range files {
		if strings.HasPrefix(k, testkit.LevelDBRel+"/") || k == "stubapp-runs.txt" {
			delete(files, k)
		}
	}
	return files
}

// diffKeys lists the keys whose values differ between a and b (names
// only, never content).
func diffKeys(a, b map[string]string) []string {
	var out []string
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			out = append(out, k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// status is the part of `paritydesk status --json` the tests read.
type status struct {
	Active       string `json:"active"`
	NeedsRecover bool   `json:"needs_recover"`
	Profiles     []struct {
		Alias  string `json:"alias"`
		Folder string `json:"folder"`
		Active bool   `json:"active"`
	} `json:"profiles"`
}

func (h *harness) status(t *testing.T, w *world) status {
	t.Helper()
	// status may exit non-zero while a run is open; its document is what
	// counts.
	r := h.paritydesk(t, w, nil, []string{"status"})
	var st status
	if err := json.Unmarshal(r.stdout, &st); err != nil {
		t.Fatalf("status --json exited %d: %v\n%s", r.code, err, r.output())
	}
	return st
}

// checkConsistent asserts I1 and the folder layout: no run is pending,
// the active profile is want (any profile when want is empty), Claude
// holds the active profile's account, every profile's folder holds its
// own account, and the data root holds nothing else but paritydesk's
// temporary directory.
func (h *harness) checkConsistent(t *testing.T, w *world, want string) {
	t.Helper()
	st := h.status(t, w)
	if st.NeedsRecover {
		t.Fatal("status reports a run that needs recover")
	}
	if st.Active == "" || (want != "" && st.Active != want) {
		t.Fatalf("active profile %q, want %q", st.Active, want)
	}
	if len(st.Profiles) != len(accounts) {
		t.Fatalf("%d profiles, want %d", len(st.Profiles), len(accounts))
	}
	expected := map[string]bool{".paritydesk-tmp": true}
	for _, p := range st.Profiles {
		folder := p.Folder
		if p.Alias == st.Active && folder != ids.DefaultFolder {
			t.Fatalf("the active profile %s is in %q, want %s", p.Alias, folder, ids.DefaultFolder)
		}
		id, err := configjson.ReadIdentity(filepath.Join(w.data, folder))
		if err != nil {
			t.Fatalf("profile %s, folder %s: %v", p.Alias, folder, err)
		}
		if id.Account != accounts[p.Alias] {
			t.Fatalf("folder %s holds another account than profile %s", folder, p.Alias)
		}
		expected[folder] = true
	}
	es, err := os.ReadDir(w.data)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		if !expected[e.Name()] {
			t.Fatalf("unexpected entry %q in the data root", e.Name())
		}
	}
}

// requireSession asserts that the active folder holds session n's record.
func (h *harness) requireSession(t *testing.T, w *world, n int) {
	t.Helper()
	name := testkit.FakeSession(n) + ".json"
	found := false
	_ = filepath.WalkDir(filepath.Join(w.data, ids.DefaultFolder, "claude-code-sessions"), func(p string, e fs.DirEntry, err error) error {
		if err == nil && e.Name() == name {
			found = true
		}
		return nil
	})
	if !found {
		t.Fatalf("session %d was not carried to the active folder", n)
	}
}

// dataTree hashes every file of the data root except paritydesk's
// temporary directory.
func dataTree(t *testing.T, w *world) map[string]string {
	t.Helper()
	tree := testkit.TreeOf(t, w.data)
	for k := range tree {
		if strings.HasPrefix(k, ".paritydesk-tmp/") {
			delete(tree, k)
		}
	}
	return tree
}

func sameTree(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------- paritydesk

// result is one paritydesk run.
type result struct {
	code           int
	stdout, stderr []byte
}

func (r result) output() string {
	return "stdout:\n" + string(r.stdout) + "\nstderr:\n" + string(r.stderr)
}

// env is the environment of every child process: the temporary HOME, a
// fixed PATH, and nothing else of the test's own environment except
// TMPDIR.
func (h *harness) env(extra []string) []string {
	e := []string{"HOME=" + h.home, "PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL=C", "NO_COLOR=1"}
	if v := os.Getenv("TMPDIR"); v != "" {
		e = append(e, "TMPDIR="+v)
	}
	return append(e, extra...)
}

// argv puts the world's global flags right after the command path, before
// the command's own flags and arguments.
func (h *harness) argv(w *world, path []string) []string {
	out := append([]string(nil), path...)
	return append(out, "--config", w.config, "--app-support-dir", w.support, "--json")
}

func (h *harness) paritydesk(t *testing.T, w *world, env, path []string, args ...string) result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, h.bin, append(h.argv(w, path), args...)...)
	cmd.Env = h.env(env)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	r := result{stdout: stdout.Bytes(), stderr: stderr.Bytes()}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		r.code = ee.ExitCode()
	default:
		t.Fatalf("paritydesk %s: %v", strings.Join(path, " "), err)
	}
	return r
}

func (h *harness) mustRun(t *testing.T, w *world, path []string, args ...string) result {
	t.Helper()
	r := h.paritydesk(t, w, nil, path, args...)
	if r.code != 0 {
		t.Fatalf("paritydesk %s exited %d\n%s", strings.Join(path, " "), r.code, r.output())
	}
	return r
}

// ---------------------------------------------------------------- stub

// writeStubConf points the stub at data/Claude and sets its veto. The
// file is replaced atomically because the stub reads it at every quit
// request.
func (h *harness) writeStubConf(t *testing.T, data string, s stubSettings) {
	t.Helper()
	mode := s.vetoMode
	if mode == "" {
		mode = "later"
	}
	text := fmt.Sprintf("# written by internal/e2e\ndata_dir = %s\nlog_dir = %s\nstate_dir = %s\nveto_seconds = %d\nveto_mode = %s\nsession = %s\n",
		filepath.Join(data, ids.DefaultFolder), h.logDir, h.stateDir, s.vetoSeconds, mode, s.session)
	h.stubData = filepath.Join(data, ids.DefaultFolder)
	tmp := h.conf + ".tmp"
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, h.conf); err != nil {
		t.Fatal(err)
	}
}

// clearVeto keeps the stub's folders and drops its veto.
func (h *harness) clearVeto(t *testing.T) {
	t.Helper()
	b, err := os.ReadFile(h.conf)
	if err != nil {
		return
	}
	var lines []string
	for _, l := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(l, "veto_seconds") {
			lines = append(lines, l)
		}
	}
	lines = append(lines, "veto_seconds = 0")
	tmp := h.conf + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, h.conf); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) command(t *testing.T, timeout time.Duration, name string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = h.env(nil)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", filepath.Base(name), err, out)
	}
	return out
}

// begin starts a subtest from a known app state: no stub process runs.
// A stub left over from an earlier step is reported, stopped, and fails
// this subtest (requireNoStub), instead of turning the next launch into a
// 60 s "did not launch". The subtest's cleanup stops every stub process
// again, whatever the subtest did and wherever it failed, and checks that
// none is left.
//
// When the subtest failed, the cleanup first logs the stub's events since
// the subtest began and the stub processes that run, so the job log shows
// what the stub did and which instance was left, not only the failed
// assertion.
func (h *harness) begin(t *testing.T) {
	t.Helper()
	from := h.eventCount(t)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("stub events during this subtest: %s", eventTail(h.events(t), from, 40))
			t.Logf("stub processes at the end of this subtest: %s", describeStubs(h.stubPIDs(t), h.events(t)))
		}
		h.stopStub(t)
		if pids := h.stubPIDs(t); len(pids) > 0 {
			t.Errorf("the stub still runs after the subtest's cleanup (pids %v)", pids)
		}
	})
	h.requireNoStub(t, "at the start of the subtest")
}

// launchStub opens the stub by bundle id, as paritydesk does, and returns
// its pid. No stub may run before: open -b would only bring that instance
// forward.
func (h *harness) launchStub(t *testing.T) int {
	t.Helper()
	h.requireNoStub(t, "before launching the stub")
	from := h.eventCount(t)
	h.command(t, time.Minute, "/usr/bin/open", "-b", h.bundleID)
	return h.waitLaunch(t, from, 0, "open -b did not launch the stub")
}

// soleStubSettle is how long waitLaunch lets other stub processes go away
// once the expected launch is seen: an old instance still finishing its
// exit, or a second instance LaunchServices started, is reported only if
// it is still there after this.
const soleStubSettle = 10 * time.Second

// waitLaunch waits for a launch event at or after index from whose pid is
// not skip and runs, then requires that it reports the data folder of the
// current stub config (h.stubData) and that, within soleStubSettle, it is
// the only stub process. On a timeout it fails with what it found
// (launchFailure): in particular a stub that already ran, which open -b
// brought forward instead of launching a new one. what names the expected
// launch.
func (h *harness) waitLaunch(t *testing.T, from, skip int, what string) int {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		evs := h.events(t)
		if l, ok := lastLaunch(evs, from, skip, alive); ok {
			if l.dataDir != h.stubData {
				t.Fatalf("%s: the stub launched (pid %d) on %s, want this world's %s", what, l.pid, l.dataDir, h.stubData)
			}
			h.waitSoleStub(t, l.pid, what)
			return l.pid
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("%s within 60s: %s", what, launchFailure(h.stubPIDs(t), evs, from))
			return 0
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// waitSoleStub waits up to soleStubSettle for pid to be the only stub
// process, and fails naming every stub process if it is not.
func (h *harness) waitSoleStub(t *testing.T, pid int, what string) {
	t.Helper()
	settle := time.Now().Add(soleStubSettle)
	for {
		pids := h.stubPIDs(t)
		if len(pids) == 1 && pids[0] == pid {
			return
		}
		if !time.Now().Before(settle) {
			t.Fatalf("%s: stub processes %s after the launch: %s; want only pid %d", what, soleStubSettle, describeStubs(pids, h.events(t)), pid)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// requireStub requires that pid is the one stub process running and that
// it was launched on the current stub config's data folder.
func (h *harness) requireStub(t *testing.T, pid int, when string) {
	t.Helper()
	if pids := h.stubPIDs(t); len(pids) != 1 || pids[0] != pid {
		t.Fatalf("%s: stub processes %v, want exactly the stub pid %d running", when, pids, pid)
	}
	if d, ok := launchDataDir(h.events(t), pid); !ok || d != h.stubData {
		t.Fatalf("%s: the running stub (pid %d) reports data folder %q, want this world's %s", when, pid, d, h.stubData)
	}
}

// requireNoStub fails when any stub process runs, naming each one with
// the data folder its launch event reports. It stops them first, so one
// failure does not spill into the subtests after it.
func (h *harness) requireNoStub(t *testing.T, when string) {
	t.Helper()
	pids := h.stubPIDs(t)
	if len(pids) == 0 {
		return
	}
	desc := describeStubs(pids, h.events(t))
	h.stopStub(t)
	t.Fatalf("%s: the stub is already running (%s), left over from an earlier step; open -b would only bring that instance forward on its old data folder, so no launch would be recorded. It has been stopped.", when, desc)
}

// requireAppAccount requires that the newest account line written to
// main.log after offset logStart names alias's account. The stub writes
// that line (the real app's "[ScheduledTasks] Initialized" line) at
// launch from the config.json of the folder it opened, before its launch
// event, so after waitLaunch it shows which profile the relaunched app
// found in Claude. The folder paths alone cannot tell: the app always
// opens Claude, whichever profile it holds.
func (h *harness) requireAppAccount(t *testing.T, logStart int64, alias, what string) {
	t.Helper()
	want, ok := accounts[alias]
	if !ok {
		t.Fatalf("no account for alias %q", alias)
	}
	got, ok := lastAccount(readFrom(t, filepath.Join(h.logDir, "main.log"), logStart))
	if !ok {
		t.Fatalf("%s: the relaunched stub logged no account line in main.log", what)
	}
	if got != string(want) {
		t.Fatalf("%s: the relaunched stub opened account %s, want %s's %s", what, got, alias, want)
	}
}

// waitExited waits for pid's exit event.
func (h *harness) waitExited(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	want := fmt.Sprintf("exit pid=%d", pid)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, e := range h.events(t) {
			if e == want && !alive(pid) {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("the stub (pid %d) did not exit", pid)
}

func (h *harness) requireEvent(t *testing.T, want string) {
	t.Helper()
	for _, e := range h.events(t) {
		if e == want {
			return
		}
	}
	t.Fatalf("the stub recorded no %q event", want)
}

// events returns the stub's event lines.
func (h *harness) events(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(h.stateDir, "stubapp-events.txt"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

// eventCount is the index the next event line will have.
func (h *harness) eventCount(t *testing.T) int {
	t.Helper()
	return len(h.events(t))
}

// stubExe is the installed stub's executable.
func (h *harness) stubExe() string {
	return filepath.Join(h.app, "Contents", "MacOS", "stubapp")
}

func (h *harness) pidFile() string { return filepath.Join(h.stateDir, "stubapp.pid") }

// pidFromFile returns the pid in the stub's pid file, or 0. The stub
// writes it in applicationDidFinishLaunching, so a stub that is still
// starting has none yet.
func (h *harness) pidFromFile() int {
	b, err := os.ReadFile(h.pidFile())
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// stubPIDs lists the running processes of the installed stub's
// executable, from ps. The match (parseStubPIDs) is stricter than
// scripts/ci/e2e.sh is_stub_pid, which accepts any command that starts
// with the executable's path: here the command must be that path, alone
// or followed by arguments. Unlike the pid file it also sees a stub that
// is still starting.
func (h *harness) stubPIDs(t *testing.T) []int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/ps", "-axww", "-o", "pid=,command=")
	cmd.Env = h.env(nil)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	var live []int
	for _, pid := range parseStubPIDs(out, h.stubExe()) {
		if alive(pid) {
			live = append(live, pid)
		}
	}
	return live
}

// waitNoStub waits until no stub process runs and reports whether that
// happened in time.
func (h *harness) waitNoStub(t *testing.T, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if len(h.stubPIDs(t)) == 0 {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// alive reports whether pid exists (signal 0 checks without sending).
func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// osaQuit sends the quit Apple Event to the stub's bundle id.
func (h *harness) osaQuit(ctx context.Context) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript",
		"-e", "on run argv", "-e", "tell application id (item 1 of argv) to quit", "-e", "end run", h.bundleID)
	cmd.Env = h.env(nil)
	return cmd.CombinedOutput()
}

// stopStub stops every running stub process and removes the pid file.
// It finds the processes with ps, not the pid file, so it also stops a
// stub that paritydesk relaunched and that is still starting (recover
// returns right after open -b). It waits (bounded) for such a stub to
// write its pid file, so the quit Apple Event finds a running
// application, then quits by Apple Event with the veto cleared. A process
// still there gets SIGTERM, then SIGKILL, each sent only to a pid that ps
// has just shown running the installed stub's executable (as
// scripts/ci/e2e.sh stop_stub does). That executable lives in the
// temporary HOME the guards of newHarness checked, so no other app is
// signalled; as in e2e.sh, a pid that exits and is reused between the ps
// listing and the signal is not excluded. This runs wherever the e2e
// tests run, which includes a maintainer's Mac when the release checklist
// runs scripts/ci/e2e.sh there.
func (h *harness) stopStub(t *testing.T) {
	t.Helper()
	defer func() { _ = os.Remove(h.pidFile()) }()
	pids := h.stubPIDs(t)
	if len(pids) == 0 {
		return
	}
	for i := 0; i < 50 && len(pids) > 0 && !containsPID(pids, h.pidFromFile()); i++ {
		time.Sleep(200 * time.Millisecond)
		pids = h.stubPIDs(t)
	}
	if len(pids) > 0 {
		h.clearVeto(t)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, _ = h.osaQuit(ctx)
		cancel()
		if h.waitNoStub(t, 10*time.Second) {
			return
		}
	}
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		pids = h.stubPIDs(t)
		if len(pids) == 0 {
			return
		}
		t.Logf("the stub (pids %v) did not quit by Apple Event; sending %v", pids, sig)
		for _, pid := range pids {
			_ = syscall.Kill(pid, sig) // a verified stub pid only
		}
		if h.waitNoStub(t, 5*time.Second) {
			return
		}
	}
	t.Errorf("the stub (pids %v) could not be stopped", h.stubPIDs(t))
}

// ---------------------------------------------------------------- stub state (pure)

// launchEvent is one "launch data_dir=<dir> pid=<pid>" line of the stub's
// events file.
type launchEvent struct {
	pid     int
	dataDir string
}

// parseLaunch parses a launch event line.
func parseLaunch(line string) (launchEvent, bool) {
	const prefix = "launch data_dir="
	if !strings.HasPrefix(line, prefix) {
		return launchEvent{}, false
	}
	i := strings.LastIndex(line, " pid=")
	if i < len(prefix) {
		return launchEvent{}, false
	}
	pid := eventPID(line)
	if pid <= 0 {
		return launchEvent{}, false
	}
	return launchEvent{pid: pid, dataDir: line[len(prefix):i]}, true
}

func eventPID(line string) int {
	i := strings.LastIndex(line, " pid=")
	if i < 0 {
		return 0
	}
	n, err := strconv.Atoi(line[i+len(" pid="):])
	if err != nil {
		return 0
	}
	return n
}

// lastLaunch returns the newest launch event at or after index from whose
// pid is not skip and runs.
func lastLaunch(events []string, from, skip int, running func(int) bool) (launchEvent, bool) {
	if from < 0 {
		from = 0
	}
	for i := len(events) - 1; i >= from; i-- {
		if l, ok := parseLaunch(events[i]); ok && l.pid != skip && running(l.pid) {
			return l, true
		}
	}
	return launchEvent{}, false
}

// launchDataDir returns the data folder of pid's newest launch event.
func launchDataDir(events []string, pid int) (string, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if l, ok := parseLaunch(events[i]); ok && l.pid == pid {
			return l.dataDir, true
		}
	}
	return "", false
}

// describeStubs names running stub processes with the data folder each
// one's launch event reports, or says "none".
func describeStubs(pids []int, events []string) string {
	if len(pids) == 0 {
		return "none"
	}
	parts := make([]string, len(pids))
	for i, pid := range pids {
		if d, ok := launchDataDir(events, pid); ok {
			parts[i] = fmt.Sprintf("pid %d on %s", pid, d)
		} else {
			parts[i] = fmt.Sprintf("pid %d, no launch event yet (still starting)", pid)
		}
	}
	return strings.Join(parts, "; ")
}

// eventTail returns the events at or after index from, at most max of
// them (the newest), joined for a log line.
func eventTail(events []string, from, limit int) string {
	if from < 0 {
		from = 0
	}
	if from >= len(events) {
		return "none"
	}
	tail := events[from:]
	skipped := 0
	if len(tail) > limit {
		skipped = len(tail) - limit
		tail = tail[skipped:]
	}
	s := strings.Join(tail, " | ")
	if skipped > 0 {
		s = fmt.Sprintf("(%d earlier omitted) %s", skipped, s)
	}
	return s
}

// launchFailure explains why no launch was seen after event index from,
// given the stub processes that run.
func launchFailure(running []int, events []string, from int) string {
	if len(running) > 0 {
		return fmt.Sprintf("no new launch event, but the stub already runs (%s): open -b brought that instance forward instead of launching one, so a stub was left running by an earlier step", describeStubs(running, events))
	}
	tail := events
	switch {
	case from > len(events):
		tail = nil
	case from > 0:
		tail = events[from:]
	}
	if len(tail) > 5 {
		tail = tail[len(tail)-5:]
	}
	if len(tail) == 0 {
		return fmt.Sprintf("no stub process runs and the stub recorded no event after event %d", from)
	}
	return fmt.Sprintf("no stub process runs and no launch event was recorded after event %d (last events: %s)", from, strings.Join(tail, " | "))
}

// parseStubPIDs returns, sorted, the pids of the lines of `ps -o
// pid=,command=` output whose command is exe, alone or followed by
// arguments.
func parseStubPIDs(ps []byte, exe string) []int {
	var out []int
	for _, line := range strings.Split(string(ps), "\n") {
		line = strings.TrimSpace(line)
		f := strings.IndexAny(line, " \t")
		if f < 0 {
			continue
		}
		pid, err := strconv.Atoi(line[:f])
		if err != nil || pid <= 0 {
			continue
		}
		cmd := strings.TrimLeft(line[f:], " \t")
		if cmd == exe || strings.HasPrefix(cmd, exe+" ") {
			out = append(out, pid)
		}
	}
	sort.Ints(out)
	return out
}

// accountLineRE matches the account line the stub writes to main.log at
// launch (internal/compat/manifests/6.x.toml, signals.active_org).
var accountLineRE = regexp.MustCompile(`\[(?:CCD)?ScheduledTasks\] Initialized \{ accountId: '([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})'`)

// lastAccount returns the account of the newest account line in log.
func lastAccount(log string) (string, bool) {
	m := accountLineRE.FindAllStringSubmatch(log, -1)
	if len(m) == 0 {
		return "", false
	}
	return m[len(m)-1][1], true
}

func containsPID(pids []int, pid int) bool {
	for _, p := range pids {
		if p == pid {
			return true
		}
	}
	return false
}

// mainLogSize is the offset the stub's next main.log line will start at.
func (h *harness) mainLogSize() int64 {
	return fileSize(filepath.Join(h.logDir, "main.log"))
}

func fileSize(p string) int64 {
	fi, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return fi.Size()
}

func readFrom(t *testing.T, p string, off int64) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if off > int64(len(b)) {
		return ""
	}
	return string(b[off:])
}
