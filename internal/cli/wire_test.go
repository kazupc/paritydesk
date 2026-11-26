package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/app"
	"github.com/kazupc/paritydesk/internal/apply"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/orchestrator"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/runner"
	"github.com/kazupc/paritydesk/internal/store/kv"
)

// refusingRunner is an app.Runner that runs nothing.
type refusingRunner struct{ calls int }

func (r *refusingRunner) Run(context.Context, app.Template, ...string) ([]byte, error) {
	r.calls++
	return nil, errors.New("no commands in tests")
}

func testManifests() []*compat.Manifest {
	return []*compat.Manifest{{
		Name:            "test",
		ReservedFolders: []string{"Claude", "Claude-3p"},
		PrefTags:        map[string]string{"sidebarMode": compat.TagUIState, "keepAwakeEnabled": compat.TagOther},
	}}
}

func wireFor(home string, readOnly bool, exec app.Runner) wireInput {
	getenv := func(string) string { return "" }
	return wireInput{
		Home:       home,
		Paths:      config.ResolvePaths(home, getenv, config.Overrides{}),
		Getenv:     getenv,
		Executable: filepath.Join(home, "bin", "paritydesk"),
		ReadOnly:   readOnly,
		Exec:       exec,
		Manifests:  testManifests(),
	}
}

func TestBuildDepsReadOnly(t *testing.T) {
	home := t.TempDir()
	d, err := buildDeps(context.Background(), &globalFlags{}, wireFor(home, true, &refusingRunner{}))
	if err != nil {
		t.Fatal(err)
	}
	if !d.ReadOnly {
		t.Fatal("Deps.ReadOnly not set")
	}
	if err := d.FS.MkdirAll(filepath.Join(home, "x"), 0o700); !errors.Is(err, fsx.ErrReadOnly) {
		t.Fatalf("FS is not read-only: %v", err)
	}
	if _, err := d.KV.OpenWritable(context.Background(), filepath.Join(home, "ls")); !errors.Is(err, kv.ErrReadOnly) {
		t.Fatalf("KV is not read-only: %v", err)
	}
	if a, ok := d.Applier.(*apply.Applier); !ok || !a.ReadOnly {
		t.Fatalf("applier %#v", d.Applier)
	}
	if want := filepath.Join(home, "Library", "Application Support"); d.DataRoot != want {
		t.Fatalf("data root %q", d.DataRoot)
	}
	if ps, ok := d.Plans.(runner.PlanStore); !ok || ps.Dir != d.Paths.RunDir {
		t.Fatalf("plan store %#v", d.Plans)
	}
	if det, ok := d.Detach.(runner.Detacher); !ok || det.IsApp == nil || det.Binary == "" {
		t.Fatalf("detacher %#v", d.Detach)
	}
	if d.State == nil || d.Gens == nil || d.Folders == nil || d.App == nil || d.Compose == nil || d.Validate == nil {
		t.Fatal("dependencies missing")
	}
	if _, err := os.Stat(filepath.Join(d.DataRoot, TempDirName)); err == nil {
		t.Fatal("buildDeps created the temp root")
	}

	// The same call without read-only mode wraps nothing.
	d, err = buildDeps(context.Background(), &globalFlags{}, wireFor(home, false, &refusingRunner{}))
	if err != nil {
		t.Fatal(err)
	}
	if d.ReadOnly {
		t.Fatal("ReadOnly set without --read-only")
	}
	if _, err := d.KV.OpenWritable(context.Background(), filepath.Join(home, "ls")); errors.Is(err, kv.ErrReadOnly) {
		t.Fatal("KV wrapped without --read-only")
	}
	// The --read-only global flag alone also selects read-only mode.
	d, err = buildDeps(context.Background(), &globalFlags{ReadOnly: true}, wireFor(home, false, &refusingRunner{}))
	if err != nil || !d.ReadOnly {
		t.Fatalf("--read-only: %v %v", d.ReadOnly, err)
	}
}

func TestBuildDepsRefusesRealPathsUnderTest(t *testing.T) {
	realHome := os.Getenv("HOME")
	if realHome == "" || !filepath.IsAbs(realHome) {
		t.Skip("HOME is not set")
	}
	tmp := t.TempDir()
	withConfig := func(body string) wireInput {
		in := wireFor(tmp, true, &refusingRunner{})
		if err := os.MkdirAll(filepath.Dir(in.Paths.ConfigFile), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(in.Paths.ConfigFile, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return in
	}
	tests := []struct {
		name string
		in   wireInput
		want error
	}{
		{"real home", wireFor(realHome, true, &refusingRunner{}), fsx.ErrRealPathInTest},
		{"real support dir", func() wireInput {
			in := wireFor(tmp, true, &refusingRunner{})
			in.Paths.SupportDir = filepath.Join(realHome, "Library", "Application Support", "paritydesk")
			return in
		}(), fsx.ErrRealPathInTest},
		{"real data root in config", withConfig("version = 1\n[app]\ndata_root = \"" + filepath.Join(realHome, "Library", "Application Support") + "\"\n"), fsx.ErrRealPathInTest},
		{"real exec runner", wireFor(tmp, true, nil), errRealRunnerInTest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildDeps(context.Background(), &globalFlags{}, tc.in)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err %v, want %v", err, tc.want)
			}
		})
	}
}

func TestBuildDepsValidatesConfig(t *testing.T) {
	home := t.TempDir()
	in := wireFor(home, true, &refusingRunner{})
	if err := os.MkdirAll(filepath.Dir(in.Paths.ConfigFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(in.Paths.ConfigFile, []byte("version = 1\n[sync]\napp_prefs = [\"secretKnob\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := buildDeps(context.Background(), &globalFlags{}, in); err == nil || !strings.Contains(err.Error(), "app_prefs") {
		t.Fatalf("invalid config accepted: %v", err)
	}
}

// TestBuildDepsWiresArchiveWhenSyncOff: profile rename and remove keep
// the artifact archive's aliases right even while artifact sync or the
// online layer is off, so the archive view is always wired.
func TestBuildDepsWiresArchiveWhenSyncOff(t *testing.T) {
	for name, body := range map[string]string{
		"sync.artifacts off":   "version = 1\n[sync]\nartifacts = false\n",
		"online.reconcile off": "version = 1\n[online]\nreconcile = \"off\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			in := wireFor(home, true, &refusingRunner{})
			if err := os.MkdirAll(filepath.Dir(in.Paths.ConfigFile), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(in.Paths.ConfigFile, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			d, err := buildDeps(context.Background(), &globalFlags{}, in)
			if err != nil {
				t.Fatal(err)
			}
			if d.Artifacts == nil {
				t.Fatal("archive view not wired")
			}
		})
	}
}

// TestBuildDepsFaults: buildDeps wires PARITYDESK_FAULT and refuses the
// pause fault for the real app's bundle id even with the e2e marker set.
func TestBuildDepsFaults(t *testing.T) {
	home := t.TempDir()
	with := func(env map[string]string) wireInput {
		in := wireFor(home, false, &refusingRunner{})
		in.Getenv = func(k string) string { return env[k] }
		return in
	}
	d, err := buildDeps(context.Background(), &globalFlags{}, with(map[string]string{envFault: "after:P8"}))
	if err != nil || d.Crash == nil || !d.Crash(orchestrator.P8Swap) {
		t.Fatalf("after:P8 not wired: %v", err)
	}
	if a, ok := d.Applier.(*apply.Applier); !ok || a.AfterFirstWrite != nil {
		t.Fatalf("applier hook set without the pause fault: %#v", d.Applier)
	}
	_, err = buildDeps(context.Background(), &globalFlags{}, with(map[string]string{envFault: "pause:P7", envE2E: "1"}))
	if !errors.Is(err, errUsage) || !strings.Contains(err.Error(), "refusing the real app's bundle id") {
		t.Fatalf("pause:P7 with the default bundle id: %v", err)
	}
}

func TestParseFaults(t *testing.T) {
	parse := func(env map[string]string) (faults, error) {
		return parseFaults(faultEnv{getenv: func(k string) string { return env[k] }})
	}
	f, err := parse(map[string]string{envFault: "after:P7"})
	if err != nil || f.crash == nil || !f.crash(orchestrator.P7Apply) || f.crash(orchestrator.P8Swap) || f.afterFirstWrite != nil {
		t.Fatalf("PARITYDESK_FAULT=after:P7 not honoured: %v", err)
	}
	// A run that skipped P7 (an empty plan, for example) reaches the
	// last crash point before P10: the fault stops it there and says so.
	ui := &recordUI{}
	f, err = parseFaults(faultEnv{getenv: func(k string) string { return map[string]string{envFault: "after:P7"}[k] }, ui: ui})
	if err != nil || !f.crash(orchestrator.P10Commit) {
		t.Fatalf("after:P7 does not stop a run that skipped P7: %v", err)
	}
	if len(ui.events) != 1 || ui.events[0].Status != "warn" || !strings.Contains(ui.events[0].Message.English(), "skipped P7") {
		t.Fatalf("events %+v", ui.events)
	}
	if f, err := parse(nil); err != nil || f.crash != nil || f.afterFirstWrite != nil {
		t.Fatalf("faults without the variable: %v", err)
	}
	// Phases without intents would silently run a whole switch; unknown
	// forms too. Both are usage errors.
	for _, v := range []string{"after:P10", "after:P11", "after:P12", "after:", "after:p7", "P7", "pause", "pause:P8", "kill:P7"} {
		if _, err := parse(map[string]string{envFault: v}); err == nil || !errors.Is(err, errUsage) {
			t.Errorf("PARITYDESK_FAULT=%s: %v, want a usage error", v, err)
		}
	}
}

// TestPauseFaultGuard: pause:P7 is honoured only with the e2e harness's
// marker, a test bundle id, a temporary HOME and a data root inside it.
func TestPauseFaultGuard(t *testing.T) {
	base := t.TempDir()
	realHome := filepath.Join(base, "Users", "someone")
	tmpHome := filepath.Join(base, "e2e", "home")
	good := faultEnv{
		home:     tmpHome,
		realHome: func() (string, error) { return realHome, nil },
		bundleID: "dev.paritydesk.stubapp",
		dataRoot: filepath.Join(tmpHome, "worlds", "w", "data"),
	}
	env := map[string]string{envFault: "pause:P7", envE2E: "1"}
	with := func(mod func(*faultEnv, map[string]string)) (faults, error) {
		fe := good
		e := map[string]string{}
		for k, v := range env {
			e[k] = v
		}
		mod(&fe, e)
		fe.getenv = func(k string) string { return e[k] }
		return parseFaults(fe)
	}
	f, err := with(func(*faultEnv, map[string]string) {})
	if err != nil || f.afterFirstWrite == nil || f.crash != nil {
		t.Fatalf("pause:P7 inside the guard: %v", err)
	}
	refused := map[string]func(*faultEnv, map[string]string){
		"no marker":        func(_ *faultEnv, e map[string]string) { delete(e, envE2E) },
		"marker not 1":     func(_ *faultEnv, e map[string]string) { e[envE2E] = "true" },
		"real bundle id":   func(fe *faultEnv, _ map[string]string) { fe.bundleID = "com.anthropic.claudefordesktop" },
		"vendor bundle id": func(fe *faultEnv, _ map[string]string) { fe.bundleID = "COM.Anthropic.other" },
		"real home": func(fe *faultEnv, _ map[string]string) {
			fe.home = realHome
			fe.dataRoot = filepath.Join(realHome, "data")
		},
		"parent of real home": func(fe *faultEnv, _ map[string]string) {
			fe.home = filepath.Dir(realHome)
			fe.dataRoot = filepath.Join(fe.home, "d")
		},
		"under real Library": func(fe *faultEnv, _ map[string]string) {
			fe.home = filepath.Join(realHome, "Library", "x")
			fe.dataRoot = filepath.Join(fe.home, "d")
		},
		"data root outside": func(fe *faultEnv, _ map[string]string) { fe.dataRoot = filepath.Join(base, "elsewhere") },
		"data root is HOME": func(fe *faultEnv, _ map[string]string) { fe.dataRoot = tmpHome },
		"real home unknown": func(fe *faultEnv, _ map[string]string) {
			fe.realHome = func() (string, error) { return "", errors.New("no user") }
		},
		// Lexically inside HOME, but a symlink leads elsewhere.
		"data root through a symlink": func(fe *faultEnv, _ map[string]string) {
			fe.dataRoot = filepath.Join(tmpHome, "escape", "data")
		},
		"HOME is a symlink to the real home": func(fe *faultEnv, _ map[string]string) {
			fe.home = filepath.Join(base, "home-link")
			fe.dataRoot = filepath.Join(fe.home, "data")
		},
	}
	for _, d := range []string{realHome, tmpHome, filepath.Join(base, "elsewhere")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(base, "elsewhere"), filepath.Join(tmpHome, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realHome, filepath.Join(base, "home-link")); err != nil {
		t.Fatal(err)
	}
	for name, mod := range refused {
		if f, err := with(mod); err == nil || !errors.Is(err, errUsage) || f.afterFirstWrite != nil {
			t.Errorf("%s: pause:P7 not refused (%v)", name, err)
		}
	}
	// With the folders present (and the temporary directory itself
	// possibly behind a symlink, as /var is on macOS) it is still allowed.
	if f, err := with(func(*faultEnv, map[string]string) {}); err != nil || f.afterFirstWrite == nil {
		t.Fatalf("pause:P7 inside the guard, folders present: %v", err)
	}
}

// TestPauseHook: the pause reports one "paused" event, then waits; a
// pause nobody ends exits with code Internal. It runs once per process.
func TestPauseHook(t *testing.T) {
	ui := &recordUI{}
	var slept time.Duration
	exits := []int{}
	hook := pauseHook(faultEnv{ui: ui, sleep: func(d time.Duration) { slept += d }, exit: func(c int) { exits = append(exits, c) }})
	hook()
	hook()
	if slept != pauseLimit || len(exits) != 1 || exits[0] != int(pderr.Internal) {
		t.Fatalf("slept %s, exits %v", slept, exits)
	}
	if len(ui.events) != 2 || ui.events[0].Phase != orchestrator.P7Apply || ui.events[0].Status != PausedStatus || ui.events[1].Status != "fail" {
		t.Fatalf("events %+v", ui.events)
	}
}

// recordUI records progress events.
type recordUI struct{ events []orchestrator.Event }

func (u *recordUI) Progress(ev orchestrator.Event) { u.events = append(u.events, ev) }
func (u *recordUI) Interactive() bool              { return false }
func (u *recordUI) Confirm(context.Context, orchestrator.Confirmation) (bool, error) {
	return false, nil
}

// treeHash hashes every path, mode and file content under dir.
func treeHash(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		h.Write([]byte(rel + "\x00" + info.Mode().String() + "\x00"))
		if info.Mode().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			h.Write(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// TestPlanReadOnlyWritesNothing runs `plan --read-only` over a synthetic
// home with data folders, state and config, through the real buildDeps
// (fake exec runner) and a fake Service: a plan comes back and not one
// byte under the home changes.
func TestPlanReadOnlyWritesNothing(t *testing.T) {
	h := newHarness(t)
	writeFile(h, ".config/paritydesk/config.toml", "version = 1\n[[profile]]\nalias = \"work\"\n[[profile]]\nalias = \"home\"\n")
	writeFile(h, "Library/Application Support/Claude/config.json", `{"lastKnownAccountUuid":"00000000-0000-4000-8000-000000000001"}`)
	writeFile(h, "Library/Application Support/Claude/Local Storage/leveldb/CURRENT", "MANIFEST-000001\n")
	writeFile(h, "Library/Application Support/Claude-home/config.json", `{"lastKnownAccountUuid":"00000000-0000-4000-8000-000000000002"}`)
	writeFile(h, "Library/Application Support/paritydesk/state/root.json", `{"v":1}`)
	before := treeHash(t, h.home)

	var deps orchestrator.Deps
	h.sys.service = func(ctx context.Context, e *env, ui orchestrator.UI) (orchestrator.Service, error) {
		paths, home, err := e.paths()
		if err != nil {
			return nil, err
		}
		in := wireFor(home, e.readOnly(), &refusingRunner{})
		in.Paths, in.UI = paths, ui
		if deps, err = buildDeps(ctx, e.g, in); err != nil {
			return nil, err
		}
		return h.svc, nil
	}
	code, out, errOut := h.run("", "plan", "--read-only", "--json", "home")
	if code != 0 {
		t.Fatalf("code %d %s", code, errOut)
	}
	if !deps.ReadOnly || h.svc.previewOpts.Save {
		t.Fatalf("read-only %v save %v", deps.ReadOnly, h.svc.previewOpts.Save)
	}
	if !strings.Contains(out, `"plan_hash":"`) || !strings.Contains(out, `"saved":false`) {
		t.Fatalf("no plan returned: %s", out)
	}
	if after := treeHash(t, h.home); after != before {
		t.Fatal("plan --read-only changed files under the home")
	}
}
