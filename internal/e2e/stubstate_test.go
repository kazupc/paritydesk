package e2e

// Unit tests of the harness's pure stub-state helpers (stub_test.go).
// They run everywhere, without the stub or PARITYDESK_E2E.

import (
	"reflect"
	"strings"
	"testing"
)

const testExe = "/private/tmp/e2e-home/Apps/Claude.app/Contents/MacOS/stubapp"

func TestParseStubPIDs(t *testing.T) {
	ps := strings.Join([]string{
		"    1 /sbin/launchd",
		"  812 " + testExe,
		"  640 " + testExe + " -psn_0_12345",
		"  700 " + testExe + "-other",              // another executable
		"  701 /usr/bin/grep " + testExe,           // mentions it as an argument
		"  702 " + testExe + "/../stubapp",         // not the same command
		"  703 /private/tmp/other/Claude.app/stub", // another bundle
		"  abc " + testExe,                         // no pid
		"",
		"\t 99\t" + testExe,
	}, "\n")
	got := parseStubPIDs([]byte(ps), testExe)
	if want := []int{99, 640, 812}; !reflect.DeepEqual(got, want) {
		t.Fatalf("parseStubPIDs = %v, want %v", got, want)
	}
	if got := parseStubPIDs(nil, testExe); got != nil {
		t.Fatalf("parseStubPIDs(empty) = %v, want none", got)
	}
}

func TestParseLaunch(t *testing.T) {
	cases := []struct {
		line string
		want launchEvent
		ok   bool
	}{
		{"launch data_dir=/w/recover/data/Claude pid=42", launchEvent{42, "/w/recover/data/Claude"}, true},
		{"launch data_dir=/w/a b/data/Claude pid=7", launchEvent{7, "/w/a b/data/Claude"}, true},
		{"launch data_dir= pid=7", launchEvent{7, ""}, true},
		{"exit pid=42", launchEvent{}, false},
		{"quit-request veto=0 mode=later pid=42", launchEvent{}, false},
		{"launch data_dir=/w/data/Claude", launchEvent{}, false},
		{"launch data_dir=/w/data/Claude pid=x", launchEvent{}, false},
		{"launch data_dir=/w/data/Claude pid=0", launchEvent{}, false},
		{"lock-busy pid=3", launchEvent{}, false},
		{"", launchEvent{}, false},
	}
	for _, c := range cases {
		got, ok := parseLaunch(c.line)
		if ok != c.ok || got != c.want {
			t.Errorf("parseLaunch(%q) = %+v, %v; want %+v, %v", c.line, got, ok, c.want, c.ok)
		}
	}
}

// The events of an earlier subtest whose relaunched stub (pid 30) was
// never quit, followed by the next subtest's launch attempt. This is a
// constructed example of the leftover-stub pattern the 60 s "did not
// launch" failures of run 45048505893 fit; that job logged no stub
// events, so it is not a copy of CI data.
var leftoverEvents = []string{
	"launch data_dir=/w/switch/data/Claude pid=10",
	"exit pid=10",
	"launch data_dir=/w/kill/data/Claude pid=20",
	"exit pid=20",
	"launch data_dir=/w/kill/data/Claude pid=30",
}

func running(pids ...int) func(int) bool {
	return func(p int) bool {
		for _, q := range pids {
			if p == q {
				return true
			}
		}
		return false
	}
}

func TestLastLaunch(t *testing.T) {
	cases := []struct {
		name    string
		events  []string
		from    int
		skip    int
		running func(int) bool
		want    launchEvent
		ok      bool
	}{
		{"new launch after from", append(append([]string(nil), leftoverEvents...), "launch data_dir=/w/veto/data/Claude pid=40"), 5, 0, running(30, 40),
			launchEvent{40, "/w/veto/data/Claude"}, true},
		// The leftover pattern: the stub (30) ran, open -b only brought
		// it forward, and nothing was recorded after from.
		{"leftover before from is not a launch", leftoverEvents, 5, 0, running(30), launchEvent{}, false},
		{"skip the old process", []string{"launch data_dir=/d pid=5", "exit pid=5", "launch data_dir=/d pid=6"}, 0, 6, running(6), launchEvent{}, false},
		{"relaunch replaces the skipped one", []string{"launch data_dir=/d pid=5", "exit pid=5", "launch data_dir=/d pid=6"}, 0, 5, running(6),
			launchEvent{6, "/d"}, true},
		{"launched but already gone", []string{"launch data_dir=/d pid=5", "exit pid=5"}, 0, 0, running(), launchEvent{}, false},
		{"negative from", []string{"launch data_dir=/d pid=5"}, -3, 0, running(5), launchEvent{5, "/d"}, true},
		{"from past the end", []string{"launch data_dir=/d pid=5"}, 4, 0, running(5), launchEvent{}, false},
		{"no events", nil, 0, 0, running(5), launchEvent{}, false},
	}
	for _, c := range cases {
		got, ok := lastLaunch(c.events, c.from, c.skip, c.running)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: lastLaunch = %+v, %v; want %+v, %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestLaunchDataDir(t *testing.T) {
	events := []string{
		"launch data_dir=/w/a/data/Claude pid=5",
		"exit pid=5",
		"launch data_dir=/w/b/data/Claude pid=5", // pid reused later
	}
	if d, ok := launchDataDir(events, 5); !ok || d != "/w/b/data/Claude" {
		t.Errorf("launchDataDir(5) = %q, %v; want the newest launch's folder", d, ok)
	}
	if d, ok := launchDataDir(events, 6); ok || d != "" {
		t.Errorf("launchDataDir(6) = %q, %v; want none", d, ok)
	}
}

func TestLaunchFailure(t *testing.T) {
	msg := launchFailure([]int{30}, leftoverEvents, 5)
	for _, want := range []string{"already runs", "pid 30 on /w/kill/data/Claude", "brought that instance forward", "left running by an earlier step"} {
		if !strings.Contains(msg, want) {
			t.Errorf("leftover stub: message %q lacks %q", msg, want)
		}
	}
	msg = launchFailure(nil, leftoverEvents, 2)
	for _, want := range []string{"no stub process runs", "after event 2", "launch data_dir=/w/kill/data/Claude pid=30"} {
		if !strings.Contains(msg, want) {
			t.Errorf("no stub: message %q lacks %q", msg, want)
		}
	}
	if strings.Contains(msg, "pid=10") {
		t.Errorf("no stub: message %q names events before from", msg)
	}
	if msg := launchFailure(nil, leftoverEvents, 5); !strings.Contains(msg, "recorded no event after event 5") {
		t.Errorf("no new events: message %q", msg)
	}
	if msg := launchFailure(nil, nil, 0); !strings.Contains(msg, "recorded no event") {
		t.Errorf("no events file: message %q", msg)
	}
}

func TestDescribeStubs(t *testing.T) {
	got := describeStubs([]int{30, 31}, leftoverEvents)
	want := "pid 30 on /w/kill/data/Claude; pid 31, no launch event yet (still starting)"
	if got != want {
		t.Fatalf("describeStubs = %q, want %q", got, want)
	}
	if got := describeStubs(nil, leftoverEvents); got != "none" {
		t.Fatalf("describeStubs(none) = %q, want none", got)
	}
}

func TestEventTail(t *testing.T) {
	cases := []struct {
		from, limit int
		want        string
	}{
		{3, 10, "exit pid=20 | launch data_dir=/w/kill/data/Claude pid=30"},
		{-1, 2, "(3 earlier omitted) exit pid=20 | launch data_dir=/w/kill/data/Claude pid=30"},
		{5, 10, "none"},
		{9, 10, "none"},
	}
	for _, c := range cases {
		if got := eventTail(leftoverEvents, c.from, c.limit); got != c.want {
			t.Errorf("eventTail(from %d, limit %d) = %q, want %q", c.from, c.limit, got, c.want)
		}
	}
	if got := eventTail(nil, 0, 10); got != "none" {
		t.Errorf("eventTail(no events) = %q, want none", got)
	}
}

func TestLastAccount(t *testing.T) {
	const (
		a = "00000000-0000-4000-8000-000000000065"
		b = "00000000-0000-4000-8000-0000000000c9"
		o = "00000000-0000-4000-8000-000000000066"
	)
	log := strings.Join([]string{
		"[2026-11-22 17:18:03.100] [info] stubapp: launched",
		"[2026-11-22 17:18:03.101] [info] [ScheduledTasks] Initialized { accountId: '" + a + "', orgId: '" + o + "' }",
		"[2026-11-22 17:18:09.000] [info] beforeQuit: vetoed by before-quit interceptor",
		"[2026-11-22 17:18:20.101] [info] [CCDScheduledTasks] Initialized { accountId: '" + b + "', orgId: '" + o + "' }",
		"",
	}, "\n")
	if got, ok := lastAccount(log); !ok || got != b {
		t.Errorf("lastAccount = %q, %v; want the newest line's %s", got, ok, b)
	}
	if got, ok := lastAccount(strings.SplitN(log, "\n", 3)[0]); ok {
		t.Errorf("lastAccount(no account line) = %q, want none", got)
	}
	if _, ok := lastAccount("[ScheduledTasks] Initialized { accountId: 'NOT-A-UUID', orgId: '" + o + "' }"); ok {
		t.Error("lastAccount accepted a malformed account")
	}
}
