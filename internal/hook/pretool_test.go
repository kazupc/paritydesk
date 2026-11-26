package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/clock"
	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/fsx/fsxtest"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/online"
)

var update = flag.Bool("update", false, "rewrite the golden files")

const (
	tQueue   = "run-0001"
	tSession = "00000000-0000-4000-8000-0000000000c1"
	tOther   = "00000000-0000-4000-8000-0000000000c2"
	tBoot    = "boot-1"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// atomicOps serializes RenameExcl like the platform's RENAME_EXCL.
type atomicOps struct {
	fsx.Ops
	mu *sync.Mutex
}

func (a atomicOps) RenameExcl(from, to string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Ops.RenameExcl(from, to)
}

// armedDeps returns tool deps over an armed queue: the probe (offered,
// unbound lease unless session is set) or, with probeDone, an offered
// sidebar read and set_view.
func armedDeps(t *testing.T, session string) (ToolDeps, *online.Store) {
	t.Helper()
	sd := filepath.Join(t.TempDir(), "paritydesk")
	store := online.NewStore(sd, atomicOps{Ops: fsxtest.New(t), mu: &sync.Mutex{}})
	probe := online.ProbeStep()
	probe.Status = online.StatusOffered
	sv := online.Step{ID: "s000", Kind: online.KindSidebar, Tool: online.ToolSetView, Input: json.RawMessage(`{"group_by":"state"}`),
		Status: online.StatusOffered, Needs: []string{"read:r000"}, After: []online.StepID{},
		Expect: online.Expect{Format: online.FormatJSONObject}, Data: json.RawMessage(`{"key":"builtin:sessions","class":"sessions","intended":{"group_by":"state"},"prior":{}}`)}
	q := &online.Queue{V: 1, ID: tQueue, Phase: online.PhaseReconcile, State: online.QueueActive, CreatedAt: t0,
		ExpiresAt: t0.Add(time.Hour), Guard: online.GuardExtra{ToolsOff: []string{"Sidebar tools are turned off right now."}},
		Steps: []online.Step{probe, sv}}
	if err := store.WriteQueue(q); err != nil {
		t.Fatal(err)
	}
	l := &online.Lease{V: 1, Queue: tQueue, Phase: online.PhaseReconcile, Session: session, BootID: tBoot,
		MonoDeadlineNS: 2e12, ExpiresAt: t0.Add(20 * time.Minute), Since: t0}
	if err := store.WriteLease(l); err != nil {
		t.Fatal(err)
	}
	d := ToolDeps{Store: store, Mode: online.GuardArmed, Mono: online.NewFakeMono(tBoot, 1e12), Clock: clock.NewFake(t0),
		Providers: online.Registry()}
	return d, store
}

func prePayload(session, tool, input string) string {
	return `{"session_id":"` + session + `","hook_event_name":"PreToolUse","tool_name":"` + tool + `","tool_input":` + input + `}`
}

func pre(t *testing.T, d ToolDeps, payload io.Reader) string {
	t.Helper()
	var out bytes.Buffer
	if err := PreToolUse(context.Background(), payload, &out, d); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func goldenFile(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/hook -update)", err)
	}
	if string(want) != got {
		t.Fatalf("%s differs:\n%s\nwant\n%s", p, got, want)
	}
}

func TestDenyDocumentsGolden(t *testing.T) {
	for name, m := range map[string]i18n.Msg{
		"deny_floor": i18n.GuardDenyFloor, "deny_unplanned": i18n.GuardDenyUnplanned, "deny_not_yet": i18n.GuardDenyNotYet,
		"deny_unreadable": i18n.GuardDenyUnreadable, "deny_duplicate": i18n.GuardDenyDuplicate, "deny_artifact_tool": i18n.GuardDenyArtifactTool,
		"deny_unplanned_artifacts": i18n.GuardDenyUnplannedArtifacts, "deny_not_yet_artifacts": i18n.GuardDenyNotYetArtifacts,
		"deny_duplicate_artifacts": i18n.GuardDenyDuplicateArtifacts,
	} {
		doc := string(DenyDocument(m))
		if strings.Count(doc, "\n") != 1 || strings.Contains(doc, "allow") {
			t.Fatalf("%s: %q", name, doc)
		}
		goldenFile(t, name, doc)
	}
}

func TestPreToolUse(t *testing.T) {
	d, store := armedDeps(t, tSession)
	// An unplanned call while armed: one deny document and a denial record.
	out := pre(t, d, strings.NewReader(prePayload(tSession, "mcp__ccd_sidebar__set_unread", `{"session_id":"self"}`)))
	if out != string(DenyDocument(i18n.GuardDenyUnplanned)) {
		t.Fatalf("unplanned: %q", out)
	}
	if ds := store.Denials(); len(ds) != 1 || ds[0].Reason != string(i18n.GuardDenyUnplanned) || ds[0].Queue != tQueue {
		t.Fatalf("denials %+v", ds)
	}
	// The planned call passes silently and leaves its marker.
	if out := pre(t, d, strings.NewReader(prePayload(tSession, online.ToolGetSession, `{"session_id":"self"}`))); out != "" {
		t.Fatalf("planned call: %q", out)
	}
	if m := store.Inflight(tQueue)[online.ProbeStepID]; len(m) != 1 || m[0].N != 1 {
		t.Fatalf("marker %+v", m)
	}
	// Another session is not armed: silent.
	if out := pre(t, d, strings.NewReader(prePayload(tOther, "mcp__ccd_sidebar__set_unread", `{}`))); out != "" {
		t.Fatalf("other session: %q", out)
	}
	// Per-turn tools never get a decision.
	for _, tool := range []string{"mcp__ccd_turn__update_plan", "mcp__ccd_turn__wrap_up"} {
		if out := pre(t, d, strings.NewReader(prePayload(tSession, tool, `{}`))); out != "" {
			t.Fatalf("%s: %q", tool, out)
		}
	}
	// Malformed and oversized payloads fail closed while armed.
	for name, in := range map[string]io.Reader{
		"not json":    strings.NewReader("{"),
		"other event": strings.NewReader(`{"hook_event_name":"PostToolUse","tool_name":"mcp__ccd_sidebar__set_view"}`),
		"oversized":   io.MultiReader(strings.NewReader(`{"x":"`), &repeatReader{n: MaxPrePayload + 10}),
	} {
		if out := pre(t, d, in); out != string(DenyDocument(i18n.GuardDenyUnreadable)) {
			t.Fatalf("%s: %q", name, out)
		}
	}
}

// repeatReader yields n bytes of 'a'.
type repeatReader struct{ n int }

func (r *repeatReader) Read(p []byte) (int, error) {
	if r.n <= 0 {
		return 0, io.EOF
	}
	k := min(len(p), r.n)
	for i := 0; i < k; i++ {
		p[i] = 'a'
	}
	r.n -= k
	return k, nil
}

func TestPreToolUseSilentWhenUnarmed(t *testing.T) {
	d, store := armedDeps(t, tSession)
	if err := store.RemoveLease(); err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{"{", prePayload(tSession, "mcp__ccd_sidebar__delete_group", `{}`),
		prePayload(tSession, "mcp__ccd_sidebar__set_unread", `{}`)} {
		if out := pre(t, d, strings.NewReader(payload)); out != "" {
			t.Fatalf("unarmed %q: %q", payload, out)
		}
	}
	d.Mode = online.GuardAlways
	if out := pre(t, d, strings.NewReader(prePayload(tSession, "mcp__ccd_sidebar__delete_group", `{}`))); out != string(DenyDocument(i18n.GuardDenyFloor)) {
		t.Fatalf("always mode: %q", out)
	}
}

// panicky is a provider whose policy panics.
type panicky struct{ online.Provider }

func (panicky) Tools() online.ToolPolicy { panic("boom") }

func TestPreToolUseFailsClosed(t *testing.T) {
	d, _ := armedDeps(t, tSession)
	d.Providers = append(d.Providers, panicky{online.ProbeProvider()})
	if out := pre(t, d, strings.NewReader(prePayload(tSession, online.ToolGetSession, `{"session_id":"self"}`))); out != string(DenyDocument(i18n.GuardDenyUnreadable)) {
		t.Fatalf("panic while armed: %q", out)
	}
	// A payload that never ends: the deadline denies while armed.
	d, _ = armedDeps(t, tSession)
	r, w := io.Pipe()
	defer w.Close()
	start := time.Now()
	out := pre(t, d, r)
	if took := time.Since(start); took > 2500*time.Millisecond {
		t.Fatalf("took %v", took)
	}
	if out != string(DenyDocument(i18n.GuardDenyUnreadable)) {
		t.Fatalf("hung payload: %q", out)
	}
}

// TestGate: N parallel identical calls against one armed queue: exactly
// one passes and creates the marker; the rest are denied duplicate.
func TestGate(t *testing.T) {
	d, store := armedDeps(t, tSession)
	const n = 12
	outs := make(chan string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out bytes.Buffer
			_ = PreToolUse(context.Background(), strings.NewReader(prePayload(tSession, online.ToolGetSession, `{"session_id":"self"}`)), &out, d)
			outs <- out.String()
		}()
	}
	wg.Wait()
	close(outs)
	passed, dup := 0, 0
	for o := range outs {
		switch o {
		case "":
			passed++
		case string(DenyDocument(i18n.GuardDenyDuplicate)):
			dup++
		default:
			t.Fatalf("unexpected %q", o)
		}
	}
	if passed != 1 || dup != n-1 || len(store.Inflight(tQueue)[online.ProbeStepID]) != 1 {
		t.Fatalf("passed %d, denied %d", passed, dup)
	}
}

// failingOps fails every RenameExcl with a non-exist error.
type failingOps struct{ fsx.Ops }

func (failingOps) RenameExcl(string, string) error { return os.ErrPermission }

func TestGateOtherError(t *testing.T) {
	d, store := armedDeps(t, tSession)
	d.Store = online.NewStore(filepath.Dir(store.Dir), failingOps{store.FS})
	if out := pre(t, d, strings.NewReader(prePayload(tSession, online.ToolGetSession, `{"session_id":"self"}`))); out != string(DenyDocument(i18n.GuardDenyUnreadable)) {
		t.Fatalf("marker error: %q", out)
	}
}

func FuzzParseToolPayload(f *testing.F) {
	f.Add([]byte(prePayload(tSession, online.ToolGetSession, `{"session_id":"self"}`)), true)
	f.Add([]byte(`{"hook_event_name":"PostToolUse","tool_name":"Artifact","tool_use_id":"../x"}`), false)
	f.Fuzz(func(t *testing.T, b []byte, preEvent bool) {
		ev := EventPostToolUse
		if preEvent {
			ev = EventPreToolUse
		}
		p, ok := ParseToolPayload(b, ev)
		if !ok {
			return
		}
		if p.HookEventName != ev || (p.SessionID != "" && len(p.SessionID) != 36) || (p.ToolUseID != "" && !toolUseIDRE.MatchString(p.ToolUseID)) {
			t.Fatalf("payload %+v", p)
		}
	})
}
