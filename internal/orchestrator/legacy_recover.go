package orchestrator

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strconv"

	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/gen"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/state"
)

// quarantine settles an interrupted legacy-quarantine run (spec-legacy-
// adopt 5.2). It never moves anything and never quits or launches the
// app: a missing or incomplete generation is deleted (nothing was moved
// into it), a complete one into which nothing was moved is deleted too,
// and otherwise the generation is recorded so rollback can put the moved
// directories back.
func (rc *recovery) quarantine(ctx context.Context) error {
	d := rc.o.d
	run := rc.root.Run
	n := run.Generation
	if data, ok := intentData(run, state.IntentQuarantining); ok {
		if v, err := strconv.ParseUint(data["n"], 10, 64); err == nil && v != 0 {
			n = v
		}
	}
	if n == 0 {
		return rc.closeRun(ctx, i18n.Tx(i18n.RecoverClosedLeftoversUnmoved))
	}
	m, err := d.Gens.Load(n)
	if err != nil || !m.Complete {
		if _, derr := d.Gens.DeleteIncomplete(n); derr != nil {
			return pderr.Wrap(pderr.Internal, "recover", derr)
		}
		return rc.closeRun(ctx, i18n.Tx(i18n.RecoverClosedLeftoversUnmoved))
	}
	gone, err := d.Gens.DeleteUnmoved(n)
	if err != nil {
		return pderr.Wrap(pderr.Internal, "recover", err)
	}
	if gone {
		return rc.closeRun(ctx, i18n.Tx(i18n.RecoverClosedLeftoversGenRemoved))
	}
	recorded := false
	for _, g := range rc.root.Generations {
		if g.N == n {
			recorded = true
		}
	}
	if !recorded {
		rc.root.Generations = append(rc.root.Generations, state.GenerationRef{N: n, RunID: run.ID, Target: run.To, At: rc.o.now(), Purpose: gen.PurposeLegacyQuarantine})
	}
	folder := rc.o.rollbackFolder(rc.root, run.To)
	moved := 0
	for _, e := range m.Entries {
		p, err := fsx.Contained(folder, e.Path)
		if err != nil {
			continue
		}
		if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
			moved++
		}
	}
	rc.note(i18n.Tx(i18n.RecoverLeftoversMoved, moved, len(m.Entries), n, n))
	return rc.closeRun(ctx, i18n.Tx(i18n.RecoverClosedLeftovers))
}
