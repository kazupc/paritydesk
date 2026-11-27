package hook

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/online"
	"github.com/kazupc/paritydesk/internal/state"
)

const payload = `{"session_id":"00000000-0000-4000-8000-000000000001","hook_event_name":"SessionStart","source":"startup","cwd":"/tmp/x","transcript_path":"/tmp/t.jsonl"}`

type fakeState struct {
	root  *state.Root
	err   error
	loads int
}

func (f *fakeState) Load() (*state.Root, error) {
	f.loads++
	return f.root, f.err
}

func inside(v bool, err error) func(context.Context) (bool, error) {
	return func(context.Context) (bool, error) { return v, err }
}

func deps(root *state.Root) Deps {
	return Deps{State: &fakeState{root: root}, InsideApp: inside(true, nil), PluginVersion: "0.1.0-dev", BinaryVersion: "0.1.0-dev"}
}

func run(t *testing.T, in io.Reader, d Deps) string {
	t.Helper()
	var out bytes.Buffer
	if err := SessionStart(context.Background(), in, &out, d); err != nil {
		t.Fatalf("SessionStart returned %v", err)
	}
	return out.String()
}

func TestSilentWhenNothingPending(t *testing.T) {
	for name, root := range map[string]*state.Root{
		"no state":   nil,
		"empty root": state.NewRoot("/tmp/data"),
		"clean verify": func() *state.Root {
			r := state.NewRoot("/tmp/data")
			r.LastVerify = &state.VerifyRecord{RunID: "run-0001", At: time.Unix(0, 0)}
			return r
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			if got := run(t, strings.NewReader(payload), deps(root)); got != "" {
				t.Fatalf("printed %q", got)
			}
		})
	}
}

func TestOneFixedLine(t *testing.T) {
	withRun := state.NewRoot("/d")
	withRun.Run = &state.Run{ID: "run-0001", Kind: state.RunSwitch}
	withDrift := state.NewRoot("/d")
	withDrift.LastVerify = &state.VerifyRecord{Drift: []model.RowID{model.RowGroupOrder}}
	withSignIn := state.NewRoot("/d")
	withSignIn.LastVerify = &state.VerifyRecord{SignIn: true, Drift: []model.RowID{model.RowSidebarView}}
	withAdd := state.NewRoot("/d")
	withAdd.PendingAdd = &state.PendingAdd{Alias: "work"}
	all := state.NewRoot("/d")
	all.Run, all.PendingAdd, all.LastVerify = withRun.Run, withAdd.PendingAdd, withSignIn.LastVerify

	tests := []struct {
		name string
		root *state.Root
		want string
	}{
		{"open run", withRun, "paritydesk recover"},
		{"drift", withDrift, "paritydesk verify"},
		{"sign-in", withSignIn, "sign-in with its own account"},
		{"pending add", withAdd, "first sign-in"},
		{"everything: recover wins", all, "paritydesk recover"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := run(t, strings.NewReader(payload), deps(tc.root))
			if strings.Count(got, "\n") != 1 || !strings.HasSuffix(got, "\n") {
				t.Fatalf("want exactly one line, got %q", got)
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if got != PendingLine(tc.root, nil, "")+"\n" {
				t.Fatalf("line differs from PendingLine")
			}
		})
	}
}

func TestSilentOutsideAppAndOnSkew(t *testing.T) {
	r := state.NewRoot("/d")
	r.Run = &state.Run{ID: "run-0001"}
	tests := []struct {
		name   string
		mutate func(d *Deps)
	}{
		{"outside the app", func(d *Deps) { d.InsideApp = inside(false, nil) }},
		{"inside-app check fails", func(d *Deps) { d.InsideApp = inside(true, errors.New("ps failed")) }},
		{"no inside-app check", func(d *Deps) { d.InsideApp = nil }},
		{"no state reader", func(d *Deps) { d.State = nil }},
		{"minor skew", func(d *Deps) { d.PluginVersion = "0.2.0" }},
		{"major skew", func(d *Deps) { d.PluginVersion = "v1.1.0" }},
		{"garbage plugin version", func(d *Deps) { d.PluginVersion = "latest" }},
		{"unreadable state", func(d *Deps) { d.State = &fakeState{err: errors.New("permission denied")} }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := deps(r)
			tc.mutate(&d)
			if got := run(t, strings.NewReader(payload), d); got != "" {
				t.Fatalf("printed %q", got)
			}
		})
	}
	// A patch difference is not skew, and no version from an old shim is
	// accepted.
	for _, v := range []string{"0.1.7", "v0.1.0", ""} {
		d := deps(r)
		d.PluginVersion = v
		if got := run(t, strings.NewReader(payload), d); got == "" {
			t.Errorf("plugin version %q silenced the hook", v)
		}
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestMalformedOrOversizedInputIsSilentSuccess(t *testing.T) {
	r := state.NewRoot("/d")
	r.Run = &state.Run{ID: "run-0001"}
	big := `{"session_id":"` + strings.Repeat("a", MaxPayload) + `"}`
	tests := map[string]io.Reader{
		"empty":           strings.NewReader(""),
		"not json":        strings.NewReader("hello"),
		"array":           strings.NewReader(`[1,2]`),
		"trailing data":   strings.NewReader(payload + "{}"),
		"other event":     strings.NewReader(`{"hook_event_name":"PreToolUse"}`),
		"wrong type":      strings.NewReader(`{"session_id":7}`),
		"oversized":       strings.NewReader(big),
		"read error":      errReader{},
		"nil reader":      nil,
		"truncated json":  strings.NewReader(payload[:30]),
		"control bytes":   strings.NewReader("{\x00\x1b]52;c;x\x07}"),
		"deeply nested":   strings.NewReader(strings.Repeat("[", 10000)),
		"hostile strings": strings.NewReader(`{"session_id":"\u001b]52;c;eA==\u0007","source":"\u202e"}`),
	}
	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			err := SessionStart(context.Background(), in, &out, deps(r))
			if err != nil {
				t.Fatalf("error %v", err)
			}
			if name == "hostile strings" {
				// Valid JSON: the line is printed, but nothing from the
				// payload is echoed.
				if strings.ContainsAny(out.String(), "\x1b\x07") || strings.Contains(out.String(), "eA==") {
					t.Fatalf("payload echoed: %q", out.String())
				}
				return
			}
			if out.Len() != 0 {
				t.Fatalf("printed %q for malformed input", out.String())
			}
		})
	}
}

func TestPanicInDependencyIsSilent(t *testing.T) {
	d := deps(nil)
	d.InsideApp = func(context.Context) (bool, error) { panic("boom") }
	if got := run(t, strings.NewReader(payload), d); got != "" {
		t.Fatalf("printed %q", got)
	}
}

func TestNoUntrustedTextInLines(t *testing.T) {
	r := state.NewRoot("/d")
	r.Run = &state.Run{ID: "evil\x1b[31m", From: "a", To: "b"}
	r.PendingAdd = &state.PendingAdd{Alias: "evil\u202e"}
	r.LastVerify = &state.VerifyRecord{RunID: "x\x07", Drift: []model.RowID{"row\x1b"}}
	line := PendingLine(r, nil, "")
	for _, bad := range []string{"evil", "\x1b", "\x07", "row"} {
		if strings.Contains(line, bad) {
			t.Fatalf("untrusted %q in %q", bad, line)
		}
	}
	for _, r := range line {
		if r > 0x7e || r < 0x20 {
			t.Fatalf("non-printable rune %U in %q", r, line)
		}
	}
}

func TestSkewed(t *testing.T) {
	tests := []struct {
		plugin, binary string
		want           bool
	}{
		{"", "0.1.0", false},
		{"0.1.0", "0.1.0-dev", false},
		{"0.2.1", "0.2.0-dev", false},
		{"0.1.9", "0.1.0", false},
		{"v0.1.0", "0.1.0", false},
		{"0.1-dev", "0.1.3", false},
		{"0.2.0", "0.1.0", true},
		{"1.1.0", "0.1.0", true},
		{"0.10.0", "0.1.0", true},
		{"0", "0.1.0", true},
		{"x.y.z", "0.1.0", true},
		{"0.1x.0", "0.1.0", true},
		{"0.1.0", "garbage", true},
		// brew install --HEAD and go build from a checkout have no release
		// version, so they are never skewed.
		{"0.1.0", "HEAD-1a2b3c4", false},
		{"0.2.0", "(devel)", false},
		// go install from a clone stamps a Go pseudo-version.
		{"0.2.0", "v0.0.0-20060102150405-0123456789ab", false},
		{"0.2.0", "v0.2.1-0.20060102150405-0123456789ab+dirty", false},
		{"0.2.0", "v0.1.1-pre.0.20060102150405-0123456789ab", false},
		{"0.2.0", "v0.0.0-20060102150405-0123456789ab+dirty", false},
		{"0.2.0", "v0.2.0-rc.1.0.20060102150405-0123456789ab", false},
		// Release versions and near misses still count.
		{"0.2.0", "v0.1.0-20060102150405", true},
		{"0.2.0", "v0.1.0", true},
		{"0.2.0", "v0.0.0-2026-abc", true},
		{"0.2.0", "x-20060102150405-0123456789ab", true},
	}
	for _, tc := range tests {
		if got := Skewed(tc.plugin, tc.binary); got != tc.want {
			t.Errorf("Skewed(%q, %q) = %v", tc.plugin, tc.binary, got)
		}
	}
}

func TestStateLoadedOnlyWhenInsideApp(t *testing.T) {
	fs := &fakeState{root: state.NewRoot("/d")}
	d := Deps{State: fs, InsideApp: inside(false, nil)}
	run(t, strings.NewReader(payload), d)
	if fs.loads != 0 {
		t.Fatalf("state loaded %d times outside the app", fs.loads)
	}
}

func FuzzParsePayload(f *testing.F) {
	for _, s := range []string{payload, "", "{}", `{"hook_event_name":"SessionStart"}`, "[", `{"session_id":null}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		p, ok := ParsePayload(b)
		if !ok && p != (SessionStartInput{}) {
			t.Fatalf("rejected payload returned fields: %+v", p)
		}
		if ok && p.HookEventName != "" && p.HookEventName != EventSessionStart {
			t.Fatalf("accepted event %q", p.HookEventName)
		}
		r := state.NewRoot("/d")
		r.Run = &state.Run{ID: "run-0001"}
		var out bytes.Buffer
		if err := SessionStart(context.Background(), bytes.NewReader(b), &out, deps(r)); err != nil {
			t.Fatal(err)
		}
		if out.Len() != 0 && out.String() != PendingLine(r, nil, "")+"\n" {
			t.Fatalf("unexpected output %q", out.String())
		}
	})
}

// fakeOnline is an OnlineReader with a fixed head and a show counter.
type fakeOnline struct {
	head  *online.QueueHead
	shown map[string]int
}

func (f *fakeOnline) ReadHead() (*online.QueueHead, error) { return f.head, nil }

func (f *fakeOnline) CreateShown(key string, max int) bool {
	if f.shown == nil {
		f.shown = map[string]int{}
	}
	if f.shown[key] >= max {
		return false
	}
	f.shown[key]++
	return true
}

func onlineRoot() *state.Root {
	r := state.NewRoot("/d")
	r.Active, r.Epoch = "home", 4
	r.Online = &state.OnlineRecord{Queue: "run-0001", Profile: "home", Epoch: 4, State: "pending"}
	return r
}

func onlinePayload(source, mode string) string {
	return `{"session_id":"00000000-0000-4000-8000-000000000001","hook_event_name":"SessionStart","source":"` + source + `","permission_mode":"` + mode + `"}`
}

func TestSessionStartOnlineLine(t *testing.T) {
	head := &online.QueueHead{V: 1, ID: "run-0001", Phase: online.PhaseReconcile, State: online.QueuePending}
	prompt, auto := i18n.T(i18n.HookOnlinePrompt)+"\n", i18n.T(i18n.HookOnlineAuto)+"\n"
	tests := []struct {
		name, source, mode, cfgMode string
		root                        func() *state.Root
		head                        *online.QueueHead
		want                        string
	}{
		{"prompt", "startup", "default", "prompt", onlineRoot, head, prompt},
		{"auto attended", "resume", "acceptEdits", "auto", onlineRoot, head, auto},
		{"auto bypass gets prompt", "startup", "bypassPermissions", "auto", onlineRoot, head, prompt},
		{"auto without mode gets prompt", "startup", "", "auto", onlineRoot, head, prompt},
		{"off", "startup", "default", "off", onlineRoot, head, ""},
		{"clear", "clear", "default", "prompt", onlineRoot, head, ""},
		{"compact", "compact", "default", "prompt", onlineRoot, head, ""},
		{"head complete", "startup", "default", "prompt", onlineRoot, &online.QueueHead{V: 1, ID: "run-0001", State: online.QueueComplete}, ""},
		{"head of another queue", "startup", "default", "prompt", onlineRoot, &online.QueueHead{V: 1, ID: "run-0002", State: online.QueuePending}, ""},
		{"epoch moved", "startup", "default", "prompt", func() *state.Root { r := onlineRoot(); r.Epoch = 5; return r }, head, ""},
		{"artifacts queue", "startup", "default", "prompt", func() *state.Root { r := onlineRoot(); r.Online.Kind = "artifacts"; return r }, head, ""},
		{"recover first", "startup", "default", "prompt", func() *state.Root {
			r := onlineRoot()
			r.Run = &state.Run{ID: "run-0002", Kind: state.RunSwitch}
			return r
		}, head, i18n.T(i18n.HookRecover) + "\n"},
		{"online before drift", "startup", "default", "prompt", func() *state.Root {
			r := onlineRoot()
			r.LastVerify = &state.VerifyRecord{Drift: []model.RowID{model.RowSidebarView}}
			return r
		}, head, prompt},
		{"drift when online is not shown", "clear", "default", "prompt", func() *state.Root {
			r := onlineRoot()
			r.LastVerify = &state.VerifyRecord{Drift: []model.RowID{model.RowSidebarView}}
			return r
		}, head, i18n.T(i18n.HookDrift) + "\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := deps(tc.root())
			d.Online, d.Mode = &fakeOnline{head: tc.head}, tc.cfgMode
			if got := run(t, strings.NewReader(onlinePayload(tc.source, tc.mode)), d); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSessionStartOnlineShowsAtMostThree: the fourth start for one queue
// is silent.
func TestSessionStartOnlineShowsAtMostThree(t *testing.T) {
	d := deps(onlineRoot())
	f := &fakeOnline{head: &online.QueueHead{V: 1, ID: "run-0001", State: online.QueueActive}}
	d.Online, d.Mode = f, "prompt"
	for i := 0; i < 3; i++ {
		if got := run(t, strings.NewReader(onlinePayload("startup", "default")), d); got == "" {
			t.Fatalf("start %d silent", i+1)
		}
	}
	if got := run(t, strings.NewReader(onlinePayload("startup", "default")), d); got != "" {
		t.Fatalf("fourth start: %q", got)
	}
	if PendingLine(onlineRoot(), f.head, "prompt") != i18n.T(i18n.HookOnlinePrompt) {
		t.Fatal("PendingLine order")
	}
}
