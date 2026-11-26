package cli

import (
	"context"
	"flag"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kazupc/paritydesk/internal/artifacts"
	"github.com/kazupc/paritydesk/internal/clock"
	"github.com/kazupc/paritydesk/internal/compat"
	"github.com/kazupc/paritydesk/internal/fsx"
	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/ids"
	"github.com/kazupc/paritydesk/internal/online"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/render"
	"github.com/kazupc/paritydesk/internal/state"
)

// allowFlags are the decision flags of `artifacts allow` (exactly one).
var allowFlags = []string{"capabilities", "shared", "public", "restore", "create", "resume", "rehome", "roundtrip"}

// artifactsGroup is the `artifacts` command group: status, plan, ingest,
// allow, skip, forget and import-legacy are visible; next, decide, pause
// and done are used by /paritydesk:artifacts.
func artifactsGroup() *command {
	hidden := func(c *command) *command { c.hidden = true; return c }
	profileFlag := func(fs *flag.FlagSet) { fs.String("profile", "", "profile alias (default: the active profile)") }
	return group("artifacts", "<status|plan|ingest|allow|skip|forget|import-legacy>",
		"Keep claude.ai artifacts the same on every profile (beta)", false,
		leaf("status", "", "Show the artifact archive", 0, 0, func(fs *flag.FlagSet) {
			profileFlag(fs)
			fs.Bool("all", false, "include forgotten and ignored artifacts")
			fs.Bool("titles", false, "include titles in --json output")
		}, runArtifactsStatus),
		leaf("plan", "", "Preview what a sync on a profile would do (approximate)", 0, 0, profileFlag, runArtifactsPlan),
		leaf("ingest", "", "Record a finished sync round now", 0, 0, nil, runArtifactsIngest),
		leaf("allow", "[<id>]", "Decide about one artifact (Terminal only)", 0, 1, func(fs *flag.FlagSet) {
			fs.Bool("capabilities", false, "sync it although it declares capabilities")
			fs.Bool("shared", false, "publish to copies shared with other people")
			fs.Bool("public", false, "publish to public copies (implies --shared)")
			fs.Bool("restore", false, "create it again on a profile where it was deleted")
			fs.Bool("create", false, "create it on a profile although the title is taken there")
			fs.Bool("resume", false, "sync a skipped or ignored artifact again")
			fs.Bool("rehome", false, "stop tracking a copy in an organization the profile no longer uses")
			fs.Bool("roundtrip", false, "allow publishing again after a page read back differently")
			profileFlag(fs)
			yesFlag(fs)
		}, runArtifactsAllow),
		leaf("skip", "<id>", "Stop syncing an artifact (it stays in the archive)", 1, 1, nil, runArtifactsSkip),
		leaf("forget", "<id>", "Stop tracking an artifact (Terminal only)", 1, 1, yesFlag, runArtifactsForget),
		leaf("import-legacy", "", "Import the artifact archive of an earlier folder-swap script", 0, 0, func(fs *flag.FlagSet) {
			fs.String("from", "", "absolute path of the old archive folder (required)")
			fs.Var(&multiFlag{}, "map", "<account-uuid>=<alias>; may be repeated")
			fs.Bool("apply", false, "import (default: dry run; Terminal only)")
			yesFlag(fs)
		}, runArtifactsImport),
		hidden(leaf("next", "", "Internal: the next calls of an artifact sync (used by /paritydesk:artifacts)", 0, 0, func(fs *flag.FlagSet) {
			callerFlags(fs)
			fs.String("unavailable", "", "tools the session could not load, comma-separated")
			fs.String("refused", "", "step ids whose call was declined, comma-separated")
			fs.Bool("wait", false, "wait until the next calls can be made (at most 60 s)")
		}, runArtifactsNext)),
		hidden(leaf("decide", "", "Internal: record an answer (used by /paritydesk:artifacts)", 0, 0, func(fs *flag.FlagSet) {
			callerFlags(fs)
			fs.String("question", "", "question id")
			fs.String("answer", "", "option id")
		}, runArtifactsDecide)),
		hidden(leaf("pause", "", "Internal: stop an artifact sync for now", 0, 0, callerFlags, runArtifactsPause)),
		hidden(leaf("done", "", "Internal: finish an artifact sync (used by /paritydesk:artifacts)", 0, 0, callerFlags, runArtifactsDone)),
	)
}

// artifactsService builds the artifact service for one command.
func (e *env) artifactsService(ctx context.Context) (*artifacts.Service, error) {
	return e.sys.artifacts(ctx, e)
}

// orchestratorRounds is the run-lock side of artifact sync, built on use.
type orchestratorRounds struct{ e *env }

func (r orchestratorRounds) ArtifactsRound(ctx context.Context, f func(root *state.Root) (*online.Draft, *online.QueueHeader, error)) (string, error) {
	svc, err := r.e.service(ctx, r.e.newUI(false, false))
	if err != nil {
		return "", err
	}
	return svc.ArtifactsRound(ctx, f)
}

func (r orchestratorRounds) CloseArtifactsQueue(ctx context.Context, queueID string) (bool, error) {
	svc, err := r.e.service(ctx, r.e.newUI(false, false))
	if err != nil {
		return false, err
	}
	return svc.CloseArtifactsQueue(ctx, queueID)
}

// artifactsLocker takes <supportDir>/locks/artifacts.lock (flock).
func artifactsLocker(supportDir string) func(ctx context.Context, wait time.Duration) (func(), error) {
	return func(ctx context.Context, wait time.Duration) (func(), error) {
		dir := filepath.Join(supportDir, "locks")
		ls, err := state.Open(dir, filepath.Join(dir, artifacts.LockName), fsx.Default(), clock.System{})
		if err != nil {
			return nil, err
		}
		l, err := ls.Lock(ctx, wait)
		if err != nil {
			return nil, err
		}
		return func() { _ = l.Release() }, nil
	}
}

// realArtifacts builds the artifact service over the online environment
// and the archive under the support directory.
func realArtifacts(ctx context.Context, e *env) (*artifacts.Service, error) {
	o, err := e.onlineEnv(ctx)
	if err != nil {
		return nil, err
	}
	paths, _, err := e.paths()
	if err != nil {
		return nil, err
	}
	ops := fsx.Default()
	if e.readOnly() {
		ops = fsx.ReadOnly(ops)
	}
	arch := artifacts.NewArchive(paths.SupportDir, ops, o.art)
	arch.Lock = artifactsLocker(paths.SupportDir)
	return &artifacts.Service{Archive: arch, Online: o.svc, OnlineSpec: o.spec, Spec: o.art, Config: o.cfg, Root: o.root,
		Rounds: orchestratorRounds{e: e}, Replica: func(ctx context.Context) online.ReplicaObs { return o.replica(ctx, time.Time{}) },
		Getenv: e.sys.getenv, DefaultDir: o.defaultDir, ReadOnly: e.readOnly(), AppVersion: o.appVersion, Clock: clock.System{},
		SupportDir: paths.SupportDir, DataRoot: o.dataRoot}, nil
}

// errTerminalOnly is the refusal of a decision leaf inside Claude (9.3).
func errTerminalOnly() error {
	return pderr.New(pderr.Usage, "artifacts", i18n.T(i18n.ArtifactsErrTerminalOnly))
}

// insideClaude reports the Claude environment (9.3).
func (e *env) insideClaude() bool { return artifacts.InClaude(e.sys.getenv) }

// confirmArtifacts asks in Terminal: --yes answers it; without a
// terminal it is exit 3 with artifacts.err_needs_yes.
func (e *env) confirmArtifacts(ctx context.Context, yes bool) artifacts.Confirm {
	return func(question string) error {
		if yes {
			return nil
		}
		u := e.newUI(false, false)
		if !u.interactive {
			return pderr.New(pderr.Declined, "artifacts", i18n.T(i18n.ArtifactsErrNeedsYes))
		}
		ok, err := u.ask(ctx, question)
		if err != nil {
			return err
		}
		if !ok {
			return pderr.New(pderr.Declined, "confirm", i18n.T(i18n.Declined))
		}
		return nil
	}
}

// aliasFlag validates --profile.
func aliasFlag(fs *flag.FlagSet) (ids.Alias, error) {
	s := getString(fs, "profile")
	if s == "" {
		return "", nil
	}
	a, err := ids.ParseAlias(s)
	if err != nil {
		return "", usageErrorf("--profile: %v", err)
	}
	return a, nil
}

func runArtifactsStatus(ctx context.Context, e *env, fs *flag.FlagSet, _ []string) error {
	a, err := aliasFlag(fs)
	if err != nil {
		return err
	}
	svc, err := e.artifactsService(ctx)
	if err != nil {
		return err
	}
	v, err := svc.Status(artifacts.StatusInput{Profile: a, All: getBool(fs, "all"), Titles: getBool(fs, "titles") && e.g.JSON})
	if err != nil {
		return err
	}
	return e.emit(v, func(p *render.Printer) {
		for _, pv := range v.Pairs {
			var words []string
			for _, c := range pv.Copies {
				w := c.Status
				if w == "" {
					w = c.State
				}
				if c.Reason != "" {
					w += " (" + c.Reason + ")"
				}
				words = append(words, string(c.Profile)+": "+w)
			}
			title := render.Sanitize(v.Titles[pv.Pair], 80)
			if title == "" {
				title = i18n.T(i18n.ArtifactsNoTitle)
			}
			p.Line("%s  %s  %s", string(pv.Pair), title, strings.Join(words, ", "))
		}
		for _, ig := range v.Ignored {
			p.Line("%s  %s: %s", ids.Mask(string(ig.Slug)), string(ig.Profile), ig.Reason)
		}
		p.Lines(v.Lines)
	}, nil)
}

func runArtifactsPlan(ctx context.Context, e *env, fs *flag.FlagSet, _ []string) error {
	a, err := aliasFlag(fs)
	if err != nil {
		return err
	}
	svc, err := e.artifactsService(ctx)
	if err != nil {
		return err
	}
	if a == "" {
		if root, rerr := svc.Root(); rerr == nil && root != nil {
			a = root.Active
		}
	}
	if a == "" {
		return usageErrorf("--profile names the profile (no profile is active)")
	}
	v, err := svc.Plan(a)
	if err != nil {
		return err
	}
	return e.emit(v, func(p *render.Printer) {
		p.Line("%s", i18n.T(i18n.ArtifactsPlanSummary,
			len(v.Read), len(v.Publish), len(v.Create), len(v.Deleted), len(v.Skipped), v.NewHere))
		p.Lines(v.Lines)
	}, nil)
}

func runArtifactsIngest(ctx context.Context, e *env, _ *flag.FlagSet, _ []string) error {
	if e.readOnly() {
		return pderr.New(pderr.Usage, "artifacts ingest", i18n.T(i18n.ArtifactsErrReadOnly))
	}
	svc, err := e.artifactsService(ctx)
	if err != nil {
		return err
	}
	v, err := svc.IngestNow(ctx)
	if err != nil {
		return err
	}
	return e.emit(v, func(p *render.Printer) { p.Lines(v.Lines) }, nil)
}

func runArtifactsAllow(ctx context.Context, e *env, fs *flag.FlagSet, args []string) error {
	if e.insideClaude() {
		return errTerminalOnly()
	}
	var what []string
	for _, f := range allowFlags {
		if getBool(fs, f) {
			what = append(what, f)
		}
	}
	if len(what) != 1 {
		return usageErrorf("artifacts allow takes exactly one of --%s", strings.Join(allowFlags, ", --"))
	}
	if what[0] == "roundtrip" && len(args) > 0 {
		return usageErrorf("artifacts allow --roundtrip takes no id")
	}
	if what[0] != "roundtrip" && len(args) != 1 {
		return usageErrorf("artifacts allow <id> --%s", what[0])
	}
	a, err := aliasFlag(fs)
	if err != nil {
		return err
	}
	if e.readOnly() {
		return pderr.New(pderr.Usage, "artifacts allow", i18n.T(i18n.ArtifactsErrReadOnly))
	}
	svc, err := e.artifactsService(ctx)
	if err != nil {
		return err
	}
	in := artifacts.AllowInput{What: what[0], Profile: a}
	if len(args) == 1 {
		in.Ref = args[0]
	}
	v, err := svc.Allow(ctx, in, e.confirmArtifacts(ctx, getBool(fs, "yes")))
	if err != nil {
		return err
	}
	return e.emit(v, func(p *render.Printer) {
		if v.Slug != "" {
			p.Line("%s", v.Slug)
		}
		p.Lines(v.Lines)
	}, nil)
}

func runArtifactsSkip(ctx context.Context, e *env, _ *flag.FlagSet, args []string) error {
	if e.readOnly() {
		return pderr.New(pderr.Usage, "artifacts skip", i18n.T(i18n.ArtifactsErrReadOnly))
	}
	svc, err := e.artifactsService(ctx)
	if err != nil {
		return err
	}
	v, err := svc.Skip(ctx, args[0])
	if err != nil {
		return err
	}
	return e.emit(v, func(p *render.Printer) { p.Lines(v.Lines) }, nil)
}

func runArtifactsForget(ctx context.Context, e *env, fs *flag.FlagSet, args []string) error {
	if e.insideClaude() {
		return errTerminalOnly()
	}
	if e.readOnly() {
		return pderr.New(pderr.Usage, "artifacts forget", i18n.T(i18n.ArtifactsErrReadOnly))
	}
	svc, err := e.artifactsService(ctx)
	if err != nil {
		return err
	}
	v, err := svc.Forget(ctx, args[0], e.confirmArtifacts(ctx, getBool(fs, "yes")))
	if err != nil {
		return err
	}
	return e.emit(v, func(p *render.Printer) { p.Lines(v.Lines) }, nil)
}

// mapFlagRE is one --map value.
var mapFlagRE = regexp.MustCompile(`^(` + ids.UUIDPattern + `)=([a-z0-9][a-z0-9-]{0,31})$`)

func runArtifactsImport(ctx context.Context, e *env, fs *flag.FlagSet, _ []string) error {
	apply := getBool(fs, "apply")
	if apply && e.insideClaude() {
		return errTerminalOnly()
	}
	from := getString(fs, "from")
	if from == "" {
		return usageErrorf("artifacts import-legacy needs --from <folder>")
	}
	mapping := map[ids.AccountID]ids.Alias{}
	for _, m := range getStrings(fs, "map") {
		parts := mapFlagRE.FindStringSubmatch(m)
		if parts == nil {
			return usageErrorf("--map takes <account-uuid>=<alias>")
		}
		mapping[ids.AccountID(parts[1])] = ids.Alias(parts[2])
	}
	if apply && e.readOnly() {
		return pderr.New(pderr.Usage, "artifacts import-legacy", i18n.T(i18n.ArtifactsErrReadOnly))
	}
	svc, err := e.artifactsService(ctx)
	if err != nil {
		return err
	}
	var confirm artifacts.Confirm
	if apply {
		confirm = e.confirmArtifacts(ctx, getBool(fs, "yes"))
	}
	v, err := svc.ImportLegacy(ctx, artifacts.ImportInput{From: from, Map: mapping, Apply: apply}, confirm)
	if err != nil {
		return err
	}
	return e.emit(v, func(p *render.Printer) { p.Lines(v.Lines) }, nil)
}

// refusedRE is one --refused step id.
var refusedRE = regexp.MustCompile(`^a[0-9]{3}$`)

// hiddenNone is the `state: none` answer of a hidden leaf.
func hiddenNone(e *env, m i18n.Msg) error {
	v := artifacts.NextOut{State: online.StateNone, Calls: []online.Call{}, Tools: []string{}, Lines: []string{i18n.T(m)}}
	return e.emit(v, func(p *render.Printer) { p.Lines(v.Lines) }, nil)
}

func runArtifactsNext(ctx context.Context, e *env, fs *flag.FlagSet, _ []string) error {
	c, err := caller(fs)
	if err != nil {
		return err
	}
	a := artifacts.NextArgs{Session: c.Session, Token: c.Token, Wait: getBool(fs, "wait")}
	if s := getString(fs, "unavailable"); s != "" {
		for _, t := range strings.Split(s, ",") {
			t = strings.TrimSpace(t)
			if !online.ValidTool(t) {
				return usageErrorf("--unavailable takes tool names")
			}
			a.Unavailable = append(a.Unavailable, t)
		}
	}
	if s := getString(fs, "refused"); s != "" {
		for _, id := range strings.Split(s, ",") {
			id = strings.TrimSpace(id)
			if !refusedRE.MatchString(id) {
				return usageErrorf("--refused takes step ids such as a001")
			}
			a.Refused = append(a.Refused, online.StepID(id))
		}
	}
	if !e.insideClaude() {
		return hiddenNone(e, i18n.ArtifactsErrOutsideApp)
	}
	if e.readOnly() {
		return hiddenNone(e, i18n.ArtifactsErrReadOnly)
	}
	svc, err := e.artifactsService(ctx)
	if err != nil {
		return err
	}
	v, err := svc.Next(ctx, a)
	if err != nil {
		return err
	}
	return e.emit(v, func(p *render.Printer) {
		for _, call := range v.Calls {
			p.Line("%s %s", string(call.ID), call.Tool)
		}
		p.Lines(v.Lines)
	}, nil)
}

// hiddenLeaf prepares decide, pause and done: flags, the Claude
// environment and read-only mode.
func hiddenLeaf(ctx context.Context, e *env, fs *flag.FlagSet, op string) (*artifacts.Service, online.Caller, bool, error) {
	c, err := caller(fs)
	if err != nil {
		return nil, c, false, err
	}
	if !e.insideClaude() {
		return nil, c, false, hiddenNone(e, i18n.ArtifactsErrOutsideApp)
	}
	if e.readOnly() {
		return nil, c, false, pderr.New(pderr.Usage, op, i18n.T(i18n.ArtifactsErrReadOnly))
	}
	svc, err := e.artifactsService(ctx)
	return svc, c, err == nil, err
}

func runArtifactsDecide(ctx context.Context, e *env, fs *flag.FlagSet, _ []string) error {
	question, answer := getString(fs, "question"), getString(fs, "answer")
	if !answerRE.MatchString(question) || !answerRE.MatchString(answer) {
		return usageErrorf("--question and --answer take ids")
	}
	svc, c, ok, err := hiddenLeaf(ctx, e, fs, "artifacts decide")
	if !ok {
		return err
	}
	v, err := svc.Decide(ctx, c, question, answer)
	if err != nil {
		return err
	}
	return e.emit(v, func(p *render.Printer) { p.Lines(v.Lines) }, nil)
}

func runArtifactsPause(ctx context.Context, e *env, fs *flag.FlagSet, _ []string) error {
	svc, c, ok, err := hiddenLeaf(ctx, e, fs, "artifacts pause")
	if !ok {
		return err
	}
	v, err := svc.Pause(ctx, c)
	if err != nil {
		return err
	}
	return e.emit(v, func(p *render.Printer) { p.Lines(v.Lines) }, nil)
}

func runArtifactsDone(ctx context.Context, e *env, fs *flag.FlagSet, _ []string) error {
	svc, c, ok, err := hiddenLeaf(ctx, e, fs, "artifacts done")
	if !ok {
		return err
	}
	v, err := svc.Done(ctx, c)
	if err != nil {
		return err
	}
	return e.emit(v, func(p *render.Printer) { p.Lines(v.Lines) }, nil)
}

// artifactsNextLine is online done's last line (A17): artifacts from
// another profile wait for the active profile in this epoch.
func artifactsNextLine(supportDir string, root *state.Root) string {
	if root == nil || root.Active == "" {
		return ""
	}
	d := artifacts.NewArchive(supportDir, fsx.ReadOnly(fsx.Default()), compat.ArtifactsSpec{}).LoadDue()
	if d == nil || d.Count <= 0 || d.Profile != root.Active || d.Epoch != root.Epoch {
		return ""
	}
	return i18n.T(i18n.OnlineArtifactsNext)
}
