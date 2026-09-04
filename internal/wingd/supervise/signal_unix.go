//go:build !windows

package supervise

import (
	"os"
	"syscall"
)

func signalTerminate(p *os.Process) error {
	return p.Signal(syscall.SIGTERM)
}

func signalKill(p *os.Process) error {
	return p.Kill()
}
