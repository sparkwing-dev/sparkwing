package main

import (
	"context"
	"os"
	"os/signal"
	"sync/atomic"
)

// interruptContext arms the termination signals for a stretch of the CLI's own
// work. The returned context is cancelled by the first one; stop unregisters
// the handler; raise ends this process the way that signal would have if it had
// never been caught, so a supervisor still sees the run as interrupted rather
// than failed. A second signal takes its default action, because the handler is
// unregistered as soon as the first arrives.
func interruptContext() (ctx context.Context, stop, raise func()) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, terminationSignals()...)
	var caught atomic.Pointer[os.Signal]
	go func() {
		select {
		case sig := <-ch:
			signal.Stop(ch)
			caught.Store(&sig)
			cancel()
		case <-ctx.Done():
		}
	}()
	stop = func() {
		signal.Stop(ch)
		cancel()
	}
	raise = func() {
		if sig := caught.Load(); sig != nil {
			reraise(*sig)
		}
	}
	return ctx, stop, raise
}
