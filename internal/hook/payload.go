package hook

import (
	"bytes"
	"encoding/json"
	"regexp"

	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/online"
)

// Tool hook events and limits.
const (
	EventPreToolUse  = "PreToolUse"
	EventPostToolUse = "PostToolUse"
	// MaxPrePayload bounds a PreToolUse payload: an extension step may
	// carry a 32 MiB input.
	MaxPrePayload = 40 << 20
)

// ToolPayload is the subset of a PreToolUse or PostToolUse payload
// paritydesk reads. Every field is untrusted; ParseToolPayload keeps only
// validated values.
type ToolPayload struct {
	SessionID      string          `json:"session_id"`
	HookEventName  string          `json:"hook_event_name"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	ToolResponse   json.RawMessage `json:"tool_response"`
	ToolUseID      string          `json:"tool_use_id"`
	PermissionMode string          `json:"permission_mode"`
}

var (
	toolUseIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	// appToolRE matches the app's own tools outside MCP (Artifact, ...).
	appToolRE = regexp.MustCompile(`^[A-Z][A-Za-z]{1,63}$`)
)

// ParseToolPayload validates one payload: one JSON object, the expected
// event name and a tool name of the guarded forms; session_id and
// tool_use_id are kept only when they match their patterns.
func ParseToolPayload(b []byte, event string) (ToolPayload, bool) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || b[0] != '{' {
		return ToolPayload{}, false
	}
	var p ToolPayload
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&p); err != nil || dec.More() {
		return ToolPayload{}, false
	}
	if p.HookEventName != event {
		return ToolPayload{}, false
	}
	if !online.ValidTool(p.ToolName) && !appToolRE.MatchString(p.ToolName) {
		return ToolPayload{}, false
	}
	if !ids.IsUUID(p.SessionID) {
		p.SessionID = ""
	}
	if !toolUseIDRE.MatchString(p.ToolUseID) {
		p.ToolUseID = ""
	}
	p.PermissionMode = ""
	return p, true
}
