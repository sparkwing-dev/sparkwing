//go:build !windows

package wingd

import (
	"os"
	"os/signal"
	"syscall"
)

// startDiagnostics makes SIGUSR1 write the daemon's counters and every
// goroutine stack to its log. A daemon burning CPU is only diagnosable
// while it is still running, and the alternative an operator reaches for
// -- killing it -- destroys the evidence, so the dump has to be
// obtainable from outside without stopping the process.
func (d *Daemon) startDiagnostics(ctxDone <-chan struct{}) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR1)
	go func() {
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
}
