package app

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/pderr"
)

// Recorded osascript stderr. The -1712 and -10004 lines are verbatim from
// the e2e job's Apple Events diagnostic on a GitHub-hosted macOS runner
// (the stub app installed as Claude.app; scripts/ci/e2e.sh). The -128 line
// is what osascript prints when the target answers the quit event with
// userCanceledErr, as AppKit does when applicationShouldTerminate returns
// terminateCancel; its character range is that of the tell statement in
// scriptQuit. The -1708 line is from the same diagnostic (the curly quotes
// are written as escapes). The -1743, -1744 and -600 lines are
// representative lines in the same format.
const (
	stderrTimeout       = "79:92: execution error: Claude got an error: AppleEvent timed out. (-1712)\n"
	stderrPrivilege     = "79:85: execution error: Claude got an error: A privilege violation occurred. (-10004)\n"
	stderrPrivFinder    = "79:92: execution error: Finder got an error: A privilege violation occurred. (-10004)\n"
	stderrCanceled      = "12:56: execution error: Claude got an error: User canceled. (-128)\n"
	stderrCanceledBare  = "12:56: execution error: User canceled. (-128)\n"
	stderrNotUnderstood = "79:92: execution error: Claude got an error: every window doesn\u2019t understand " +
		"the \u201ccount\u201d message. (-1708)\n"
	stderrDenied     = "12:56: execution error: Not authorized to send Apple events to Claude. (-1743)\n"
	stderrConsent    = "12:56: execution error: Claude got an error: Not authorized to send Apple events to Claude. (-1744)\n"
	stderrNotRunning = "12:56: execution error: Claude got an error: Application isn\u2019t running. (-600)\n"
)

func TestOSAStatusRecorded(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   int
	}{
		{"timeout (CI)", stderrTimeout, -1712},
		{"privilege violation (CI)", stderrPrivilege, -10004},
		{"privilege violation, Finder (CI)", stderrPrivFinder, -10004},
		{"not understood (CI)", stderrNotUnderstood, -1708},
		{"user canceled", stderrCanceled, -128},
		{"user canceled, no app prefix", stderrCanceledBare, -128},
		{"automation denied", stderrDenied, -1743},
		{"automation needs consent", stderrConsent, -1744},
		{"not running", stderrNotRunning, -600},
		{"CRLF", strings.ReplaceAll(stderrTimeout, "\n", "\r\n"), -1712},
		{"number on an earlier line", stderrCanceled + "osascript: trailing note\n", -128},
		{"last numbered line wins", stderrTimeout + stderrDenied, -1743},
		{"canceled without a number", "execution error: User canceled.\n", -128},
		{"cancelled, British spelling", "execution error: User cancelled.", -128},
		{"number inside the text only", "execution error: error (-1743) in the middle.\n", 0},
		{"no number", "osascript: something went wrong\n", 0},
		{"empty", "", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := osaStatus([]byte(tc.stderr)); got != tc.want {
				t.Fatalf("osaStatus(%q) = %d, want %d", tc.stderr, got, tc.want)
			}
		})
	}
}

// TestQuitStderrToOutcome runs recorded stderr through the parser and the
// quit mapping, as ExecRunner and osaController.Quit do.
func TestQuitStderrToOutcome(t *testing.T) {
	tests := []struct {
		name       string
		stderr     string
		code       pderr.Code
		automation bool // ErrAutomationDenied
		refused    bool // ErrQuitRefused
		notDeliv   bool // ErrQuitNotDelivered
		notRunning bool // ErrNotRunning
		timedOut   bool // ErrEventTimedOut
	}{
		{"refused (-128)", stderrCanceled, pderr.AppDidNotQuit, false, true, false, false, false},
		{"refused, no app prefix", stderrCanceledBare, pderr.AppDidNotQuit, false, true, false, false, false},
		{"refused, no number", "execution error: User canceled.\n", pderr.AppDidNotQuit, false, true, false, false, false},
		{"timeout (-1712, CI)", stderrTimeout, pderr.AppDidNotQuit, false, false, false, false, true},
		{"privilege violation (-10004, CI)", stderrPrivilege, pderr.AppDidNotQuit, false, false, true, false, false},
		{"not understood (-1708, CI)", stderrNotUnderstood, pderr.AppDidNotQuit, false, false, true, false, false},
		{"automation denied (-1743)", stderrDenied, pderr.AutomationMissing, true, false, true, false, false},
		{"automation needs consent (-1744)", stderrConsent, pderr.AutomationMissing, true, false, true, false, false},
		{"not running (-600)", stderrNotRunning, pderr.Internal, false, false, false, true, false},
		{"no number", "osascript: something went wrong\n", pderr.AppDidNotQuit, false, false, true, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ce := &CommandError{Template: TmplQuit, ExitCode: 1, OSAStatus: osaStatus([]byte(tc.stderr))}
			run := &fakeRunner{t: t, reply: func(Template, []string) ([]byte, error) { return nil, ce }}
			err := osaController{run: run}.Quit(t.Context(), testBundle)
			if err == nil {
				t.Fatal("no error")
			}
			if got := pderr.CodeOf(err); got != tc.code {
				t.Fatalf("code %d, want %d (%v)", got, tc.code, err)
			}
			for _, c := range []struct {
				target error
				want   bool
			}{
				{ErrAutomationDenied, tc.automation},
				{ErrQuitRefused, tc.refused},
				{ErrQuitNotDelivered, tc.notDeliv},
				{ErrNotRunning, tc.notRunning},
				{ErrEventTimedOut, tc.timedOut},
			} {
				if errors.Is(err, c.target) != c.want {
					t.Fatalf("errors.Is(%v, %v) = %v, want %v", err, c.target, !c.want, c.want)
				}
			}
			// The raw error (and so the AppleScript number) stays in the
			// chain for the quit step's message, except for "not running".
			var got *CommandError
			if !tc.notRunning && (!errors.As(err, &got) || got.OSAStatus != ce.OSAStatus) {
				t.Fatalf("%v does not keep the command error", err)
			}
		})
	}
}

func TestClassifyOSAOnlyTCCIsAutomation(t *testing.T) {
	for _, n := range []int{0, -1, -128, -600, -609, -903, -1700, -1708, -1712, -1728, -1742, -1745, -10000, -10004} {
		if classifyOSA(n) == osaAutomation {
			t.Errorf("%d classified as missing Automation", n)
		}
	}
	for _, n := range []int{osaNotPermitted, osaWouldNeedUser} {
		if classifyOSA(n) != osaAutomation {
			t.Errorf("%d not classified as missing Automation", n)
		}
	}
	if classifyOSA(osaPrivilegeError) != osaOther || classifyOSA(osaTimeout) != osaTimedOut ||
		classifyOSA(osaUserCanceled) != osaRefused || classifyOSA(osaProcNotFound) != osaNotRunning {
		t.Error("classification of -10004, -1712, -128 or -600 changed")
	}
}

func TestProbeMapping(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   pderr.Code
		denied bool
	}{
		{-1743, pderr.AutomationMissing, true},
		{-1744, pderr.AutomationMissing, true},
		{-10004, pderr.Internal, false},
		{-1712, pderr.Internal, false},
		{-128, pderr.Internal, false},
	} {
		run := &fakeRunner{t: t, reply: func(Template, []string) ([]byte, error) {
			return nil, &CommandError{Template: TmplProbe, ExitCode: 1, OSAStatus: tc.status}
		}}
		err := osaController{run: run}.ProbeAutomation(t.Context(), testBundle)
		if pderr.CodeOf(err) != tc.code || errors.Is(err, ErrAutomationDenied) != tc.denied ||
			errors.Is(err, ErrQuitNotDelivered) || errors.Is(err, ErrQuitRefused) ||
			errors.Is(err, ErrEventTimedOut) != (tc.status == osaTimeout) {
			t.Errorf("%d: got %v", tc.status, err)
		}
	}
}

// refusingFixture is a quit fixture whose quit event is answered with
// -128; logVeto makes the app log its quit-vetoed line first, as the stub
// in cancel mode (and the real app's dialog) does.
func refusingFixture(t *testing.T, exitAfter time.Duration, logVeto bool) *quitFixture {
	f := newQuitFixture(t, exitAfter, false)
	f.run.reply = func(tm Template, _ []string) ([]byte, error) {
		if tm != TmplQuit {
			return nil, nil
		}
		if logVeto {
			f.log.append(stamp("[quit] refused by interceptor"))
		}
		return nil, &CommandError{Template: TmplQuit, ExitCode: 1, OSAStatus: osaStatus([]byte(stderrCanceled))}
	}
	return f
}

func TestQuitAndWaitRefused(t *testing.T) {
	noVetoSig := testSig
	noVetoSig.QuitVetoed = ""
	tests := []struct {
		name      string
		exitAfter time.Duration
		logVeto   bool
		sig       compat.Signals
		exited    bool
		vetoed    bool
	}{
		{"vetoed in main.log, keeps running", 0, true, testSig, false, true},
		{"vetoed in main.log, quits by itself", 2 * time.Second, true, testSig, true, true},
		{"no veto line although the signal is known", 0, false, testSig, false, false},
		{"no veto line, quits by itself", 2 * time.Second, false, testSig, true, false},
		// Without the signal a refusal is not reported as the dialog: a
		// -128 reply is the app's answer, so no dialog need be open.
		{"veto signal unknown", 0, false, noVetoSig, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := refusingFixture(t, tc.exitAfter, tc.logVeto)
			vetoes := 0
			res, err := f.p.QuitAndWait(t.Context(), testBundle, testBundlePath, tc.sig, 3*time.Second, func() { vetoes++ })
			if !res.Refused || res.NoReply || res.Vetoed != tc.vetoed || res.Exited != tc.exited || vetoes != map[bool]int{true: 1}[tc.vetoed] {
				t.Fatalf("result %+v vetoes %d", res, vetoes)
			}
			if !slices.Equal(f.run.templates(), []Template{TmplQuit}) {
				t.Fatalf("commands after the quit event: %v", f.run.templates())
			}
			if tc.exited {
				if err != nil || res.Pending {
					t.Fatalf("err %v pending %v", err, res.Pending)
				}
				return
			}
			if pderr.CodeOf(err) != pderr.AppDidNotQuit || !errors.Is(err, ErrQuitRefused) ||
				errors.Is(err, ErrAutomationDenied) || errors.Is(err, ErrQuitNotDelivered) {
				t.Fatalf("err %v (code %d)", err, pderr.CodeOf(err))
			}
			// Still running after a refusal: a late quit is possible, so
			// the caller watches for it, with or without the dialog.
			if !res.Pending {
				t.Fatal("not pending after a refusal")
			}
			if res.Elapsed < 3*time.Second {
				t.Fatalf("gave up after %v, before the timeout", res.Elapsed)
			}
		})
	}
}

// TestQuitAndWaitEventTimeout: a quit event without a reply in time
// (-1712) may have reached the app (it can hold the reply while its
// dialog is open), so it is waited on like a refusal and never reported
// as undelivered or as missing Automation.
func TestQuitAndWaitEventTimeout(t *testing.T) {
	tests := []struct {
		name      string
		exitAfter time.Duration
		logVeto   bool
		exited    bool
	}{
		{"keeps running, dialog in main.log", 0, true, false},
		{"keeps running, no dialog", 0, false, false},
		{"quits after the reply timed out", 2 * time.Second, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newQuitFixture(t, tc.exitAfter, false)
			f.run.reply = func(tm Template, _ []string) ([]byte, error) {
				if tc.logVeto {
					f.log.append(stamp("[quit] refused by interceptor"))
				}
				return nil, &CommandError{Template: TmplQuit, ExitCode: 1, OSAStatus: osaStatus([]byte(stderrTimeout))}
			}
			vetoes := 0
			res, err := f.p.QuitAndWait(t.Context(), testBundle, testBundlePath, testSig, 3*time.Second, func() { vetoes++ })
			if !res.NoReply || res.Refused || res.Vetoed != tc.logVeto || vetoes != map[bool]int{true: 1}[tc.logVeto] || res.Exited != tc.exited {
				t.Fatalf("result %+v vetoes %d", res, vetoes)
			}
			if !slices.Equal(f.run.templates(), []Template{TmplQuit}) {
				t.Fatalf("commands after the quit event: %v", f.run.templates())
			}
			if tc.exited {
				if err != nil || res.Pending {
					t.Fatalf("err %v pending %v", err, res.Pending)
				}
				return
			}
			if pderr.CodeOf(err) != pderr.AppDidNotQuit || !errors.Is(err, ErrEventTimedOut) || errors.Is(err, ErrQuitNotDelivered) ||
				errors.Is(err, ErrAutomationDenied) || errors.Is(err, ErrQuitRefused) {
				t.Fatalf("err %v", err)
			}
			if !res.Pending || res.Elapsed < 3*time.Second {
				t.Fatalf("pending %v after %v", res.Pending, res.Elapsed)
			}
		})
	}
}

// TestQuitAndWaitUndelivered: -10004 and other failures before the app
// could answer return at once, without a wait.
func TestQuitAndWaitUndelivered(t *testing.T) {
	f := newQuitFixture(t, 0, false)
	f.run.reply = func(Template, []string) ([]byte, error) {
		return nil, &CommandError{Template: TmplQuit, ExitCode: 1, OSAStatus: osaStatus([]byte(stderrPrivilege))}
	}
	res, err := f.p.QuitAndWait(t.Context(), testBundle, testBundlePath, testSig, time.Minute, nil)
	if pderr.CodeOf(err) != pderr.AppDidNotQuit || !errors.Is(err, ErrQuitNotDelivered) ||
		errors.Is(err, ErrAutomationDenied) || errors.Is(err, ErrQuitRefused) || res != (QuitResult{}) {
		t.Fatalf("got %+v %v", res, err)
	}
	if !f.clk.Now().Equal(t0) {
		t.Fatalf("waited %v after a failed quit event", f.clk.Now().Sub(t0))
	}
}
