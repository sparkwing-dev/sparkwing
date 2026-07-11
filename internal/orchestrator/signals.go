package orchestrator

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// withInterruptCancel cancels the returned context when the process
// receives SIGINT or SIGTERM, with a [runInterruptedError] cause naming
// the signal so the run finalizes as cancelled with the real reason.
// After the first signal, notification stops, so a second signal falls
// back to the default handler and kills a wedged process outright.
func withInterruptCancel(parent context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case sig := <-ch:
			signal.Stop(ch)
			cancel(&runInterruptedError{signal: sig})
		case <-ctx.Done():
		}
	}()
	return ctx, func() {
		signal.Stop(ch)
		cancel(nil)
	}
}
