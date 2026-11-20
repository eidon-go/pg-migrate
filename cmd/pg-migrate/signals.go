package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

// newSignalContext returns a context that is cancelled when the process
// receives SIGINT or SIGTERM, and a stop function that restores the default
// signal handling.
//
// Commands run on this context so an interrupted migration unwinds through the
// library's normal error paths — the in-flight statement is cancelled, a
// partially-applied notransaction script is recorded as failed, and the
// advisory lock is released — instead of the process dying mid-statement with
// none of that bookkeeping.
//
// The handler is removed the moment the first signal arrives. The shutdown it
// triggers is bounded by the library's own timeouts, but if it still outstays
// its welcome a second signal falls back to the default disposition and kills
// the process outright.
func newSignalContext() (ctx context.Context, stop func()) {
	ctx, cancel := context.WithCancel(context.Background())

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		select {
		case sig := <-sigCh:
			signal.Stop(sigCh)

			slog.Warn("Signal received, cancelling the migration run", "signal", sig.String())
			cancel()
		case <-ctx.Done():
		}
	}()

	return ctx, func() {
		signal.Stop(sigCh)
		cancel()
	}
}
