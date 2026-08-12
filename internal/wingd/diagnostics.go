package wingd

import (
	"fmt"
	"os"
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
//
// What it reads under the lock is counting only, never a ledger snapshot
// or any formatting: this runs on a daemon suspected of being stuck, so
// the hold has to be as short as counting a few maps.
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

// daemonLogSinks are the process handles whose writes reach the daemon
// log. A spawned daemon has both its stdout and its stderr pointed at
// d.log by the client that started it, and logs through stderr. Tests
// replace this with a handle of their own.
var daemonLogSinks = func() []*os.File { return []*os.File{os.Stdout, os.Stderr} }

// writeDiagnosticDump writes the daemon's counters and every goroutine
// stack to its log, rotating the log first when it is already over cap.
func (d *Daemon) writeDiagnosticDump() {
	d.rotateLogForDump()
	d.cfg.logf("diagnostics: %s", d.diagnosticSummary())
	d.cfg.logf("diagnostics: goroutine dump\n%s", dumpGoroutineStacks(diagnosticsStackBytes))
}

// rotateLogForDump rotates the daemon log once when it is over cap and
// re-points the daemon's output at the fresh file.
//
// Rotating at spawn is not enough for this writer. One dump appends up
// to 2MB, and the daemon it is asked of is by definition still running:
// a resident daemon can be asked for dozens over the weeks between
// restarts, and the spawn-time check never sees any of them. Rotating
// here uses the same helper and the same once-rotated .1 shape, so the
// two rotations cannot drift apart.
//
// Re-pointing the output is what makes the rotation mean anything. The
// daemon's log is a file descriptor it inherited, not a path it reopens,
// so a bare rename leaves it writing into d.log.1 -- d.log would be
// gone, the next rotation would find nothing to rename, and the file
// that keeps growing would be the one meant to be the archive.
//
// It acts only when the daemon's output really is that log file. A
// daemon started in a terminal writes to the terminal: renaming a log it
// is not writing, and redirecting an operator's console into a file,
// would both be wrong.
func (d *Daemon) rotateLogForDump() {
	path, err := LogPath(d.cfg.Home)
	if err != nil {
		return
	}
	var sinks []*os.File
	for _, s := range daemonLogSinks() {
		if s != nil && sameOpenFile(s, path) {
			sinks = append(sinks, s)
		}
	}
	if len(sinks) == 0 {
		return
	}
	rotated, err := RotateLogOverCap(d.cfg.Home)
	if err != nil || !rotated {
		return
	}
	restore := func() { _ = os.Rename(path+".1", path) }
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		restore()
		return
	}
	defer func() { _ = f.Close() }()
	for _, s := range sinks {
		if rerr := redirectFD(int(f.Fd()), int(s.Fd())); rerr != nil {
			// safety: a sink still on the renamed inode would write the
			// dump into the archive; put the log back where it was.
			restore()
			return
		}
	}
}

// sameOpenFile reports whether an open handle and a path name the same
// file, which is how the daemon tells "my log is d.log" from "my log is
// whatever terminal started me".
func sameOpenFile(f *os.File, path string) bool {
	open, err := f.Stat()
	if err != nil {
		return false
	}
	named, err := os.Stat(path)
	if err != nil {
		return false
	}
	return os.SameFile(open, named)
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
