//go:build !windows

package main

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func terminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
}

func reraise(sig os.Signal) {
	unix, ok := sig.(syscall.Signal)
	if !ok {
		return
	}
	signal.Reset(unix)
	if err := syscall.Kill(os.Getpid(), unix); err != nil {
		slog.Default().Debug("re-raise termination signal", "signal", unix, "err", err)
	}
}
