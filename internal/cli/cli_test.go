package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/clock"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/hook"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/orchestrator"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/state"
)

func TestVersion(t *testing.T) {
	var out, errb bytes.Buffer
	code := Execute(context.Background(), []string{"version"}, strings.NewReader(""), &out, &errb)
	if code != 0 || !strings.HasPrefix(out.String(), "paritydesk ") {
		t.Fatalf("code %d out %q err %q", code, out.String(), errb.String())
	}
}

func TestUnknownCommandIsUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Execute(context.Background(), []string{"nope"}, strings.NewReader(""), &out, &errb); code != 2 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(errb.String(), "PD-E2") {
		t.Fatalf("stderr %q", errb.String())
	}
}

// TestRealServiceRefusedUnderTest runs a command through the production
// system: buildDeps refuses the real home under go test, so nothing real
// is touched and the run fails.
func TestRealServiceRefusedUnderTest(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Execute(context.Background(), []string{"status"}, strings.NewReader(""), &out, &errb); code == 0 {
		t.Fatalf("status succeeded against the real home: %q", out.String())
	}
}

func TestFlagsAfterArgsAndJSON(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Execute(context.Background(), []string{"version", "--json"}, strings.NewReader(""), &out, &errb); code != 0 || !strings.Contains(out.String(), `"version"`) {
		t.Fatalf("code %d out %q err %q", code, out.String(), errb.String())
	}
	out.Reset()
	if code := Execute(context.Background(), []string{"--json", "version"}, strings.NewReader(""), &out, &errb); code != 0 || !strings.Contains(out.String(), `"version"`) {
		t.Fatalf("code %d out %q", code, out.String())
	}
}

func TestArgCountIsUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := Execute(context.Background(), []string{"profile", "adopt", "only-one"}, strings.NewReader(""), &out, &errb); code != 2 {
		t.Fatalf("code %d", code)
	}
}

// TestJobArgsParse checks that every flag the detached job receives is
// registered on `switch`, and that parsing the job's argv reproduces the
// SwitchOptions it was built from (with FromRunner set, Detach dropped).
func TestJobArgsParse(t *testing.T) {
	tests := []orchestrator.SwitchOptions{
		{
			To: "work", Yes: true, Only: []model.Category{model.CatSessions}, Skip: []model.Category{model.CatWindow},
			NoLaunch: true, NoReopen: true, ReopenCLISession: "00000000-0000-4000-8000-000000000001",
			AllowDeletions: 2, AcceptWorktreeRisk: true, LogOffset: 10,
			ConfirmedPlan: strings.Repeat("ab", 32), ConfirmFirstSync: true,
		},
		{To: "", LogOffset: -1},
		{To: "home", Detach: true, LogOffset: 0, Only: []model.Category{model.CatSessions, model.CatPins, model.CatMCP}},
	}
	for _, opts := range tests {
		h := newHarness(t)
		g := orchestrator.JobGlobals{Config: "/c.toml", StateDir: "/s", AppSupportDir: "/a"}
		args := opts.JobArgs(g)
		code, out, errOut := h.run("", args...)
		if code != 0 {
			t.Fatalf("args %q: code %d out %q err %q", args, code, out, errOut)
		}
		want := opts
		want.FromRunner, want.Detach = true, false
		if !reflect.DeepEqual(h.svc.switchOpts, want) {
			t.Fatalf("args %q parsed to\n %+v\nwant\n %+v", args, h.svc.switchOpts, want)
		}
		if h.g.Config != g.Config || h.g.StateDir != g.StateDir || h.g.AppSupportDir != g.AppSupportDir {
			t.Fatalf("global paths not inherited: %+v", h.g)
		}
	}
}

func TestFlagsAfterPositionalsAndDoubleDash(t *testing.T) {
	tests := []struct {
		args []string
		to   string
		yes  bool
		only []model.Category
		code int
	}{
		{[]string{"switch", "home", "--yes", "--only", "sessions,pins"}, "home", true, []model.Category{model.CatSessions, model.CatPins}, 0},
		{[]string{"switch", "--yes", "home"}, "home", true, nil, 0},
		{[]string{"switch", "--yes", "--", "home"}, "home", true, nil, 0},
		{[]string{"switch", "--", "-"}, "", false, nil, 0},
		{[]string{"switch", "-"}, "", false, nil, 0},
		{[]string{"switch"}, "", false, nil, 0},
		{[]string{"switch", "--", "--yes"}, "", false, nil, 2},
		{[]string{"switch", "home", "work"}, "", false, nil, 2},
		{[]string{"--json", "switch", "home", "--yes"}, "home", true, nil, 0},
	}
	for _, tc := range tests {
		h := newHarness(t)
		code, _, errOut := h.run("", tc.args...)
		if code != tc.code {
			t.Fatalf("%q: code %d, want %d (%s)", tc.args, code, tc.code, errOut)
		}
		if code != 0 {
			continue
		}
		o := h.svc.switchOpts
		if string(o.To) != tc.to || o.Yes != tc.yes || !reflect.DeepEqual(o.Only, tc.only) {
			t.Fatalf("%q parsed to %+v", tc.args, o)
		}
	}
	h := newHarness(t)
	if code, _, _ := h.run("", "plan", "--only", "pins", "--", "home"); code != 0 || h.svc.previewOpts.To != "home" ||
		!reflect.DeepEqual(h.svc.previewOpts.Only, []model.Category{model.CatPins}) {
		t.Fatalf("plan options %+v", h.svc.previewOpts)
	}
}

func TestUsageErrors(t *testing.T) {
	tests := [][]string{
		{"nope"},
		{"profile"},
		{"profile", "nope"},
		{"switch", "Work"},
		{"switch", "--only", "artifacts"},
		{"switch", "--only", "sessions", "--skip", "sessions"},
		{"switch", "--only", "sessions,sessions"},
		{"switch", "--reopen-session", "not-a-uuid"},
		{"switch", "--reopen-session", "local_00000000-0000-4000-8000-000000000001"},
		{"switch", "--allow-deletions", "-1"},
		{"switch", "--confirmed-plan", "../../etc"},
		{"switch", "--confirmed-plan", "abc123"},
		{"switch", "--confirmed-plan", strings.Repeat("A", 64)},
		{"switch", "--no-such-flag"},
		{"plan", "-x"},
		{"rollback", "latest"},
		{"generations", "show", "-1"},
		{"profile", "adopt", "work", "../Claude"},
		{"profile", "adopt", "work", "Claude"},
		{"profile", "add", "-"},
		{"profile", "rename", "a", "B"},
		{"init"},
		{"init", "--alias", "x", "--label", "bad\x1blabel"},
		{"sync", "--allow-deletions", "-2"},
		{"log", "--run", "../x"},
		{"help", "nope"},
		{"version", "extra"},
	}
	for _, args := range tests {
		h := newHarness(t)
		code, out, errOut := h.run("", args...)
		if code != 2 {
			t.Errorf("%q: code %d, want 2 (out %q err %q)", args, code, out, errOut)
			continue
		}
		if !strings.Contains(errOut, "PD-E2") || strings.ContainsRune(errOut, 0x1b) {
			t.Errorf("%q: stderr %q", args, errOut)
		}
		if len(h.svc.calls) != 0 {
			t.Errorf("%q: service called: %v", args, h.svc.calls)
		}
	}
}

func TestExitCodeMapping(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code int
		msg  string
	}{
		{"declined", pderr.New(pderr.Declined, "switch", ""), 3, "PD-E3"},
		{"app did not quit", pderr.New(pderr.AppDidNotQuit, "switch/P3", ""), 10, "PD-E10"},
		{"breaker", breakerErr(), 22, "exceed the limit"},
		{"sign-in with alias", pderr.New(pderr.NeedsSignIn, "verify", "sign in with home's account only"), 51, "home's account only"},
		{"needs recover", fmtWrap(pderr.New(pderr.NeedsRecover, "switch", "")), 41, "paritydesk recover"},
		{"not implemented", pderr.ErrNotImplemented, 70, "PD-E70"},
		{"plain error", errors.New("boom"), 70, "boom"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.svc.errs["switch"] = tc.err
			code, out, errOut := h.run("", "switch", "home")
			if code != tc.code || !strings.Contains(errOut, tc.msg) {
				t.Fatalf("code %d stderr %q", code, errOut)
			}
			if out != "" {
				t.Fatalf("stdout %q", out)
			}
		})
	}
}

func fmtWrap(err error) error { return errors.Join(errors.New("context"), err) }

func TestResultCodeWithoutError(t *testing.T) {
	h := newHarness(t)
	h.svc.result = &orchestrator.Result{From: "work", To: "home", Code: pderr.SwapOnly, Mode: "swap-only"}
	code, out, errOut := h.run("", "switch", "home")
	if code != 31 || !strings.Contains(errOut, "PD-E31") || !strings.Contains(out, "Switched from work to home") {
		t.Fatalf("code %d out %q err %q", code, out, errOut)
	}
	code, out, _ = h.run("", "switch", "home", "--json")
	if code != 31 || strings.Count(out, "\n") != 1 || !strings.Contains(out, `"code":31`) {
		t.Fatalf("json: code %d out %q", code, out)
	}
}

func TestOptionsMapping(t *testing.T) {
	h := newHarness(t)
	if code, _, e := h.run("", "sync", "--yes", "--allow-deletions=4", "--no-launch"); code != 0 {
		t.Fatal(e)
	}
	if want := (orchestrator.SyncOptions{Yes: true, AllowDeletions: 4, NoLaunch: true}); h.svc.syncOpts != want {
		t.Fatalf("sync %+v", h.svc.syncOpts)
	}
	if code, _, e := h.run("", "recover", "--rollback"); code != 0 {
		t.Fatal(e)
	}
	if want := (orchestrator.RecoverOptions{Rollback: true}); h.svc.recoverOpts != want {
		t.Fatalf("recover %+v", h.svc.recoverOpts)
	}
	if code, _, e := h.run("", "rollback"); code != 0 || h.svc.rollbackN != 0 || h.svc.propagate {
		t.Fatalf("rollback %d %v %s", h.svc.rollbackN, h.svc.propagate, e)
	}
	if code, _, e := h.run("", "rollback", "--propagate", "12"); code != 0 || h.svc.rollbackN != 12 || !h.svc.propagate {
		t.Fatalf("rollback %d %v %s", h.svc.rollbackN, h.svc.propagate, e)
	}
	if code, _, e := h.run("", "status", "--short"); code != 0 || !h.svc.statusShort {
		t.Fatal(e)
	}
	if code, _, e := h.run("", "doctor", "--grant-automation"); code != 0 || !h.svc.doctorOpts.GrantAutomation {
		t.Fatal(e)
	}
	if code, _, e := h.run("", "init", "--alias", "work", "--label", "Work laptop", "--managed"); code != 0 ||
		h.svc.initOpts != (orchestrator.InitOptions{Alias: "work", Label: "Work laptop", Managed: true}) {
		t.Fatalf("init %+v %s", h.svc.initOpts, e)
	}
	h.svc.args = nil
	for _, args := range [][]string{
		{"profile", "adopt", "home", "Claude-home", "--unmanaged"},
		{"profile", "add", "client", "--managed"},
		{"profile", "rename", "home", "personal"},
		{"profile", "remove", "personal"},
	} {
		if code, _, e := h.run("", args...); code != 0 {
			t.Fatalf("%q: %s", args, e)
		}
	}
	want := []string{"home", "Claude-home", "client", "managed", "home", "personal", "personal"}
	if !reflect.DeepEqual(h.svc.args, want) {
		t.Fatalf("profile args %q", h.svc.args)
	}
}

func TestReadOnlyEnvironment(t *testing.T) {
	h := newHarness(t)
	h.env["PARITYDESK_READONLY"] = "1"
	if code, _, e := h.run("", "plan"); code != 0 || h.svc.previewOpts.Save {
		t.Fatalf("plan saved in read-only mode (%s)", e)
	}
	for _, args := range [][]string{{"backups", "prune"}, {"backups", "purge", "--yes"}, {"uninstall", "--yes"}} {
		if code, _, _ := h.run("", args...); code != 2 {
			t.Errorf("%q ran in read-only mode: code %d", args, code)
		}
	}
	if h.gens.purged || h.gens.pruned != nil {
		t.Fatal("generations changed in read-only mode")
	}
	if code, _, _ := h.run("", "init", "--alias", "work", "--unmanaged"); code != 0 {
		t.Fatal("init failed")
	}
	if _, err := os.Stat(filepath.Join(h.home, "Library", "Application Support", "paritydesk", HookBinaryFile)); err == nil {
		t.Fatal("hook-binary written in read-only mode")
	}
}

func TestConfirmations(t *testing.T) {
	summary := fakePlan().Summarize()
	conf := orchestrator.Confirmation{Kind: orchestrator.ConfirmPlan, Plan: fakePlan(), Summary: &summary,
		Lines: []string{"busy: local_x\x1b[2J"}}
	tests := []struct {
		name  string
		tty   bool
		yes   bool
		kind  orchestrator.ConfirmKind
		input string
		code  int
	}{
		{"tty yes", true, false, orchestrator.ConfirmPlan, "y\n", 0},
		{"tty YES", true, false, orchestrator.ConfirmPlan, " YES \n", 0},
		{"tty no", true, false, orchestrator.ConfirmPlan, "n\n", 3},
		{"tty empty is no", true, false, orchestrator.ConfirmPlan, "\n", 3},
		{"tty end of input is no", true, false, orchestrator.ConfirmPlan, "", 3},
		{"no tty without --yes", false, false, orchestrator.ConfirmPlan, "y\n", 2},
		{"no tty with --yes", false, true, orchestrator.ConfirmPlan, "", 0},
		{"--yes does not answer new deletions", false, true, orchestrator.ConfirmNewDestructive, "", 2},
		{"new deletions asked on a tty", true, true, orchestrator.ConfirmNewDestructive, "y\n", 0},
		{"a late answer still counts", true, false, orchestrator.ConfirmPlan, "y\n", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.tty = tc.tty
			c := conf
			c.Kind = tc.kind
			h.svc.confirm = &c
			args := []string{"switch", "home"}
			if tc.yes {
				args = append(args, "--yes")
			}
			code, _, errOut := h.run(tc.input, args...)
			if code != tc.code {
				t.Fatalf("code %d, want %d; stderr %q", code, tc.code, errOut)
			}
			if tc.tty && !(tc.yes && tc.kind != orchestrator.ConfirmNewDestructive) {
				if !strings.Contains(errOut, "[y/N]") || !strings.Contains(errOut, "DESTRUCTIVE: 1 deletions") {
					t.Fatalf("prompt missing: %q", errOut)
				}
				if strings.Contains(errOut, "\x1b[2J") {
					t.Fatalf("unsanitized line: %q", errOut)
				}
				if strings.Index(errOut, "DESTRUCTIVE") > strings.Index(errOut, "[y/N]") {
					t.Fatalf("DESTRUCTIVE line printed after the question: %q", errOut)
				}
			}
		})
	}
}

func TestProgress(t *testing.T) {
	ev := orchestrator.Event{Phase: orchestrator.P3Quit, Status: "ok", Message: "Claude quit\x1b]52;c;x\x07"}
	t.Run("plain", func(t *testing.T) {
		h := newHarness(t)
		var errb bytes.Buffer
		e := &env{g: &globalFlags{}, stdin: strings.NewReader(""), stdout: &bytes.Buffer{}, stderr: &errb, sys: h.sys}
		e.newUI(false, false).Progress(ev)
		if got := errb.String(); got != "P3 ok   Claude quit\n" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("ndjson", func(t *testing.T) {
		h := newHarness(t)
		var errb bytes.Buffer
		e := &env{g: &globalFlags{JSON: true}, stdin: strings.NewReader(""), stdout: &bytes.Buffer{}, stderr: &errb, sys: h.sys}
		e.newUI(false, true).Progress(ev)
		if got := errb.String(); got != `{"v":1,"phase":"P3","status":"ok","message":"Claude quit"}`+"\n" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("json without progress is quiet", func(t *testing.T) {
		h := newHarness(t)
		var errb bytes.Buffer
		e := &env{g: &globalFlags{JSON: true}, stdin: strings.NewReader(""), stdout: &bytes.Buffer{}, stderr: &errb, sys: h.sys}
		e.newUI(false, false).Progress(ev)
		if errb.Len() != 0 {
			t.Fatalf("got %q", errb.String())
		}
	})
}

// TestManagedIsAskedOrRequired: init, profile add and profile adopt ask
// whether the account is managed on a terminal, and refuse without a
// terminal unless --managed or --unmanaged is given.
func TestManagedIsAskedOrRequired(t *testing.T) {
	for _, args := range [][]string{
		{"init", "--alias", "work"},
		{"profile", "add", "client"},
		{"profile", "adopt", "home", "Claude-home"},
	} {
		h := newHarness(t)
		if code, _, e := h.run("", args...); code != 2 || !strings.Contains(e, "--managed or --unmanaged") {
			t.Errorf("%q without a terminal: code %d %s", args, code, e)
		}
		if code, _, _ := h.run("", append(args, "--managed", "--unmanaged")...); code != 2 {
			t.Errorf("%q with both flags: code %d", args, code)
		}
		h = newHarness(t)
		h.tty = true
		code, _, errOut := h.run("y\n", args...)
		if code != 0 || !strings.Contains(errOut, "managed by an employer or client") {
			t.Errorf("%q on a terminal: code %d stderr %q", args, code, errOut)
		}
	}
	h := newHarness(t)
	h.tty = true
	if code, _, _ := h.run("yes\n", "init", "--alias", "work"); code != 0 || !h.svc.initOpts.Managed {
		t.Fatalf("init answer yes: %+v", h.svc.initOpts)
	}
	h = newHarness(t)
	h.tty = true
	if code, _, _ := h.run("\n", "init", "--alias", "work"); code != 0 || h.svc.initOpts.Managed {
		t.Fatalf("init default answer: %+v", h.svc.initOpts)
	}
}

func TestInitAsksForAliasOnATerminal(t *testing.T) {
	h := newHarness(t)
	h.tty = true
	code, _, errOut := h.run("work\n", "init")
	if code != 0 || h.svc.initOpts.Alias != "work" || !strings.Contains(errOut, "Alias for the account") {
		t.Fatalf("code %d alias %q stderr %q", code, h.svc.initOpts.Alias, errOut)
	}
}

func TestHookBinaryRecorded(t *testing.T) {
	for _, cmd := range [][]string{{"init", "--alias", "work", "--unmanaged"}, {"doctor"}} {
		h := newHarness(t)
		if code, _, e := h.run("", cmd...); code != 0 {
			t.Fatalf("%q: %s", cmd, e)
		}
		p := filepath.Join(h.home, "Library", "Application Support", "paritydesk", HookBinaryFile)
		b, err := os.ReadFile(p)
		if err != nil || string(b) != h.exe+"\n" {
			t.Fatalf("%q: hook-binary %q %v", cmd, b, err)
		}
		fi, _ := os.Stat(p)
		di, _ := os.Stat(filepath.Dir(p))
		if fi.Mode().Perm() != 0o600 || di.Mode().Perm() != 0o700 {
			t.Fatalf("modes %v %v", fi.Mode(), di.Mode())
		}
	}
	h := newHarness(t)
	h.exe = "relative/paritydesk"
	if code, _, _ := h.run("", "doctor"); code != 70 {
		t.Fatalf("a relative executable path was recorded (code %d)", code)
	}
}

func TestHookSessionStart(t *testing.T) {
	open := state.NewRoot("/d")
	open.Run = &state.Run{ID: "run-0001"}
	payload := `{"session_id":"00000000-0000-4000-8000-000000000001","hook_event_name":"SessionStart"}`
	tests := []struct {
		name  string
		root  *state.Root
		inApp bool
		args  []string
		in    string
		want  string
	}{
		{"pending", open, true, nil, payload, "paritydesk recover"},
		{"nothing pending", state.NewRoot("/d"), true, nil, payload, ""},
		{"outside the app", open, false, nil, payload, ""},
		{"version skew", open, true, []string{"--plugin-version", "9.9.0"}, payload, ""},
		{"same version", open, true, []string{"--plugin-version", Version}, payload, "paritydesk recover"},
		{"unknown flag still exits 0", open, true, []string{"--surprise"}, payload, ""},
		{"malformed payload", open, true, nil, "{", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.root, h.inApp = tc.root, tc.inApp
			code, out, errOut := h.run(tc.in, append([]string{"hook", "session-start"}, tc.args...)...)
			if code != 0 || errOut != "" {
				t.Fatalf("code %d stderr %q", code, errOut)
			}
			if tc.want == "" && out != "" || tc.want != "" && (!strings.Contains(out, tc.want) || strings.Count(out, "\n") != 1) {
				t.Fatalf("stdout %q", out)
			}
		})
	}
	h := newHarness(t)
	h.sys.hookDeps = func(context.Context, *env, string) (hook.Deps, error) { return hook.Deps{}, errors.New("no state") }
	if code, _, _ := h.run(payload, "hook", "session-start"); code != 0 {
		t.Fatalf("a panic or failure in the hook path changed the exit code: %d", code)
	}
}

func TestBackupsPurgeAsks(t *testing.T) {
	h := newHarness(t)
	h.tty = true
	if code, _, _ := h.run("n\n", "backups", "purge"); code != 3 || h.gens.purged {
		t.Fatalf("declined purge: code %d purged %v", code, h.gens.purged)
	}
	h.tty = false
	if code, _, _ := h.run("y\n", "backups", "purge"); code != 2 || h.gens.purged {
		t.Fatalf("non-interactive purge without --yes: code %d", code)
	}
	h.tty = true
	if code, _, _ := h.run("y\n", "backups", "purge"); code != 0 || !h.gens.purged {
		t.Fatalf("confirmed purge: code %d", code)
	}
}

func TestBackupsPrunePolicyFromConfig(t *testing.T) {
	h := newHarness(t)
	writeFile(h, ".config/paritydesk/config.toml", "version = 1\n[backups]\nkeep = 4\nleveldb_clones_keep = 1\nmax_size = \"1MiB\"\n")
	if code, _, e := h.run("", "backups", "prune"); code != 0 {
		t.Fatal(e)
	}
	if p := h.gens.pruned; p == nil || p.Keep != 4 || p.LevelDBClones != 1 || p.MaxBytes != 1<<20 {
		t.Fatalf("policy %+v", p)
	}
}

func TestConfigValidate(t *testing.T) {
	h := newHarness(t)
	if code, _, _ := h.run("", "config", "validate"); code != 2 {
		t.Fatalf("missing config: code %d", code)
	}
	writeFile(h, ".config/paritydesk/config.toml", "version = 1\n[[profile]]\nalias = \"work\"\nfolder = \"Claude-3P\"\n")
	if code, _, e := h.run("", "config", "validate"); code != 2 || !strings.Contains(e, "reserved") {
		t.Fatalf("reserved folder: code %d %q", code, e)
	}
	writeFile(h, ".config/paritydesk/config.toml", "version = 1\nunknown = 1\n")
	if code, _, _ := h.run("", "config", "validate"); code != 2 {
		t.Fatalf("unknown key: code %d", code)
	}
	h.env["PARITYDESK_CONFIG"] = filepath.Join(h.home, "elsewhere.toml")
	if code, out, _ := h.run("", "config", "path"); code != 0 || strings.TrimSpace(out) != filepath.Join(h.home, "elsewhere.toml") {
		t.Fatalf("PARITYDESK_CONFIG ignored: %q", out)
	}
}

func TestUninstallKeepsDataFolders(t *testing.T) {
	h := newHarness(t)
	h.svc.status.Active = ""
	data := filepath.Join(h.home, "Library", "Application Support")
	writeFile(h, "Library/Application Support/Claude-home/config.json", "{}")
	writeFile(h, "Library/Application Support/Claude-work/config.json", "{}")
	writeFile(h, "Library/Application Support/paritydesk/state/root.json", "{}")
	writeFile(h, "Library/Application Support/paritydesk/generations/1/manifest.json", "{}")
	writeFile(h, "Library/Application Support/paritydesk/hook-binary", "/x\n")
	writeFile(h, ".config/paritydesk/config.toml", "version = 1\n")
	code, out, errOut := h.run("", "uninstall", "--yes")
	if code != 0 {
		t.Fatalf("code %d %s", code, errOut)
	}
	for _, keep := range []string{"Claude-home/config.json", "Claude-work/config.json", "paritydesk/generations/1/manifest.json"} {
		if _, err := os.Stat(filepath.Join(data, keep)); err != nil {
			t.Errorf("%s was removed", keep)
		}
	}
	for _, gone := range []string{"paritydesk/state", "paritydesk/hook-binary"} {
		if _, err := os.Stat(filepath.Join(data, gone)); err == nil {
			t.Errorf("%s was kept", gone)
		}
	}
	wantMove := "mv '" + filepath.Join(data, "Claude-home") + "' '" + filepath.Join(data, "Claude") + "'"
	if !strings.Contains(out, wantMove) {
		t.Errorf("mv hint missing:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(h.home, ".config", "paritydesk", "config.toml")); err != nil {
		t.Error("config removed")
	}

	h2 := newHarness(t)
	writeFile(h2, "Library/Application Support/paritydesk/generations/1/manifest.json", "{}")
	if code, out, _ := h2.run("", "uninstall", "--yes", "--keep-backups=false"); code != 0 || strings.Contains(out, "mv ") {
		t.Fatalf("code %d out %q", code, out)
	}
	if _, err := os.Stat(filepath.Join(h2.home, "Library", "Application Support", "paritydesk")); err == nil {
		t.Error("empty support directory kept")
	}
}

func TestUninstallRefusesForeignStateDir(t *testing.T) {
	h := newHarness(t)
	foreign := filepath.Join(h.home, "projects")
	writeFile(h, "projects/important.txt", "keep me")
	if code, _, _ := h.run("", "uninstall", "--yes", "--state-dir", foreign); code == 0 {
		t.Fatal("uninstall removed a directory it does not own")
	}
	if _, err := os.Stat(filepath.Join(foreign, "important.txt")); err != nil {
		t.Fatal("foreign file removed")
	}
}

func TestLogAndHistory(t *testing.T) {
	h := newHarness(t)
	if code, out, _ := h.run("", "log"); code != 0 || !strings.Contains(out, "No runs") {
		t.Fatalf("empty log: %q", out)
	}
	writeLogs(h)
	code, out, _ := h.run("", "log", "--run", "run-0001")
	if code != 0 || !strings.Contains(out, `"phase":"P0"`) || strings.Contains(out, "not json") {
		t.Fatalf("log: %q", out)
	}
	if code, _, _ := h.run("", "log", "--run", "run-9999"); code != 2 {
		t.Fatalf("missing run: code %d", code)
	}
	code, out, _ = h.run("", "history", "--plain")
	if code != 0 || !strings.HasPrefix(out, "RUN\tMODIFIED") || !strings.Contains(out, "run-0002\t") {
		t.Fatalf("history: %q", out)
	}
	if strings.Contains(out, "work\x1b") {
		t.Fatalf("unvalidated alias in history: %q", out)
	}
}

func TestTextOutputIsSanitized(t *testing.T) {
	h := newHarness(t)
	h.svc.status.Profiles[1].Problem = "evil\x1b]52;c;eA==\x07\u202etxt"
	h.svc.status.Warnings = []string{"warn\x1b[31m"}
	code, out, errOut := h.run("", "status")
	if code != 0 {
		t.Fatal(errOut)
	}
	for _, s := range []string{out, errOut} {
		if strings.ContainsAny(s, "\x1b\x07\u202e") {
			t.Fatalf("control characters printed: %q", s)
		}
	}
	h.svc.result = &orchestrator.Result{From: "work", To: "home", Warnings: []string{"w\x1b[2J"}, Plan: &plan.Plan{}}
	_, out, _ = h.run("", "switch", "home", "--json")
	if strings.Contains(out, `\u001b`) {
		t.Fatalf("warning not sanitized in JSON: %q", out)
	}
}

func TestDestructiveLineLastInTextPlan(t *testing.T) {
	h := newHarness(t)
	h.env["PARITYDESK_READONLY"] = "1"
	_, out, _ := h.run("", "plan", "home")
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[len(lines)-2], "DESTRUCTIVE:") {
		t.Fatalf("DESTRUCTIVE is not the last summary line:\n%s", out)
	}
}

// TestDeletingCommandsRefuseOpenRuns checks that backups prune and purge
// and uninstall delete nothing while a run is open, a profile add waits
// for its sign-in, or another process holds the run lock.
func TestDeletingCommandsRefuseOpenRuns(t *testing.T) {
	commands := [][]string{{"backups", "prune"}, {"backups", "purge", "--yes"}, {"uninstall", "--yes"}}
	tests := []struct {
		name     string
		setup    func(t *testing.T, st *state.Store, root *state.Root) func()
		wantCode int
	}{
		{name: "open run", wantCode: 41, setup: func(t *testing.T, st *state.Store, root *state.Root) func() {
			root.Run = &state.Run{ID: "run-0001", Kind: state.RunSwitch, From: "work", To: "home", Generation: 3}
			commitRoot(t, st, root)
			return func() {}
		}},
		{name: "pending profile add", wantCode: 41, setup: func(t *testing.T, st *state.Store, root *state.Root) func() {
			root.PendingAdd = &state.PendingAdd{Alias: "home", ParkedFrom: "work"}
			commitRoot(t, st, root)
			return func() {}
		}},
		{name: "run lock busy", wantCode: 11, setup: func(t *testing.T, st *state.Store, root *state.Root) func() {
			commitRoot(t, st, root)
			l, err := st.Lock(context.Background(), 0)
			if err != nil {
				t.Fatal(err)
			}
			return func() { _ = l.Release() }
		}},
	}
	for _, tc := range tests {
		for _, args := range commands {
			t.Run(tc.name+"/"+strings.Join(args, " "), func(t *testing.T) {
				h := newHarness(t)
				paths := config.ResolvePaths(h.home, func(k string) string { return h.env[k] }, config.Overrides{})
				dataRoot := filepath.Join(h.home, "Library", "Application Support")
				st, err := state.Open(paths.StateDir, state.LockFile(paths.SupportDir, dataRoot), fsx.Default(), clock.System{})
				if err != nil {
					t.Fatal(err)
				}
				done := tc.setup(t, st, state.NewRoot(dataRoot))
				defer done()
				code, _, _ := h.run("", args...)
				if code != tc.wantCode {
					t.Fatalf("code %d, want %d", code, tc.wantCode)
				}
				if h.gens.purged || h.gens.pruned != nil {
					t.Error("generations changed")
				}
				if _, err := os.Stat(filepath.Join(paths.StateDir, "root.json")); err != nil {
					t.Errorf("the journal was removed: %v", err)
				}
			})
		}
	}
}

func commitRoot(t *testing.T, st *state.Store, root *state.Root) {
	t.Helper()
	if err := st.Commit(root); err != nil {
		t.Fatal(err)
	}
}

// TestJSONOutputHidesHome checks that documents meant to be attached to
// issues never carry the home directory: doctor (hook binary path) and
// a --json error whose OS error names a path under home.
func TestJSONOutputHidesHome(t *testing.T) {
	tests := []struct {
		name  string
		setup func(h *harness)
		args  []string
	}{
		{"doctor", nil, []string{"--json", "doctor"}},
		{"doctor text", nil, []string{"doctor"}},
		{"error document", func(h *harness) {
			if err := os.MkdirAll(filepath.Join(h.home, ".config", "paritydesk", "config.toml"), 0o700); err != nil {
				t.Fatal(err)
			}
		}, []string{"--json", "config", "show"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			if tc.setup != nil {
				tc.setup(h)
			}
			_, out, errOut := h.run("", tc.args...)
			if out == "" {
				t.Fatal("no output")
			}
			if strings.Contains(out, h.home) || strings.Contains(errOut, h.home) {
				t.Fatalf("home directory in output: %s %s", out, errOut)
			}
		})
	}
}

// TestVerifyQuietWithoutChrome: Claude in Chrome connects on first use, not
// at launch, so verify says nothing about it until a connection is seen.
func TestVerifyQuietWithoutChrome(t *testing.T) {
	for _, seen := range []bool{false, true} {
		h := newHarness(t)
		h.svc.verify.BridgeSeen = seen
		code, out, errOut := h.run("", "verify")
		if code != 0 {
			t.Fatalf("seen=%v: code %d, stderr %q", seen, code, errOut)
		}
		all := out + errOut
		if got := strings.Contains(all, "Claude in Chrome"); got != seen {
			t.Errorf("seen=%v: Chrome line shown=%v in %q", seen, got, all)
		}
	}
}
