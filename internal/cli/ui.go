package cli

import (
	"bufio"
	"context"
	"strings"
	"time"

	"github.com/kazupc/paritydesk/internal/i18n"
	"github.com/kazupc/paritydesk/internal/orchestrator"
	"github.com/kazupc/paritydesk/internal/pderr"
	"github.com/kazupc/paritydesk/internal/render"
)

// termUI is the render-backed orchestrator.UI: progress on stderr (plain
// lines, or NDJSON with --json-progress) and [y/N] prompts on stderr,
// answered on stdin. Defaults are safe: anything but y or yes declines.
type termUI struct {
	p            *render.Printer // writes to stderr
	in           func() *bufio.Reader
	interactive  bool
	yes          bool
	jsonProgress bool
	quiet        bool // --json without --json-progress: no progress lines
	now          func() time.Time
	// events keeps every progress event with its time, for the run log.
	events []loggedEvent
}

var _ orchestrator.UI = (*termUI)(nil)

// newUI returns the UI for one command. yes is the command's --yes.
func (e *env) newUI(yes, jsonProgress bool) *termUI {
	p := e.printer()
	errp := &render.Printer{Out: e.stderr, Err: e.stderr, Mode: p.Mode}
	errp.Mode.JSON = false
	return &termUI{
		p:            errp,
		in:           e.stdinReader,
		interactive:  e.interactive(),
		yes:          yes,
		jsonProgress: jsonProgress,
		quiet:        e.g.JSON && !jsonProgress,
		now:          e.sys.now,
	}
}

// stdinReader returns the shared buffered reader over stdin.
func (e *env) stdinReader() *bufio.Reader {
	if e.in == nil {
		e.in = bufio.NewReader(e.stdin)
	}
	return e.in
}

// Progress prints one event.
func (u *termUI) Progress(ev orchestrator.Event) {
	ev.Message = render.Sanitize(ev.Message, 200)
	u.events = append(u.events, loggedEvent{At: u.now(), Event: ev})
	switch {
	case u.jsonProgress:
		if b, err := render.Document(ev); err == nil {
			_, _ = u.p.Out.Write(b)
		}
	case u.quiet, ev.Status == orchestrator.StatusInfo:
		// Kept in the run log only.
	default:
		u.p.Line("%s", i18n.T(i18n.Progress, render.Sanitize(string(ev.Phase), 8), render.Sanitize(ev.Status, 8), ev.Message))
	}
}

// Interactive reports whether questions can be answered.
func (u *termUI) Interactive() bool { return u.interactive }

// Confirm asks one question. With --yes it answers yes, except for
// destructive differences found after the preview, which always ask. A
// non-interactive run without an answer is an error.
func (u *termUI) Confirm(ctx context.Context, c orchestrator.Confirmation) (bool, error) {
	if u.yes && c.Kind != orchestrator.ConfirmNewDestructive {
		return true, nil
	}
	if !u.interactive {
		return false, errNoTerminal()
	}
	if c.Summary != nil {
		v := render.PlanView{Summary: *c.Summary}
		if c.Plan != nil {
			v.From, v.To = string(c.Plan.From), string(c.Plan.Target.Alias)
			v.Approximate, v.FirstSync = c.Plan.Approximate, c.Plan.NeedsConfirmation
		}
		u.p.Lines(render.PlanLines(v))
	}
	u.p.Lines(c.Lines)
	ok, err := u.ask(ctx, questionFor(c.Kind))
	if err != nil {
		return false, err
	}
	return ok, nil
}

// ask prints question and reads one answer line; only y or yes accept.
func (u *termUI) ask(ctx context.Context, question string) (bool, error) {
	_, _ = u.p.Out.Write([]byte(render.Sanitize(question, 0)))
	line, err := u.readLine(ctx)
	if err != nil {
		return false, err
	}
	a := strings.ToLower(strings.TrimSpace(line))
	return a == "y" || a == "yes", nil
}

// readLine reads one line from stdin, giving up when ctx ends.
func (u *termUI) readLine(ctx context.Context) (string, error) {
	type result struct {
		s   string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		s, err := u.in().ReadString('\n')
		ch <- result{s, err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-ch:
		if r.err != nil && r.s == "" {
			// End of input counts as the default answer: no.
			return "", nil
		}
		return r.s, nil
	}
}

// questionFor returns the [y/N] question for a confirmation kind.
func questionFor(k orchestrator.ConfirmKind) string {
	ids := map[orchestrator.ConfirmKind]i18n.Msg{
		orchestrator.ConfirmPlan:           i18n.AskPlan,
		orchestrator.ConfirmNewDestructive: i18n.AskNewDestructive,
		orchestrator.ConfirmBaseInvalid:    i18n.AskBaseInvalid,
		orchestrator.ConfirmBusy:           i18n.AskBusy,
		orchestrator.ConfirmOutsideSwitch:  i18n.AskOutsideSwitch,
		orchestrator.ConfirmRecover:        i18n.AskRecover,
		orchestrator.ConfirmRollback:       i18n.AskRollback,
		orchestrator.ConfirmAddBind:        i18n.AskAddBind,
	}
	if id, ok := ids[k]; ok {
		return i18n.T(id)
	}
	return i18n.T(i18n.AskPlan)
}

// errNoTerminal is returned when a question cannot be asked.
func errNoTerminal() error {
	return pderr.New(pderr.Usage, "confirm", i18n.T(i18n.ConfirmNoTTY))
}

// confirmLocal asks a question for a command that the CLI runs itself
// (backups purge, uninstall): --yes answers it; a declined question is
// exit 3.
func (e *env) confirmLocal(ctx context.Context, yes bool, id i18n.Msg) error {
	if yes {
		return nil
	}
	u := e.newUI(false, false)
	if !u.interactive {
		return errNoTerminal()
	}
	ok, err := u.ask(ctx, i18n.T(id))
	if err != nil {
		return err
	}
	if !ok {
		return pderr.New(pderr.Declined, "confirm", i18n.T(i18n.Declined))
	}
	return nil
}
