package wingd

import (
	"fmt"
	"runtime"
)

// diagnosticsStackBytes caps one goroutine dump. A spinning daemon can
// hold thousands of goroutines and the dump goes into an operational log
// file, so the record has to be bounded even when the process is not.
const diagnosticsStackBytes = 2 << 20

// diagnosticSummary is a one-line count of what the daemon is holding:
// enough to tell a busy daemon from a stuck one before reading stacks.
// It never waits for the daemon mutex -- a dump requested because the
// daemon is wedged must not wedge on the thing being investigated -- and
// reports the contended mutex instead, which is itself the finding.
func (d *Daemon) diagnosticSummary() string {
	if !d.mu.TryLock() {
		return fmt.Sprintf("goroutines=%d counts=unavailable (daemon mutex held) version=%s",
			runtime.NumGoroutine(), d.cfg.Version)
	}
	conns := len(d.conns)
	guards := len(d.guards)
	reattach := len(d.reattachWait)
	snap := d.ledger.Snapshot()
	d.mu.Unlock()
	return fmt.Sprintf("goroutines=%d conns=%d guards=%d leases=%d waiters=%d awaiting-reattach=%d version=%s",
		runtime.NumGoroutine(), conns, guards, len(snap.Leases), len(snap.Waiters), reattach, d.cfg.Version)
}

// dumpGoroutineStacks returns every live goroutine's stack, truncated to
// maxBytes.
func dumpGoroutineStacks(maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = diagnosticsStackBytes
	}
	buf := make([]byte, maxBytes)
	n := runtime.Stack(buf, true)
	return string(buf[:n])
}
