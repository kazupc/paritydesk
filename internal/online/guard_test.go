package online

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/kazupc/paritydesk/internal/i18n"
)

const (
	toolSetPinnedFull = "mcp__ccd_sidebar__set_pinned"
	pinInput          = `{"pinned":true,"session_id":"local_00000000-0000-4000-8000-000000000003"}`
)

// testPins is a provider of pin steps for the guard and next tests (the
// pins unit owns the real one).
type testPins struct{ question bool }

func (testPins) Name() string  { return "pins-test" }
func (testPins) Kinds() []Kind { return []Kind{KindPin} }
func (testPins) Build(BuildInput) ([]Step, []Warning, error) {
	return nil, nil, nil
}
func (testPins) Advance(*Queue, map[StepID]Result, time.Time) error { return nil }
func (testPins) Interpret(s Step, r Result) (StepStatus, string)    { return InterpretBuiltin(s, r) }
func (testPins) Tools() ToolPolicy {
	return ToolPolicy{Kinds: []Kind{KindPin}, Exact: []string{toolSetPinnedFull}}
}
func (p testPins) Questions(q *Queue) []Question {
	if !p.question || q.Decisions["pins"] != "" {
		return nil
	}
	for _, s := range q.Steps {
		if s.Kind == KindPin && s.Status == StatusPending {
			return []Question{{ID: "pins", Header: i18n.Raw("Pins"), Text: i18n.Raw("t"), Options: []Option{{ID: "apply"}, {ID: "add_only"}, {ID: optionSkip}}}}
		}
	}
	return nil
}

func pinStep(id StepID, input string, status StepStatus) Step {
	return Step{ID: id, Kind: KindPin, Tool: toolSetPinnedFull, Input: json.RawMessage(input), Status: status,
		Needs: []string{"read:r000", "decision:pins"}, After: []StepID{}, Card: true,
		Expect: Expect{Format: FormatText, OK: []string{"Session pinned."}}}
}

// guardQueue is an armed queue: the probe (done) and one pin step.
func guardQueue(pinStatus StepStatus, decided bool) *Queue {
	probe := ProbeStep()
	probe.Status = StatusDone
	q := &Queue{V: 1, ID: tQueue, Phase: PhaseReconcile, State: QueueActive, Steps: []Step{probe, pinStep("p000", pinInput, pinStatus)}}
	if decided {
		q.Decisions = map[string]string{"pins": "apply"}
	}
	return q
}

func guardInput(q *Queue, tool, input string) GuardInput {
	l := &Lease{V: 1, Queue: tQueue, Session: tSession, BootID: tBoot, MonoDeadlineNS: 2e12, ExpiresAt: t0.Add(time.Hour)}
	gi := GuardInput{Mode: GuardArmed, Lease: l, Queue: q, Session: tSession, Tool: tool, Now: t0,
		Mono: MonoNow{BootID: tBoot, NS: 1e12, OK: true}, Policies: Policies(append(baseRegistry(), testPins{})),
		Recorded: map[StepID]bool{ProbeStepID: true}, Inflight: map[StepID]int{}}
	if input != "" {
		gi.Input = json.RawMessage(input)
	}
	return gi
}

func TestGuardRules(t *testing.T) {
	offered := func() *Queue { return guardQueue(StatusOffered, true) }
	tests := []struct {
		name string
		in   func() GuardInput
		deny i18n.Msg // "" = no decision
		step StepID
	}{
		{"0 other server", func() GuardInput { return guardInput(offered(), "mcp__ccd_turn__update_plan", "{}") }, "", ""},
		{"0 wrap_up", func() GuardInput { return guardInput(offered(), "mcp__ccd_turn__wrap_up", "{}") }, "", ""},
		{"0 mark_chapter", func() GuardInput { return guardInput(offered(), "mcp__ccd_session__mark_chapter", "{}") }, "", ""},
		{"0 view", func() GuardInput { return guardInput(offered(), "mcp__ccd_view__get_layout", "{}") }, "", ""},
		{"0 user tool", func() GuardInput { return guardInput(offered(), "Bash", `{"command":"ls"}`) }, "", ""},
		{"1 no lease", func() GuardInput {
			g := guardInput(offered(), "mcp__ccd_sidebar__delete_group", "{}")
			g.Lease = nil
			return g
		}, "", ""},
		{"1 always mode floor unarmed", func() GuardInput {
			g := guardInput(offered(), "mcp__ccd_sidebar__delete_group", "{}")
			g.Lease, g.Mode = nil, GuardAlways
			return g
		}, i18n.GuardDenyFloor, ""},
		{"1 always mode ArtifactData write", func() GuardInput {
			g := guardInput(offered(), "ArtifactData", `{"action":"set"}`)
			g.Lease, g.Mode = nil, GuardAlways
			return g
		}, i18n.GuardDenyFloor, ""},
		{"1 always mode Artifact read", func() GuardInput {
			g := guardInput(offered(), "Artifact", `{"action":"read"}`)
			g.Lease, g.Mode = nil, GuardAlways
			return g
		}, "", ""},
		{"1 lease of another session", func() GuardInput {
			g := guardInput(offered(), toolSetPinnedFull, pinInput)
			g.Session = tOther
			return g
		}, "", ""},
		{"1 stop", func() GuardInput {
			g := guardInput(offered(), toolSetPinnedFull, `{"x":1}`)
			g.Stop = true
			return g
		}, "", ""},
		{"1 wall expired", func() GuardInput {
			g := guardInput(offered(), toolSetPinnedFull, `{"x":1}`)
			g.Now = t0.Add(2 * time.Hour)
			return g
		}, "", ""},
		{"1 mono expired", func() GuardInput {
			g := guardInput(offered(), toolSetPinnedFull, `{"x":1}`)
			g.Mono.NS = 3e12
			return g
		}, "", ""},
		{"1 other boot", func() GuardInput {
			g := guardInput(offered(), toolSetPinnedFull, `{"x":1}`)
			g.Mono.BootID = "boot-2"
			return g
		}, "", ""},
		{"2 floor", func() GuardInput { return guardInput(offered(), "mcp__ccd_session_mgmt__delete_session", "{}") }, i18n.GuardDenyFloor, ""},
		{"2 db tool", func() GuardInput { return guardInput(offered(), "mcp__claude-artifacts__db_set", "{}") }, i18n.GuardDenyFloor, ""},
		{"2 share", func() GuardInput { return guardInput(offered(), "mcp__claude-artifacts__share_artifact", "{}") }, i18n.GuardDenyFloor, ""},
		{"2 list_sessions", func() GuardInput { return guardInput(offered(), "mcp__ccd_session_mgmt__list_sessions", "{}") }, i18n.GuardDenyFloor, ""},
		{"2a artifact tool", func() GuardInput { return guardInput(offered(), "Artifact", "{}") }, i18n.GuardDenyArtifactTool, ""},
		{"3 queue id mismatch mutation", func() GuardInput {
			q := offered()
			q.ID = "run-0002"
			return guardInput(q, toolSetPinnedFull, pinInput)
		}, i18n.GuardDenyUnreadable, ""},
		{"3 queue id mismatch read", func() GuardInput {
			q := offered()
			q.ID = "run-0002"
			return guardInput(q, "mcp__ccd_sidebar__list_sections", "{}")
		}, "", ""},
		{"3 missing queue", func() GuardInput {
			g := guardInput(offered(), toolSetPinnedFull, pinInput)
			g.Queue = nil
			return g
		}, i18n.GuardDenyUnreadable, ""},
		{"3 cancelled", func() GuardInput {
			q := offered()
			q.State = QueueCancelled
			return guardInput(q, toolSetPinnedFull, pinInput)
		}, i18n.GuardDenyUnreadable, ""},
		{"4 read list", func() GuardInput { return guardInput(offered(), "mcp__ccd_settings__get_settings", "{}") }, "", ""},
		{"5 unparsable input", func() GuardInput { return guardInput(offered(), toolSetPinnedFull, "") }, i18n.GuardDenyUnreadable, ""},
		{"6 unbound lease, not the probe", func() GuardInput {
			g := guardInput(offered(), toolSetPinnedFull, pinInput)
			g.Lease.Session = ""
			return g
		}, i18n.GuardDenyNotYet, ""},
		{"6 unbound lease, the probe", func() GuardInput {
			q := offered()
			q.Steps[0].Status = StatusOffered
			g := guardInput(q, ToolGetSession, `{"session_id":"self"}`)
			g.Lease.Session = ""
			g.Session = tOther
			return g
		}, "", ProbeStepID},
		{"7 offered and ready", func() GuardInput { return guardInput(offered(), toolSetPinnedFull, pinInput) }, "", "p000"},
		{"7 key order irrelevant", func() GuardInput {
			return guardInput(offered(), toolSetPinnedFull, `{"session_id":"local_00000000-0000-4000-8000-000000000003","pinned":true}`)
		}, "", "p000"},
		{"8 pending (not offered)", func() GuardInput {
			return guardInput(guardQueue(StatusPending, true), toolSetPinnedFull, pinInput)
		}, i18n.GuardDenyNotYet, ""},
		{"8 before the decision", func() GuardInput {
			return guardInput(guardQueue(StatusOffered, false), toolSetPinnedFull, pinInput)
		}, i18n.GuardDenyNotYet, ""},
		{"8 attempts used, call in flight", func() GuardInput {
			g := guardInput(offered(), toolSetPinnedFull, pinInput)
			g.Inflight = map[StepID]int{"p000": 1}
			return g
		}, i18n.GuardDenyDuplicate, ""},
		{"8 attempts used, result recorded", func() GuardInput {
			g := guardInput(offered(), toolSetPinnedFull, pinInput)
			g.Inflight = map[StepID]int{"p000": 1}
			g.Recorded["p000"] = true
			return g
		}, i18n.GuardDenyNotYet, ""},
		{"8 get_session (the probe is done)", func() GuardInput {
			return guardInput(offered(), ToolGetSession, `{"session_id":"self"}`)
		}, i18n.GuardDenyNotYet, ""},
		{"9 extra key", func() GuardInput {
			return guardInput(offered(), toolSetPinnedFull, `{"pinned":true,"session_id":"local_00000000-0000-4000-8000-000000000003","x":1}`)
		}, i18n.GuardDenyUnplanned, ""},
		{"9 missing key", func() GuardInput { return guardInput(offered(), toolSetPinnedFull, `{"pinned":true}`) }, i18n.GuardDenyUnplanned, ""},
		{"9 type change", func() GuardInput {
			return guardInput(offered(), toolSetPinnedFull, `{"pinned":"true","session_id":"local_00000000-0000-4000-8000-000000000003"}`)
		}, i18n.GuardDenyUnplanned, ""},
		{"9 trimmed id variant", func() GuardInput {
			return guardInput(offered(), toolSetPinnedFull, `{"pinned":true,"session_id":" local_00000000-0000-4000-8000-000000000003"}`)
		}, i18n.GuardDenyUnplanned, ""},
		{"9 self instead of the id", func() GuardInput {
			return guardInput(offered(), toolSetPinnedFull, `{"pinned":true,"session_id":"self"}`)
		}, i18n.GuardDenyUnplanned, ""},
		{"9 get_session of another session", func() GuardInput {
			return guardInput(offered(), ToolGetSession, `{"session_id":"local_00000000-0000-4000-8000-000000000003"}`)
		}, i18n.GuardDenyUnplanned, ""},
		{"9 unplanned set_unread", func() GuardInput {
			return guardInput(offered(), "mcp__ccd_sidebar__set_unread", `{"session_id":"self","unread":true}`)
		}, i18n.GuardDenyUnplanned, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := Decide(tc.in())
			if d.Deny != (tc.deny != "") || d.Reason != tc.deny || d.Step != tc.step {
				t.Fatalf("decision %+v, want deny %q step %q", d, tc.deny, tc.step)
			}
			if tc.step != "" && d.N != 1 {
				t.Fatalf("N %d", d.N)
			}
		})
	}
}

// TestGuardArtifactsWording: in an artifacts round the reasons that name
// the next command name paritydesk artifacts next; online next only
// answers that the queue belongs to /paritydesk:artifacts there. The
// other reasons keep their wording.
func TestGuardArtifactsWording(t *testing.T) {
	artifacts := func(st StepStatus) *Queue {
		q := guardQueue(st, true)
		q.Kind = QueueKindArtifacts
		return q
	}
	tests := []struct {
		name string
		in   func() GuardInput
		want i18n.Msg
	}{
		{"unplanned", func() GuardInput {
			return guardInput(artifacts(StatusOffered), "mcp__ccd_sidebar__set_unread", `{"session_id":"self","unread":true}`)
		}, i18n.GuardDenyUnplannedArtifacts},
		{"not yet", func() GuardInput { return guardInput(artifacts(StatusPending), toolSetPinnedFull, pinInput) }, i18n.GuardDenyNotYetArtifacts},
		{"unbound lease", func() GuardInput {
			g := guardInput(artifacts(StatusOffered), toolSetPinnedFull, pinInput)
			g.Lease.Session = ""
			return g
		}, i18n.GuardDenyNotYetArtifacts},
		{"duplicate", func() GuardInput {
			g := guardInput(artifacts(StatusOffered), toolSetPinnedFull, pinInput)
			g.Inflight = map[StepID]int{"p000": 1}
			return g
		}, i18n.GuardDenyDuplicateArtifacts},
		{"floor", func() GuardInput {
			return guardInput(artifacts(StatusOffered), "mcp__ccd_session_mgmt__delete_session", "{}")
		}, i18n.GuardDenyFloor},
		{"unreadable", func() GuardInput { return guardInput(artifacts(StatusOffered), toolSetPinnedFull, "") }, i18n.GuardDenyUnreadable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if d := Decide(tc.in()); !d.Deny || d.Reason != tc.want {
				t.Fatalf("decision %+v, want %q", d, tc.want)
			}
		})
	}
	for _, m := range []i18n.Msg{i18n.GuardDenyUnplannedArtifacts, i18n.GuardDenyNotYetArtifacts, i18n.GuardDenyDuplicateArtifacts} {
		if txt := i18n.T(m); !strings.Contains(txt, "paritydesk artifacts next") || strings.Contains(txt, "online next") {
			t.Fatalf("%s: %q", m, txt)
		}
	}
	if got := DenyReasonFor(guardQueue(StatusOffered, true), i18n.GuardDenyUnplanned); got != i18n.GuardDenyUnplanned {
		t.Fatalf("switch round reason %q", got)
	}
}

// TestGuardRetryAttempt: after online retry of a step that was called
// once, the second pass is allowed, and no third.
func TestGuardRetryAttempt(t *testing.T) {
	q := guardQueue(StatusOffered, true)
	q.Steps[1].Retries, q.Steps[1].RetryBase = 1, 1
	g := guardInput(q, toolSetPinnedFull, pinInput)
	g.Inflight = map[StepID]int{"p000": 1}
	if d := Decide(g); d.Deny || d.Step != "p000" || d.N != 2 {
		t.Fatalf("decision %+v", d)
	}
	g.Inflight = map[StepID]int{"p000": 2}
	if d := Decide(g); !d.Deny || d.Reason != i18n.GuardDenyDuplicate {
		t.Fatalf("third pass %+v", d)
	}
}

// TestGuardRetryNotCalled: a retried step that was never called (no
// marker) gets exactly one pass, not two: a second identical call while
// the first has no result is a duplicate.
func TestGuardRetryNotCalled(t *testing.T) {
	q := guardQueue(StatusOffered, true)
	q.Steps[1].Retries, q.Steps[1].RetryBase = 1, 0
	g := guardInput(q, toolSetPinnedFull, pinInput)
	if d := Decide(g); d.Deny || d.Step != "p000" || d.N != 1 {
		t.Fatalf("first pass %+v", d)
	}
	g.Inflight = map[StepID]int{"p000": 1}
	if d := Decide(g); !d.Deny || d.Reason != i18n.GuardDenyDuplicate {
		t.Fatalf("second pass %+v", d)
	}
}

// TestGuardFloorWinsOverListedStep: a queue that lists a floor tool is
// invalid, and the guard denies the tool anyway.
func TestGuardFloorWinsOverListedStep(t *testing.T) {
	q := guardQueue(StatusOffered, true)
	q.Steps[1].Tool = "mcp__ccd_sidebar__delete_group"
	if err := q.Validate(Policies(append(baseRegistry(), testPins{})), nil); err == nil {
		t.Fatal("queue with a floor tool validated")
	}
	if d := Decide(guardInput(q, "mcp__ccd_sidebar__delete_group", pinInput)); !d.Deny || d.Reason != i18n.GuardDenyFloor {
		t.Fatalf("decision %+v", d)
	}
	q.Guard.AlwaysDeny = []string{"mcp__ccd_sidebar__set_unread"}
	if d := Decide(guardInput(q, "mcp__ccd_sidebar__set_unread", "{}")); d.Reason != i18n.GuardDenyFloor {
		t.Fatalf("manifest always_deny: %+v", d)
	}
}

// TestGuardInputHash: steps with an input hash match by hash.
func TestGuardInputHash(t *testing.T) {
	q := guardQueue(StatusOffered, true)
	q.Steps[1].InputSHA256 = InputSHA(json.RawMessage(pinInput))
	q.Steps[1].Input = nil
	if d := Decide(guardInput(q, toolSetPinnedFull, pinInput)); d.Deny || d.Step != "p000" {
		t.Fatalf("decision %+v", d)
	}
}

// TestGuardSoundness (property 1): no decision for a guarded mutation
// only when the call equals an offered, ready step exactly; never for a
// floor tool while armed; always none for another server's tool.
func TestGuardSoundness(t *testing.T) {
	tools := []string{toolSetPinnedFull, "mcp__ccd_sidebar__set_view", "mcp__ccd_sidebar__delete_group",
		"mcp__ccd_session_mgmt__list_sessions", "mcp__ccd_turn__update_plan", "mcp__other__thing", ToolGetSession,
		"mcp__claude-artifacts__db_get", "mcp__ccd_sidebar__list_sections"}
	inputs := []string{pinInput, `{"pinned":false,"session_id":"local_00000000-0000-4000-8000-000000000003"}`, `{}`, `{"session_id":"self"}`}
	rapid.Check(t, func(rt *rapid.T) {
		status := rapid.SampledFrom([]StepStatus{StatusPending, StatusOffered, StatusDone, StatusFailed}).Draw(rt, "status")
		q := guardQueue(status, rapid.Bool().Draw(rt, "decided"))
		g := guardInput(q, rapid.SampledFrom(tools).Draw(rt, "tool"), rapid.SampledFrom(inputs).Draw(rt, "input"))
		g.Inflight = map[StepID]int{"p000": rapid.IntRange(0, 2).Draw(rt, "inflight")}
		d := Decide(g)
		srv := serverOf(g.Tool)
		switch {
		case srv != "ccd_sidebar" && srv != "ccd_session_mgmt" && srv != "claude-artifacts" && g.Tool != "Artifact":
			if d.Deny {
				rt.Fatalf("decision for another server's tool %s", g.Tool)
			}
		case InFloor(g.Tool, nil) || inUntrusted(g.Tool):
			if !d.Deny {
				rt.Fatalf("floor tool %s passed", g.Tool)
			}
		case !d.Deny && g.Tool != "mcp__ccd_sidebar__list_sections":
			s := q.Step(d.Step)
			if s == nil || s.Status != StatusOffered || s.Tool != g.Tool || string(s.Input) != string(g.Input) {
				rt.Fatalf("%s %s passed without an offered exact step (%+v)", g.Tool, g.Input, d)
			}
		}
	})
}

// baseRegistry is the registry without the real pins provider, for the
// tests that model pin steps with testPins (the real provider validates
// its step data and has its own tests in pins_test.go).
func baseRegistry() []Provider { return []Provider{ProbeProvider(), SidebarProvider()} }
