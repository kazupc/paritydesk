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

	"github.com/kazupc/paritydesk/internal/app"
	"github.com/kazupc/paritydesk/internal/apply"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/orchestrator"
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

func TestFaultInjector(t *testing.T) {
	env := map[string]string{"PARITYDESK_FAULT": "after:P7"}
	f := faultInjector(func(k string) string { return env[k] })
	if f == nil || !f(orchestrator.P7Apply) || f(orchestrator.P8Swap) {
		t.Fatal("PARITYDESK_FAULT=after:P7 not honoured")
	}
	if faultInjector(func(string) string { return "" }) != nil {
		t.Fatal("fault injector without the variable")
	}
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
