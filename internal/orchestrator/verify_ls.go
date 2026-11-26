package orchestrator

import (
	"context"
	"strings"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/sources"
	"github.com/kazupc/paritydesk/internal/state"
)

// lsVerifyRow is one row P12 reads back from Local Storage.
type lsVerifyRow struct {
	row      model.RowID
	spec     compat.RowSpec
	lsv      sources.LSVerifier
	key      state.BaseKey
	intended *model.RowSnapshot
}

// compareLS compares every row kept only in Local Storage (its adapter
// implements sources.LSVerifier) with its base, reading a snapshot copy
// of the running app's Local Storage (no LOCK needed, allowed in
// read-only mode). A copy that cannot be opened, or a row that cannot be
// read from it, is listed in res.Unverified: never drift, never an error;
// the next switch's harvest compares it with the base. Otherwise drift is
// handled as in compareMirror: contested rows are reported as replaced,
// and when in.record is set the contested entries are stored and the base
// moves to what was observed.
func (o *Orchestrator) compareLS(ctx context.Context, in verifyIn, res *VerifyResult) error {
	d := o.d
	rs := in.rows
	if rs == nil {
		rs = model.AllRows
	}
	var todo []lsVerifyRow
	for _, r := range rs {
		spec, ok := in.manifest.Row(r)
		lsv, isLS := d.Adapters[r].(sources.LSVerifier)
		if !ok || !isLS || !d.Config.RowEnabled(r) {
			continue
		}
		key := state.BaseKey{Alias: in.alias, Replica: in.replica, Row: r}
		ref, ok := in.root.Bases[key.String()]
		if !ok {
			continue
		}
		intended, err := d.State.GetSnapshot(ref)
		if err != nil {
			continue
		}
		todo = append(todo, lsVerifyRow{row: r, spec: spec, lsv: lsv, key: key, intended: intended})
	}
	if len(todo) == 0 {
		return nil
	}
	claude := o.claudePath()
	reader, err := o.lsReader(ctx, claude)
	if err != nil || reader == nil {
		var names []string
		for _, t := range todo {
			res.Unverified = append(res.Unverified, t.row)
			names = append(names, string(t.row))
		}
		o.progress(P12Verify, "warn", i18n.Tx(i18n.ProgressP12LSUnreadable, strings.Join(names, ", ")))
		return nil
	}
	defer closeReader(reader)
	for _, t := range todo {
		observed, err := t.lsv.VerifyItems(ctx, sources.ReadContext{Folder: claude, Replica: in.replica, LS: reader,
			Spec: t.spec.Expand(in.replica), Manifest: in.manifest})
		if err != nil {
			res.Unverified = append(res.Unverified, t.row)
			o.progress(P12Verify, "warn", i18n.Tx(i18n.ProgressP12LSRowUnreadable, t.row))
			continue
		}
		if err := o.recordObserved(in, res, t, observed); err != nil {
			return err
		}
	}
	return nil
}

// recordObserved compares one row's observed items with its base and
// records the drift as compareMirror does.
func (o *Orchestrator) recordObserved(in verifyIn, res *VerifyResult, t lsVerifyRow, observed *model.RowSnapshot) error {
	contested, err := driftOf(t.row, in.replica, t.intended, observed)
	if err != nil {
		return pderr.Wrap(pderr.Internal, "verify", err)
	}
	if len(contested) == 0 {
		return nil
	}
	if t.spec.Contested {
		res.Replaced = append(res.Replaced, t.row)
	} else {
		res.Drift = append(res.Drift, t.row)
	}
	if !in.record {
		return nil
	}
	if in.root.Contested == nil {
		in.root.Contested = map[string]model.Contested{}
	}
	for _, c := range contested {
		in.root.Contested[state.ContestedKey(t.key, c.Key)] = c
	}
	newRef, err := o.d.State.PutSnapshot(observed)
	if err != nil {
		return pderr.Wrap(pderr.Internal, "state", err)
	}
	in.root.Bases[t.key.String()] = newRef
	return nil
}
