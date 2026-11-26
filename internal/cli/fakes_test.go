package cli

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kazupc/paritydesk/internal/artifacts"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/gen"
	"github.com/kazupc/paritydesk/internal/hook"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/model"
	"github.com/kazupc/paritydesk/internal/online"
	"github.com/kazupc/paritydesk/internal/orchestrator"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/plan"
	"github.com/kazupc/paritydesk/internal/state"
)

// fixedTime is the clock of every fixture.
var fixedTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func fakeUUID(n int) string {
	return "00000000-0000-4000-8000-" + strings.Repeat("0", 11) + string(rune('0'+n))
}

// fakePlan returns a plan with changes in two categories, one deletion.
func fakePlan() *plan.Plan {
	return &plan.Plan{
		Version: 1, RunID: "run-0001", From: "work",
		Target:      plan.Target{Alias: "home", Folder: "Claude-home"},
		Approximate: true, CreatedAt: fixedTime,
		Ops: []plan.Op{{Kind: plan.OpWriteFile, Row: model.RowSessions, Path: "claude-code-sessions/x.json", PreHash: "a", PostHash: "b"}},
		Changes: []model.Change{
			{Row: model.RowSessions, Kind: model.ChangeAdded, Key: "local_" + fakeUUID(1)},
			{Row: model.RowSessions, Kind: model.ChangeUpdated, Key: "local_" + fakeUUID(2)},
			{Row: model.RowSessions, Kind: model.ChangeDeleted, Key: "local_" + fakeUUID(3)},
			{Row: model.RowPinnedOrder, Kind: model.ChangeMoved, Key: "order"},
		},
	}
}

// fakeService is an orchestrator.Service that records its calls.
type fakeService struct {
	mu    sync.Mutex
	calls []string
	errs  map[string]error

	switchOpts  orchestrator.SwitchOptions
	previewOpts orchestrator.PreviewOptions
	syncOpts    orchestrator.SyncOptions
	recoverOpts orchestrator.RecoverOptions
	initOpts    orchestrator.InitOptions
	doctorOpts  orchestrator.DoctorOptions
	rollbackN   uint64
	propagate   bool
	statusShort bool
	args        []string

	status *orchestrator.Status
	verify *orchestrator.VerifyResult
	// result overrides the result of Switch, Preview and Sync.
	result *orchestrator.Result
	ui     orchestrator.UI
	// doctorChecks, when set, replaces the checks Doctor returns.
	doctorChecks []orchestrator.Check
	// confirm, when set, makes Switch ask the UI first.
	confirm *orchestrator.Confirmation
	// busy is the busy list Preview reports (BusyWait one minute).
	busy []orchestrator.BusyView
	// confirmIn is the last OnlineConfirm input; confirm2 replaces its
	// result.
	confirmIn orchestrator.OnlineConfirmInput
	confirm2  *orchestrator.OnlineConfirmResult
	// legacyOpts are the options of every AdoptLegacy call; legacyDry and
	// legacyApply are its results (legacyErr is returned with an apply).
	legacyOpts  []orchestrator.LegacyOptions
	legacyDry   *orchestrator.LegacyResult
	legacyApply *orchestrator.LegacyResult
	legacyErr   error
}

func newFakeService() *fakeService {
	native := false
	return &fakeService{
		errs: map[string]error{},
		status: &orchestrator.Status{
			Active: "work", Previous: "home", Generations: 2, NativeMultiAccount: &native,
			Profiles: []orchestrator.ProfileStatus{
				{Alias: "work", Folder: "Claude", Active: true, Account: "0000\u2026", BaseValid: true},
				{Alias: "home", Folder: "Claude-home", Account: "0000\u2026", Managed: true},
			},
		},
		verify:      &orchestrator.VerifyResult{SessionFocused: true, PairingOK: true, BridgeSeen: true},
		legacyDry:   legacyDryFixture(),
		legacyApply: legacyApplyFixture(),
	}
}

func (f *fakeService) record(name string, args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
	f.args = append(f.args, args...)
	return f.errs[name]
}

func (f *fakeService) runResult(p *plan.Plan) *orchestrator.Result {
	if f.result != nil {
		return f.result
	}
	return &orchestrator.Result{
		RunID: "run-0001", From: p.From, To: p.Target.Alias, Mode: compat.Mode("full"),
		Plan: p, PlanHash: p.Hash(), DestructiveDigest: p.DestructiveDigest(),
	}
}

func (f *fakeService) Switch(ctx context.Context, o orchestrator.SwitchOptions) (*orchestrator.Result, error) {
	f.switchOpts = o
	if f.confirm != nil {
		ok, err := f.ui.Confirm(ctx, *f.confirm)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, pderr.New(pderr.Declined, "switch", "")
		}
	}
	if err := f.record("switch"); err != nil {
		return nil, err
	}
	p := fakePlan()
	p.Approximate = false
	r := f.runResult(p)
	if f.result == nil {
		r.Generation = 7
		r.Timings = map[orchestrator.Phase]time.Duration{orchestrator.P3Quit: 7 * time.Second}
	}
	return r, nil
}

func (f *fakeService) Preview(_ context.Context, o orchestrator.PreviewOptions) (*orchestrator.Result, error) {
	f.previewOpts = o
	if err := f.record("preview"); err != nil {
		return nil, err
	}
	r := f.runResult(fakePlan())
	if f.busy != nil {
		r.Busy, r.BusyWait = f.busy, time.Minute
	}
	return r, nil
}

func (f *fakeService) Sync(_ context.Context, o orchestrator.SyncOptions) (*orchestrator.Result, error) {
	f.syncOpts = o
	if err := f.record("sync"); err != nil {
		return nil, err
	}
	p := fakePlan()
	p.From, p.Approximate = "work", false
	p.Target.Alias = "work"
	return f.runResult(p), nil
}

func (f *fakeService) VerifyAfter(context.Context, string) (*orchestrator.VerifyResult, error) {
	if err := f.record("verify-after"); err != nil {
		return nil, err
	}
	return f.verify, nil
}

func (f *fakeService) Verify(context.Context) (*orchestrator.VerifyResult, error) {
	if err := f.record("verify"); err != nil {
		return nil, err
	}
	return f.verify, nil
}

func (f *fakeService) Recover(_ context.Context, o orchestrator.RecoverOptions) (*orchestrator.RecoverResult, error) {
	f.recoverOpts = o
	if err := f.record("recover"); err != nil {
		return nil, err
	}
	return &orchestrator.RecoverResult{Actions: []string{"closed the interrupted run"}, Active: "work"}, nil
}

func (f *fakeService) Rollback(_ context.Context, n uint64, propagate bool) (*orchestrator.RollbackResult, error) {
	f.rollbackN, f.propagate = n, propagate
	if err := f.record("rollback"); err != nil {
		return nil, err
	}
	return &orchestrator.RollbackResult{Generation: n, Restored: 4, Conflicts: []string{"sessions/local_" + fakeUUID(2)}}, nil
}

func (f *fakeService) Rebase(context.Context) error { return f.record("rebase") }

func (f *fakeService) Status(_ context.Context, short bool) (*orchestrator.Status, error) {
	f.statusShort = short
	if err := f.record("status"); err != nil {
		return nil, err
	}
	if short {
		// The short status reads no log.
		c := *f.status
		c.AccountChanges, c.NativeMultiAccount = nil, nil
		return &c, nil
	}
	return f.status, nil
}

func (f *fakeService) Doctor(_ context.Context, o orchestrator.DoctorOptions) ([]orchestrator.Check, error) {
	f.doctorOpts = o
	if err := f.record("doctor"); err != nil {
		return nil, err
	}
	if f.doctorChecks != nil {
		return f.doctorChecks, nil
	}
	return []orchestrator.Check{
		{ID: "app", Status: "ok", Detail: "Claude 6.28899.2 (tested)"},
		{ID: "automation", Status: "warn", Detail: "not granted yet\x1b[2J"},
	}, nil
}

func (f *fakeService) Init(_ context.Context, o orchestrator.InitOptions) error {
	f.initOpts = o
	return f.record("init")
}

func (f *fakeService) ProfileAdopt(_ context.Context, a ids.Alias, folder string, managed bool) error {
	if managed {
		return f.record("profile-adopt", string(a), folder, "managed")
	}
	return f.record("profile-adopt", string(a), folder)
}

func (f *fakeService) ProfileAdd(_ context.Context, a ids.Alias, managed bool) error {
	m := "unmanaged"
	if managed {
		m = "managed"
	}
	return f.record("profile-add", string(a), m)
}

func (f *fakeService) ProfileRename(_ context.Context, from, to ids.Alias) error {
	return f.record("profile-rename", string(from), string(to))
}

func (f *fakeService) ProfileRemove(_ context.Context, a ids.Alias) error {
	return f.record("profile-remove", string(a))
}

func (f *fakeService) OnlineConfirm(_ context.Context, in orchestrator.OnlineConfirmInput) (*orchestrator.OnlineConfirmResult, error) {
	f.confirmIn = in
	if err := f.record("online-confirm"); err != nil {
		return nil, err
	}
	if f.confirm2 != nil {
		return f.confirm2, nil
	}
	return &orchestrator.OnlineConfirmResult{State: "complete", Queue: in.Queue, Profile: "home"}, nil
}

func (f *fakeService) OnlineCancel(_ context.Context, queue string) (bool, error) {
	return true, f.record("online-cancel", queue)
}

func (f *fakeService) AdoptLegacy(_ context.Context, o orchestrator.LegacyOptions) (*orchestrator.LegacyResult, error) {
	f.legacyOpts = append(f.legacyOpts, o)
	if err := f.record("adopt-legacy"); err != nil {
		return nil, err
	}
	if o.Apply {
		r := *f.legacyApply
		return &r, f.legacyErr
	}
	r := *f.legacyDry
	r.Folders = append([]orchestrator.LegacyFolder(nil), r.Folders...)
	for i := range r.Folders {
		// The alias the person typed resolves the active folder.
		if a := o.Aliases["Claude"]; a != "" && r.Folders[i].Active && r.Folders[i].Reason == "needs_alias" {
			r.Folders[i].Alias, r.Folders[i].AliasSource, r.Folders[i].Status, r.Folders[i].Reason = a, "flag", "adopt", ""
		}
	}
	return &r, nil
}

func (f *fakeService) ArtifactsRound(_ context.Context, _ func(*state.Root) (*online.Draft, *online.QueueHeader, error)) (string, error) {
	return "", f.record("artifacts-round")
}

func (f *fakeService) CloseArtifactsQueue(_ context.Context, queue string) (bool, error) {
	return true, f.record("artifacts-close", queue)
}

var _ orchestrator.Service = (*fakeService)(nil)

// fakeGens is a generationStore.
type fakeGens struct {
	purged bool
	pruned *gen.PrunePolicy
	// pinned adds a pinned quarantine generation to List.
	pinned bool
}

func (g *fakeGens) List() ([]gen.Info, error) {
	list := []gen.Info{
		{N: 1, RunID: "run-0001", Alias: "work", Created: fixedTime, Entries: 3, LevelDB: true, Complete: true, Bytes: 2048},
		{N: 2, RunID: "run-0002", Alias: "home", Created: fixedTime.Add(time.Hour), Entries: 1, Complete: false, Bytes: 10},
	}
	if g.pinned {
		list = append(list, gen.Info{N: 3, RunID: "run-0003", Alias: "home", Created: fixedTime, Entries: 2, Complete: true,
			Pinned: true, Purpose: gen.PurposeLegacyQuarantine})
	}
	return list, nil
}

func (g *fakeGens) Load(n uint64) (*gen.Manifest, error) {
	return &gen.Manifest{Version: 1, N: n, RunID: "run-0001", Alias: "work", Created: fixedTime, Complete: true,
		Entries: []gen.Entry{
			{Kind: gen.EntryFile, Row: model.RowSessions, Path: "claude-code-sessions/x.json", Present: true, PreHash: "a", PostHash: "b"},
			{Kind: gen.EntryLSValue, Row: model.RowGroupOrder, Key: "code-projects-order.<id>", Present: true, PreHash: "c", PostHash: "d"},
		}}, nil
}

func (g *fakeGens) Prune(p gen.PrunePolicy) error { g.pruned = &p; return nil }
func (g *fakeGens) Purge() error                  { g.purged = true; return nil }

// fakeState is a hook.StateReader.
type fakeState struct{ root *state.Root }

func (s fakeState) Load() (*state.Root, error) { return s.root, nil }

// harness is a test system over a temporary home.
type harness struct {
	t     *testing.T
	home  string
	env   map[string]string
	tty   bool
	svc   *fakeService
	gens  *fakeGens
	root  *state.Root // for the hook
	ui    orchestrator.UI
	exe   string
	g     *globalFlags // the global flags of the last run
	sys   *system
	inApp bool
	// ol is the online environment of the `online` commands, built on
	// first use over the temporary home (online_test.go).
	ol *onlineEnv
	// art is the artifact service of the `artifacts` commands, built on
	// first use (artifacts_test.go); artReplica is main.log's replica
	// for it (nil: unknown).
	art        *artifacts.Service
	artReplica *model.Replica
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	home := t.TempDir()
	h := &harness{
		t: t, home: home, svc: newFakeService(), gens: &fakeGens{},
		env:   map[string]string{"LANG": "en_US.UTF-8"},
		exe:   filepath.Join(home, "bin", "paritydesk"),
		inApp: true,
	}
	h.sys = &system{
		getenv:     func(k string) string { return h.env[k] },
		home:       func() (string, error) { return h.home, nil },
		executable: func() (string, error) { return h.exe, nil },
		isTerminal: func(any) bool { return h.tty },
		isTTY:      func(any) bool { return h.tty },
		now:        func() time.Time { return fixedTime },
		service: func(_ context.Context, e *env, ui orchestrator.UI) (orchestrator.Service, error) {
			h.g, h.ui = e.g, ui
			h.svc.ui = ui
			return h.svc, nil
		},
		gens:  func(context.Context, *env) (generationStore, error) { return h.gens, nil },
		quiet: realQuiet, // real lock and root.json under the temporary home
		hookDeps: func(_ context.Context, e *env, pv string) (hook.Deps, error) {
			return hook.Deps{
				State:         fakeState{h.root},
				InsideApp:     func(context.Context) (bool, error) { return h.inApp, nil },
				PluginVersion: pv, BinaryVersion: Version,
			}, nil
		},
		online:       func(context.Context, *env) (*onlineEnv, error) { return h.onlineEnv(), nil },
		onlineCancel: func(context.Context, *env) (*online.Service, error) { return h.onlineEnv().svc, nil },
		toolDeps: func(context.Context, *env) (hook.ToolDeps, error) {
			return hook.ToolDeps{Store: h.onlineEnv().svc.Store, Mode: "armed", Mono: h.onlineEnv().svc.Mono,
				Clock: h.onlineEnv().svc.Clock, Providers: h.onlineEnv().svc.Providers}, nil
		},
		rules: func(*env) ([]string, map[string]string, error) {
			return []string{"Claude", "Claude-3p"}, map[string]string{"sidebarMode": "ui_state", "keepAwakeEnabled": "other"}, nil
		},
		artifacts: func(_ context.Context, e *env) (*artifacts.Service, error) {
			s := h.artifactsService()
			s.ReadOnly = e.readOnly()
			return s, nil
		},
	}
	return h
}

// run executes the command line and returns the exit code, stdout and
// stderr.
func (h *harness) run(stdin string, args ...string) (int, string, string) {
	h.t.Helper()
	var out, errb bytes.Buffer
	var in io.Reader = strings.NewReader(stdin)
	code := execute(context.Background(), args, in, &out, &errb, h.sys)
	return code, out.String(), errb.String()
}

// normalize replaces the temporary home in output.
func (h *harness) normalize(s string) string {
	return strings.ReplaceAll(s, h.home, "/HOME")
}
