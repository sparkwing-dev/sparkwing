//go:build windows

package wingd

// startDiagnostics is a no-op on Windows, which has no SIGUSR1.
func (d *Daemon) startDiagnostics(<-chan struct{}) {}
