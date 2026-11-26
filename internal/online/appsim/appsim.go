// Package appsim is a test-only, independent Go port of the app's own pin
// behaviour (app 7.37095.0, renderer shared chunk): the family map that
// links a session to its family root (pS with oS, fS, dS, cS, sS, lS, uS,
// $x, rS, Qx, Bx and the filed-apart set of $S), the cascade unpin of a
// family root (CC with the SC() flag and the nested-layout switch), and
// set_pinned with its already-state answer, hidden-section refusal and
// dispatch timeout (Q and the main process's 8 s wait).
//
// It ports the app's code, not paritydesk's design, so property tests can
// check the pin provider against it. It is imported only by _test.go
// files (a policy test checks this). Minified names are cited as
// evidence only.
package appsim

import (
	"encoding/json"
	"sort"
)

// Lineage kinds (Zx).
const (
	KindSideSession   = "side_session"
	KindHandOff       = "hand_off"
	KindSuggestedTask = "suggested_task"
	KindFork          = "fork"
)

// Lineage is a session's lineage member (validated as rS does).
type Lineage struct {
	SessionID               string `json:"sessionId"`
	Kind                    string `json:"kind"`
	Initiator               string `json:"initiator"`
	AttachesThroughArchived bool   `json:"attachesThroughArchived,omitempty"`
}

// SpawnedFrom is a session's spawnedFrom member.
type SpawnedFrom struct {
	SessionID      string `json:"sessionId"`
	SideSession    bool   `json:"sideSession,omitempty"`
	SharedCheckout bool   `json:"sharedCheckout,omitempty"`
	UserFork       bool   `json:"userFork,omitempty"`
}

// Session is the part of a session record the family map reads.
type Session struct {
	ID                     string       `json:"sessionId"`
	IsArchived             bool         `json:"isArchived,omitempty"`
	ProjectThreadStarted   bool         `json:"projectThreadStarted,omitempty"`
	LineageDetached        bool         `json:"lineageDetached,omitempty"`
	DisplayParentSessionID string       `json:"displayParentSessionId,omitempty"`
	Lineage                *Lineage     `json:"lineage,omitempty"`
	SpawnedFrom            *SpawnedFrom `json:"spawnedFrom,omitempty"`
	ForkedFromSessionID    string       `json:"forkedFromSessionId,omitempty"`
	ScheduledTaskID        string       `json:"scheduledTaskId,omitempty"`
}

// FromRecord decodes a session record (unknown members are ignored).
func FromRecord(id string, v json.RawMessage) Session {
	var s Session
	_ = json.Unmarshal(v, &s)
	s.ID = id
	return s
}

type parentLink struct {
	id, kind string
}

// rS: a lineage counts only with a non-empty id, a known kind and a
// known initiator.
func validLineage(l *Lineage) *Lineage {
	if l == nil || l.SessionID == "" || (l.Initiator != "claude" && l.Initiator != "user") {
		return nil
	}
	switch l.Kind {
	case KindSideSession, KindHandOff, KindSuggestedTask, KindFork:
		return l
	}
	return nil
}

// oS.
func oS(e *Session, t bool) *parentLink {
	if e.LineageDetached {
		return nil
	}
	if t {
		if n := validLineage(e.Lineage); n != nil {
			return &parentLink{n.SessionID, n.Kind}
		}
	}
	if e.SpawnedFrom != nil && e.SpawnedFrom.SideSession {
		return &parentLink{e.SpawnedFrom.SessionID, KindSideSession}
	}
	if t && e.ForkedFromSessionID != "" {
		return &parentLink{e.ForkedFromSessionID, KindFork}
	}
	return nil
}

// Qx.
func qx(p *parentLink) string {
	if p != nil && (p.kind == KindSideSession || p.kind == KindHandOff) {
		return p.id
	}
	return ""
}

// sS.
func sS(e *Session, t *parentLink) string {
	if e.SpawnedFrom != nil && e.SpawnedFrom.UserFork {
		return ""
	}
	return qx(t)
}

// cS.
func cS(e *Session) string { return sS(e, oS(e, true)) }

// lS.
func lS(e *Session) string {
	if e.Lineage == nil || !e.Lineage.AttachesThroughArchived {
		return ""
	}
	t := oS(e, true)
	if t != nil && (t.kind == KindFork || t.kind == KindSuggestedTask) {
		return t.id
	}
	return sS(e, t)
}

// $x: walk up through archived sessions to the first live one.
func walkArchived(e *Session, get func(string) *Session, fn func(*Session) string) string {
	i := fn(e)
	if i == "" || e.IsArchived {
		return i
	}
	seen := map[string]bool{e.ID: true}
	o := i
	for k := 0; k < 32; k++ {
		x := get(o)
		if x == nil || seen[o] {
			return i
		}
		if !x.IsArchived {
			return o
		}
		seen[o] = true
		r := fn(x)
		if r == "" {
			return i
		}
		o = r
	}
	return i
}

// uS.
func uS(e *Session, get func(string) *Session, nested bool) string {
	if nested && lS(e) != "" {
		return walkArchived(e, get, lS)
	}
	if p := oS(e, nested); p != nil {
		return p.id
	}
	return ""
}

// dS.
func dS(e *Session) bool {
	t := e.SpawnedFrom
	if t != nil && t.SideSession && (t.SharedCheckout || !e.LineageDetached) {
		return false
	}
	return cS(e) == ""
}

// fS.
func fS(e *Session) string {
	if e.DisplayParentSessionID != "" && dS(e) {
		return e.DisplayParentSessionID
	}
	return ""
}

// FamilyMap is pS(sessions, nested, filedApart): every session that has a
// family root, mapped to that root. Sessions with projectThreadStarted
// are left out (Bx); a filed-apart session links to nothing; a live child
// does not link to an archived parent in the nested layout; display-parent
// cycles are cut.
func FamilyMap(sessions []Session, nested bool, filedApart map[string]bool) map[string]string {
	var r []*Session
	byID := map[string]*Session{}
	for i := range sessions {
		s := &sessions[i]
		if s.ProjectThreadStarted {
			continue
		}
		r = append(r, s)
		byID[s.ID] = s
	}
	get := func(id string) *Session { return byID[id] }
	a := func(e *Session) string {
		if nested {
			return fS(e)
		}
		return ""
	}
	o := func(e *Session) string {
		if e == nil || filedApart[e.ID] {
			return ""
		}
		p := a(e)
		if p == "" {
			p = uS(e, get, nested)
		}
		ps := byID[p]
		if ps != nil && p != e.ID && !(nested && ps.IsArchived && !e.IsArchived) {
			return p
		}
		return ""
	}
	cycles := func(e *Session) bool {
		seen := map[string]bool{}
		n := o(e)
		for n != "" && n != e.ID && !seen[n] {
			seen[n] = true
			n = o(byID[n])
		}
		return n == e.ID
	}
	cut := map[string]bool{}
	for _, e := range r {
		if a(e) != "" && cycles(e) {
			cut[e.ID] = true
		}
	}
	l := func(e *Session) string {
		if e == nil || cut[e.ID] {
			return ""
		}
		return o(e)
	}
	out := map[string]string{}
	for _, e := range r {
		t := l(e)
		if t == "" {
			continue
		}
		seen := map[string]bool{e.ID: true, t: true}
		n := l(byID[t])
		for n != "" && !seen[n] {
			t = n
			seen[n] = true
			n = l(byID[t])
		}
		if n == "" {
			out[e.ID] = t
		}
	}
	return out
}

// App is one simulated renderer: the sessions, the pin list and the
// switches that change the cascade.
type App struct {
	Sessions []Session
	Pinned   map[string]bool
	// SC is the flag that turns the cascade off (SC()).
	SC bool
	// Nested is the nested sidebar layout; without it the family map is
	// empty.
	Nested bool
	// FiledApart are sessions filed into custom groups apart from their
	// family.
	FiledApart map[string]bool
	// Hidden are sessions whose own section is hidden.
	Hidden map[string]bool
	// Late are dispatched commands whose answer timed out; Flush applies
	// them.
	Late []Call
}

// Call is a set_pinned call.
type Call struct {
	ID     string
	Pinned bool
}

// Answer texts of set_pinned (P6, P7, P15).
const (
	TextPinned          = "Session pinned."
	TextUnpinned        = "Session unpinned."
	TextAlreadyPinned   = "The session was already pinned."
	TextAlreadyUnpinned = "The session was already unpinned."
	TextHidden          = "This session's section is hidden, so unpinning it could take it out of the user's sight. Nothing was changed. The user can unpin it themselves."
	TextTimeout         = "The app window didn't answer. Try once more."
	TextArchived        = "Session is archived; archived sessions aren't shown in the sidebar's groups or pins."
	TextScheduled       = "Session is a scheduled task run. It can't be grouped, pinned or marked."
	TextNotFound        = `Session "%s" not found.`
)

func (a *App) session(id string) *Session {
	for i := range a.Sessions {
		if a.Sessions[i].ID == id {
			return &a.Sessions[i]
		}
	}
	return nil
}

// cascade is CC: unpinning pinned root e first unpins every pinned
// session whose family root is e.
func (a *App) cascade(e string) {
	if a.SC || !a.Pinned[e] || !a.Nested {
		return
	}
	for child, root := range FamilyMap(a.Sessions, true, a.FiledApart) {
		if root == e && a.Pinned[child] {
			delete(a.Pinned, child)
		}
	}
}

// apply is Q after the checks: set the pin, the cascade first on unpin.
func (a *App) apply(id string, pinned bool) {
	if !pinned {
		a.cascade(id)
		delete(a.Pinned, id)
		return
	}
	a.Pinned[id] = true
}

// SetPinned is the tool: the main process's archived and scheduled-run
// refusals, then the renderer's already-state answer, hidden-section
// refusal and apply. timeout answers the timeout text after the command
// was dispatched; the renderer applies it later (Flush).
func (a *App) SetPinned(id string, pinned, timeout bool) string {
	s := a.session(id)
	if s == nil {
		return "Session \"" + id + "\" not found."
	}
	if s.IsArchived {
		return TextArchived
	}
	if s.ScheduledTaskID != "" {
		return TextScheduled
	}
	if timeout {
		a.Late = append(a.Late, Call{ID: id, Pinned: pinned})
		return TextTimeout
	}
	return a.renderer(id, pinned)
}

func (a *App) renderer(id string, pinned bool) string {
	if a.Pinned[id] == pinned {
		if pinned {
			return TextAlreadyPinned
		}
		return TextAlreadyUnpinned
	}
	if !pinned && a.Hidden[id] {
		return TextHidden
	}
	a.apply(id, pinned)
	if pinned {
		return TextPinned
	}
	return TextUnpinned
}

// Flush applies the late commands (a timed-out call the renderer ran
// anyway).
func (a *App) Flush() {
	for _, c := range a.Late {
		a.renderer(c.ID, c.Pinned)
	}
	a.Late = nil
}

// PinnedIDs returns the pinned ids, sorted.
func (a *App) PinnedIDs() []string {
	var out []string
	for id, ok := range a.Pinned {
		if ok {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}
