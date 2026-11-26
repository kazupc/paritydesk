package orchestrator

import (
	"context"
	"errors"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/sources/pins"
	"github.com/kazupc/paritydesk/internal/state"
	"github.com/kazupc/paritydesk/internal/store/idb"
)

// pinsChecks adds the `pins` check (and `pins-mirror` when the main list
// and the app's backup list disagree): it reads the active folder's pin
// list read-only, through the same adapter as a run, with the side facts
// of root (spec-pins 12.2). It never prints ids.
func (dr *doctor) pinsChecks(ctx context.Context, m *compat.Manifest, root *state.Root) {
	o := dr.o
	spec, ok := compat.RowSpec{}, false
	if m != nil {
		spec, ok = o.rowSpec(m, model.RowPins)
	}
	ad := o.d.Adapters[model.RowPins]
	if !o.d.Config.Sync.Pins || !ok || spec.WritePath != compat.PathOn || spec.IDB == nil || !o.onlineRowSelectable(m, model.RowPins) || ad == nil {
		dr.add("pins", "ok", i18n.Tx(i18n.DoctorOnlinePinsOff))
		return
	}
	ps := root.Profiles[root.Active]
	var rep model.Replica
	if ps != nil {
		rep = ps.Replica
	}
	folder := o.claudePath()
	ls, err := o.lsReader(ctx, folder)
	if err != nil {
		ls = nil
	}
	defer closeReader(ls)
	var sessions *model.RowSnapshot
	if sa := o.d.Adapters[model.RowSessions]; sa != nil && rep.Account != "" {
		if sspec, ok := o.rowSpec(m, model.RowSessions); ok {
			sessions, _ = sa.Read(ctx, sources.ReadContext{Folder: folder, Replica: rep, LS: ls, Spec: sspec.Expand(rep), Manifest: m})
		}
	}
	side := sideRef{Alias: root.Active, Replica: rep, Folder: folder, LS: ls, BaseValid: true}
	k := state.BaseKey{Alias: root.Active, Replica: rep, Row: model.RowPins}
	report := &sources.ReadReport{}
	rc := sources.ReadContext{Folder: folder, Replica: rep, LS: ls, Spec: spec.Expand(rep), Manifest: m,
		Side: o.pinsSideFacts(root, side, k, sessions), Report: report}
	snap, err := ad.Read(ctx, rc)
	switch {
	case err != nil && pins.ReasonOf(err) == idb.ReasonStaleStore:
		dr.add("pins", "fail", i18n.Tx(i18n.DoctorOnlinePinsStale))
	case err != nil:
		reason := pins.ReasonOf(err)
		if !errors.Is(err, sources.ErrSourceUnavailable) || reason == "" {
			reason = idb.ReasonOther
		}
		dr.add("pins", "warn", i18n.Tx(i18n.DoctorOnlinePinsUnavailableReason, pinsReasonText(reason)))
	case report.PinsSource == pins.SourceMirror:
		dr.add("pins", "warn", i18n.Tx(i18n.DoctorOnlinePinsMirrorCount, pinsReasonText(idb.Reason(report.PinsReason)), len(snap.Items)))
	default:
		dr.add("pins", "ok", i18n.Tx(i18n.DoctorOnlinePinsIDBCount, len(snap.Items)))
		if report.MirrorDiffers {
			dr.add("pins-mirror", "warn", i18n.Tx(i18n.DoctorOnlinePinsMirrorDiffers))
		}
	}
}
