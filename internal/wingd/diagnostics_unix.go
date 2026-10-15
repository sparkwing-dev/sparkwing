//go:build !windows

package wingd

import (
	"os"
	"os/signal"
	"syscall"
)

func (d *Daemon) startDiagnostics(ctxDone <-chan struct{}) <-chan struct{} {
	ch := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(ch, syscall.SIGUSR1)
	go func() {
		defer close(done)
		defer signal.Stop(ch)
		for {
			select {
			case <-ctxDone:
				return
			case <-d.quit:
				return
			case <-ch:
				d.writeDiagnosticDump()
			}
		}
	}()
	return done
}
