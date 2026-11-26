package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/online"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/rows"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/sources/pins"
	"github.com/kazupc/paritydesk/internal/sources/sidebar"
	"github.com/kazupc/paritydesk/internal/state"
	"github.com/kazupc/paritydesk/internal/store/idb"
)

// Plan.Skipped reasons of row pins.
const (
	reasonPinsUnreadable = "pins_unreadable"
	reasonOnlineOff      = "carried only while online reconcile is on"
)

// starredField is the sessions-row record member the app derives from
// the folder's own pin list (spec-pins P10). It is used only as evidence
// that a pin list existed, never as a pin list.
const starredField = "isStarred"

// pinsInfo is what reading row pins found, for the online build, the
// result and the warnings.
type pinsInfo struct {
	// Source is T's source: "idb", "mirror" or "none" (row skipped).
	Source string
	// Reason is the idb.Reason of the unreadable side ("" when read).
	Reason idb.Reason
	// Side is the alias of the side that could not be read.
	Side ids.Alias
	// Mirror lists the aliases whose list came from the app's mirror.
	Mirror []ids.Alias
	// TRaw are T's pin ids before exclusion.
	TRaw []string
	// Union: either side's pins base was invalid (both lists combined).
	Union bool
	// SProbePassed and TProbePassed clear the freshness probe of that
	// profile when its read is stored as a base.
	SProbePassed, TProbePassed bool
	// TSessions is T's sessions snapshot used for the family.
	TSessions *model.RowSnapshot
	// TFacts are the side facts of T's P5 read; the P10 re-read uses them
	// too (the probe is checked there as well, spec-pins 9.3).
	TFacts *sources.SideFacts
}

// onlineRowSelectable reports whether a row written through the app's
// tools ("on") can be planned: the online layer runs for this manifest
// and its registry has a provider for the row (pins only in this
// version: a provider of pin steps).
func (o *Orchestrator) onlineRowSelectable(m *compat.Manifest, r model.RowID) bool {
	return r == model.RowPins && o.onlineActive(m) && o.d.Online.Provides(online.KindPin)
}

// sessionsFor returns the sessions-row snapshots of both sides: the ones
// this plan merged, or a read of the sessions adapter (read only, nothing
// merged or stored) when the sessions row is not planned (--only pins,
// --skip sessions, a data boundary).
func (o *Orchestrator) sessionsFor(ctx context.Context, in planInput, pl *planned) (s, t *model.RowSnapshot, err error) {
	if w := pl.Work[model.RowSessions]; w != nil {
		return w.S, w.T, nil
	}
	if pl.AuxSessions.S != nil || pl.AuxSessions.T != nil {
		return pl.AuxSessions.S, pl.AuxSessions.T, nil
	}
	ad := o.d.Adapters[model.RowSessions]
	spec, ok := o.rowSpec(in.Manifest, model.RowSessions)
	if ad == nil || !ok {
		return nil, nil, nil
	}
	read := func(side sideRef, name string) (*model.RowSnapshot, error) {
		rc := sources.ReadContext{Folder: side.Folder, Replica: side.Replica, LS: side.LS, Spec: spec.Expand(side.Replica), Manifest: in.Manifest}
		snap, err := ad.Read(ctx, rc)
		if err != nil {
			return nil, pderr.Wrap(pderr.Internal, fmt.Sprintf("read %s (%s)", model.RowSessions, name), err)
		}
		return snap, nil
	}
	if s, err = read(in.S, "leaving"); err != nil {
		return nil, nil, err
	}
	if t, err = read(in.T, "entering"); err != nil {
		return nil, nil, err
	}
	pl.AuxSessions.S, pl.AuxSessions.T = s, t
	return s, t, nil
}

// starredIDs are the records whose isStarred member is boolean true.
func starredIDs(s *model.RowSnapshot) map[string]bool {
	out := map[string]bool{}
	if s == nil {
		return out
	}
	for k, it := range s.Items {
		var m map[string]json.RawMessage
		if json.Unmarshal(it.Value, &m) == nil && strings.TrimSpace(string(m[starredField])) == "true" {
			out[k] = true
		}
	}
	return out
}

// pinsSideFacts are the side facts of one side's pins read (spec-pins
// 6.4 item 5): its pins base, its sessions' isStarred and its probe.
func (o *Orchestrator) pinsSideFacts(root *state.Root, side sideRef, k state.BaseKey, sessions *model.RowSnapshot) *sources.SideFacts {
	f := &sources.SideFacts{StarredIDs: starredIDs(sessions), ProbeSince: root.PinsProbes[side.Alias]}
	if ref, ok := root.Bases[k.String()]; ok && side.BaseValid {
		if base, err := o.d.State.GetSnapshot(ref); err == nil {
			f.BaseValid, f.BaseEmpty = true, len(base.Items) == 0
		}
	}
	return f
}

// pinsRereadFacts are the side facts of a pins re-read of profile a's
// folder outside a run's plan (recover, rollback): its pins base from
// root and its probe; isStarred comes from the same re-read's sessions.
func (o *Orchestrator) pinsRereadFacts(root *state.Root, a ids.Alias, rep model.Replica) *pinsReread {
	k := state.BaseKey{Alias: a, Replica: rep, Row: model.RowPins}
	f := o.pinsSideFacts(root, sideRef{Alias: a, Replica: rep, BaseValid: true}, k, nil)
	f.StarredIDs = nil
	return &pinsReread{Facts: *f}
}

// withoutKeys returns a copy of s without the excluded keys (nil stays
// nil); tombstones of excluded keys go too.
func withoutKeys(s *model.RowSnapshot, excl map[string]bool) *model.RowSnapshot {
	if s == nil || len(excl) == 0 {
		return s
	}
	c := s.Clone()
	for k := range excl {
		delete(c.Items, k)
		delete(c.Tombstones, k)
	}
	return c
}

// mergePins reads row pins on both sides with their side facts, removes
// the sessions sync.exclude keeps private from every merge input, and
// merges (spec-pins 6.4). An unreadable side skips only this row
// (*errRowSkipped "pins_unreadable"); the reason is kept in pl.Pins.
func (o *Orchestrator) mergePins(ctx context.Context, in planInput, pl *planned, acks map[string]bool) (*rowWork, error) {
	r := model.RowPins
	spec, _ := o.rowSpec(in.Manifest, r)
	ad := o.d.Adapters[r]
	info := &pinsInfo{Source: string(online.PinsNone)}
	pl.Pins = info
	sSess, tSess, err := o.sessionsFor(ctx, in, pl)
	if err != nil {
		return nil, err
	}
	info.TSessions = tSess
	w := &rowWork{
		Spec: spec,
		SKey: state.BaseKey{Alias: in.S.Alias, Replica: in.S.Replica, Row: r},
		TKey: state.BaseKey{Alias: in.T.Alias, Replica: in.T.Replica, Row: r},
	}
	read := func(side sideRef, k state.BaseKey, sess *model.RowSnapshot, name string) (*model.RowSnapshot, *sources.ReadReport, error) {
		rep := &sources.ReadReport{}
		facts := o.pinsSideFacts(in.Root, side, k, sess)
		if name == "entering" {
			info.TFacts = facts
		}
		rc := sources.ReadContext{Folder: side.Folder, Replica: side.Replica, LS: side.LS, Spec: spec.Expand(side.Replica),
			Manifest: in.Manifest, Side: facts, Report: rep}
		snap, err := ad.Read(ctx, rc)
		if errors.Is(err, sources.ErrSourceUnavailable) {
			info.Reason, info.Side = pins.ReasonOf(err), side.Alias
			if info.Reason == "" {
				info.Reason = idb.ReasonOther
			}
			return nil, nil, &errRowSkipped{Reason: reasonPinsUnreadable}
		}
		if err != nil {
			return nil, nil, readError(r, name, err)
		}
		if rep.PinsSource == pins.SourceMirror {
			info.Mirror = append(info.Mirror, side.Alias)
		}
		return snap, rep, nil
	}
	sSnap, sRep, err := read(in.S, w.SKey, sSess, "leaving")
	if err != nil {
		return nil, err
	}
	tSnap, tRep, err := read(in.T, w.TKey, tSess, "entering")
	if err != nil {
		return nil, err
	}
	info.Source = tRep.PinsSource
	info.SProbePassed, info.TProbePassed = sRep.ProbePassed, tRep.ProbePassed
	for _, k := range tSnap.Keys() {
		info.TRaw = append(info.TRaw, k)
	}
	if info.TRaw == nil {
		info.TRaw = []string{}
	}
	// Sessions that sync.exclude keeps private never travel: their keys
	// leave every input at once, which can never look like a removal.
	sessSpec, _ := o.rowSpec(in.Manifest, model.RowSessions)
	excl := rows.ExcludedSessionIDs([]*model.RowSnapshot{sSess, tSess},
		o.policy(in, model.CategoryOf(model.RowSessions), sessSpec, acks, nil))
	w.S, w.T = withoutKeys(sSnap, excl), withoutKeys(tSnap, excl)
	leaving := o.side(in.Root, in.S, w.SKey, w.S)
	entering := o.side(in.Root, in.T, w.TKey, w.T)
	leaving.Base, entering.Base = withoutKeys(leaving.Base, excl), withoutKeys(entering.Base, excl)
	info.Union = !leaving.BaseValid || !entering.BaseValid
	mi := rows.Input{
		Row: r, Leaving: leaving, Entering: entering,
		Canonical:       withoutKeys(o.object(in.Root.Canonical[r]), excl),
		LeavingOverlay:  withoutKeys(o.object(in.Root.Overlays[w.SKey.String()]), excl),
		EnteringOverlay: withoutKeys(o.object(in.Root.Overlays[w.TKey.String()]), excl),
		Policy:          o.policy(in, model.CategoryOf(r), spec, acks, nil),
	}
	out, err := o.d.Rows[r].Merge(mi)
	if err != nil {
		return nil, pderr.Wrap(pderr.Internal, fmt.Sprintf("merge %s", r), err)
	}
	w.Out = &out
	return w, nil
}

// pinsHidden predicts which unpins the app would refuse because the
// session's own section is hidden (spec-pins 8.3): the session's custom
// group (T's Local Storage) names a section whose shown member is false
// (the planned sidebar-view, else T's own). nil when either is unknown.
func (r *run) pinsHidden(ctx context.Context, pl *planned) map[string]bool {
	m := r.manifest
	if m == nil || pl == nil {
		return nil
	}
	sp := m.Online.Pins
	t := r.sideT()
	var sections *model.RowSnapshot
	if w := pl.Work[model.RowSidebarView]; w != nil && w.Out != nil {
		sections = w.Out.Desired
	} else if snap, err := r.o.readRowLS(ctx, m, t.Folder, t.Replica, model.RowSidebarView, t.LS, nil, false); err == nil {
		sections = snap
	}
	if sections == nil {
		return nil
	}
	repl := strings.NewReplacer("{acct}", string(t.Replica.Account), "{org}", string(t.Replica.Org))
	path := make([]string, len(sp.AssignPath))
	for i, p := range sp.AssignPath {
		path[i] = repl.Replace(p)
	}
	assign, groups, err := sidebar.CodeAssignments(t.LS, sp.AssignStoreKey, path)
	if err != nil {
		return nil
	}
	hiddenSection := map[string]bool{}
	for k, it := range sections.Items {
		id, ok := strings.CutPrefix(k, "section:")
		if !ok {
			continue
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(it.Value, &obj) == nil && strings.TrimSpace(string(obj["shown"])) == "false" {
			hiddenSection[id] = true
		}
	}
	out := map[string]bool{}
	for key, group := range assign {
		id, ok := strings.CutPrefix(key, sp.RowKeyPrefix)
		if ok && groups[group] && hiddenSection[group] {
			out[id] = true
		}
	}
	return out
}

// pinsBuild fills the pins part of an online BuildInput from the plan.
func (r *run) pinsBuild(ctx context.Context, pl *planned, in *online.BuildInput) {
	info := pl.Pins
	if info == nil || info.Reason != "" || pl.Work[model.RowPins] == nil {
		in.PinsSource = online.PinsNone
		return
	}
	in.PinsSource = online.PinsSource(info.Source)
	in.PinsRaw = info.TRaw
	in.PinsUnion = info.Union
	if in.Current[model.RowSessions] == nil && info.TSessions != nil {
		in.Current[model.RowSessions] = info.TSessions
	}
	in.Hidden = r.pinsHidden(ctx, pl)
}

// pinsSummary sets the pins members of Result.Online and returns the
// warnings of the pins read (P5 only; at P2 an unreadable list only
// changes the plan line).
func (r *run) pinsSummary(pl *planned, s *online.Summary, in online.BuildInput, approximate bool) *online.Summary {
	info := pl.Pins
	if info == nil {
		return s
	}
	if s == nil {
		s = &online.Summary{PinsSource: string(online.PinsNone)}
	}
	if info.Reason != "" {
		s.PinsSource, s.PinsReason = string(online.PinsNone), string(info.Reason)
		if !approximate {
			r.warn(P5Harvest, i18n.Tx(i18n.OnlineWarnPinsUnreadable, string(info.Side)))
			r.warn(P5Harvest, i18n.Tx(i18n.OnlineWarnPinsReason, pinsReasonText(info.Reason)))
		}
		return s
	}
	s.PinsSource = info.Source
	if s.PinsBlocked == 0 {
		s.PinsBlocked = online.PinsBlocked(in)
	}
	if !approximate {
		for _, a := range info.Mirror {
			r.warn(P5Harvest, i18n.Tx(i18n.OnlineWarnPinsMirror, string(a)))
		}
	}
	return s
}

// pinsReasonText is the fixed text of a reason (never built from error
// strings).
func pinsReasonText(r idb.Reason) i18n.Text {
	m := map[idb.Reason]i18n.Msg{
		idb.ReasonDirAbsent: i18n.PinsReasonDirAbsent, idb.ReasonKeyAbsent: i18n.PinsReasonKeyAbsent,
		idb.ReasonAmbiguous: i18n.PinsReasonAmbiguous, idb.ReasonUnsupported: i18n.PinsReasonUnsupported,
		idb.ReasonBlob: i18n.PinsReasonBlobWrapped, idb.ReasonVersion: i18n.PinsReasonVersion,
		idb.ReasonTorn: i18n.PinsReasonTorn, idb.ReasonUnclean: i18n.PinsReasonUnclean,
		idb.ReasonPending: i18n.PinsReasonPending, idb.ReasonTooLarge: i18n.PinsReasonTooLarge,
		idb.ReasonBackend: i18n.PinsReasonBackend, idb.ReasonFormat: i18n.PinsReasonFormat,
		idb.ReasonStaleStore: i18n.PinsReasonStaleStore,
	}
	if id, ok := m[r]; ok {
		return i18n.Tx(id)
	}
	return i18n.Tx(i18n.PinsReasonOther)
}

// clearPinsProbe deletes a profile's freshness probe after a read of its
// folder passed it; the caller commits it with the base that read became.
func clearPinsProbe(root *state.Root, a ids.Alias) {
	if root.PinsProbes != nil {
		delete(root.PinsProbes, a)
		if len(root.PinsProbes) == 0 {
			root.PinsProbes = nil
		}
	}
}

// pinsConfirmed applies the pins part of a confirmation to root: T's pins
// base moves (spec-pins 9) and, when steps changed the list, the
// freshness probe starts at the queue's creation.
func (o *Orchestrator) pinsConfirmed(root *state.Root, rec *state.OnlineRecord, pc online.PinsConfirm) (bool, error) {
	if pc.Base == nil {
		return false, nil
	}
	changed := false
	k := state.BaseKey{Alias: rec.Profile, Replica: rec.Replica, Row: model.RowPins}.String()
	if pc.Moved {
		ref, err := o.d.State.PutSnapshot(pc.Base)
		if err != nil {
			return false, pderr.Wrap(pderr.Internal, "state", err)
		}
		root.Bases[k] = ref
		changed = true
	}
	if pc.Changed > 0 {
		if _, set := root.PinsProbes[rec.Profile]; !set {
			if root.PinsProbes == nil {
				root.PinsProbes = map[ids.Alias]int64{}
			}
			root.PinsProbes[rec.Profile] = rec.Created.UnixMilli()
			changed = true
		}
	}
	return changed, nil
}

// keepPinsSummary copies the pins members of the P5 summary into the
// published one (the draft's counts do not know them).
func keepPinsSummary(s, prev *online.Summary) {
	if s == nil || prev == nil {
		return
	}
	s.PinsSource, s.PinsReason = prev.PinsSource, prev.PinsReason
	if s.PinsBlocked == 0 {
		s.PinsBlocked = prev.PinsBlocked
	}
}
