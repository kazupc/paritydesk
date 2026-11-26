package cli

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/orchestrator"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/redact"
	"github.com/kazupc/paritydesk/internal/runner"
)

// TestDetachedJobWritesResult checks that the launchd job (--from-runner
// with a run id in the environment) writes its result file and removes
// its plist, and that an ordinary run writes nothing.
func TestDetachedJobWritesResult(t *testing.T) {
	const id = "job-0001-abcdef"
	tests := []struct {
		name       string
		fromRunner bool
		runID      string
		svcErr     error
		wantResult bool
		wantCode   int
	}{
		{"job succeeds", true, id, nil, true, 0},
		{"job fails", true, id, pderr.New(pderr.SessionsBusy, "switch", ""), true, int(pderr.SessionsBusy)},
		{"no run id", true, "", nil, false, 0},
		{"invalid run id", true, "BAD ID", nil, false, 0},
		{"not the job", false, id, nil, false, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			writeExecutable(t, h.exe)
			if tc.runID != "" {
				h.env[runner.EnvRunID] = tc.runID
			}
			if tc.svcErr != nil {
				h.svc.errs["switch"] = tc.svcErr
			}
			paths := config.ResolvePaths(h.home, func(k string) string { return h.env[k] }, config.Overrides{})
			plist := filepath.Join(paths.RunDir, runner.LabelPrefix+id+".plist")
			if err := os.MkdirAll(paths.RunDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(plist, []byte("<plist/>"), 0o600); err != nil {
				t.Fatal(err)
			}
			args := []string{"switch", "home"}
			if tc.fromRunner {
				args = []string{"switch", "--" + orchestrator.FlagFromRunner, "--" + orchestrator.FlagYes, "home"}
			}
			h.run("", args...)
			res, err := runner.ReadResult(filepath.Join(paths.RunDir, id+".result.json"))
			if !tc.wantResult {
				if err == nil {
					t.Fatalf("unexpected result file: %+v", res)
				}
				return
			}
			if err != nil {
				t.Fatalf("result file: %v", err)
			}
			if res.RunID != id || res.Code != tc.wantCode {
				t.Fatalf("result %+v", res)
			}
			if tc.wantCode != 0 && res.ErrorID != pderr.ID(pderr.Code(tc.wantCode)) {
				t.Fatalf("error id %q", res.ErrorID)
			}
			if _, err := os.Stat(plist); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("plist not removed: %v", err)
			}
		})
	}
}

func writeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
}

// TestSwitchWritesRunLog checks that a switch leaves a redacted run log
// that history and log read back.
func TestSwitchWritesRunLog(t *testing.T) {
	h := newHarness(t)
	code, _, _ := h.run("", "switch", "--"+orchestrator.FlagYes, "home")
	if code != 0 {
		t.Fatalf("switch exit %d", code)
	}
	paths := config.ResolvePaths(h.home, func(k string) string { return h.env[k] }, config.Overrides{})
	info, err := os.Lstat(filepath.Join(paths.LogDir, "run-0001.jsonl"))
	if err != nil {
		t.Fatalf("run log: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("run log mode %v", info.Mode().Perm())
	}
	code, out, _ := h.run("", "--json", "history")
	if code != 0 || !strings.Contains(out, `"run":"run-0001"`) || !strings.Contains(out, `"code":0`) {
		t.Fatalf("history exit %d: %s", code, out)
	}
}

// TestRunLogRedactsMessages checks that home paths and UUIDs never reach
// a run log.
func TestRunLogRedactsMessages(t *testing.T) {
	home := "/Users/someone"
	ev := []loggedEvent{{Event: orchestrator.Event{Phase: orchestrator.P3Quit, Status: "warn",
		Message: i18n.Raw("folder " + home + "/Library/x id 00000000-0000-4000-8000-000000000001")}}}
	res := &orchestrator.Result{RunID: "run-0002", From: "a", To: "b", Code: pderr.OK}
	got := string(runLogLines(res, ev, redact.Redactor{Home: home}, fixedTime))
	if strings.Contains(got, home) || strings.Contains(got, "00000000-0000-4000-8000-000000000001") {
		t.Fatalf("log not redacted: %s", got)
	}
	if n := strings.Count(got, "\n"); n != 2 {
		t.Fatalf("lines: %d", n)
	}
}

// TestRunLogRecordsMessageAliases: an event that names profile aliases
// records them, so a support bundle can replace them later; invalid
// values are dropped.
func TestRunLogRecordsMessageAliases(t *testing.T) {
	ev := []loggedEvent{{Event: orchestrator.Event{Phase: orchestrator.P10Commit, Status: "ok",
		Message: i18n.Raw("removed client-x; its folder was left as it is"),
		Data:    map[string]any{orchestrator.EventAliases: []string{"client-x", "Not An Alias"}}}}}
	res := &orchestrator.Result{RunID: "run-0003", From: "a", To: "b", Code: pderr.OK}
	got := string(runLogLines(res, ev, redact.Redactor{Home: "/Users/someone"}, fixedTime))
	if !strings.Contains(got, `"aliases":["client-x"]`) || strings.Contains(got, "Not An Alias") {
		t.Fatalf("aliases not recorded: %s", got)
	}
}
