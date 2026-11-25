package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/clock"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/pderr"
)

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// quitFixture is a platform whose app exits at exitAt (never when zero)
// and whose quit event optionally makes the app log a veto.
type quitFixture struct {
	clk    *clock.Fake
	log    *testLog
	run    *fakeRunner
	p      *Platform
	exitAt time.Time
}

func newQuitFixture(t *testing.T, exitAfter time.Duration, veto bool) *quitFixture {
	f := &quitFixture{clk: clock.NewFake(t0), log: newTestLog(t)}
	if exitAfter > 0 {
		f.exitAt = t0.Add(exitAfter)
	}
	f.log.append(stamp("[quit] refused by interceptor")) // an old veto, before the offset
	f.run = &fakeRunner{t: t, reply: func(tm Template, _ []string) ([]byte, error) {
		if tm == TmplQuit && veto {
			f.log.append(stamp("[quit] refused by interceptor"))
		}
		return nil, nil
	}}
	procs := procFunc(func() []Process {
		others := []Process{
			{PID: 50, Exe: testBundlePath + "/Contents/Helpers/chrome-native-host"},
			{PID: 51, Exe: "/usr/bin/other"},
		}
		if !f.exitAt.IsZero() && !f.clk.Now().Before(f.exitAt) {
			return others
		}
		return append(others, appProc(100), appProc(101))
	})
	f.p = &Platform{Procs: procs, Control: osaController{run: f.run}, Log: f.log.log(), Clock: f.clk}
	return f
}

func TestQuitAndWaitExits(t *testing.T) {
	f := newQuitFixture(t, 1100*time.Millisecond, false)
	vetoes := 0
	res, err := f.p.QuitAndWait(t.Context(), testBundle, testBundlePath, testSig, 10*time.Second, func() { vetoes++ })
	if err != nil {
		t.Fatal(err)
	}
	if !res.Exited || res.Vetoed || res.Pending || vetoes != 0 {
		t.Fatalf("result %+v vetoes %d", res, vetoes)
	}
	if res.Elapsed < time.Second || res.Elapsed > 1500*time.Millisecond {
		t.Fatalf("elapsed %v", res.Elapsed)
	}
	if !slices.Equal(f.run.templates(), []Template{TmplQuit}) {
		t.Fatalf("commands %v", f.run.templates())
	}
}

func TestQuitAndWaitVetoThenExit(t *testing.T) {
	f := newQuitFixture(t, 5*time.Second, true)
	vetoes := 0
	res, err := f.p.QuitAndWait(t.Context(), testBundle, testBundlePath, testSig, 30*time.Second, func() { vetoes++ })
	if err != nil {
		t.Fatal(err)
	}
	if !res.Exited || !res.Vetoed || res.Pending || vetoes != 1 {
		t.Fatalf("result %+v vetoes %d", res, vetoes)
	}
	if !slices.Equal(f.run.templates(), []Template{TmplQuit}) {
		t.Fatalf("commands %v", f.run.templates())
	}
}

func TestQuitAndWaitTimeout(t *testing.T) {
	for _, veto := range []bool{true, false} {
		f := newQuitFixture(t, 0, veto)
		vetoes := 0
		res, err := f.p.QuitAndWait(t.Context(), testBundle, testBundlePath, testSig, 3*time.Second, func() { vetoes++ })
		if pderr.CodeOf(err) != pderr.AppDidNotQuit {
			t.Fatalf("veto=%v: code %d (%v)", veto, pderr.CodeOf(err), err)
		}
		if res.Exited || res.Vetoed != veto || res.Pending != veto || vetoes != map[bool]int{true: 1}[veto] {
			t.Fatalf("veto=%v: result %+v vetoes %d", veto, res, vetoes)
		}
		if res.Elapsed < 3*time.Second || res.Elapsed > 3250*time.Millisecond {
			t.Fatalf("elapsed %v", res.Elapsed)
		}
		if !slices.Equal(f.run.templates(), []Template{TmplQuit}) {
			t.Fatalf("veto=%v: commands after the quit event: %v", veto, f.run.templates())
		}
	}
}

func TestQuitAndWaitNotDelivered(t *testing.T) {
	f := newQuitFixture(t, 0, false)
	f.run.reply = func(Template, []string) ([]byte, error) {
		return nil, &CommandError{Template: TmplQuit, ExitCode: 1, OSAStatus: -1743}
	}
	_, err := f.p.QuitAndWait(t.Context(), testBundle, testBundlePath, testSig, time.Minute, nil)
	if !errors.Is(err, ErrAutomationDenied) || !f.clk.Now().Equal(t0) {
		t.Fatalf("got %v after %v", err, f.clk.Now().Sub(t0))
	}
}

func TestWaitExitedSeesLateExit(t *testing.T) {
	f := newQuitFixture(t, 7*time.Second, false)
	exited, err := f.p.WaitExited(t.Context(), testBundlePath, 10*time.Second)
	if err != nil || !exited {
		t.Fatalf("exited=%v err=%v", exited, err)
	}
	if got := f.clk.Now().Sub(t0); got < 7*time.Second || got > 7250*time.Millisecond {
		t.Fatalf("waited %v", got)
	}
	g := newQuitFixture(t, 0, false)
	exited, err = g.p.WaitExited(t.Context(), testBundlePath, 2*time.Second)
	if err != nil || exited {
		t.Fatalf("still running: exited=%v err=%v", exited, err)
	}
	if len(f.run.calls)+len(g.run.calls) != 0 {
		t.Fatal("WaitExited sent a command")
	}
}

func TestWaitTurnEnded(t *testing.T) {
	ctx := t.Context()
	l := newTestLog(t)
	clk := clock.NewFake(t0)
	p := &Platform{Log: l.log(), Clock: clk}
	// A turn end before from does not count.
	l.append(stamp("[turn] done id="+sessA), stamp("[turn] begin id="+sessA))
	from, _ := l.log().Size()
	// The turn ended between Submit and P3: after from, before the call.
	l.append(stamp("[turn] done id="+sessB), stamp("[turn] done id="+sessA))
	if err := p.WaitTurnEnded(ctx, from, testSig, sessA, time.Minute); err != nil {
		t.Fatal(err)
	}
	if got := clk.Now().Sub(t0); got != turnSettle {
		t.Fatalf("waited %v, want only the settle delay", got)
	}

	clk2 := clock.NewFake(t0)
	p2 := &Platform{Log: l.log(), Clock: clk2}
	from, _ = l.log().Size()
	l.append(stamp("[turn] done id=" + sessB))
	err := p2.WaitTurnEnded(ctx, from, testSig, sessA, 5*time.Second)
	if pderr.CodeOf(err) != pderr.SessionsBusy {
		t.Fatalf("timeout: %v", err)
	}
	if got := clk2.Now().Sub(t0); got < 5*time.Second || got > 5250*time.Millisecond {
		t.Fatalf("waited %v", got)
	}
	if err := p2.WaitTurnEnded(ctx, 0, compat.Signals{}, sessA, time.Second); !errors.Is(err, ErrSignalUnknown) {
		t.Fatalf("got %v", err)
	}
	if err := p2.WaitTurnEnded(ctx, 0, testSig, "bad", time.Second); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("got %v", err)
	}
}

func TestWaitTurnEndedLaterLine(t *testing.T) {
	l := newTestLog(t)
	clk := clock.NewFake(t0)
	// The line appears after a few polls.
	appendAt := t0.Add(time.Second)
	appended := false
	p := &Platform{Log: hookLog{fileLog: l.log(), before: func() {
		if !appended && !clk.Now().Before(appendAt) {
			l.append(stamp("[turn] done id=" + sessA))
			appended = true
		}
	}}, Clock: clk}
	if err := p.WaitTurnEnded(t.Context(), 0, testSig, sessA, time.Minute); err != nil {
		t.Fatal(err)
	}
	if got := clk.Now().Sub(t0); got < time.Second+turnSettle || got > time.Second+turnSettle+pollInterval {
		t.Fatalf("waited %v", got)
	}
}

// hookLog runs before ahead of every Events call.
type hookLog struct {
	fileLog
	before func()
}

func (h hookLog) Events(ctx context.Context, from int64, sig compat.Signals, s ids.SessionID) ([]LogEvent, int64, error) {
	h.before()
	return h.fileLog.Events(ctx, from, sig, s)
}

func TestCheckQuiescence(t *testing.T) {
	after := t0
	late := t0.Add(time.Second)
	procs := procFunc(func() []Process {
		return []Process{
			{PID: 50, Exe: testBundlePath + "/Contents/Helpers/chrome-native-host", Started: late},
			{PID: 60, Exe: "/usr/libexec/mdworker", Started: late},
			{PID: 70, Exe: "/usr/bin/editor", Started: late},
			{PID: 80, Exe: "/usr/local/bin/launcher-copy", Args: []string{"--user-data-dir", "/data/Claude"}, Started: time.Unix(1, 0)},
		}
	})
	p := &Platform{
		Procs: procs,
		Locks: fakeLocks{"/data/Claude/Local Storage/leveldb/LOCK": true},
		Holders: fakeHolders{
			{PID: 60, Command: "mdworker", Path: "/data/Claude/a", Access: "r"},
			{PID: 70, Command: "editor", Path: "/data/Claude/b", Access: "w"},
			{PID: os.Getpid(), Path: "/data/Claude/c", Access: "w"},
		},
	}
	in := QuiescenceInput{
		BundlePath: testBundlePath, BundleID: testBundle, DataRoot: "/data",
		Folders:   []string{"/data/Claude"},
		LockFiles: []string{"/data/Claude/Local Storage/leveldb/LOCK", "/data/Claude-a/Local Storage/leveldb/LOCK"},
		After:     after,
	}
	rep, err := p.CheckQuiescence(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Quiet || !slices.Equal(rep.AppPIDs, []int{80}) || rep.UpdaterRunning {
		t.Fatalf("report %+v", rep)
	}
	if !slices.Equal(rep.LocksHeld, []string{"/data/Claude/Local Storage/leveldb/LOCK"}) {
		t.Fatalf("locks %v", rep.LocksHeld)
	}
	if len(rep.WriteHolders) != 1 || rep.WriteHolders[0].PID != 70 || len(rep.ReadOnlyHolders) != 1 || rep.ReadOnlyHolders[0].PID != 60 {
		t.Fatalf("holders %+v / %+v", rep.WriteHolders, rep.ReadOnlyHolders)
	}
	if len(rep.StartedAfter) != 1 || rep.StartedAfter[0].PID != 70 {
		t.Fatalf("started after %+v", rep.StartedAfter)
	}

	quiet := &Platform{
		Procs: procFunc(func() []Process {
			return []Process{{PID: 50, Exe: testBundlePath + "/Contents/Helpers/chrome-native-host"}}
		}),
		Locks:   fakeLocks{},
		Holders: fakeHolders{{PID: 60, Path: "/data/Claude/a", Access: "r"}},
	}
	rep, err = quiet.CheckQuiescence(t.Context(), in)
	if err != nil || !rep.Quiet || len(rep.ReadOnlyHolders) != 1 {
		t.Fatalf("quiet: %+v %v", rep, err)
	}
	updating := &Platform{
		Procs: procFunc(func() []Process {
			return []Process{{PID: 90, Exe: testBundlePath + "/Contents/Frameworks/Squirrel.framework/Resources/ShipIt"}}
		}),
		Locks: fakeLocks{}, Holders: fakeHolders{},
	}
	rep, err = updating.CheckQuiescence(t.Context(), in)
	if err != nil || rep.Quiet || !rep.UpdaterRunning || len(rep.AppPIDs) != 0 {
		t.Fatalf("updater: %+v %v", rep, err)
	}
}

// writeInfoPlist creates <dir>/Example.app with an XML Info.plist.
func writeBundle(t *testing.T, dir, bundleID string, binary bool) string {
	t.Helper()
	app := filepath.Join(dir, "Example.app")
	if err := os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "MacOS", "Example"), []byte("#!/bin/false\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte(infoPlist(bundleID))
	if binary {
		data = []byte("bplist00\x00\x01synthetic")
	}
	if err := os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return app
}

func infoPlist(bundleID string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleDocumentTypes</key>
	<array><dict><key>CFBundleTypeName</key><string>nested</string></dict></array>
	<key>CFBundleExecutable</key>
	<string>Example</string>
	<key>CFBundleIdentifier</key>
	<string>` + bundleID + `</string>
	<key>LSRequiresNativeExecution</key>
	<true/>
	<key>CFBundleShortVersionString</key>
	<string>1.2.3</string>
	<key>CFBundleVersion</key>
	<string>1.2.3.45</string>
</dict>
</plist>
`
}

func TestLocate(t *testing.T) {
	sys, user := t.TempDir(), t.TempDir()
	writeBundle(t, sys, "com.example.other", false)
	path := writeBundle(t, user, testBundle, true)
	run := &fakeRunner{t: t, reply: func(tm Template, args []string) ([]byte, error) {
		if tm != TmplPlutil {
			t.Errorf("unexpected %s", tm)
		}
		return []byte(infoPlist(testBundle)), nil
	}}
	b := &plistBundle{run: run, clock: clock.NewFake(t0), home: t.TempDir(), dirs: []string{sys, user}, name: "Example"}
	id, err := b.Locate(t.Context(), testBundle)
	if err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(filepath.Join(path, "Contents", "MacOS", "Example"))
	if id.Path != path || id.Version != "1.2.3" || id.Build != "1.2.3.45" || id.BundleID != testBundle ||
		id.ExecIno == 0 || id.ExecSize != fi.Size() || !id.ExecMtime.Equal(fi.ModTime().UTC()) {
		t.Fatalf("identity %+v", id)
	}
	if len(run.calls) != 1 || run.calls[0].args[0] != filepath.Join(path, "Contents", "Info.plist") {
		t.Fatalf("calls %+v", run.calls)
	}
	if _, err := b.Locate(t.Context(), "com.example.missing"); !errors.Is(err, ErrBundleNotFound) {
		t.Fatalf("got %v", err)
	}
	if _, err := b.Locate(t.Context(), "-bad id"); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("got %v", err)
	}
}

func TestParsePlistStrings(t *testing.T) {
	got, err := parsePlistStrings([]byte(infoPlist("com.example.x")))
	if err != nil {
		t.Fatal(err)
	}
	if got["CFBundleIdentifier"] != "com.example.x" || got["CFBundleExecutable"] != "Example" || got["CFBundleTypeName"] != "" {
		t.Fatalf("got %v", got)
	}
	if _, err := parsePlistStrings([]byte("<plist><dict><key>a</key>")); err == nil {
		t.Fatal("truncated plist parsed")
	}
}

func TestUpdateStagedAndInstalled(t *testing.T) {
	home := t.TempDir()
	clk := clock.NewFake(t0)
	stateDir := filepath.Join(home, "Library", "Caches", testBundle+".ShipIt")
	state := filepath.Join(stateDir, "ShipItState.plist")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, []byte(infoPlist("x")), 0o600); err != nil {
		t.Fatal(err)
	}
	// The updater starts 1 s after the quit, installs, removes its state
	// at 3 s and exits at 4 s.
	updater := Process{PID: 77, Exe: "/Apps/Example.app/Contents/Frameworks/Squirrel.framework/Resources/ShipIt", Args: []string{testBundle + ".ShipIt"}}
	procs := procFunc(func() []Process {
		el := clk.Now().Sub(t0)
		if el >= 3*time.Second {
			_ = os.Remove(state)
		}
		if el >= time.Second && el < 4*time.Second {
			return []Process{updater}
		}
		return nil
	})
	b := &plistBundle{procs: procs, clock: clk, home: home, dirs: []string{t.TempDir()}, name: "Example"}
	staged, err := b.UpdateStaged(t.Context(), testBundle)
	if err != nil || !staged {
		t.Fatalf("staged=%v err=%v", staged, err)
	}
	if err := b.WaitUpdateInstalled(t.Context(), testBundle, time.Minute); err != nil {
		t.Fatal(err)
	}
	if got := clk.Now().Sub(t0); got < 4*time.Second || got > 4250*time.Millisecond {
		t.Fatalf("waited %v", got)
	}
	staged, err = b.UpdateStaged(t.Context(), testBundle)
	if err != nil || staged {
		t.Fatalf("after install: staged=%v err=%v", staged, err)
	}

	// A staged state that never installs times out with UpdatePending.
	if err := os.WriteFile(state, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	stuck := &plistBundle{procs: procFunc(func() []Process { return nil }), clock: clock.NewFake(t0), home: home, name: "Example"}
	if err := stuck.WaitUpdateInstalled(t.Context(), testBundle, 2*time.Second); pderr.CodeOf(err) != pderr.UpdatePending {
		t.Fatalf("got %v", err)
	}
	// An updater process alone also counts as staged.
	running := &plistBundle{procs: procFunc(func() []Process { return []Process{updater} }), clock: clk, home: t.TempDir(), name: "Example"}
	if staged, err := running.UpdateStaged(t.Context(), testBundle); err != nil || !staged {
		t.Fatalf("updater running: %v %v", staged, err)
	}
}

func TestNewDarwinRefusesRealThings(t *testing.T) {
	run := &fakeRunner{t: t}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	tests := []struct {
		name string
		cfg  DarwinConfig
	}{
		{"nil exec", DarwinConfig{Home: t.TempDir()}},
		{"real home", DarwinConfig{Home: home, Exec: run}},
		{"empty home", DarwinConfig{Exec: run}},
		{"relative home", DarwinConfig{Home: "home", Exec: run}},
		{"system app dir", DarwinConfig{Home: t.TempDir(), Exec: run, AppDirs: []string{"/Applications"}}},
		{"bad bundle id", DarwinConfig{Home: t.TempDir(), Exec: run, BundleID: "-x"}},
		{"bad app name", DarwinConfig{Home: t.TempDir(), Exec: run, AppName: "../x"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if p, err := NewDarwin(tc.cfg); err == nil {
				t.Fatalf("accepted: %+v", p)
			}
		})
	}
	if len(run.calls) != 0 {
		t.Fatalf("commands ran: %+v", run.calls)
	}
}

func TestNewDarwinWiresParts(t *testing.T) {
	home := t.TempDir()
	run := &fakeRunner{t: t}
	clk := clock.NewFake(t0)
	p, err := NewDarwin(DarwinConfig{Home: home, BundleID: testBundle, Exec: run, Clock: clk, AppName: "Example"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Clock != clk {
		t.Fatal("clock not wired")
	}
	path := writeBundle(t, filepath.Join(home, "Applications"), testBundle, false)
	id, err := p.Locate(t.Context(), testBundle)
	if err != nil || id.Path != path {
		t.Fatalf("locate: %+v %v", id, err)
	}
	logDir := filepath.Join(home, "Library", "Logs", "Claude")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "main.log"), []byte(stamp("[acct] account="+acctP+" org="+orgX+" active")), 0o600); err != nil {
		t.Fatal(err)
	}
	org, found, err := p.ActiveOrg(t.Context(), testSig, acctP)
	if err != nil || !found || org != orgX {
		t.Fatalf("active org: %q %v %v", org, found, err)
	}
	if err := p.Launch(t.Context(), testBundle); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(run.templates(), []Template{TmplOpenBundle}) {
		t.Fatalf("commands %v", run.templates())
	}
}

// Squirrel leaves its JSON state behind after a successful install; only
// a state whose update bundle still exists is a staged update.
func TestUpdateStagedLeftoverState(t *testing.T) {
	home := t.TempDir()
	stateDir := filepath.Join(home, "Library", "Caches", testBundle+".ShipIt")
	bundle := filepath.Join(stateDir, "update.abc", "Example.app")
	if err := os.MkdirAll(bundle, 0o700); err != nil {
		t.Fatal(err)
	}
	b := &plistBundle{procs: procFunc(func() []Process { return nil }), clock: clock.NewFake(t0), home: home, name: "Example"}
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(stateDir, "ShipItState.plist"), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	state := func(u string) string {
		return `{"bundleIdentifier":"` + testBundle + `","updateBundleURL":"` + u + `","useUpdateBundleName":true}`
	}
	for _, c := range []struct {
		name, state string
		want        bool
	}{
		{"bundle present", state("file://" + bundle + "/"), true},
		{"bundle gone", state("file://" + filepath.Join(stateDir, "update.gone", "Example.app") + "/"), false},
		{"bundle outside the state dir", state("file:///Applications/Example.app/"), true},
		{"not a file URL", state("https://example.invalid/x"), true},
		{"no bundle URL", `{"bundleIdentifier":"x"}`, true},
		{"not JSON", "x", true},
	} {
		write(c.state)
		got, err := b.UpdateStaged(t.Context(), testBundle)
		if err != nil || got != c.want {
			t.Errorf("%s: staged=%v err=%v, want %v", c.name, got, err, c.want)
		}
	}
	// A leftover state does not hold WaitUpdateInstalled.
	write(state("file://" + filepath.Join(stateDir, "update.gone", "Example.app") + "/"))
	if err := b.WaitUpdateInstalled(t.Context(), testBundle, time.Second); err != nil {
		t.Fatal(err)
	}
}
