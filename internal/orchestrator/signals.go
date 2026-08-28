package orchestrator

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

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
