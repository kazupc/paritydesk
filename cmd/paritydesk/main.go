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
	// Interrupts cancel the context; phases stop at the next safe point and
	// state intents make the rest recoverable.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Execute(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
