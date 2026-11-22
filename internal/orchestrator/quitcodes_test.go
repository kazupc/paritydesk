package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/app"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/pderr"
)

// TestQuitFailureExitCodes checks the exit code and message of each way
// the quit event can fail, with errors and results shaped as
// app.Platform.QuitAndWait returns them. Only missing Automation (-1743,
// -1744) exits 15. A refusal (-128) and an unanswered event (-1712) are
// waited on and then watched for a late quit; a veto among them sets
// QuitPending. A privilege violation (-10004) and an unexplained failure
// exit 10 at once. Every exit 10 keeps the raw error and names its
// number.
func TestQuitFailureExitCodes(t *testing.T) {
	osa := func(n int) error { return &app.CommandError{Template: app.TmplQuit, ExitCode: 1, OSAStatus: n} }
	notDelivered := func(e error) error { return fmt.Errorf("%w: %w", app.ErrQuitNotDelivered, e) }
	// timedOut is QuitAndWait's error at its timeout after a refused or
	// unanswered quit event (quitErr, as osaController.Quit maps it).
	timedOut := func(quitErr error) error {
		return &pderr.Error{Code: pderr.AppDidNotQuit, Op: "app/quit", Msg: "the app did not quit before the timeout", Err: quitErr}
	}
	refused := pderr.Wrap(pderr.AppDidNotQuit, "app/quit", fmt.Errorf("%w: %w", app.ErrQuitRefused, osa(-128)))
	noReply := pderr.Wrap(pderr.AppDidNotQuit, "app/quit", fmt.Errorf("%w: %w", app.ErrEventTimedOut, osa(-1712)))
	tests := []struct {
		name     string
		err      error
		res      app.QuitResult
		lateExit bool
		want     pderr.Code
		mention  []string
		watched  bool // the late-quit watch ran
		pending  bool // Result.QuitPending
		relaunch bool // Claude quit late and was relaunched
	}{
		{name: "automation denied", err: pderr.Wrap(pderr.AutomationMissing, "app/quit",
			notDelivered(fmt.Errorf("%w: %w", app.ErrAutomationDenied, osa(-1743)))), want: pderr.AutomationMissing, mention: []string{"-1743"}},
		{name: "automation needs consent", err: pderr.Wrap(pderr.AutomationMissing, "app/quit",
			notDelivered(fmt.Errorf("%w: %w", app.ErrAutomationDenied, osa(-1744)))), want: pderr.AutomationMissing, mention: []string{"-1744"}},
		{name: "refused, no dialog in main.log", err: timedOut(refused),
			res:  app.QuitResult{Refused: true, Pending: true},
			want: pderr.AppDidNotQuit, mention: []string{"Claude refused to quit", "-128", "still running"}, watched: true},
		{name: "refused, then quit late", err: timedOut(refused),
			res: app.QuitResult{Refused: true, Pending: true}, lateExit: true,
			want: pderr.AppDidNotQuit, mention: []string{"quit late"}, watched: true, relaunch: true},
		{name: "refused with the dialog", err: timedOut(refused),
			res:  app.QuitResult{Refused: true, Vetoed: true, Pending: true},
			want: pderr.AppDidNotQuit, mention: []string{"still asking whether to quit"}, watched: true, pending: true},
		{name: "no reply in time", err: timedOut(noReply),
			res:  app.QuitResult{NoReply: true, Pending: true},
			want: pderr.AppDidNotQuit, mention: []string{"did not answer the quit event", "-1712", "grant-automation"}, watched: true},
		{name: "privilege violation", err: pderr.Wrap(pderr.AppDidNotQuit, "app/quit", notDelivered(osa(-10004))),
			want: pderr.AppDidNotQuit, mention: []string{"-10004", "sandbox"}},
		{name: "no error number", err: pderr.Wrap(pderr.AppDidNotQuit, "app/quit", notDelivered(osa(0))),
			want: pderr.AppDidNotQuit, mention: []string{"the quit event to Claude failed"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.app.quitErr, e.app.quitRes, e.app.lateExit = tc.err, tc.res, tc.lateExit
			res, err := e.orch().Switch(context.Background(), yes("b"))
			if got := codeOf(t, err); got != tc.want {
				t.Fatalf("code %d, want %d (%v)", got, tc.want, err)
			}
			// The message shown is the first Msg in the chain, or the
			// whole error without one.
			shown := err.Error()
			var pe *pderr.Error
			if errors.As(err, &pe) && pe.Msg != "" {
				shown = pe.Msg
			}
			for _, m := range tc.mention {
				if !strings.Contains(shown, m) {
					t.Errorf("message %q does not mention %q (%v)", shown, m, err)
				}
			}
			if tc.want == pderr.AppDidNotQuit && !errors.Is(err, tc.err) {
				t.Errorf("%v does not keep the quit error", err)
			}
			if (e.app.watches > 0) != tc.watched || res.QuitPending != tc.pending {
				t.Errorf("watches %d pending %v", e.app.watches, res.QuitPending)
			}
			wantLaunches := 0
			if tc.relaunch {
				wantLaunches = 1
			}
			if !e.app.running || e.app.launches != wantLaunches {
				t.Errorf("running %v launches %d", e.app.running, e.app.launches)
			}
			if len(e.gens.gens) != 0 || e.account(ids.DefaultFolder) != acctA {
				t.Error("something was written")
			}
			e.checkInvariants(tc.name)
		})
	}
}

// TestPreflightProbeTimeout: a probe without a reply in time (-1712) is
// not missing Automation, but the run says what to check.
func TestPreflightProbeTimeout(t *testing.T) {
	e := newEnv(t)
	e.app.autoErr = pderr.Wrap(pderr.Internal, "app/probe", fmt.Errorf("%w: %w", app.ErrEventTimedOut,
		&app.CommandError{Template: app.TmplProbe, ExitCode: 1, OSAStatus: -1712}))
	res, err := e.orch().Switch(context.Background(), yes("b"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range res.Warnings {
		found = found || strings.Contains(w, "-1712")
	}
	if !found {
		t.Errorf("no warning about the unanswered probe: %v", res.Warnings)
	}
}
