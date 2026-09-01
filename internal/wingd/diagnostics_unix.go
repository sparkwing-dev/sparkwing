//go:build !windows

package wingd

import (
	"os"
	"os/signal"
	"syscall"
)

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
