package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/clock"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/fsx/fsxtest"
	"github.com/kazupc/paritydesk/internal/hook"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/online"
	"github.com/kazupc/paritydesk/internal/orchestrator"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/rows"
	"github.com/kazupc/paritydesk/internal/state"
)

const (
	olQueue   = "run-0001"
	olSession = "00000000-0000-4000-8000-0000000000c1"
	olOther   = "00000000-0000-4000-8000-0000000000c2"
	olLocal   = "local_00000000-0000-4000-8000-0000000000d1"
)

var olReplica = model.Replica{Account: "00000000-0000-4000-8000-000000000001", Org: "00000000-0000-4000-8000-000000000002"}

// countReader is a deterministic byte source for tokens and temp names.
type countReader struct {
	mu sync.Mutex
	n  byte
}

func (r *countReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range p {
		r.n++
		p[i] = r.n
	}
	return len(p), nil
}

// manifest7 returns the embedded 7.x manifest.
func manifest7(t testing.TB) *compat.Manifest {
	t.Helper()
	ms, err := compat.Load("")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.Name == "7.x" {
			return m
		}
	}
	t.Fatal("no 7.x manifest")
	return nil
}

// onlineEnv returns the harness's online environment over the temporary
// home (fsxtest, fixed clocks, deterministic tokens).
func (h *harness) onlineEnv() *onlineEnv {
	if h.ol != nil {
		return h.ol
	}
	m := manifest7(h.t)
	cfg := config.Default()
	sd := filepath.Join(h.home, "Library", "Application Support", "paritydesk")
	store := online.NewStore(sd, fsxtest.New(h.t))
	rnd := &countReader{}
	store.Rand = rnd
	var mu sync.Mutex
	store.Lock = func(context.Context, time.Duration) (func(), error) { mu.Lock(); return mu.Unlock, nil }
	svc := &online.Service{Store: store, Config: cfg.Online, Clock: clock.NewFake(fixedTime),
		Mono: online.NewFakeMono("boot-1", 1_000_000_000_000), Providers: online.Registry(),
		Settings: online.NewSettings(m.Online, cfg.Online), Rand: rnd}
	h.ol = &onlineEnv{svc: svc, root: func() (*state.Root, error) { return h.root, nil }, defaultDir: true,
		spec: m.Online, cfg: cfg,
		insideApp: func(context.Context) (bool, error) { return h.inApp, nil },
		replica: func(context.Context, time.Time) online.ReplicaObs {
			return online.ReplicaObs{Known: true, Replica: olReplica}
		},
		activeOrg: func(context.Context, ids.AccountID) (ids.OrgID, bool) { return "", false }}
	return h.ol
}

// sidebarDraft builds a real draft: the whole-list group-by and sort
// differ, so the sidebar provider queues one set_view (plus the probe
// and the list_sections read).
func sidebarDraft(t testing.TB, m *compat.Manifest) online.Draft {
	t.Helper()
	snap := func(v string) *model.RowSnapshot {
		s := &model.RowSnapshot{Row: model.RowSidebarView, Schema: 1, Items: map[string]model.Item{}}
		it, err := model.NewItem("section:sessions", json.RawMessage(v), nil, model.Stamp{}, false)
		if err != nil {
			t.Fatal(err)
		}
		s.Items[it.Key] = it
		return s
	}
	desired := snap(`{"id":"sessions","kind":"sessions","groupBy":"status","sortBy":"alpha"}`)
	current := snap(`{"id":"sessions","kind":"sessions","groupBy":"date","sortBy":"recency"}`)
	in := online.BuildInput{Spec: m.Online, Config: config.Default().Online, From: "work", To: "home", Replica: olReplica,
		Outputs: map[model.RowID]*rows.Output{model.RowSidebarView: {Desired: desired}},
		Current: map[model.RowID]*model.RowSnapshot{model.RowSidebarView: current}, Phase: online.PhaseReconcile}
	d := online.Build(in, online.Registry())
	if d.Empty() {
		t.Fatalf("empty draft: %+v", d.Warnings)
	}
	return d
}

// pinDraft is a real pins draft: two sessions to pin (both with an
// approval card); with breaker, three unpins as well, above the deletion
// limits, so the question offers "Pin only".
func pinDraft(t testing.TB, breaker bool) online.Draft {
	t.Helper()
	snap := func(row model.RowID, value string, ns ...int) *model.RowSnapshot {
		s := &model.RowSnapshot{Row: row, Schema: 1, Items: map[string]model.Item{}}
		for _, n := range ns {
			it, err := model.NewItem(fmt.Sprintf("local_00000000-0000-4000-8000-%012d", n), json.RawMessage(value), nil, model.Stamp{}, false)
			if err != nil {
				t.Fatal(err)
			}
			s.Items[it.Key] = it
		}
		return s
	}
	current := []int{}
	if breaker {
		current = []int{3, 4, 5}
	}
	in := online.BuildInput{Spec: manifest7(t).Online, Config: config.Default().Online, From: "work", To: "home", Replica: olReplica,
		Outputs: map[model.RowID]*rows.Output{model.RowPins: {Desired: snap(model.RowPins, "true", 1, 2)}},
		Current: map[model.RowID]*model.RowSnapshot{model.RowPins: snap(model.RowPins, "true", current...),
			model.RowSessions: snap(model.RowSessions, "{}", 1, 2, 3, 4, 5)},
		PinsSource: online.PinsIDB, Phase: online.PhaseReconcile, Limits: plan.Limits{MaxDeleteFraction: 0.1, MaxDeleteMin: 2}}
	d := online.Build(in, online.Registry())
	if d.Empty() {
		t.Fatalf("empty pins draft: %+v", d.Warnings)
	}
	return d
}

// publish publishes d as the harness's queue and records it in h.root.
func (h *harness) publish(d online.Draft) {
	h.t.Helper()
	o := h.onlineEnv()
	if h.root == nil {
		h.root = state.NewRoot("/data")
	}
	h.root.Active, h.root.Epoch = "home", 4
	h.root.Profiles["home"] = &state.ProfileState{Alias: "home", Replica: olReplica}
	sha, err := o.svc.Publish(context.Background(), d, online.QueueHeader{ID: olQueue, Profile: "home", From: "work",
		Replica: olReplica, Epoch: 4, Spec: o.spec})
	if err != nil {
		h.t.Fatal(err)
	}
	h.root.Online = &state.OnlineRecord{Queue: olQueue, Profile: "home", From: "work", Replica: olReplica, Epoch: 4,
		Created: fixedTime, State: "pending", Counts: d.Counts, StepsSHA: sha}
}

// recordProbe writes the probe's result for session olSession.
func (h *harness) recordProbe() {
	h.t.Helper()
	r := online.Result{V: 1, Queue: olQueue, Step: online.ProbeStepID, Tool: online.ToolGetSession,
		InputSHA: online.InputSHA(json.RawMessage(`{"session_id":"self"}`)), Session: olSession, At: fixedTime,
		MonoNS: 1_000_000_000_000, BootID: "boot-1", Outcome: online.OutcomeOK, Parsed: json.RawMessage(`{"id":"` + olLocal + `"}`)}
	if err := h.onlineEnv().svc.Store.CreateResult(r); err != nil {
		h.t.Fatal(err)
	}
}

// nextJSON runs `online next --json` with args and decodes the answer.
func (h *harness) nextJSON(args ...string) (int, string, online.Batch) {
	h.t.Helper()
	code, out, _ := h.run("", append([]string{"online", "next", "--json"}, args...)...)
	var b online.Batch
	_ = json.Unmarshal([]byte(out), &b)
	return code, out, b
}

var onlineCases = []struct {
	name  string
	args  []string
	setup func(h *harness)
	code  int
}{
	{name: "online_status_none", args: []string{"online", "status"}},
	{name: "online_status", args: []string{"online", "status"}, setup: func(h *harness) {
		h.publish(sidebarDraft(h.t, manifest7(h.t)))
		_ = h.onlineEnv().svc.Store.CreateDenial(online.Denial{At: fixedTime, Tool: "mcp__ccd_sidebar__set_unread",
			Reason: "guard.deny_unplanned", Queue: olQueue}, online.MonoNow{NS: 5, OK: true})
	}},
	{name: "online_next_calls", args: []string{"online", "next", "--session", olSession}, setup: func(h *harness) {
		h.publish(sidebarDraft(h.t, manifest7(h.t)))
	}},
	{name: "online_next_question", args: []string{"online", "next", "--session", olSession}, setup: func(h *harness) {
		h.publish(pinDraft(h.t, false))
		h.nextJSON("--session", olSession)
		h.recordProbe()
	}},
	{name: "online_next_question_breaker", args: []string{"online", "next", "--session", olSession}, setup: func(h *harness) {
		h.publish(pinDraft(h.t, true))
		h.nextJSON("--session", olSession)
		h.recordProbe()
	}},
	{name: "online_next_wait", args: []string{"online", "next", "--session", olSession}, setup: func(h *harness) {
		h.publish(sidebarDraft(h.t, manifest7(h.t)))
		h.nextJSON("--session", olSession)
		h.recordProbe()
	}},
	{name: "online_next_no_hooks", args: []string{"online", "next", "--session", olSession}, setup: func(h *harness) {
		h.publish(sidebarDraft(h.t, manifest7(h.t)))
		h.nextJSON("--session", olSession)
		h.nextJSON("--session", olSession)
	}},
	{name: "online_next_busy", args: []string{"online", "next", "--session", olOther}, setup: func(h *harness) {
		h.publish(sidebarDraft(h.t, manifest7(h.t)))
		h.nextJSON("--session", olSession)
	}},
	{name: "online_done", args: []string{"online", "done", "--session", olSession}, setup: func(h *harness) {
		h.publish(sidebarDraft(h.t, manifest7(h.t)))
		h.svc.confirm2 = &orchestrator.OnlineConfirmResult{State: "complete", Queue: olQueue, Profile: "home",
			Counts: online.Counts{Sidebar: 1, Done: 1}, BasesAdvanced: 2, ContestedCleared: 1,
			Steps: []online.Step{{ID: "r000", Kind: online.KindRead, Status: online.StatusDone},
				{ID: "r001", Kind: online.KindRead, Status: online.StatusDone},
				{ID: "s000", Kind: online.KindSidebar, Status: online.StatusDone}}}
	}},
	{name: "online_cancel", args: []string{"online", "cancel"}, setup: func(h *harness) {
		h.publish(sidebarDraft(h.t, manifest7(h.t)))
	}},
	{name: "online_capture", args: []string{"online", "capture", "--session", olSession}, setup: func(h *harness) {
		h.root = state.NewRoot("/data")
		h.root.Active = "work"
		h.root.Profiles["work"] = &state.ProfileState{Alias: "work", Replica: olReplica}
	}},
	{name: "plan_online", args: []string{"plan", "home"}, setup: func(h *harness) {
		r := h.svc.runResult(fakePlan())
		r.Online = &online.Summary{PinsAdd: 2, PinsRemove: 1, Sidebar: 1, PinsSource: "idb"}
		h.svc.result = r
	}},
	{name: "plan_only_pins", args: []string{"plan", "--only", "pins", "home"}, setup: func(h *harness) {
		r := h.svc.runResult(fakePlan())
		r.Online = &online.Summary{PinsAdd: 1, PinsRemove: 1, PinsKeep: 1, PinsBlocked: 1, PinsSource: "idb"}
		r.Warnings = []string{i18n.T(i18n.OnlineWarnPinsFamilyBlocked, 1)}
		h.svc.result = r
	}},
	{name: "plan_pins_unreadable", args: []string{"plan", "home"}, setup: func(h *harness) {
		r := h.svc.runResult(fakePlan())
		r.Online = &online.Summary{Sidebar: 1, PinsSource: "none", PinsReason: "unsupported"}
		r.Warnings = []string{i18n.T(i18n.OnlineWarnPinsUnreadable, "home"), i18n.T(i18n.OnlineWarnPinsReason, i18n.T(i18n.PinsReasonUnsupported))}
		h.svc.result = r
	}},
	{name: "plan_pins_preview", args: []string{"plan", "home"}, setup: func(h *harness) {
		r := h.svc.runResult(fakePlan())
		r.Online = &online.Summary{PinsSource: "none", PinsReason: "torn"}
		h.svc.result = r
	}},
	{name: "switch_pins", args: []string{"switch", "home", "--yes"}, setup: func(h *harness) {
		r := h.svc.runResult(fakePlan())
		r.Online = &online.Summary{PinsAdd: 2, PinsRemove: 1, PinsKeep: 1, PinsSource: "mirror", Queued: true}
		h.svc.result = r
	}},
	{name: "doctor_pins", args: []string{"doctor"}, setup: func(h *harness) {
		h.svc.doctorChecks = []orchestrator.Check{
			{ID: "pins", Status: "ok", Detail: i18n.T(i18n.DoctorOnlinePinsIDBCount, 3)},
			{ID: "pins-mirror", Status: "warn", Detail: i18n.T(i18n.DoctorOnlinePinsMirrorDiffers)},
			{ID: "pins", Status: "ok", Detail: i18n.T(i18n.DoctorOnlinePinsOff)},
			{ID: "pins", Status: "warn", Detail: i18n.T(i18n.DoctorOnlinePinsMirrorCount, i18n.T(i18n.PinsReasonBlobWrapped), 3)},
			{ID: "pins", Status: "warn", Detail: i18n.T(i18n.DoctorOnlinePinsUnavailableReason, i18n.T(i18n.PinsReasonUnsupported))},
			{ID: "pins", Status: "fail", Detail: i18n.T(i18n.DoctorOnlinePinsStale)},
		}
	}},
	{name: "status_online", args: []string{"status"}, setup: func(h *harness) {
		h.svc.status.Online = &orchestrator.OnlineStatus{State: "pending", Profile: "work",
			Counts: model.OnlineCounts{PinsAdd: 2, PinsRemove: 1, Sidebar: 1}}
	}},
	{name: "status_short_online", args: []string{"status", "--short"}, setup: func(h *harness) {
		h.svc.status.Online = &orchestrator.OnlineStatus{State: "pending", Profile: "work",
			Counts: model.OnlineCounts{PinsAdd: 2, PinsRemove: 1, Sidebar: 1}}
	}},
}

func TestOnlineJSONGolden(t *testing.T) {
	for _, tc := range onlineCases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			if tc.setup != nil {
				tc.setup(h)
			}
			code, out, _ := h.run("", append([]string{"--json"}, tc.args...)...)
			if code != tc.code {
				t.Fatalf("code %d, want %d; stdout %s", code, tc.code, out)
			}
			if !strings.HasPrefix(out, `{"v":1`) || strings.Count(out, "\n") != 1 {
				t.Fatalf("not one JSON document with v first: %q", out)
			}
			golden(t, "json/"+tc.name, h.normalize(out))
		})
	}
}

// TestOnlineTextOutput: the text forms print the fixed lines.
func TestOnlineTextOutput(t *testing.T) {
	h := newHarness(t)
	if _, out, _ := h.run("", "online", "status"); out != i18n.T(i18n.OnlineStatusNone)+"\n" {
		t.Fatalf("status: %q", out)
	}
	h.svc.status.Online = &orchestrator.OnlineStatus{State: "pending", Profile: "work", Counts: model.OnlineCounts{Sidebar: 2}}
	_, out, _ := h.run("", "status", "--short")
	if !strings.Contains(out, " online:2\n") {
		t.Fatalf("short status: %q", out)
	}
	_, out, _ = h.run("", "status")
	if !strings.Contains(out, i18n.T(i18n.OnlineStatusPending, 0, 0, 2, "work")) {
		t.Fatalf("status: %q", out)
	}
}

// TestOnlineFlagValidation: --session, --token, --unavailable, --question
// and --answer are validated (exit 2).
func TestOnlineFlagValidation(t *testing.T) {
	for _, args := range [][]string{
		{"online", "next", "--session", "not-a-uuid"},
		{"online", "next", "--token", "XYZ"},
		{"online", "next", "--unavailable", "rm -rf"},
		{"online", "decide", "--question", "pins", "--answer", "Apply all"},
		{"online", "done", "--session", "x"},
		{"online", "capture", "--session", "../x"},
	} {
		h := newHarness(t)
		if code, _, _ := h.run("", args...); code != 2 {
			t.Errorf("%v: code %d, want 2", args, code)
		}
	}
}

// TestOnlineReadOnly: writing leaves refuse read-only mode; capture
// answers state none.
func TestOnlineReadOnly(t *testing.T) {
	for _, leaf := range []string{"next", "decide", "retry", "done", "cancel"} {
		h := newHarness(t)
		args := []string{"online", leaf, "--read-only"}
		if leaf == "decide" {
			args = append(args, "--question", "pins", "--answer", "skip")
		}
		if code, _, _ := h.run("", args...); code != 2 {
			t.Errorf("%s: code %d, want 2", leaf, code)
		}
	}
	h := newHarness(t)
	code, out, _ := h.run("", "online", "capture", "--json", "--read-only")
	if code != 0 || !strings.Contains(out, `"state":"none"`) {
		t.Fatalf("capture read-only: %d %s", code, out)
	}
}

// TestOnlineSupportDir: next and capture answer none with the fixed line
// when --app-support-dir is not the default (the hooks would not see the
// lease).
func TestOnlineSupportDir(t *testing.T) {
	h := newHarness(t)
	h.onlineEnv().defaultDir = false
	for _, leaf := range []string{"next", "capture"} {
		code, out, _ := h.run("", "online", leaf, "--json")
		if code != 0 || !strings.Contains(out, `"state":"none"`) || !strings.Contains(out, i18n.T(i18n.OnlineErrSupportDir)) {
			t.Fatalf("%s: %d %s", leaf, code, out)
		}
	}
}

// TestOnlineRunOpen: queue commands exit 41 while a run is open; cancel
// still works.
func TestOnlineRunOpen(t *testing.T) {
	h := newHarness(t)
	h.publish(sidebarDraft(t, manifest7(t)))
	h.root.Run = &state.Run{ID: "run-0002", Kind: state.RunSwitch}
	for _, leaf := range []string{"next", "retry", "done"} {
		if code, _, _ := h.run("", "online", leaf); code != 41 {
			t.Errorf("%s: code %d, want 41", leaf, code)
		}
	}
	if code, out, _ := h.run("", "online", "cancel", "--json"); code != 0 || !strings.Contains(out, `"state":"cancelled"`) {
		t.Fatalf("cancel: %d %s", code, out)
	}
}

// TestOnlineReconcileLoop runs the skill's loop against the real store:
// probe, read, wait for the settle time, the set_view call, complete.
func TestOnlineReconcileLoop(t *testing.T) {
	h := newHarness(t)
	h.publish(sidebarDraft(t, manifest7(t)))
	o := h.onlineEnv()
	_, _, b := h.nextJSON("--session", olSession)
	if b.State != online.StateCalls || len(b.Calls) != 1 || b.Calls[0].ID != online.ProbeStepID || b.Token == "" {
		t.Fatalf("first answer %+v", b)
	}
	token := b.Token
	h.recordProbe()
	_, _, b = h.nextJSON("--session", olSession, "--token", token)
	if b.State != online.StateWait || b.RetryAfterMS <= 0 {
		t.Fatalf("second answer %+v", b)
	}
	o.svc.Clock.(*clock.Fake).Advance(time.Minute)
	_, _, b = h.nextJSON("--session", olSession, "--token", token)
	if b.State != online.StateCalls || len(b.Calls) != 1 || b.Calls[0].Tool != online.ToolListSections {
		t.Fatalf("third answer %+v", b)
	}
	sections := `[{"id":"sessions","kind":"sessions","name":"Sessions","order":0,"session_count":3,"group_by":"date","sort_by":"recency","ascending":false,"collapsed":false,"shown":true}]`
	h.recordTool(b.Calls[0], sections)
	_, _, b = h.nextJSON("--session", olSession, "--token", token)
	if b.State != online.StateCalls || len(b.Calls) != 1 || b.Calls[0].Tool != online.ToolSetView ||
		string(b.Calls[0].Input) != `{"group_by":"state","sort_by":"alpha"}` {
		t.Fatalf("fourth answer %+v %s", b, b.Calls)
	}
	h.recordTool(b.Calls[0], `{"changed":1,"view":{"group_by":"state","status":"active","sort_by":"alpha","last_activity":0}}`)
	_, _, b = h.nextJSON("--session", olSession, "--token", token)
	if b.State != online.StateComplete || b.Counts.Done != 1 {
		t.Fatalf("last answer %+v", b)
	}
	code, out, _ := h.run("", "online", "done", "--json", "--session", olSession, "--token", token)
	if code != 0 || h.svc.confirmIn.Queue != olQueue || h.svc.confirmIn.Caller.Token != token || !strings.Contains(out, `"state":"complete"`) {
		t.Fatalf("done: %d %s %+v", code, out, h.svc.confirmIn)
	}
}

// recordTool runs the PostToolUse hook for a call with a text result.
func (h *harness) recordTool(c online.Call, text string) {
	h.t.Helper()
	payload := map[string]any{"session_id": olSession, "hook_event_name": "PostToolUse", "tool_name": c.Tool,
		"tool_input": c.Input, "tool_response": map[string]any{"content": []map[string]string{{"type": "text", "text": text}}}}
	b, err := json.Marshal(payload)
	if err != nil {
		h.t.Fatal(err)
	}
	code, out, _ := h.run(string(b), "hook", "post-tool-use")
	if code != 0 || out != "" {
		h.t.Fatalf("post-tool-use: %d %q", code, out)
	}
}

// TestHookToolLeaves: the guard prints one deny document for an unplanned
// call while armed; post-tool-use is lenient (exit 0, silent) on a bad
// command line, and pre-tool-use exits non-zero without output, so the
// shim applies its armed check.
func TestHookToolLeaves(t *testing.T) {
	h := newHarness(t)
	h.publish(sidebarDraft(t, manifest7(t)))
	h.nextJSON("--session", olSession)
	call := `{"session_id":"` + olSession + `","hook_event_name":"PreToolUse","tool_name":"mcp__ccd_sidebar__set_unread","tool_input":{"session_id":"self","unread":true}}`
	code, out, _ := h.run(call, "hook", "pre-tool-use", "--plugin-version", "9.9.9")
	if code != 0 || !bytes.Contains([]byte(out), []byte(`"permissionDecision":"deny"`)) || strings.Count(out, "\n") != 1 {
		t.Fatalf("pre-tool-use: %d %q", code, out)
	}
	if code, out, _ := h.run("not json", "hook", "post-tool-use", "--bogus"); code != 0 || out != "" {
		t.Fatalf("post-tool-use: %d %q", code, out)
	}
	if code, out, errOut := h.run("not json", "hook", "pre-tool-use", "--bogus"); code == 0 || out != "" || errOut != "" {
		t.Fatalf("pre-tool-use with a bad flag: %d %q %q", code, out, errOut)
	}
	// Dependencies that cannot be built: no decision is possible.
	h.sys.toolDeps = func(context.Context, *env) (hook.ToolDeps, error) { return hook.ToolDeps{}, fmt.Errorf("no home") }
	if code, out, _ := h.run(call, "hook", "pre-tool-use"); code == 0 || out != "" {
		t.Fatalf("pre-tool-use without deps: %d %q", code, out)
	}
	// A panic outside the guard.
	h.sys.toolDeps = func(context.Context, *env) (hook.ToolDeps, error) { panic("boom") }
	if code, out, _ := h.run(call, "hook", "pre-tool-use"); code == 0 || out != "" {
		t.Fatalf("pre-tool-use after a panic: %d %q", code, out)
	}
	if code, _, _ := h.run(call, "hook", "post-tool-use"); code != 0 {
		t.Fatalf("post-tool-use after a panic: %d", code)
	}
}
