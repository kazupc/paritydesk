package orchestrator

import (
	"context"
	"time"

	"github.com/kazupc/paritydesk/internal/pderr"
)

// Notifications a detached switch posts, because nobody watches its
// output. The text is fixed (PRIVACY.md): no profile names, ids or paths.
const (
	notifyTitle    = "paritydesk"
	notifySwitched = "The switch finished. Claude is open on the other account."
	notifyFinished = "The switch finished."
	notifyCheck    = "The switch finished, but the check after launch found a problem. Run paritydesk status in Terminal."
	notifyStopped  = "The switch did not finish. Run paritydesk status in Terminal."
	notifyTimeout  = 10 * time.Second
)

// notify posts a notification for a detached run. A failure is ignored:
// the result file and `paritydesk status` carry the outcome either way.
func (r *run) notify(ctx context.Context, text string) {
	if !r.opts.FromRunner {
		return
	}
	nctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), notifyTimeout)
	defer cancel()
	_ = r.d().App.Notify(nctx, notifyTitle, text)
	r.notified = true
}

// notifyEnd posts the last notification of a detached run: a success
// that launched Claude was announced at launch, so only a later problem
// adds one; any other outcome gets exactly one.
func (r *run) notifyEnd(ctx context.Context, code pderr.Code) {
	ok := code == pderr.OK || code == pderr.SwapOnly || code == pderr.Degraded
	switch {
	case r.notified && ok:
	case r.notified:
		r.notify(ctx, notifyCheck)
	case ok:
		r.notify(ctx, notifyFinished)
	default:
		r.notify(ctx, notifyStopped)
	}
}
