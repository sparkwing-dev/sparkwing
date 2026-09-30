//go:build windows

package supervise

import "os"

func signalTerminate(p *os.Process) error {
	return signalKill(p)
}

func signalKill(p *os.Process) error {
	return p.Kill()
}

func signalDump(*os.Process) error { return os.ErrInvalid }
