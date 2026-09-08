//go:build !windows

package procgroup

import (
	"os"
	"os/signal"
	"syscall"
	"time"
)

const forwardedTerminationExitCode = 128 + int(syscall.SIGTERM)

func killOwnedGroup(group int) {
	_ = syscall.Kill(-group, syscall.SIGKILL)
}

func forwardTerminationToOwned() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM)
	go func() {
		<-ch
		KillOwned()
		signal.Reset(syscall.SIGTERM)
		_ = syscall.Kill(syscall.Getpid(), syscall.SIGTERM)
		// safety: the re-raised signal ends the process; this only runs if
		// something else still handles SIGTERM.
		time.Sleep(time.Second)
		os.Exit(forwardedTerminationExitCode)
	}()
}
