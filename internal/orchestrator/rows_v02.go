package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/rows"
	"github.com/kazupc/paritydesk/internal/sources"
)

// reasonSourceUnavailable is the Plan.Skipped reason of a row whose
// source could not be read this time.
const reasonSourceUnavailable = "source unavailable"

// errRowSkipped leaves one row out of a plan with a reason (Plan.Skipped):
// its source could not be read this time. Every other row is planned as
// usual. A row may use its own reason (pins: "pins_unreadable").
type errRowSkipped struct{ Reason string }

func (e *errRowSkipped) Error() string { return "row skipped: " + e.Reason }

// readAdapter reads one row through its adapter: a sources.RefReader is
// read with ReadRefs(refs) (Read of such an adapter is never called), any
// other adapter with Read.
func readAdapter(ctx context.Context, ad sources.Adapter, rc sources.ReadContext, refs []string) (*model.RowSnapshot, error) {
	if rr, ok := ad.(sources.RefReader); ok {
		return rr.ReadRefs(ctx, rc, refs)
	}
	return ad.Read(ctx, rc)
}

// readError maps an adapter read error of row r on one side: a source
// that is unavailable skips the row, anything else fails the run as an
// internal error.
func readError(r model.RowID, side string, err error) error {
	if errors.Is(err, sources.ErrSourceUnavailable) {
		return &errRowSkipped{Reason: reasonSourceUnavailable}
	}
	return pderr.Wrap(pderr.Internal, fmt.Sprintf("read %s (%s)", r, side), err)
}

// envRemapOf returns the environment remap of this plan's environments
// row (nil when that row was not merged).
func envRemapOf(prior map[model.RowID]*rows.Output) map[string]string {
	if out := prior[model.RowEnvironments]; out != nil {
		return out.EnvRemap
	}
	return nil
}

// mergeSkipped returns the rows skipped before planning plus those skipped
// while planning, without modifying either map.
func mergeSkipped(before, during map[model.RowID]string) map[model.RowID]string {
	if len(during) == 0 {
		return before
	}
	out := maps.Clone(before)
	if out == nil {
		out = map[model.RowID]string{}
	}
	maps.Copy(out, during)
	return out
}

// readRow reads one row of a folder on its own (spec-legacy-adopt 4.6):
// it opens a Local Storage reader for this one read (snapshot rules as
// lsReader), reads the row without refs, and closes the reader.
func (o *Orchestrator) readRow(ctx context.Context, m *compat.Manifest, folder string, rep model.Replica, r model.RowID) (*model.RowSnapshot, error) {
	ls, err := o.lsReader(ctx, folder)
	if err != nil {
		return nil, err
	}
	defer closeReader(ls)
	return o.readRowLS(ctx, m, folder, rep, r, ls, nil, false)
}
