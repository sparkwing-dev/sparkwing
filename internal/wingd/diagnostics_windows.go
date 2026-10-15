//go:build windows

package wingd

func (d *Daemon) startDiagnostics(<-chan struct{}) <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}
