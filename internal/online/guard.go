package online

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/model"
)

// GuardMode is config online.guard.
type GuardMode string

// Guard modes.
const (
	GuardArmed  GuardMode = "armed"  // the floor is denied only while a queue is armed
	GuardAlways GuardMode = "always" // the floor is denied in every session
)

// GuardedServers are the app servers whose state paritydesk changes or
// whose reads pull other sessions' content (fixed in code; the manifest
// may only add). Every other server's tools get no decision.
var GuardedServers = []string{"ccd_sidebar", "ccd_session_mgmt", "ccd_settings", "claude-artifacts"}

// GuardedTools are single tools outside the guarded servers that the
// guard also looks at (spec-artifacts A12).
var GuardedTools = []string{"Artifact", "ArtifactComments", "ArtifactData", "ArtifactCheck", "mcp__ccd_directory__request_directory"}

// artifactTools are the app's own artifact tools (rule 2a).
var artifactTools = []string{"Artifact", "ArtifactComments", "ArtifactData", "ArtifactCheck"}

// floorTools are denied in every armed session (and in every session with
// guard = "always"), even when a queue lists them.
var floorTools = []string{
	"mcp__claude-artifacts__delete_artifact",
	"mcp__claude-artifacts__delete_asset",
	"mcp__claude-artifacts__declare_capabilities",
	"mcp__ccd_sidebar__delete_group",
	"mcp__ccd_sidebar__delete_section",
	"mcp__ccd_session_mgmt__delete_session",
	"mcp__ccd_session_mgmt__clear_session",
	"mcp__ccd_session_mgmt__set_remote_control",
}

// untrustedReads pull other sessions' content into the context; denied
// while armed.
var untrustedReads = []string{
	"mcp__ccd_session_mgmt__list_events",
	"mcp__ccd_session_mgmt__search_session_transcripts",
	"mcp__ccd_session_mgmt__export_transcript",
	"mcp__ccd_session_mgmt__read_cloud_session",
	"mcp__ccd_session_mgmt__list_sessions",
}

// armedReads run without a step while armed.
var armedReads = []string{
	"mcp__ccd_sidebar__list_sections",
	"mcp__ccd_sidebar__list_groups",
	"mcp__ccd_settings__get_settings",
}

// serverOf returns the server of an MCP tool name ("" otherwise).
func serverOf(tool string) string {
	rest, ok := strings.CutPrefix(tool, "mcp__")
	if !ok {
		return ""
	}
	i := strings.Index(rest, "__")
	if i <= 0 {
		return ""
	}
	return rest[:i]
}

// guardedToolName reports one of the non-MCP guarded tool names.
func guardedToolName(tool string) bool { return slices.Contains(artifactTools, tool) }

// Guarded reports whether the guard looks at tool: a tool of a guarded
// server (or one the manifest adds), or a guarded single tool.
func Guarded(tool string, extra GuardExtra) bool {
	if slices.Contains(GuardedTools, tool) || slices.Contains(extra.ToolsExtra, tool) {
		return true
	}
	srv := serverOf(tool)
	return srv != "" && (slices.Contains(GuardedServers, srv) || slices.Contains(extra.ServersExtra, srv))
}

// InFloor reports a floor tool: the fixed list, any claude-artifacts db_*
// tool, any guarded-server tool whose name contains "share", plus the
// manifest's always_deny.
func InFloor(tool string, alwaysDeny []string) bool {
	if slices.Contains(floorTools, tool) || slices.Contains(alwaysDeny, tool) {
		return true
	}
	srv := serverOf(tool)
	if srv == "" {
		return false
	}
	name := strings.TrimPrefix(tool, "mcp__"+srv+"__")
	if srv == "claude-artifacts" && strings.HasPrefix(name, "db_") {
		return true
	}
	return slices.Contains(GuardedServers, srv) && strings.Contains(name, "share")
}

// inUntrusted reports an untrusted-text read.
func inUntrusted(tool string) bool { return slices.Contains(untrustedReads, tool) }

// alwaysFloorArtifact is the input-dependent floor of the app's artifact
// tools in guard = "always" mode: ArtifactData writes, and Artifact
// deletes, shares and capability declarations.
func alwaysFloorArtifact(tool string, input json.RawMessage) bool {
	if tool != "ArtifactData" && tool != "Artifact" {
		return false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(input, &obj) != nil {
		return true // unreadable input of a guarded tool: deny in always mode
	}
	var action string
	_ = json.Unmarshal(obj["action"], &action)
	if tool == "ArtifactData" {
		return slices.Contains([]string{"set", "update", "delete", "str_replace", "batch"}, action)
	}
	_, caps := obj["capabilities"]
	return action == "delete" || strings.Contains(action, "share") || caps
}

// GuardInput is everything Decide needs. Every value comes from the hook
// payload (validated) or the online folder.
type GuardInput struct {
	Mode     GuardMode
	Stop     bool   // the stop file exists
	Lease    *Lease // nil = none
	Queue    *Queue // the lease's queue; nil = missing
	QueueErr error  // load or validate error of that queue
	// Session is the payload session_id (validated UUID or "").
	Session string
	Tool    string
	// Input is the canonical tool input, nil when unparsable.
	Input json.RawMessage
	// Inflight are the PreToolUse passes recorded per step; Recorded the
	// steps that have a result file.
	Inflight map[StepID]int
	Recorded map[StepID]bool
	Policies []ToolPolicy
	Now      time.Time
	Mono     MonoNow
}

// GuardDecision is a deny (with a fixed reason) or no decision. Step and
// N name the in-flight marker to create when a step matched.
type GuardDecision struct {
	Deny   bool
	Reason i18n.Msg
	Step   StepID
	N      int
}

func deny(m i18n.Msg) GuardDecision { return GuardDecision{Deny: true, Reason: m} }

// Decide is the PreToolUse decision (8.2; the first matching rule wins).
// It never answers "allow": a call that passes gets no decision, so the
// app's own approval cards still appear.
func Decide(in GuardInput) GuardDecision {
	var extra GuardExtra
	if in.Queue != nil {
		extra = in.Queue.Guard
	}
	if len(in.Input) > 0 {
		c, err := model.Canonical(in.Input)
		if err != nil {
			c = nil
		}
		in.Input = c
	}
	// Rule 0: not a guarded tool.
	if !Guarded(in.Tool, extra) {
		return GuardDecision{}
	}
	floor := InFloor(in.Tool, extra.AlwaysDeny)
	// Rule 1: not armed for this session.
	if in.Stop || !in.Lease.Fresh(in.Now, in.Mono, in.Stop) || (in.Lease.Session != "" && in.Lease.Session != in.Session) {
		if in.Mode == GuardAlways && (floor || alwaysFloorArtifact(in.Tool, in.Input)) {
			return deny(i18n.GuardDenyFloor)
		}
		return GuardDecision{}
	}
	// Rule 2: floor and untrusted reads.
	if floor || inUntrusted(in.Tool) {
		return deny(i18n.GuardDenyFloor)
	}
	// Rule 2a: the app's own artifact tools pause while armed.
	if guardedToolName(in.Tool) {
		return deny(i18n.GuardDenyArtifactTool)
	}
	read := slices.Contains(armedReads, in.Tool)
	for _, p := range in.Policies {
		if slices.Contains(p.Reads, in.Tool) {
			read = true
		}
	}
	// Rule 3: no usable queue.
	if in.QueueErr != nil || in.Queue == nil || in.Queue.ID != in.Lease.Queue || in.Queue.State == QueueCancelled {
		if read {
			return GuardDecision{}
		}
		return deny(i18n.GuardDenyUnreadable)
	}
	// Rule 4: armed read list.
	if read {
		return GuardDecision{}
	}
	// Rule 5: input not readable.
	if len(in.Input) == 0 {
		return deny(i18n.GuardDenyUnreadable)
	}
	matches := matchingSteps(in)
	// Rule 6: an unbound lease lets only the probe through.
	if in.Lease.Session == "" {
		probe := false
		for _, s := range matches {
			probe = probe || s.ID == ProbeStepID
		}
		if !probe {
			return deny(i18n.GuardDenyNotYet)
		}
		matches = []*Step{in.Queue.Step(ProbeStepID)}
	}
	if len(matches) == 0 {
		// Rule 9: an unplanned call on a guarded server.
		return deny(i18n.GuardDenyUnplanned)
	}
	// Rule 7: an offered, ready step with attempts left.
	for _, s := range matches {
		if s.Status == StatusOffered && guardReady(in, s) && in.Inflight[s.ID] < s.MaxAttempts() {
			return GuardDecision{Step: s.ID, N: in.Inflight[s.ID] + 1}
		}
	}
	// Rule 8: listed, but not ready. An offered step whose attempts are
	// used by a call still without a result is the same call running in
	// parallel (the gate of 8.3).
	for _, s := range matches {
		if s.Status == StatusOffered && in.Inflight[s.ID] >= s.MaxAttempts() && !in.Recorded[s.ID] {
			return deny(i18n.GuardDenyDuplicate)
		}
	}
	return deny(i18n.GuardDenyNotYet)
}

// DecideLeaseUnknown is the decision while the shim's armed marker says
// a lease is live but the lease itself cannot be read or judged (an
// unreadable, oversized or symlinked lease.json, or no monotonic clock):
// fail closed for every guarded mutation. Tools outside the guarded set
// and the armed read list get no decision; the floor and the untrusted
// reads are denied as in rule 2; the app's artifact tools as in rule 2a.
func DecideLeaseUnknown(tool string, input json.RawMessage, extra GuardExtra, policies []ToolPolicy) GuardDecision {
	if !Guarded(tool, extra) {
		return GuardDecision{}
	}
	if InFloor(tool, extra.AlwaysDeny) || inUntrusted(tool) {
		return deny(i18n.GuardDenyFloor)
	}
	if guardedToolName(tool) {
		return deny(i18n.GuardDenyArtifactTool)
	}
	if slices.Contains(armedReads, tool) {
		return GuardDecision{}
	}
	for _, p := range policies {
		if slices.Contains(p.Reads, tool) {
			return GuardDecision{}
		}
	}
	return deny(i18n.GuardDenyUnreadable)
}

// CaptureCall reports whether a call is exactly one of a live capture's
// read steps for this session (7.1: the capture arms no guard, so the
// switch session keeps every tool it needs, even under a fresh reconcile
// lease). Floor tools and untrusted reads never count.
func CaptureCall(capq *Queue, session, tool string, input json.RawMessage, policies []ToolPolicy) bool {
	if capq == nil || capq.Phase != PhaseCapture || (capq.Session != "" && capq.Session != session) {
		return false
	}
	if InFloor(tool, capq.Guard.AlwaysDeny) || inUntrusted(tool) || guardedToolName(tool) {
		return false
	}
	c, err := model.Canonical(input)
	if err != nil || len(input) == 0 {
		return false
	}
	for _, s := range matchingSteps(GuardInput{Queue: capq, Tool: tool, Input: c, Policies: policies}) {
		if s.Kind == KindCapture {
			return true
		}
	}
	return false
}

// matchingSteps returns the steps with the same tool and canonical input
// (or input hash) as the call.
func matchingSteps(in GuardInput) []*Step {
	var out []*Step
	for i := range in.Queue.Steps {
		s := &in.Queue.Steps[i]
		if s.Tool != in.Tool {
			continue
		}
		input := in.Input
		if pol := policyFor(in.Policies, *s); pol.Normalize != nil {
			n, _, ok := pol.Normalize(in.Tool, input)
			if !ok {
				continue
			}
			if c, err := model.Canonical(n); err == nil {
				input = c
			}
		}
		switch {
		case s.InputSHA256 != "":
			if InputSHA(input) == s.InputSHA256 {
				out = append(out, s)
			}
		case len(s.Input) > 0 && string(s.Input) == string(input):
			out = append(out, s)
		}
	}
	return out
}

// guardReady checks what the guard can see of a step's preconditions:
// answered decisions, recorded reads and done after-steps. settled and
// replica were checked by `online next` when it offered the step.
func guardReady(in GuardInput, s *Step) bool {
	q := in.Queue
	for _, n := range s.Needs {
		switch {
		case strings.HasPrefix(n, "decision:"):
			if v := q.Decisions[strings.TrimPrefix(n, "decision:")]; v == "" || v == optionSkip {
				return false
			}
		case strings.HasPrefix(n, "read:"):
			id := StepID(strings.TrimPrefix(n, "read:"))
			r := q.Step(id)
			if r == nil || !(r.Status == StatusDone || in.Recorded[id]) {
				return false
			}
		}
	}
	for _, a := range s.After {
		as := q.Step(a)
		if as == nil {
			return false
		}
		if as.Status == StatusDone || (as.Status == StatusSkipped && as.Kind == KindSectionCreate && as.Reason == "") {
			continue
		}
		if (as.Kind == KindRead || as.Kind == KindCapture) && in.Recorded[a] {
			continue
		}
		return false
	}
	return true
}

// optionSkip is the option id that skips a question's steps.
const optionSkip = "skip"
