package compat_test

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/testkit"
)

// manifest7x returns the embedded 7.x manifest.
func manifest7x(t *testing.T) *compat.Manifest {
	t.Helper()
	ms, err := compat.Load("")
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := compat.Select(ms, "7.37095.0")
	if err != nil || m == nil {
		t.Fatalf("select: %v", err)
	}
	return m
}

// anchored compiles a signal the way the app does: {session} as the
// session group, anchored at the start of the message.
func anchored(t *testing.T, p string) *regexp.Regexp {
	t.Helper()
	return regexp.MustCompile("^(?:" + strings.ReplaceAll(p, compat.SessionPlaceholder, compat.SessionGroup) + ")")
}

// TestSignals7xMatch: each 7.x pattern matches its message (built from the
// app's code text) and fails on near misses: another id form, an
// upper-case UUID, a missing word boundary, other account lines, and the
// same text with leading characters.
func TestSignals7xMatch(t *testing.T) {
	sig := manifest7x(t).Signals
	s := testkit.FakeSession(1)
	a, b := testkit.FakeUUID(0xa1), testkit.FakeUUID(0xb2)
	arrow := "→"
	type tc struct {
		pat  string
		msg  string
		want bool
	}
	cases := []tc{
		{sig.QuitVetoed, "beforeQuit: vetoed by before-quit interceptor", true},
		{sig.TurnStarted, "Sending message to session " + s, true},
		{sig.TurnStarted, "Sending message to session " + strings.ToUpper(s), false},
		{sig.TurnStarted, "Sending message to session " + s + "x", false},
		{sig.TurnStarted, "Sending message to session local_x", false},
		{sig.TurnEnded, "[Stop hook] Query completed for session " + s, true},
		{sig.WorkHeld, "[keep-awake] code session hold taken (turn, battery=false)", true},
		{sig.WorkReleased, "[keep-awake] code session hold released (idle, held 1200ms, battery=false)", true},
		{sig.WorkReleased, "[keep-awake] code session hold released (Idle, held 1200ms)", false},
		{sig.WorkUncounted, "[keep-awake] session " + s + " no longer counted (stalled)", true},
		{sig.WorkUncounted, "[keep-awake] session " + s + " no longer counted (max)", true},
		{sig.WorkUncounted, "[keep-awake] session " + s + " no longer counted (idle)", false},
		{sig.AppStarted, "[startup-perf] main window shown 812 ms after launch", true},
		{sig.FocusedSession, "[CCD] LocalSessions.setFocusedSession: sessionId=" + s, true},
		{sig.FocusedSession, "setFocusedSession: sessionId=" + s, true},
		{sig.BridgeConnected, "[claude-in-chrome] Paired with Chrome extension (duration: 990ms)", true},
		{sig.BridgeConnected, "[claude-in-chrome] Chrome extension connected to bridge", true},
		{sig.BridgeConnected, "[claude-in-chrome] Connecting to bridge", false},
		{sig.PairingDisabled, "[Chrome Extension MCP] local pairing disabled for this launch: userData relocated", true},
		{sig.PairingState, "[Chrome Extension MCP] local pairing beside the relay: established", true},
		{sig.PairingState, "[Chrome Extension MCP] local pairing beside the relay: closed", true},
		{sig.PairingState, "[Chrome Extension MCP] local pairing beside the relay: connecting", false},
		{sig.UncleanExit, "[updater] Previous run did not exit cleanly; prompt install is switched off", true},
		{sig.ActiveOrg, "[CCDScheduledTasks] Initialized { accountId: '" + a + "', orgId: '" + b + "' }", true},
		{sig.AccountChanged, "[account] Login-state transition (loggedOut: false " + arrow + " false, uuid: " + a + " " + arrow + " " + b + "), clearing oauth cache", true},
		{sig.AccountChanged, "[account] Login-state transition (loggedOut: null " + arrow + " false, uuid: " + a + " " + arrow + " " + b + "), clearing oauth cache", true},
		{sig.AccountChanged, "[account] Login-state transition (loggedOut: false " + arrow + " false, uuid: " + a + " " + arrow + " " + b + "), preserving oauth cache (same account uuid)", false},
		{sig.AccountChanged, "[account] Login-state transition (loggedOut: true " + arrow + " false, uuid: <none> " + arrow + " " + b + "), clearing oauth cache", false},
		{sig.AccountChanged, "[account] Login-state transition (loggedOut: false " + arrow + " true, uuid: " + a + " " + arrow + " " + b + "), clearing oauth cache", false},
		{sig.AccountChanged, "[account] Login-state transition (loggedOut: false " + arrow + " false, uuid: " + strings.ToUpper(a) + " " + arrow + " " + b + "), clearing oauth cache", false},
		{sig.AccountChangedHint[0], "[connector-owner] account changed — disconnecting 2 stored built-in connector(s)", true},
		{sig.AccountChangedHint[1], "Cannot get base query config: oauth failed (not_logged_in detail=account changed mid-flow)", true},
	}
	clears := []string{
		"Interrupting session " + s,
		"[interruptTurn] Interrupting session " + s,
		"LocalSessions.interrupt: sessionId=" + s,
		"LocalSessions.stop: sessionId=" + s,
		"LocalSessions.stopFamily: sessionId=" + s,
		"Stopping session " + s,
		"[interruptTurn] Stopping session " + s + ": its process has not started",
		"Session " + s + " query error: x",
		"Session " + s + " query interrupted (intentional stop)",
		"Session " + s + " query iterator completed",
		"[CCD] " + s + ": the turn failed; 1 queued message(s) now held unsent",
		"Session " + s + " stream ended without a result message, transitioning to idle",
		"[Result] Turn succeeded for session " + s,
		"[Result] Turn failed for session " + s + ": x",
		"[Result] Turn ended by user interrupt for session " + s,
	}
	for _, msg := range clears {
		matched := false
		for _, p := range sig.TurnCleared {
			if anchored(t, p).MatchString(msg) {
				matched = true
			}
			if anchored(t, p).MatchString("x " + msg) {
				t.Errorf("turn_cleared %q matches with leading text", p)
			}
		}
		if !matched {
			t.Errorf("no turn_cleared item matches %q", msg)
		}
	}
	for _, c := range cases {
		if c.pat == "" {
			t.Errorf("empty pattern for %q", c.msg)
			continue
		}
		re := anchored(t, c.pat)
		if got := re.MatchString(c.msg); got != c.want {
			t.Errorf("%q on %q: match %v, want %v", c.pat, c.msg, got, c.want)
		}
		if c.want && re.MatchString("> "+c.msg) {
			t.Errorf("%q matches with leading text", c.pat)
		}
	}
	re := anchored(t, sig.AccountChanged)
	m := re.FindStringSubmatch("[account] Login-state transition (loggedOut: false " + arrow + " false, uuid: " + a + " " + arrow + " " + b + "), clearing oauth cache")
	if m == nil || m[re.SubexpIndex("from")] != a || m[re.SubexpIndex("to")] != b {
		t.Fatalf("account_changed groups: %q", m)
	}
}

// TestSignals6xEmpty: no busy, account or pairing signal was observed on
// 6.x, so those keys are empty.
func TestSignals6xEmpty(t *testing.T) {
	sig := testkit.Manifest(t).Signals
	for name, p := range map[string]string{
		"turn_started": sig.TurnStarted, "work_held": sig.WorkHeld, "work_released": sig.WorkReleased,
		"work_uncounted": sig.WorkUncounted, "account_changed": sig.AccountChanged, "pairing_disabled": sig.PairingDisabled,
		"pairing_state": sig.PairingState, "login_required": sig.LoginRequired, "native_multi_account": sig.NativeMultiAccount,
	} {
		if p != "" {
			t.Errorf("%s must stay empty until observed", name)
		}
	}
	if len(sig.TurnCleared)+len(sig.WorkIdleReasons)+len(sig.WorkHoldPrefs)+len(sig.AccountChangedHint) != 0 {
		t.Error("6.x lists must stay empty until observed")
	}
}

// TestDeprecatedNativeMulti: an override copied from a v0.1 manifest with
// native_multi_account still loads; a non-empty value is reported.
func TestDeprecatedNativeMulti(t *testing.T) {
	base := embeddedText(t)
	withKey := strings.Replace(base, "account_changed_hint = []", "account_changed_hint = []\nnative_multi_account = ''", 1)
	dir := t.TempDir()
	writeOverride(t, dir, "override.toml", withKey)
	ms, err := compat.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if keys := ms[0].DeprecatedKeys(); len(keys) != 0 {
		t.Fatalf("empty value reported: %v", keys)
	}
	dir = t.TempDir()
	writeOverride(t, dir, "override.toml", strings.Replace(withKey, "native_multi_account = ''", "native_multi_account = '(not even a valid pattern'", 1))
	ms, err = compat.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if keys := ms[0].DeprecatedKeys(); !slices.Equal(keys, []string{"signals.native_multi_account"}) {
		t.Fatalf("keys %v", keys)
	}
	for _, m := range ms[1:] {
		if len(m.DeprecatedKeys()) != 0 {
			t.Errorf("embedded %s reports deprecated keys", m.Name)
		}
	}
}
