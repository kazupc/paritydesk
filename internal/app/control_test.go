package app

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/pderr"
)

func TestQuitErrorMapping(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		is       []error
		notIs    []error
		code     pderr.Code
		wantNil  bool
		checkErr bool
	}{
		{name: "delivered", wantNil: true},
		{name: "automation denied", err: &CommandError{Template: TmplQuit, ExitCode: 1, OSAStatus: -1743},
			is: []error{ErrAutomationDenied, ErrQuitNotDelivered}, notIs: []error{ErrQuitRefused}, code: pderr.AutomationMissing},
		{name: "automation needs consent", err: &CommandError{Template: TmplQuit, ExitCode: 1, OSAStatus: -1744},
			is: []error{ErrAutomationDenied, ErrQuitNotDelivered}, code: pderr.AutomationMissing},
		{name: "refused", err: &CommandError{Template: TmplQuit, ExitCode: 1, OSAStatus: -128},
			is: []error{ErrQuitRefused}, notIs: []error{ErrQuitNotDelivered, ErrAutomationDenied}, code: pderr.AppDidNotQuit},
		{name: "not running", err: &CommandError{Template: TmplQuit, ExitCode: 1, OSAStatus: -600},
			is: []error{ErrNotRunning}, notIs: []error{ErrQuitNotDelivered}, code: pderr.Internal},
		{name: "timeout", err: &CommandError{Template: TmplQuit, ExitCode: 1, OSAStatus: -1712},
			is: []error{ErrEventTimedOut}, notIs: []error{ErrQuitNotDelivered, ErrAutomationDenied, ErrQuitRefused}, code: pderr.AppDidNotQuit},
		{name: "privilege violation", err: &CommandError{Template: TmplQuit, ExitCode: 1, OSAStatus: -10004},
			is: []error{ErrQuitNotDelivered}, notIs: []error{ErrAutomationDenied, ErrQuitRefused}, code: pderr.AppDidNotQuit},
		{name: "no status", err: &CommandError{Template: TmplQuit, ExitCode: 1},
			is: []error{ErrQuitNotDelivered}, notIs: []error{ErrAutomationDenied, ErrQuitRefused}, code: pderr.AppDidNotQuit},
		{name: "did not start", err: errors.New("exec failed"),
			is: []error{ErrQuitNotDelivered}, code: pderr.Internal},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			run := &fakeRunner{t: t, reply: func(Template, []string) ([]byte, error) { return nil, tc.err }}
			err := osaController{run: run}.Quit(t.Context(), testBundle)
			if !slices.Equal(run.templates(), []Template{TmplQuit}) || run.calls[0].args[0] != testBundle {
				t.Fatalf("calls %+v", run.calls)
			}
			if tc.wantNil {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			for _, target := range tc.is {
				if !errors.Is(err, target) {
					t.Fatalf("%v is not %v", err, target)
				}
			}
			for _, target := range tc.notIs {
				if errors.Is(err, target) {
					t.Fatalf("%v is %v", err, target)
				}
			}
			if got := pderr.CodeOf(err); got != tc.code {
				t.Fatalf("code %d, want %d", got, tc.code)
			}
		})
	}
}

func TestProbeAutomation(t *testing.T) {
	run := &fakeRunner{t: t, reply: func(Template, []string) ([]byte, error) {
		return nil, &CommandError{Template: TmplProbe, ExitCode: 1, OSAStatus: -1743}
	}}
	err := osaController{run: run}.ProbeAutomation(t.Context(), testBundle)
	if !errors.Is(err, ErrAutomationDenied) || errors.Is(err, ErrQuitNotDelivered) || pderr.CodeOf(err) != pderr.AutomationMissing {
		t.Fatalf("got %v", err)
	}
	if !slices.Equal(run.templates(), []Template{TmplProbe}) {
		t.Fatalf("calls %v", run.templates())
	}
}

func TestLaunchOpenSessionGit(t *testing.T) {
	run := &fakeRunner{t: t, reply: func(tm Template, _ []string) ([]byte, error) {
		if tm == TmplGitPorcelain {
			return []byte(" M file.go\n"), nil
		}
		return nil, nil
	}}
	c := osaController{run: run}
	if err := c.Launch(t.Context(), testBundle); err != nil {
		t.Fatal(err)
	}
	if err := c.OpenSession(t.Context(), ids.SessionID(sessA)); err != nil {
		t.Fatal(err)
	}
	if err := c.OpenSession(t.Context(), "local_../x"); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("got %v", err)
	}
	dirty, err := gitPorcelain{run: run}.Dirty(t.Context(), t.TempDir())
	if err != nil || !dirty {
		t.Fatalf("dirty=%v err=%v", dirty, err)
	}
	want := []call{
		{TmplOpenBundle, []string{testBundle}},
		{TmplOpenURL, []string{sessionURLPrefix + sessA}},
	}
	for i, w := range want {
		if run.calls[i].t != w.t || !slices.Equal(run.calls[i].args, w.args) {
			t.Fatalf("call %d = %+v, want %+v", i, run.calls[i], w)
		}
	}
	if len(run.calls) != 3 {
		t.Fatalf("calls %+v", run.calls)
	}
}

func TestNotifySanitizes(t *testing.T) {
	run := &fakeRunner{t: t}
	c := osaController{run: run}
	long := strings.Repeat("\u00e9", maxNotifyText+50)
	tests := []struct {
		title, text         string
		wantTitle, wantText string
	}{
		{"paritydesk", "Switched.", "paritydesk", "Switched."},
		{"", "a\x1b[31mb\nc", notifyTitle, "a [31mb c"},
		{"--x", "- -y", "x", "y"},
		{"t", long, "t", strings.Repeat("\u00e9", maxNotifyText)},
		{"t", "bad\xffutf8", "t", "badutf8"},
	}
	for _, tc := range tests {
		run.calls = nil
		if err := c.Notify(t.Context(), tc.title, tc.text); err != nil {
			t.Fatal(err)
		}
		if len(run.calls) != 1 {
			t.Fatalf("%q: calls %+v", tc.text, run.calls)
		}
		got := run.calls[0].args
		if got[0] != tc.wantTitle || got[1] != tc.wantText || utf8.RuneCountInString(got[1]) > maxNotifyText {
			t.Fatalf("%q: got %q", tc.text, got)
		}
	}
	run.calls = nil
	if err := c.Notify(t.Context(), "t", " \n- "); err != nil || len(run.calls) != 0 {
		t.Fatalf("empty text sent: %v %+v", err, run.calls)
	}
}

func TestOSAStatus(t *testing.T) {
	tests := []struct {
		stderr string
		want   int
	}{
		{"0:42: execution error: Not authorized to send Apple events. (-1743)\n", -1743},
		{"execution error: Application isn't running. (-600)", -600},
		{"syntax error", 0},
		{"", 0},
	}
	for _, tc := range tests {
		if got := osaStatus([]byte(tc.stderr)); got != tc.want {
			t.Fatalf("%q: got %d", tc.stderr, got)
		}
	}
	e := &CommandError{Template: TmplQuit, ExitCode: 1, OSAStatus: -1743}
	if !strings.Contains(e.Error(), "-1743") {
		t.Fatal(e.Error())
	}
}
