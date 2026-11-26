package cli

import (
	"context"
	"flag"
	"regexp"
	"strings"
	"time"

	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/config"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/online"
	"github.com/kazupc/paritydesk/internal/orchestrator"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/render"
	"github.com/kazupc/paritydesk/internal/state"
)

// onlineEnv is what the `online` commands use: the online service over
// <SupportDir>/online, a lock-free view of root.json, and what they learn
// from the app (its manifest's [online] table, main.log, the process
// table). Tests replace it with fakes (system.online).
type onlineEnv struct {
	svc *online.Service
	// root loads root.json without the run lock (nil root: no state).
	root func() (*state.Root, error)
	// defaultDir: SupportDir is the default one, the only one the
	// plugin's hooks know (4.1).
	defaultDir bool
	// spec is the installed Claude's [online] table (Enabled false when
	// unknown); cfg the effective config (nil: no config file).
	spec compat.OnlineSpec
	cfg  *config.Config
	// insideApp reports whether this runs inside the desktop app.
	insideApp func(ctx context.Context) (bool, error)
	// replica reads main.log for the newest replica since t.
	replica func(ctx context.Context, since time.Time) online.ReplicaObs
	// activeOrg is the active account's org from main.log, when known.
	activeOrg func(ctx context.Context, account ids.AccountID) (ids.OrgID, bool)
	// art is the installed Claude's [artifacts] table (Enabled false when
	// unknown), appVersion its version and dataRoot the expanded data
	// root (artifact sync).
	art        compat.ArtifactsSpec
	appVersion string
	dataRoot   string
}

// onlineGroup is the `online` command group: status, retry and cancel
// are visible; capture, next, decide and done are used by the plugin's
// skills.
func onlineGroup() *command {
	hidden := func(c *command) *command { c.hidden = true; return c }
	return group("online", "<status|retry|cancel>", i18n.HelpOnline, false,
		leaf("status", "", i18n.HelpOnlineStatus, 0, 0, nil, runOnlineStatus),
		leaf("retry", "", i18n.HelpOnlineRetry, 0, 0, callerFlags, runOnlineRetry),
		leaf("cancel", "", i18n.HelpOnlineCancel, 0, 0, nil, runOnlineCancel),
		hidden(leaf("capture", "", i18n.HelpOnlineCapture, 0, 0, func(fs *flag.FlagSet) {
			sessionFlag(fs)
			fs.Bool("finish", false, usage(i18n.FlagFinish))
		}, runOnlineCapture)),
		hidden(leaf("next", "", i18n.HelpOnlineNext, 0, 0, func(fs *flag.FlagSet) {
			callerFlags(fs)
			fs.String("unavailable", "", usage(i18n.FlagUnavailable))
			fs.Bool("wait", false, usage(i18n.FlagWait))
		}, runOnlineNext)),
		hidden(leaf("decide", "", i18n.HelpOnlineDecide, 0, 0, func(fs *flag.FlagSet) {
			callerFlags(fs)
			fs.String("question", "", usage(i18n.FlagQuestion))
			fs.String("answer", "", usage(i18n.FlagAnswer))
		}, runOnlineDecide)),
		hidden(leaf("done", "", i18n.HelpOnlineDone, 0, 0, callerFlags, runOnlineDone)),
	)
}

func sessionFlag(fs *flag.FlagSet) {
	fs.String("session", "", usage(i18n.FlagSession))
}

func callerFlags(fs *flag.FlagSet) {
	sessionFlag(fs)
	fs.String("token", "", usage(i18n.FlagToken))
}

var (
	tokenFlagRE = regexp.MustCompile(`^[0-9a-f]{32}$`)
	answerRE    = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)
)

// caller validates --session and --token (exit 2 otherwise).
func caller(fs *flag.FlagSet) (online.Caller, error) {
	s := getString(fs, "session")
	if s != "" && !ids.IsUUID(s) {
		return online.Caller{}, usageError(i18n.UsageSessionFlag)
	}
	c := online.Caller{Session: s}
	if f := fs.Lookup("token"); f != nil {
		t := getString(fs, "token")
		if t != "" && !tokenFlagRE.MatchString(t) {
			return online.Caller{}, usageError(i18n.UsageTokenFlag)
		}
		c.Token = t
	}
	return c, nil
}

// onlineEnvOf builds the online environment.
func (e *env) onlineEnv(ctx context.Context) (*onlineEnv, error) { return e.sys.online(ctx, e) }

// rootInfo loads root.json (a missing one is an empty root).
func (o *onlineEnv) rootInfo() (online.RootInfo, *state.Root, error) {
	r, err := o.root()
	if err != nil {
		return online.RootInfo{}, nil, pderr.Wrap(pderr.Internal, "state", err)
	}
	return online.RootInfoOf(r), r, nil
}

// on reports whether the online layer is on for this app and config.
func (o *onlineEnv) on() bool {
	return o.spec.Enabled && o.cfg != nil && o.cfg.Online.Reconcile != config.OnlineReconcileOff
}

// batchLines sanitizes the fixed lines of a document.
func emitBatch(e *env, b online.Batch, err error) error {
	if b.Lines == nil {
		b.Lines = []i18n.Text{}
	}
	return e.emit(b, func(p *render.Printer) { p.Texts(b.Lines) }, err)
}

// noneBatch is a `state: none` answer with one fixed line (or none).
func noneBatch(m i18n.Msg) online.Batch {
	b := online.Batch{State: online.StateNone, Calls: []online.Call{}, Tools: []string{}, Lines: []i18n.Text{}}
	if m != "" {
		b.Lines = []i18n.Text{i18n.Tx(m)}
	}
	return b
}

func runOnlineStatus(ctx context.Context, e *env, _ *flag.FlagSet, _ []string) error {
	o, err := e.onlineEnv(ctx)
	if err != nil {
		return err
	}
	info, _, err := o.rootInfo()
	if err != nil {
		return err
	}
	v := o.svc.Status(info)
	return e.emit(v, func(p *render.Printer) { p.Texts(v.Lines) }, nil)
}

func runOnlineCancel(ctx context.Context, e *env, _ *flag.FlagSet, _ []string) error {
	if e.readOnly() {
		return pderr.NewT(pderr.Usage, "online cancel", i18n.OnlineErrReadOnly)
	}
	// The emergency stop (4.5) needs only the online folder: it does not
	// load the config, the state or the app, which may be what is broken.
	svc, err := e.sys.onlineCancel(ctx, e)
	if err != nil {
		return err
	}
	v, err := svc.Cancel(ctx)
	if err != nil {
		return err
	}
	// root.json follows when the run lock is free; otherwise the queue
	// head (cancelled) is ahead of it, which status and the hooks read.
	if svc, serr := e.service(ctx, e.newUI(false, false)); serr == nil {
		_, _ = svc.OnlineCancel(ctx, v.Queue)
	}
	return e.emit(v, func(p *render.Printer) { p.Texts(v.Lines) }, nil)
}

// captureView is the document of `online capture`.
type captureView struct {
	State   string        `json:"state"`
	Capture string        `json:"capture,omitempty"`
	Calls   []online.Call `json:"calls"`
	Tools   []string      `json:"tools"`
	Lines   []i18n.Text   `json:"lines"`
}

func runOnlineCapture(ctx context.Context, e *env, fs *flag.FlagSet, _ []string) error {
	c, err := caller(fs)
	if err != nil {
		return err
	}
	none := func(m i18n.Msg) error {
		v := captureView{State: online.StateNone, Calls: []online.Call{}, Tools: []string{}, Lines: []i18n.Text{}}
		if m != "" {
			v.Lines = []i18n.Text{i18n.Tx(m)}
		}
		return e.emit(v, func(p *render.Printer) { p.Texts(v.Lines) }, nil)
	}
	if e.readOnly() {
		return none(i18n.OnlineErrReadOnly)
	}
	o, err := e.onlineEnv(ctx)
	if err != nil {
		return none("")
	}
	if !o.defaultDir {
		return none(i18n.OnlineErrSupportDir)
	}
	if getBool(fs, "finish") {
		b, err := o.svc.CaptureFinish(ctx)
		if err != nil {
			return err
		}
		v := captureView{State: b.State, Calls: []online.Call{}, Tools: []string{}, Lines: []i18n.Text{}}
		return e.emit(v, func(p *render.Printer) { p.Line("%s", i18n.T(i18n.OnlineCaptureDone)) }, nil)
	}
	_, root, err := o.rootInfo()
	if err != nil || root == nil || !o.on() || root.Run != nil || root.Active == "" || root.Profiles[root.Active] == nil {
		return none("")
	}
	if inside, ierr := o.insideApp(ctx); ierr != nil || !inside {
		return none("")
	}
	rep := root.Profiles[root.Active].Replica
	if org, ok := o.activeOrg(ctx, rep.Account); ok {
		rep.Org = org
	}
	b, err := o.svc.CaptureStart(ctx, online.CaptureInput{Spec: o.spec, Config: o.cfg.Online, Profile: root.Active,
		Replica: rep, Session: c.Session}, root.Epoch)
	if err != nil {
		return err
	}
	v := captureView{State: b.State, Capture: b.Queue, Calls: b.Calls, Tools: b.Tools, Lines: []i18n.Text{}}
	return e.emit(v, func(p *render.Printer) {
		for _, call := range v.Calls {
			p.Line("%s %s", string(call.ID), call.Tool)
		}
	}, nil)
}

// queueCommand prepares next, decide, retry and done: flags, read-only
// mode, the environment and root.json.
func queueCommand(ctx context.Context, e *env, fs *flag.FlagSet, op string) (*onlineEnv, online.Caller, online.RootInfo, error) {
	c, err := caller(fs)
	if err != nil {
		return nil, c, online.RootInfo{}, err
	}
	if e.readOnly() {
		return nil, c, online.RootInfo{}, pderr.NewT(pderr.Usage, op, i18n.OnlineErrReadOnly)
	}
	o, err := e.onlineEnv(ctx)
	if err != nil {
		return nil, c, online.RootInfo{}, err
	}
	info, _, err := o.rootInfo()
	if err != nil {
		return nil, c, online.RootInfo{}, err
	}
	if info.RunOpen {
		return nil, c, info, pderr.NewT(pderr.NeedsRecover, op, i18n.ErrInterrupted)
	}
	return o, c, info, nil
}

func runOnlineNext(ctx context.Context, e *env, fs *flag.FlagSet, _ []string) error {
	o, c, info, err := queueCommand(ctx, e, fs, "online next")
	if err != nil {
		return err
	}
	var unavailable []string
	if s := getString(fs, "unavailable"); s != "" {
		for _, t := range strings.Split(s, ",") {
			t = strings.TrimSpace(t)
			if !online.ValidTool(t) {
				return usageError(i18n.UsageUnavailableSyntax)
			}
			unavailable = append(unavailable, t)
		}
	}
	if !o.defaultDir {
		return emitBatch(e, noneBatch(i18n.OnlineErrSupportDir), nil)
	}
	if !o.on() {
		return emitBatch(e, noneBatch(i18n.OnlineStatusNone), nil)
	}
	b, err := o.svc.Next(ctx, online.NextCall{Caller: c, Root: info, Unavailable: unavailable, Wait: getBool(fs, "wait"),
		Replica: o.replica})
	if err != nil {
		return err
	}
	return emitBatch(e, b, nil)
}

func runOnlineDecide(ctx context.Context, e *env, fs *flag.FlagSet, _ []string) error {
	question, answer := getString(fs, "question"), getString(fs, "answer")
	if !answerRE.MatchString(question) || !answerRE.MatchString(answer) {
		return usageError(i18n.UsageQuestionAnswer)
	}
	o, c, info, err := queueCommand(ctx, e, fs, "online decide")
	if err != nil {
		return err
	}
	v, err := o.svc.Decide(ctx, c, info, question, answer)
	if err != nil {
		return err
	}
	return e.emit(v, func(p *render.Printer) { p.Texts(v.Lines) }, nil)
}

func runOnlineRetry(ctx context.Context, e *env, fs *flag.FlagSet, _ []string) error {
	o, c, info, err := queueCommand(ctx, e, fs, "online retry")
	if err != nil {
		return err
	}
	v, err := o.svc.Retry(ctx, c, info)
	if err != nil {
		return err
	}
	return e.emit(v, func(p *render.Printer) { p.Texts(v.Lines) }, nil)
}

func runOnlineDone(ctx context.Context, e *env, fs *flag.FlagSet, _ []string) error {
	o, c, info, err := queueCommand(ctx, e, fs, "online done")
	if err != nil {
		return err
	}
	if info.Record == nil {
		v := online.StatusView{State: online.StateNone, Steps: []online.StepView{}, Lines: []i18n.Text{i18n.Tx(i18n.OnlineStatusNone)}}
		return e.emit(v, func(p *render.Printer) { p.Texts(v.Lines) }, nil)
	}
	if info.Record.Kind != "" {
		v := online.StatusView{State: online.StateNone, Queue: info.Record.Queue, Steps: []online.StepView{},
			Lines: []i18n.Text{i18n.Tx(i18n.OnlineArtifactsQueue)}}
		return e.emit(v, func(p *render.Printer) { p.Texts(v.Lines) }, nil)
	}
	svc, err := e.service(ctx, e.newUI(false, false))
	if err != nil {
		return err
	}
	res, err := svc.OnlineConfirm(ctx, orchestrator.OnlineConfirmInput{Queue: info.Record.Queue, Caller: c})
	if err != nil {
		return err
	}
	v := doneView(res)
	if res.State == string(online.QueueComplete) {
		// A17: artifacts from another profile wait (due.json, no lock).
		if paths, _, perr := e.paths(); perr == nil {
			if _, root, rerr := o.rootInfo(); rerr == nil {
				if line := artifactsNextLine(paths.SupportDir, root); !line.IsZero() {
					v.Lines = append(v.Lines, line)
				}
			}
		}
	}
	return e.emit(v, func(p *render.Printer) { p.Texts(v.Lines) }, nil)
}

// doneView is the document of `online done`.
func doneView(res *orchestrator.OnlineConfirmResult) online.StatusView {
	v := online.StatusView{State: res.State, Queue: res.Queue, Profile: res.Profile, Counts: res.Counts,
		Steps: []online.StepView{}, Lines: []i18n.Text{}}
	switch res.State {
	case online.StateBusyElsewhere:
		v.Lines = []i18n.Text{i18n.Tx(i18n.OnlineBusyElsewhere)}
		return v
	case online.StateStale:
		v.Lines = []i18n.Text{i18n.Tx(i18n.OnlineStatusStale)}
		return v
	case online.StateNone:
		v.Lines = []i18n.Text{i18n.Tx(i18n.OnlineStatusNone)}
		return v
	}
	total, failed := 0, 0
	for _, s := range res.Steps {
		v.Steps = append(v.Steps, online.StepView{ID: s.ID, Kind: s.Kind, Status: s.Status, Reason: s.Reason})
		if s.Kind == online.KindRead || s.Kind == online.KindCapture || (s.Status == online.StatusSkipped && s.Reason == "") {
			continue
		}
		total++
		if s.Status != online.StatusDone && s.Status != online.StatusSkipped {
			failed++
		}
	}
	v.Confirmed = &online.ConfirmView{BasesAdvanced: res.BasesAdvanced, ContestedCleared: res.ContestedCleared,
		ContestedLeft: res.ContestedLeft, RootBusy: res.RootBusy}
	v.Lines = append(v.Lines, i18n.Tx(i18n.OnlineDoneSummary, res.Counts.Done, total, res.Counts.Skipped, failed))
	if failed > 0 {
		v.Lines = append(v.Lines, i18n.Tx(i18n.OnlineDoneFailedHint))
	}
	if res.RootBusy {
		v.Lines = append(v.Lines, i18n.Tx(i18n.OnlineDoneRootBusy))
	}
	return v
}
