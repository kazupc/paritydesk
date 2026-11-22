// Command paritydesk switches the Claude desktop app between accounts on
// one Mac and keeps everything except the account in step.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/kazupc/paritydesk/internal/cli"
)

func main() {
	// Interrupts (Ctrl-C, or SIGTERM from launchd at logout) cancel the
	// context. The orchestrator checks it only at phase boundaries up to
	// the P5 commit; from the P5 commit to P10, and on every abort,
	// rollback and relaunch path, it works on context.WithoutCancel with
	// its own timeout, so a signal never strands a half-applied run. State
	// intents make anything else recoverable.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Execute(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
