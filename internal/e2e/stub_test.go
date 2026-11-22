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
	"strconv"
	"strings"
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

func TestStubApp(t *testing.T) {
	h := newHarness(t)
	t.Run("switch a to b to a", h.testSwitchABA)
	t.Run("recover after a killed run", h.testRecover)
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
	t.Cleanup(func() { h.quitStub(t) })
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
	w := h.newWorld(t, "switch", "60s", stubSettings{})
	h.initWorld(t, w)
	pid := h.launchStub(t)

	h.mustSwitch(t, w, "b", nil)
	h.waitExited(t, pid, 10*time.Second)
	pid = h.waitRelaunched(t, pid)
	h.checkConsistent(t, w, "b")
	// Session 11 exists only in a's folder; the switch carries it to b.
	h.requireSession(t, w, 11)

	h.mustSwitch(t, w, "a", nil)
	h.waitExited(t, pid, 10*time.Second)
	h.waitRelaunched(t, pid)
	h.checkConsistent(t, w, "a")
	// Session 12 exists only in b's folder.
	h.requireSession(t, w, 12)
	h.quitStub(t)
}

func (h *harness) testRecover(t *testing.T) {
	w := h.newWorld(t, "recover", "60s", stubSettings{})
	h.initWorld(t, w)
	// PARITYDESK_FAULT stops the run right after a phase's intent, as if
	// the process had been killed there.
	for _, phase := range []string{"P6", "P7", "P8", "P9", "P10", "P11"} {
		target := other(h.status(t, w).Active)
		h.ensureStub(t)
		r := h.paritydesk(t, w, []string{"PARITYDESK_FAULT=after:" + phase}, []string{"switch"}, switchFlags(target)...)
		t.Logf("fault after %s: switch exited %d", phase, r.code)
		h.recover(t, w)
		h.checkConsistent(t, w, "")
	}
	// A real SIGKILL once the apply phase has started.
	target := other(h.status(t, w).Active)
	h.ensureStub(t)
	killed := h.killSwitchAt(t, w, target, "P7")
	t.Logf("killed during P7: %v", killed)
	if killed && !h.status(t, w).NeedsRecover {
		t.Error("status does not report the killed run")
	}
	h.recover(t, w)
	h.checkConsistent(t, w, "")
	h.quitStub(t)
}

func (h *harness) testVetoWait(t *testing.T) {
	const veto = 6
	w := h.newWorld(t, "veto", "60s", stubSettings{vetoSeconds: veto, vetoMode: "later", session: testkit.FakeSession(1)})
	h.initWorld(t, w)
	pid := h.launchStub(t)
	logStart := fileSize(filepath.Join(h.logDir, "main.log"))
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
	h.checkConsistent(t, w, "b")
	h.quitStub(t)
}

func (h *harness) testVetoTimeout(t *testing.T) {
	// The stub refuses the quit and keeps running far past quit_timeout
	// (5s, the minimum); late_quit_watch is 0.
	w := h.newWorld(t, "veto-timeout", "5s", stubSettings{vetoSeconds: 300, vetoMode: "cancel"})
	h.initWorld(t, w)
	pid := h.launchStub(t)
	before := dataTree(t, w)
	r := h.paritydesk(t, w, nil, []string{"switch"}, switchFlags("b")...)
	switch r.code {
	case 10: // AppDidNotQuit: the expected outcome
	case 15:
		// AutomationMissing: app.Quit maps any osascript failure except
		// -600 to "quit not delivered". If the refused quit makes
		// osascript fail, the user is told Automation is missing. The
		// safety property below still holds; the message is wrong.
		t.Logf("NOTE: a refused quit was reported as missing Automation (exit 15)")
	default:
		t.Fatalf("switch exited %d, want 10 (did not quit)\n%s", r.code, r.output())
	}
	if !alive(pid) {
		t.Fatal("the stub is no longer running; paritydesk must never force a quit")
	}
	if after := dataTree(t, w); !sameTree(before, after) {
		t.Fatal("the data root changed although the quit timed out")
	}
	st := h.status(t, w)
	if st.Active != "a" || st.NeedsRecover {
		t.Fatalf("after the timeout: active %q, needs_recover %v; want a, false", st.Active, st.NeedsRecover)
	}
	h.quitStub(t)
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

// recover runs paritydesk recover. When it asks for the app to be quit
// first (exit 41 while the stub runs), it quits the stub and tries once
// more, as a user would.
func (h *harness) recover(t *testing.T, w *world) {
	t.Helper()
	r := h.paritydesk(t, w, nil, []string{"recover"}, "--yes")
	if r.code == 41 && h.stubRunning() {
		h.quitStub(t)
		r = h.paritydesk(t, w, nil, []string{"recover"}, "--yes")
	}
	if r.code != 0 {
		t.Fatalf("recover exited %d\n%s", r.code, r.output())
	}
}

// killSwitchAt starts a switch with NDJSON progress and kills it with
// SIGKILL as soon as phase starts. It reports whether the kill happened
// before the run ended by itself.
func (h *harness) killSwitchAt(t *testing.T, w *world, target, phase string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	args := append(h.argv(w, []string{"switch"}), append([]string{"--json-progress"}, switchFlags(target)...)...)
	cmd := exec.CommandContext(ctx, h.bin, args...)
	cmd.Env = h.env(nil)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	killed := false
	sc := bufio.NewScanner(stderr)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var ev struct {
			Phase  string `json:"phase"`
			Status string `json:"status"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil || killed {
			continue
		}
		if ev.Phase == phase && ev.Status == "start" {
			killed = cmd.Process.Kill() == nil
		}
	}
	err = cmd.Wait()
	var ee *exec.ExitError
	if killed && !(errors.As(err, &ee) && ee.ProcessState.Sys().(syscall.WaitStatus).Signal() == syscall.SIGKILL) {
		killed = false // the run had ended before the signal
	}
	return killed
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

func other(alias string) string {
	if alias == "a" {
		return "b"
	}
	return "a"
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

// launchStub opens the stub by bundle id, as paritydesk does, and returns
// its pid.
func (h *harness) launchStub(t *testing.T) int {
	t.Helper()
	from := len(h.events(t))
	h.command(t, time.Minute, "/usr/bin/open", "-b", h.bundleID)
	return h.waitLaunch(t, from, 0)
}

// ensureStub launches the stub unless it runs.
func (h *harness) ensureStub(t *testing.T) {
	t.Helper()
	if !h.stubRunning() {
		h.launchStub(t)
	}
}

// waitRelaunched waits for a launch by another process than old.
func (h *harness) waitRelaunched(t *testing.T, old int) int {
	t.Helper()
	return h.waitLaunch(t, 0, old)
}

// waitLaunch waits for a launch event at or after index from whose pid is
// not skip, and for that process to run.
func (h *harness) waitLaunch(t *testing.T, from, skip int) int {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		evs := h.events(t)
		for i := len(evs) - 1; i >= from; i-- {
			if !strings.HasPrefix(evs[i], "launch ") {
				continue
			}
			if pid := eventPID(evs[i]); pid > 0 && pid != skip && alive(pid) {
				return pid
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("the stub did not launch")
	return 0
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

// stubRunning reports whether the pid in the stub's pid file runs.
func (h *harness) stubRunning() bool {
	b, err := os.ReadFile(filepath.Join(h.stateDir, "stubapp.pid"))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	return err == nil && pid > 0 && alive(pid)
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

// quitStub quits the stub when it runs: first by Apple Event with the
// veto cleared, then, if it is still there, with SIGKILL. Only the stub's
// own pid is ever signalled.
func (h *harness) quitStub(t *testing.T) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(h.stateDir, "stubapp.pid"))
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 || !alive(pid) {
		return
	}
	h.clearVeto(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	_, _ = h.osaQuit(ctx)
	cancel()
	for i := 0; i < 100 && alive(pid); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if alive(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		for i := 0; i < 50 && alive(pid); i++ {
			time.Sleep(100 * time.Millisecond)
		}
	}
	_ = os.Remove(filepath.Join(h.stateDir, "stubapp.pid"))
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
