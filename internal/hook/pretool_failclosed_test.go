package hook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/online"
)

const setUnread = "mcp__ccd_sidebar__set_unread"

// TestPreToolUseLeaseUnreadable: while the armed marker holds a future
// deadline, a lease.json that cannot be read (bad JSON, too large, a
// symlink) fails closed for guarded mutations; reads and tools outside
// the guarded servers keep no decision. Without a live marker the same
// lease stays silent.
func TestPreToolUseLeaseUnreadable(t *testing.T) {
	cases := map[string]func(t *testing.T, p string){
		"bad json":  func(t *testing.T, p string) { writeRaw(t, p, []byte("{not json")) },
		"too large": func(t *testing.T, p string) { writeRaw(t, p, []byte(strings.Repeat(" ", 5000)+"{}")) },
		"symlink": func(t *testing.T, p string) {
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			alt := filepath.Join(t.TempDir(), "lease.json")
			writeRaw(t, alt, b)
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(alt, p); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			d, store := armedDeps(t, tSession)
			mutate(t, filepath.Join(store.Dir, online.FileLease))
			for _, tc := range []struct {
				tool, input string
				want        i18n.Msg // "" = no decision
			}{
				{setUnread, `{"session_id":"self","unread":true}`, i18n.GuardDenyUnreadable},
				{"mcp__ccd_session_mgmt__delete_session", `{"session_id":"local_00000000-0000-4000-8000-000000000003"}`, i18n.GuardDenyFloor},
				{"mcp__ccd_session_mgmt__list_sessions", `{}`, i18n.GuardDenyFloor},
				{online.ToolSetView, `{"group_by":"state"}`, i18n.GuardDenyUnreadable},
				{online.ToolListSections, `{}`, ""},
				{"mcp__ccd_turn__update_plan", `{}`, ""},
			} {
				out := pre(t, d, strings.NewReader(prePayload(tSession, tc.tool, tc.input)))
				want := ""
				if tc.want != "" {
					want = string(DenyDocument(tc.want))
				}
				if out != want {
					t.Fatalf("%s: %q, want %q", tc.tool, out, want)
				}
			}
			// The marker gone (online cancel removes it first): silent.
			if err := store.RemoveArmed(); err != nil {
				t.Fatal(err)
			}
			if out := pre(t, d, strings.NewReader(prePayload(tSession, setUnread, `{"session_id":"self","unread":true}`))); out != "" {
				t.Fatalf("not armed: %q", out)
			}
		})
	}
}

// TestPreToolUseNoMonotonic: a lease that cannot be judged because the
// monotonic clock fails counts as armed while the marker is live.
func TestPreToolUseNoMonotonic(t *testing.T) {
	d, store := armedDeps(t, tSession)
	m := online.NewFakeMono(tBoot, 1e12)
	m.Broken = true
	d.Mono = m
	call := prePayload(tSession, setUnread, `{"session_id":"self","unread":true}`)
	if out := pre(t, d, strings.NewReader(call)); out != string(DenyDocument(i18n.GuardDenyUnreadable)) {
		t.Fatalf("no clock while armed: %q", out)
	}
	if err := store.RemoveArmed(); err != nil {
		t.Fatal(err)
	}
	if out := pre(t, d, strings.NewReader(call)); out != "" {
		t.Fatalf("no clock, not armed: %q", out)
	}
}

// TestPreToolUseSlowStore: a store whose reads block (a hung disk) cannot
// hold PreToolUse past its budget: the process answers within 2.5 s,
// denying while armed (or when even the armed marker cannot be read) and
// staying silent when the marker says it is not armed.
func TestPreToolUseSlowStore(t *testing.T) {
	cases := []struct {
		name    string
		block   func(rel string) bool
		noArmed bool
		want    string
	}{
		{"lease blocks, armed", func(rel string) bool { return rel == online.FileLease }, false, string(DenyDocument(i18n.GuardDenyUnreadable))},
		{"everything blocks", func(string) bool { return true }, false, string(DenyDocument(i18n.GuardDenyUnreadable))},
		{"lease blocks, not armed", func(rel string) bool { return rel == online.FileLease }, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, store := armedDeps(t, tSession)
			if tc.noArmed {
				if err := store.RemoveArmed(); err != nil {
					t.Fatal(err)
				}
			}
			// A blocked read ends with a panic once the test is over, so
			// the abandoned goroutines touch no file while the temporary
			// directory is removed (PreToolUse recovers it).
			release := make(chan struct{})
			defer close(release)
			store.BeforeRead = func(rel string) {
				if tc.block(rel) {
					<-release
					panic("store released")
				}
			}
			start := time.Now()
			out := pre(t, d, strings.NewReader(prePayload(tSession, setUnread, `{"session_id":"self","unread":true}`)))
			if took := time.Since(start); took > 2500*time.Millisecond {
				t.Fatalf("took %v", took)
			}
			if out != tc.want {
				t.Fatalf("%q, want %q", out, tc.want)
			}
		})
	}
}

// TestPreToolUseCaptureUnderReconcileLease: the capture's c001 is the same
// call as the probe. With a fresh reconcile lease whose probe is done, the
// capture call still passes (7.1: the capture arms no guard), and
// PostToolUse records it for the capture too.
func TestPreToolUseCaptureUnderReconcileLease(t *testing.T) {
	d, store := armedDeps(t, tSession)
	q, err := store.ReadQueue(online.PhaseReconcile)
	if err != nil {
		t.Fatal(err)
	}
	q.Step(online.ProbeStepID).Status = online.StatusDone
	if err := store.WriteQueue(q); err != nil {
		t.Fatal(err)
	}
	call := prePayload(tSession, online.ToolGetSession, `{"session_id":"self"}`)
	// Without a capture the probe call is not ready any more.
	if out := pre(t, d, strings.NewReader(call)); out != string(DenyDocument(i18n.GuardDenyNotYet)) {
		t.Fatalf("no capture: %q", out)
	}
	mono := online.Read(d.Mono)
	capq, err := online.BuildCapture(online.CaptureInput{Session: tSession}, d.Providers, "cap-20260102T030405Z-0a0b0c0d",
		t0, mono, 1, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteQueue(capq); err != nil {
		t.Fatal(err)
	}
	if out := pre(t, d, strings.NewReader(call)); out != "" {
		t.Fatalf("capture call: %q", out)
	}
	// Another session's call is not the capture's.
	if out := pre(t, d, strings.NewReader(prePayload(tOther, setUnread, `{"session_id":"self","unread":true}`))); out != "" {
		t.Fatalf("other session (lease bound to another): %q", out)
	}
	// Another tool of the same session stays guarded.
	if out := pre(t, d, strings.NewReader(prePayload(tSession, setUnread, `{"session_id":"self","unread":true}`))); out != string(DenyDocument(i18n.GuardDenyUnplanned)) {
		t.Fatalf("unplanned: %q", out)
	}
	local := "local_00000000-0000-4000-8000-0000000000d1"
	post(t, d, strings.NewReader(postPayload(tSession, online.ToolGetSession, `{"session_id":"self"}`,
		`{"sessionId":"`+local+`"}`, false)))
	if r, ok := store.CaptureResults(capq.ID, tSession, nil)[online.CaptureStepID]; !ok || string(r.Parsed) != `{"id":"`+local+`"}` {
		t.Fatalf("capture result %+v", r)
	}
	if _, ok := store.Results(tQueue, nil)[online.ProbeStepID]; !ok {
		t.Fatal("reconcile result not recorded")
	}
}

// TestPreToolUseAlwaysModeExtras: with online.guard = "always" the queue's
// manifest additions (always_deny) are enforced in a session that holds no
// lease.
func TestPreToolUseAlwaysModeExtras(t *testing.T) {
	d, store := armedDeps(t, tSession)
	q, err := store.ReadQueue(online.PhaseReconcile)
	if err != nil {
		t.Fatal(err)
	}
	q.Guard.AlwaysDeny = []string{"mcp__ccd_sidebar__mark_completed"}
	if err := store.WriteQueue(q); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveLease(); err != nil {
		t.Fatal(err)
	}
	call := prePayload(tSession, "mcp__ccd_sidebar__mark_completed", `{"session_id":"self"}`)
	if out := pre(t, d, strings.NewReader(call)); out != "" {
		t.Fatalf("armed mode, no lease: %q", out)
	}
	d.Mode = online.GuardAlways
	if out := pre(t, d, strings.NewReader(call)); out != string(DenyDocument(i18n.GuardDenyFloor)) {
		t.Fatalf("always mode: %q", out)
	}
}

func writeRaw(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
