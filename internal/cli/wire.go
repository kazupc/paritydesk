package cli

import (
	"context"

	"github.com/kazupc/paritydesk/internal/orchestrator"
	"github.com/kazupc/paritydesk/internal/pderr"
)

// buildDeps constructs the orchestrator's dependencies from the real
// implementations: config.Load/Validate, state.Open, gen.Open,
// profile.Scanner with configjson.ReadIdentity and sources.DiscoverReplicas,
// app.NewDarwin, compat.Gate over compat.Load, leveldb.New, all.Registry,
// rows.Registry, plan.Validate, apply.Applier, sources.Directory,
// worktrees.Guard, runner.Detacher, and a render-backed UI. In read-only
// mode fsx.ReadOnly wraps the filesystem.
func buildDeps(ctx context.Context, g *globalFlags) (orchestrator.Deps, error) {
	return orchestrator.Deps{}, pderr.ErrNotImplemented
}
