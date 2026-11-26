package compat

import (
	"strings"
	"testing"
)

// validSignals is a small valid signal set the table below breaks.
func validSignals() Signals {
	return Signals{
		TurnStarted:     `Sending message to session {session}\b`,
		TurnEnded:       `\[Stop hook\] Query completed for session {session}\b`,
		TurnCleared:     []string{`Interrupting session {session}\b`},
		WorkHeld:        `\[keep-awake\] code session hold taken \(`,
		WorkReleased:    `\[keep-awake\] code session hold released \((?P<reason>[a-z_]+),`,
		WorkIdleReasons: []string{"idle"},
		WorkUncounted:   `\[keep-awake\] session {session} no longer counted`,
		WorkHoldPrefs:   []string{"ccKeepAwakeWhileWorking"},
		PairingState:    `\[Chrome Extension MCP\] local pairing: (?P<state>established|closed)`,
		ActiveOrg:       `\[ScheduledTasks\] Initialized (?P<account>[0-9a-f-]+) (?P<org>[0-9a-f-]+)`,
		AccountChanged:  `\[account\] Login-state transition (?P<from>[0-9a-f-]+) (?P<to>[0-9a-f-]+)`,
		AccountChangedHint: []string{
			`\[connector-owner\] account changed\b`,
		},
		NativeMultiAccount: `(never checked`,
	}
}

func TestSignalsValidate(t *testing.T) {
	if err := validSignals().validate(); err != nil {
		t.Fatalf("valid set: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*Signals)
		key    string
	}{
		{"turn_cleared item without {session}", func(s *Signals) { s.TurnCleared = []string{`Interrupting all sessions now`} }, "signals.turn_cleared[0]"},
		{"empty turn_cleared item", func(s *Signals) { s.TurnCleared = []string{""} }, "signals.turn_cleared[0]"},
		{"{session} twice", func(s *Signals) { s.TurnEnded = `Query completed for {session} and {session}` }, "signals.turn_ended"},
		{"turn_started without {session}", func(s *Signals) { s.TurnStarted = `Sending message to session` }, "signals.turn_started"},
		{"work_uncounted without {session}", func(s *Signals) { s.WorkUncounted = `\[keep-awake\] session no longer counted` }, "signals.work_uncounted"},
		{"work_released without reason", func(s *Signals) { s.WorkReleased = `\[keep-awake\] code session hold released` }, "signals.work_released"},
		{"work_idle_reasons without work_released", func(s *Signals) { s.WorkReleased = "" }, "signals.work_idle_reasons"},
		{"bad reason token", func(s *Signals) { s.WorkIdleReasons = []string{"Idle-Now"} }, "signals.work_idle_reasons[0]"},
		{"work_uncounted without work_held", func(s *Signals) { s.WorkHeld, s.WorkHoldPrefs = "", nil }, "signals.work_uncounted"},
		{"work_hold_prefs without work_held", func(s *Signals) { s.WorkHeld, s.WorkUncounted = "", "" }, "signals.work_hold_prefs"},
		{"bad preference name", func(s *Signals) { s.WorkHoldPrefs = []string{"cc.keep"} }, "signals.work_hold_prefs[0]"},
		{"account_changed without to", func(s *Signals) {
			s.AccountChanged = `\[account\] Login-state transition (?P<from>[0-9a-f-]+)`
		}, "signals.account_changed"},
		{"pairing_state without state", func(s *Signals) { s.PairingState = `\[Chrome Extension MCP\] local pairing` }, "signals.pairing_state"},
		{"starts with ^", func(s *Signals) { s.LoginRequired = `^You need to sign in again` }, "signals.login_required"},
		{"starts with .*", func(s *Signals) { s.LoginRequired = `.*need to sign in again` }, "signals.login_required"},
		{"starts with .+", func(s *Signals) { s.LoginRequired = `.+need to sign in again` }, "signals.login_required"},
		{"short literal", func(s *Signals) { s.UncleanExit = `[a-z]+ x` }, "signals.unclean_exit"},
		{"does not compile", func(s *Signals) { s.UncleanExit = `(Previous run did not exit` }, "signals.unclean_exit"},
		{"hint with {session}", func(s *Signals) { s.AccountChangedHint = []string{`\[connector-owner\] {session} changed`} }, "signals.account_changed_hint[0]"},
		{"hint with a named group", func(s *Signals) {
			s.AccountChangedHint = []string{`\[connector-owner\] account (?P<x>changed)`}
		}, "signals.account_changed_hint[0]"},
		{"hint without active_org", func(s *Signals) { s.ActiveOrg = "" }, "signals.account_changed_hint"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := validSignals()
			tc.mutate(&s)
			err := s.validate()
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.HasPrefix(err.Error(), tc.key+":") {
				t.Errorf("error %q does not name %s", err, tc.key)
			}
			// Errors never carry pattern text.
			for _, frag := range []string{"Interrupting", "sign in again", "[a-z]", "Previous run", "connector-owner", "keep-awake", "Login-state", "local pairing"} {
				if strings.Contains(err.Error(), frag) {
					t.Errorf("error %q contains pattern text %q", err, frag)
				}
			}
		})
	}
}

func TestRequiredLiteral(t *testing.T) {
	tests := []struct{ pattern, want string }{
		{`Sending message to session {session}\b`, "Sending message to session "},
		{`Interrupting session {session}\b`, "Interrupting session "},
		{`\[(?:CCD)?ScheduledTasks\] Initialized \{ accountId: \x27(?P<account>[0-9a-f]{8})\x27 \}`, "ScheduledTasks] Initialized { accountId: '"},
		{`\[account\] Login-state transition \(loggedOut: (?:null|true|false) \x{2192} false`, "[account] Login-state transition (loggedOut: "},
		{`\[CCD\] {session}: the turn failed\b`, ": the turn failed"},
		{`LocalSessions\.(?:interrupt|stop|stopFamily): sessionId={session}\b`, "LocalSessions."},
		{`\[claude-in-chrome\] (?:Chrome extension connected to bridge|Paired with Chrome extension\b)`, "[claude-in-chrome] "},
		{`(?:\[[a-z-]{1,40}\] )?Chrome extension connected to bridge`, "Chrome extension connected to bridge"},
		{`Session {session} query (?:error|interrupted|iterator completed)\b`, "Session "},
		{`exactly this text`, "exactly this text"},
		{`(`, ""},
		{`(?i)case folded literal text`, ""},
		{`[a-z]+ x`, " x"},
	}
	for _, tc := range tests {
		if got := RequiredLiteral(tc.pattern); got != tc.want {
			t.Errorf("RequiredLiteral(%q) = %q, want %q", tc.pattern, got, tc.want)
		}
	}
	// The session group, never the sample id, replaces {session}.
	if got := RequiredLiteral(`{session}`); got != "" {
		t.Errorf("{session} alone gave %q", got)
	}
}
