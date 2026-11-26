package orchestrator

import (
	"context"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/rows"
	"github.com/kazupc/paritydesk/internal/sources"
)

// reasonNeedsSidebarView is the skip reason of row sidebar-seed when the
// sidebar-view row is not planned in this run: the seed values come from
// the carried view.
const reasonNeedsSidebarView = "needs sidebar-view in this run"

// sidebarPolicy adds the facts the sidebar-offline merges need to a row's
// policy: for sidebar-seed the values derived from the sidebar-view row's
// Desired of this switch (prior), and for an adapter implementing
// sources.TargetStater the entering folder's state. A state that cannot
// be read counts as "store missing and seeded", so nothing is written to
// that row, with a warning.
func (o *Orchestrator) sidebarPolicy(ctx context.Context, r model.RowID, ad sources.Adapter, tRC sources.ReadContext,
	spec compat.RowSpec, prior map[model.RowID]*rows.Output, pol *rows.Policy) error {
	if r == model.RowSidebarSeed {
		var view *model.RowSnapshot
		if out := prior[model.RowSidebarView]; out != nil {
			view = out.Desired
		}
		from, err := rows.SeedValues(view, spec.Fields)
		if err != nil {
			return pderr.Wrap(pderr.Internal, "merge sidebar-seed", err)
		}
		pol.SeedFrom = from
	}
	st, ok := ad.(sources.TargetStater)
	if !ok {
		return nil
	}
	ts, err := st.TargetState(ctx, tRC)
	if err != nil {
		if ctx.Err() != nil {
			return pderr.Wrap(pderr.Declined, "interrupted", ctx.Err())
		}
		ts = sources.TargetState{StoreMissing: true, Seeded: true}
		o.progress(P2Preview, "warn", i18n.Tx(i18n.ProgressP2EnteringUnreadable, r))
	}
	pol.EnteringStoreMissing, pol.EnteringSeeded = ts.StoreMissing, ts.Seeded
	return nil
}
