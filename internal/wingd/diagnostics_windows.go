//go:build windows

package wingd

import "errors"

// startDiagnostics is a no-op on Windows, which has no SIGUSR1.
func (d *Daemon) startDiagnostics(<-chan struct{}) {}

// redirectFD has no Windows implementation. Nothing calls it there --
// the only caller is the dump path, which SIGUSR1 drives -- but the
// rotation code is shared, so the symbol has to exist.
func redirectFD(int, int) error {
	return errors.New("wingd: descriptor redirection is not supported on windows")
}
