package wingd

import (
	"fmt"
	"runtime"
)

const diagnosticsStackBytes = 2 << 20

func (d *Daemon) diagnosticSummary() string {
	if !d.mu.TryLock() {
		return fmt.Sprintf("goroutines=%d counts=unavailable (daemon mutex held) version=%s",
			runtime.NumGoroutine(), d.cfg.Version)
	}
	conns := len(d.conns)
	guards := len(d.guards)
	leases := len(d.leaseRun)
	reattach := len(d.reattachWait)
	holders, waiters := 0, 0
	for c := range d.conns {
		switch c.role {
		case roleHolder:
			holders++
		case roleWaiter:
			waiters++
		}
	}
	d.mu.Unlock()
	return fmt.Sprintf("goroutines=%d conns=%d holders=%d waiters=%d leases=%d guards=%d awaiting-reattach=%d version=%s",
		runtime.NumGoroutine(), conns, holders, waiters, leases, guards, reattach, d.cfg.Version)
}

func (d *Daemon) writeDiagnosticDump() {
	if _, err := RotateLogOverCap(d.cfg.Home); err != nil {
		d.cfg.logf("diagnostics: could not rotate the daemon log: %v", err)
	}
	d.cfg.logf("diagnostics: %s", d.diagnosticSummary())
	d.cfg.logf("diagnostics: goroutine dump\n%s", dumpGoroutineStacks(diagnosticsStackBytes))
}

func dumpGoroutineStacks(maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = diagnosticsStackBytes
	}
	buf := make([]byte, maxBytes)
	n := runtime.Stack(buf, true)
	return string(buf[:n])
}
