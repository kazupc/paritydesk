package orchestrator

import (
	"context"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/state"
)

// Switch runs P0–P12 (or hands off to the detached runner first).
func (o *Orchestrator) Switch(ctx context.Context, opts SwitchOptions) (*Result, error) {
	opts.LogOffset, opts.LogInode = o.logCursor(opts.LogOffset, opts.LogInode)
	r := o.newRun(state.RunSwitch, opts)
	if err := r.newRunID(); err != nil {
		return r.fail(err)
	}
	if o.d.ReadOnly {
		return o.readOnlyPlan(ctx, r)
	}
	if !opts.FromRunner {
		detach := opts.Detach
		if !detach {
			inside, err := o.d.Detach.InsideApp(ctx)
			if err != nil {
				return r.fail(pderr.Wrap(pderr.Internal, "switch", err))
			}
			detach = inside
		}
		if detach {
			return o.detachedParent(ctx, r)
		}
	}
	r.interactive = o.interactive(opts.FromRunner)
	res, err := r.execute(ctx)
	r.notifyEnd(ctx, pderr.CodeOf(err))
	return res, err
}

// logCursor records main.log's position (inode and size) first, unless
// the parent already did (off >= 0), while the invoking turn still runs.
func (o *Orchestrator) logCursor(off int64, ino uint64) (int64, uint64) {
	if off >= 0 {
		return off, ino
	}
	if c, err := o.d.App.LogCursor(); err == nil {
		return c.Off, c.Ino
	}
	return 0, 0
}

// invokingCLI is the Claude Code session running this command: plan's
// --invoking-session, or the switch's --reopen-session.
func (r *run) invokingCLI() string {
	if r.previewCLI != "" {
		return r.previewCLI
	}
	return r.opts.ReopenCLISession
}

func (r *run) fail(err error) (*Result, error) {
	r.res.Code = pderr.CodeOf(err)
	return r.res, err
}

// execute runs a locked switch or sync from P0 to P12.
func (r *run) execute(ctx context.Context) (*Result, error) {
	defer r.release()
	steps := []func(context.Context) error{
		func(ctx context.Context) error { return r.preflight(ctx, true) },
		r.previewAndConfirm,
		r.quitApp,
		r.p4,
		r.harvest,
	}
	for _, step := range steps {
		if err := step(ctx); err != nil {
			return r.fail(err)
		}
		if err := r.ctxCheck(ctx); err != nil {
			return r.fail(err)
		}
	}
	cctx, cancel := uncancel(ctx)
	defer cancel()
	if err := r.commitAndApply(cctx); err != nil {
		return r.fail(err)
	}
	r.launch(cctx)
	if r.launched {
		// Announce the switch when Claude is up, not after the checks.
		r.notify(cctx, r.onlineNotice(notifySwitched))
	}
	return r.res, r.finalCode(r.verifyP12(ctx))
}

func (r *run) previewAndConfirm(ctx context.Context) error {
	if err := r.preview(ctx); err != nil {
		return r.abort(ctx, err)
	}
	if err := r.confirmP2(ctx); err != nil {
		return r.abort(ctx, err)
	}
	return nil
}

// commitAndApply runs the uncancellable part: P5 commit, P6, P7, P8, P9
// and P10.
func (r *run) commitAndApply(ctx context.Context) error {
	if !r.swapOnly() {
		if err := r.commitP5(); err != nil {
			return err
		}
		if !r.p5.Plan.Empty() {
			if err := r.backup(ctx); err != nil {
				return err
			}
			if err := r.applyPlan(ctx); err != nil {
				return err
			}
		}
	}
	if r.kind == state.RunSwitch {
		if err := r.swap(ctx); err != nil {
			return err
		}
		if !r.fallback {
			if err := r.park(ctx); err != nil {
				return err
			}
		}
	}
	if r.kind == state.RunSync && r.root.Active != r.S.Profile.Alias {
		r.root.Previous, r.root.Active = r.root.Active, r.S.Profile.Alias
	}
	// The last crash point before P10 closes the run: a fault armed for
	// a phase this run skipped (Deps.Crash) stops here instead, so it
	// never lets a whole switch or sync through.
	if err := r.o.crashPoint(P10Commit); err != nil {
		return err
	}
	return r.commitP10(ctx)
}

// readOnlyPlan is a switch or sync in read-only mode: P0 without the lock
// and P2, nothing written.
func (o *Orchestrator) readOnlyPlan(ctx context.Context, r *run) (*Result, error) {
	defer r.release()
	r.previewOnly = true
	if err := r.preflight(ctx, false); err != nil {
		return r.fail(err)
	}
	r.previewBusy(ctx, r.invokingCLI())
	if err := r.preview(ctx); err != nil {
		return r.fail(err)
	}
	o.progress(P2Preview, "ok", "read-only mode: nothing was changed")
	return r.res, nil
}

// detachedParent hands the run to launchd. It never takes the run lock.
func (o *Orchestrator) detachedParent(ctx context.Context, r *run) (*Result, error) {
	r.res.Detached = true
	if o.interactive(false) {
		r.interactive = true
		if err := r.confirmForJob(ctx); err != nil {
			return r.fail(err)
		}
	} else if r.opts.ConfirmedPlan == "" && !r.opts.Yes {
		return r.fail(pderr.New(pderr.Declined, "switch", "a detached switch needs --confirmed-plan or --yes"))
	}
	g := JobGlobals{Config: o.d.Paths.ConfigFile, StateDir: o.d.Paths.StateDir, AppSupportDir: o.d.Paths.SupportDir}
	id, err := o.d.Detach.Submit(ctx, r.opts.JobArgs(g))
	if err != nil {
		return r.fail(pderr.Wrap(pderr.Internal, "detach", err))
	}
	r.res.RunID = id
	o.progress(P0Preflight, "ok", "the switch continues in the background")
	return r.res, nil
}

// confirmForJob is the interactive parent's read-only preflight, preview
// and confirmation; the confirmed plan is saved for the job.
func (r *run) confirmForJob(ctx context.Context) error {
	defer r.release()
	if err := r.preflight(ctx, false); err != nil {
		return err
	}
	if r.decision.Mode == compat.ModePreviewOnly {
		return pderr.New(pderr.CompatRefused, "compat", "this Claude version is untested and require_tested is set; only plan works")
	}
	r.previewBusy(ctx, r.invokingCLI())
	if err := r.preview(ctx); err != nil {
		return err
	}
	if err := r.confirmP2(ctx); err != nil {
		return err
	}
	if r.opts.ConfirmedPlan != "" {
		return nil
	}
	h, err := r.o.d.Plans.Save(r.p2.Plan)
	if err != nil {
		return pderr.Wrap(pderr.Internal, "plans", err)
	}
	r.opts.ConfirmedPlan = h
	return nil
}

// Preview computes the approximate plan (P0, P2) without quitting, and
// saves it for --confirmed-plan when opts.Save is set.
func (o *Orchestrator) Preview(ctx context.Context, opts PreviewOptions) (*Result, error) {
	r := o.newRun(state.RunSwitch, SwitchOptions{To: opts.To, Only: opts.Only, Skip: opts.Skip, LogOffset: -1})
	r.previewCLI = opts.InvokingCLISession
	if err := r.newRunID(); err != nil {
		return r.fail(err)
	}
	res, err := o.readOnlyPlan(ctx, r)
	if err != nil {
		return res, err
	}
	if opts.Save && !o.d.ReadOnly {
		if _, err := o.d.Plans.Save(res.Plan); err != nil {
			return r.fail(pderr.Wrap(pderr.Internal, "plans", err))
		}
	}
	return res, nil
}

// SyncOptions are the flags of `sync`.
type SyncOptions struct {
	Yes            bool
	AllowDeletions int
	NoLaunch       bool
}

// Sync re-applies canonical state to the active profile: P3–P7, P10–P12,
// no swap. It refuses to run inside the app, since it quits the app.
func (o *Orchestrator) Sync(ctx context.Context, opts SyncOptions) (*Result, error) {
	r := o.newRun(state.RunSync, SwitchOptions{Yes: opts.Yes, AllowDeletions: opts.AllowDeletions,
		NoLaunch: opts.NoLaunch, NoReopen: true, LogOffset: -1})
	if err := r.newRunID(); err != nil {
		return r.fail(err)
	}
	inside, err := o.d.Detach.InsideApp(ctx)
	if err != nil {
		return r.fail(pderr.Wrap(pderr.Internal, "sync", err))
	}
	if inside {
		return r.fail(pderr.New(pderr.Usage, "sync", "sync quits Claude; run it from Terminal, not from inside Claude"))
	}
	if o.d.ReadOnly {
		return o.readOnlyPlan(ctx, r)
	}
	r.interactive = o.interactive(false)
	return r.execute(ctx)
}
