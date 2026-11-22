package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kazupc/paritydesk/internal/e2e"
	"github.com/kazupc/paritydesk/internal/orchestrator"
	"github.com/kazupc/paritydesk/internal/pderr"
)

// Fault injection for the recovery drill and the end-to-end tests.
const (
	envFault = "PARITYDESK_FAULT"
	// envE2E is set by the e2e harness (internal/e2e) for the paritydesk
	// runs that may use the pause fault.
	envE2E = "PARITYDESK_E2E"
	// pauseLimit bounds a pause: when nobody kills the paused process,
	// it exits with code Internal, still without any cleanup.
	pauseLimit = 2 * time.Minute
	// PausedStatus is the progress status of the pause fault's event.
	PausedStatus = "paused"
)

// crashPhases are the phases whose intents consult Deps.Crash. P10 closes
// the run in one commit and P11 runs after it, so they have none. A run
// may still skip a listed phase (P0 without a staged update, P6 and P7
// for an empty plan, P5 to P7 when only the folders are swapped, P8 and
// P9 in a sync, P9 after the fallback swap); the fault then stops the
// run just before P10 instead (crashAt).
var crashPhases = []orchestrator.Phase{
	orchestrator.P0Preflight, orchestrator.P3Quit, orchestrator.P5Harvest, orchestrator.P6Backup,
	orchestrator.P7Apply, orchestrator.P8Swap, orchestrator.P9Park,
}

// faults are the hooks PARITYDESK_FAULT asks for.
type faults struct {
	// crash is Deps.Crash.
	crash func(orchestrator.Phase) bool
	// afterFirstWrite is apply.Applier.AfterFirstWrite.
	afterFirstWrite func()
}

// faultEnv is what parseFaults needs to know about the run.
type faultEnv struct {
	getenv func(string) string
	home   string // $HOME of this process
	// realHome returns the user database's home (e2e.RealHome).
	realHome func() (string, error)
	bundleID string // the configured bundle id
	dataRoot string
	ui       orchestrator.UI
	// sleep and exit end a pause nobody killed; tests replace them.
	sleep func(time.Duration)
	exit  func(int)
}

// parseFaults reads PARITYDESK_FAULT:
//
//	after:<phase>  the recovery drill (docs/release-checklist.md): the run
//	               stops right after that phase's intent, as if killed.
//	               The phase must have intents (crashPhases); any other
//	               phase is refused. A switch or sync that skips the
//	               phase stops just before P10 instead, with a warning.
//	pause:P7       end-to-end tests only: P7 pauses once its first write
//	               has landed in the entering folder, reports a "paused"
//	               progress event and waits to be killed (internal/e2e
//	               sends SIGKILL). Refused unless e2eGuard passes.
//
// Unset or empty means no faults. Anything else is a usage error, so a
// mistyped fault never runs a real switch; it also makes every command
// that builds the orchestrator (status and recover included) exit 2
// until the variable is fixed or unset.
func parseFaults(fe faultEnv) (faults, error) {
	if fe.getenv == nil {
		return faults{}, nil
	}
	v := fe.getenv(envFault)
	if v == "" {
		return faults{}, nil
	}
	kind, arg, _ := strings.Cut(v, ":")
	phase := orchestrator.Phase(arg)
	switch kind {
	case "after":
		for _, p := range crashPhases {
			if p == phase {
				return faults{crash: crashAt(phase, fe.ui)}, nil
			}
		}
		return faults{}, usageErrorf("%s=after:%s: that phase has no intent to stop after; use one of %s", envFault, arg, phaseList(crashPhases))
	case "pause":
		if phase != orchestrator.P7Apply {
			return faults{}, usageErrorf("%s=pause:%s: only pause:%s is supported", envFault, arg, orchestrator.P7Apply)
		}
		if err := e2eGuard(fe); err != nil {
			return faults{}, usageErrorf("%s=pause:%s is refused: %v", envFault, arg, err)
		}
		return faults{afterFirstWrite: pauseHook(fe)}, nil
	}
	return faults{}, usageErrorf("%s=%q is not understood; use after:<phase> or pause:%s", envFault, v, orchestrator.P7Apply)
}

// crashAt is Deps.Crash for after:<phase>: it stops the run after that
// phase's intent. A switch or sync consults it once more with P10Commit
// before closing the run; reaching that point means the run skipped the
// phase, so it stops there instead and says so, rather than finishing a
// real switch the drill did not ask for.
func crashAt(phase orchestrator.Phase, ui orchestrator.UI) func(orchestrator.Phase) bool {
	return func(after orchestrator.Phase) bool {
		if after == phase {
			return true
		}
		if after != orchestrator.P10Commit {
			return false
		}
		if ui != nil {
			ui.Progress(orchestrator.Event{Phase: orchestrator.P10Commit, Status: "warn",
				Message: fmt.Sprintf("fault injection: this run skipped %s, so %s=after:%s stops it before %s instead",
					phase, envFault, phase, orchestrator.P10Commit)})
		}
		return true
	}
}

func phaseList(ps []orchestrator.Phase) string {
	s := make([]string, len(ps))
	for i, p := range ps {
		s[i] = string(p)
	}
	return strings.Join(s, ", ")
}

// e2eGuard allows the pause fault only inside the e2e harness: the
// harness's marker variable is set, e2e.CheckTarget accepts the bundle id
// and $HOME (not the real app's vendor, not the real home or a parent of
// it, not under its Library), both as given and with symlinks resolved,
// and the data root lies inside $HOME, also with symlinks resolved.
func e2eGuard(fe faultEnv) error {
	if fe.getenv(envE2E) != "1" {
		return fmt.Errorf("only the end-to-end tests may use it (%s=1 is not set)", envE2E)
	}
	if fe.realHome == nil {
		return fmt.Errorf("the real home directory is unknown")
	}
	realHome, err := fe.realHome()
	if err != nil {
		return err
	}
	if err := e2e.CheckTarget(fe.bundleID, fe.home, realHome); err != nil {
		return err
	}
	if !filepath.IsAbs(fe.home) || !filepath.IsAbs(fe.dataRoot) || !filepath.IsAbs(realHome) {
		return fmt.Errorf("HOME, the data root and the real home must be absolute paths")
	}
	home, root, real := resolvePath(fe.home), resolvePath(fe.dataRoot), resolvePath(realHome)
	if err := e2e.CheckTarget(fe.bundleID, home, real); err != nil {
		return err
	}
	for _, hr := range [][2]string{{filepath.Clean(fe.home), filepath.Clean(fe.dataRoot)}, {home, root}} {
		if !strings.HasPrefix(hr[1], hr[0]+string(filepath.Separator)) {
			return fmt.Errorf("the data root is not inside HOME")
		}
	}
	return nil
}

// resolvePath returns the absolute, clean path p with symlinks resolved
// in its longest existing prefix; the rest, which does not exist yet,
// cannot be a symlink.
func resolvePath(p string) string {
	p = filepath.Clean(p)
	rest := ""
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(p, rest)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

// pauseHook reports the pause and blocks until the process is killed. A
// pause nobody ends within pauseLimit exits with code Internal, without
// cleanup, as a kill would leave things.
func pauseHook(fe faultEnv) func() {
	sleep, exit := fe.sleep, fe.exit
	var once sync.Once
	return func() {
		once.Do(func() {
			if fe.ui != nil {
				fe.ui.Progress(orchestrator.Event{Phase: orchestrator.P7Apply, Status: PausedStatus,
					Message: "fault injection: paused after the first write; waiting to be killed"})
			}
			sleep(pauseLimit)
			if fe.ui != nil {
				fe.ui.Progress(orchestrator.Event{Phase: orchestrator.P7Apply, Status: "fail",
					Message: "fault injection: the paused run was not killed; exiting without cleanup"})
			}
			exit(int(pderr.Internal))
		})
	}
}
