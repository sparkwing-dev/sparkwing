//go:build windows

package supervise

import "os"

func signalTerminate(p *os.Process) error {
	return signalKill(p)
}

func signalKill(p *os.Process) error {
	return p.Kill()
}
