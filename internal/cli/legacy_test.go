package cli

import (
	"bytes"
	"flag"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/orchestrator"
)

func TestIsHidden(t *testing.T) {
	fs := flag.NewFlagSet("x", flag.ContinueOnError)
	fs.Bool("apply", false, hiddenUsage+"internal")
	other := flag.NewFlagSet("y", flag.ContinueOnError)
	other.Bool("apply", false, "apply the thing")
	if !isHidden(fs.Lookup("apply")) || isHidden(other.Lookup("apply")) {
		t.Fatal("hiding must be per flag, not per name")
	}
	var b bytes.Buffer
	printFlags(&b, other, "flags", func(string) bool { return true })
	if !strings.Contains(b.String(), "--apply") {
		t.Fatalf("a visible --apply on another command is listed: %q", b.String())
	}
	b.Reset()
	printFlags(&b, fs, "flags", func(string) bool { return true })
	if strings.Contains(b.String(), "apply") {
		t.Fatalf("hidden flag listed: %q", b.String())
	}
	if len(visibleFlags(func(f *flag.FlagSet) { f.Bool("apply", false, hiddenUsage+"x") })) != 0 ||
		len(visibleFlags(func(f *flag.FlagSet) { f.Bool("apply", false, "x") })) != 1 {
		t.Fatal("completion does not use isHidden")
	}
}

// The legacy flags appear in no usage text, help or completion.
func TestLegacyFlagsHidden(t *testing.T) {
	h := newHarness(t)
	_, out, _ := h.run("", "profile", "adopt", "--help")
	for _, n := range append(legacyOnlyFlags, flagFromLegacy) {
		if strings.Contains(out, n) {
			t.Errorf("usage lists --%s", n)
		}
	}
	for _, shell := range completionShells {
		s, err := completionScript(shell)
		if err != nil {
			t.Fatal(err)
		}
		// `artifacts import-legacy --apply` is a visible flag of its own
		// (spec-artifacts 9); only the profile adopt flags must be hidden.
		var kept []string
		for _, line := range strings.Split(s, "\n") {
			if !strings.Contains(line, "artifacts import-legacy") {
				kept = append(kept, line)
			}
		}
		s = strings.Join(kept, "\n")
		for _, n := range append(legacyOnlyFlags, flagFromLegacy) {
			if strings.Contains(s, "--"+n) || strings.Contains(s, "-l "+n+" ") {
				t.Errorf("%s completion offers %s", shell, n)
			}
		}
	}
}

func TestLegacyUsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
		code int
		want string
	}{
		{"positional with from-legacy", []string{"profile", "adopt", "--from-legacy", "work", "Claude-X"}, 2, "takes no arguments"},
		{"ordinary form, one argument", []string{"profile", "adopt", "work"}, 2, "paritydesk profile adopt <alias> <folder>"},
		{"ordinary form, no argument", []string{"profile", "adopt"}, 2, "paritydesk profile adopt <alias> <folder>"},
		{"legacy flag without from-legacy", []string{"profile", "adopt", "work", "Claude-Work", "--apply", "--unmanaged"}, 2, "needs --from-legacy"},
		{"quarantine without a terminal", []string{"profile", "adopt", "--from-legacy", "--apply", "--quarantine"}, 2, "needs a terminal"},
		{"apply without a plan", []string{"profile", "adopt", "--from-legacy", "--apply", "--unmanaged"}, 3, "--legacy-plan"},
		{"apply without a managed choice", []string{"profile", "adopt", "--from-legacy", "--apply", "--legacy-plan", legacyHash}, 2, "--managed or --unmanaged"},
		{"managed with legacy-managed", []string{"profile", "adopt", "--from-legacy", "--legacy-managed", "home", "--managed"}, 2, "excludes"},
		{"unknown legacy-managed alias", []string{"profile", "adopt", "--from-legacy", "--apply", "--legacy-managed", "nobody",
			"--legacy-plan", legacyHash}, 2, "not a profile this run creates"},
		{"bad alias flag", []string{"profile", "adopt", "--from-legacy", "--legacy-alias", "Other=work"}, 2, "<folder>=<alias>"},
		{"bad alias", []string{"profile", "adopt", "--from-legacy", "--legacy-alias", "Claude=Not An Alias"}, 2, "not a profile alias"},
		{"bad exclude", []string{"profile", "adopt", "--from-legacy", "--legacy-exclude", "../x"}, 2, "legacy-exclude"},
		{"relative state file", []string{"profile", "adopt", "--from-legacy", "--legacy-state", "state.json"}, 2, "absolute"},
		{"bad plan hash", []string{"profile", "adopt", "--from-legacy", "--legacy-plan", "abc"}, 2, "plan hash"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			code, out, errOut := h.run("", tc.args...)
			if code != tc.code || !strings.Contains(out+errOut, tc.want) {
				t.Fatalf("code %d, want %d; output %q", code, tc.code, out+errOut)
			}
			if tc.name == "quarantine without a terminal" && len(h.svc.calls) != 0 {
				t.Fatalf("the service was called before the terminal check: %v", h.svc.calls)
			}
		})
	}
}

func TestLegacyNonInteractiveApply(t *testing.T) {
	h := newHarness(t)
	code, _, errOut := h.run("", "profile", "adopt", "--from-legacy", "--apply", "--legacy-managed", "home",
		"--legacy-alias", "Claude=work", "--legacy-plan", legacyHash)
	if code != 0 {
		t.Fatalf("code %d: %s", code, errOut)
	}
	if len(h.svc.legacyOpts) != 2 {
		t.Fatalf("calls %v", h.svc.legacyOpts)
	}
	dry, apply := h.svc.legacyOpts[0], h.svc.legacyOpts[1]
	if dry.Apply || dry.PlanHash != "" || !apply.Apply || apply.PlanHash != legacyHash || apply.Quarantine {
		t.Fatalf("options %+v %+v", dry, apply)
	}
	if !apply.Managed["home"] || apply.Managed["work"] || len(apply.Managed) != 2 || apply.Aliases["Claude"] != "work" {
		t.Fatalf("managed %v aliases %v", apply.Managed, apply.Aliases)
	}
}

func TestLegacyInteractive(t *testing.T) {
	h := newHarness(t)
	h.tty = true
	h.svc.legacyDry.Folders[0].AliasSource, h.svc.legacyDry.Folders[0].Alias = "", ""
	h.svc.legacyDry.Folders[0].Status, h.svc.legacyDry.Folders[0].Reason = "skip", "needs_alias"
	// Answers: the alias, managed for work and home, then the question.
	code, out, errOut := h.run("work\ny\nn\ny\n", "profile", "adopt", "--from-legacy", "--apply", "--quarantine")
	if code != 0 {
		t.Fatalf("code %d: %s %s", code, out, errOut)
	}
	if len(h.svc.legacyOpts) != 3 {
		t.Fatalf("calls %d", len(h.svc.legacyOpts))
	}
	apply := h.svc.legacyOpts[2]
	if apply.Aliases[ids.DefaultFolder] != "work" || apply.PlanHash != legacyHash || !apply.Quarantine || !apply.Apply {
		t.Fatalf("apply options %+v", apply)
	}
	if !strings.Contains(errOut, i18n.T(i18n.AskLegacyAliasActive)) || !strings.Contains(errOut, "move 2 directories") {
		t.Fatalf("questions missing: %q", errOut)
	}
	// Declined: exit 3, no apply.
	h = newHarness(t)
	h.tty = true
	code, _, _ = h.run("n\nn\nn\n", "profile", "adopt", "--from-legacy", "--apply", "--legacy-alias", "Claude=work")
	if code != 3 || len(h.svc.legacyOpts) != 1 {
		t.Fatalf("declined: code %d calls %d", code, len(h.svc.legacyOpts))
	}
	// A --legacy-plan that differs from the dry run: exit 3.
	h = newHarness(t)
	h.tty = true
	other := strings.Repeat("1", 32) + "." + strings.Repeat("2", 32)
	code, out, _ = h.run("y\ny\ny\n", "profile", "adopt", "--from-legacy", "--apply", "--legacy-plan", other)
	if code != 3 || len(h.svc.legacyOpts) != 1 {
		t.Fatalf("changed plan: code %d %s", code, out)
	}
}

func TestLegacyTextGolden(t *testing.T) {
	for name, res := range map[string]func(h *harness){
		"dry":       func(h *harness) {},
		"nothing":   func(h *harness) { h.svc.legacyDry = legacyNothingFixture() },
		"no_active": func(h *harness) { h.svc.legacyDry = legacyNoActiveFixture() },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			res(h)
			code, out, _ := h.run("", "profile", "adopt", "--from-legacy")
			if code != 0 {
				t.Fatalf("code %d", code)
			}
			lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
			if !strings.HasPrefix(lines[len(lines)-1], "Data folders:") {
				t.Fatalf("the destructive line is not last: %q", lines[len(lines)-1])
			}
			golden(t, "legacy/"+name, out)
		})
	}
	h := newHarness(t)
	h.svc.legacyApply = legacyDegradedFixture()
	h.svc.legacyErr = errDegraded()
	code, out, _ := h.run("", "profile", "adopt", "--from-legacy", "--apply", "--unmanaged", "--legacy-plan", legacyHash)
	if code != 32 {
		t.Fatalf("degraded code %d", code)
	}
	golden(t, "legacy/degraded", out)
	// No label or path of a hint file ever appears: the fixture has none,
	// and the apply command carries explicit aliases only.
	if strings.Contains(out, "legacy-state") {
		t.Fatal("the state file flag leaked into the output")
	}
}

func TestPurgeWarnsAboutPinned(t *testing.T) {
	h := newHarness(t)
	h.gens.pinned = true
	h.tty = true
	code, out, errOut := h.run("y\n", "backups", "purge")
	if code != 0 || !h.gens.purged {
		t.Fatalf("code %d", code)
	}
	warn := i18n.T(i18n.BackupsPinnedWarn, 1)
	i, q := strings.Index(errOut, warn), strings.Index(errOut, i18n.T(i18n.AskPurge))
	if i < 0 || q < 0 || i > q {
		t.Fatalf("warning not before the question: %q %q", out, errOut)
	}
	h = newHarness(t)
	h.gens.pinned = true
	code, out, _ = h.run("", "--json", "backups", "purge", "--yes")
	if code != 0 || !strings.Contains(out, `"pinned":1`) {
		t.Fatalf("json %d %s", code, out)
	}
}

// In Terminal, a --legacy-plan whose inputs half differs (for example the
// printed command plus --quarantine) says the flags differ, not that the
// folders changed.
func TestLegacyInteractivePlanFlagsDiffer(t *testing.T) {
	h := newHarness(t)
	h.tty = true
	folders, _, _ := strings.Cut(legacyHash, ".")
	other := folders + "." + strings.Repeat("2", 32)
	code, out, errOut := h.run("y\ny\ny\n", "profile", "adopt", "--from-legacy", "--apply", "--legacy-plan", other)
	if code != 3 || !strings.Contains(out+errOut, "flags differ") || len(h.svc.legacyOpts) != 1 {
		t.Fatalf("code %d: %s %s", code, out, errOut)
	}
}

// --apply --quarantine needs real terminals: character devices that are
// not terminals (such as /dev/null) do not pass.
func TestLegacyQuarantineNeedsARealTerminal(t *testing.T) {
	h := newHarness(t)
	h.tty = true
	h.sys.isTTY = func(any) bool { return false }
	code, out, errOut := h.run("", "profile", "adopt", "--from-legacy", "--apply", "--quarantine")
	if code != 2 || !strings.Contains(out+errOut, "needs a terminal") || len(h.svc.calls) != 0 {
		t.Fatalf("code %d: %s %s", code, out, errOut)
	}
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !isTerminal(f) || isTTY(f) {
		t.Fatalf("%s: character device %v, terminal %v", os.DevNull, isTerminal(f), isTTY(f))
	}
}

func TestLegacyPrintedDiffersLine(t *testing.T) {
	res := legacyDryFixture()
	res.Warnings = append(res.Warnings, orchestrator.LegacyWarnPrintedDiffers)
	if !slices.Contains(legacyLines(res), i18n.T(i18n.LegacyWarnPrintedDiffers)) {
		t.Fatal("no line for printed_differs")
	}
}
