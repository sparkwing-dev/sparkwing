//go:build unix

package local

import (
	"os"
	"runtime"
	"syscall"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
)

// usageFrom reads the kernel's own accounting for the finished
// process. It is exact where sampling is not: a node that lived under
// one sampling interval is invisible to the sampler but still shows
// its true CPU time and peak RSS here. PR3 folds these figures onto
// the node row.
func usageFrom(ps *os.ProcessState) *runner.ResourceUsage {
	if ps == nil {
		return nil
	}
	ru, ok := ps.SysUsage().(*syscall.Rusage)
	if !ok || ru == nil {
		return nil
	}
	return &runner.ResourceUsage{
		CPUTime:     time.Duration(ru.Utime.Nano()) + time.Duration(ru.Stime.Nano()),
		MaxRSSBytes: maxRSSBytes(ru.Maxrss),
	}
}

// maxRSSBytes normalizes ru_maxrss, which Linux reports in kilobytes
// and the BSDs (macOS included) report in bytes.
func maxRSSBytes(maxrss int64) int64 {
	if maxrss <= 0 {
		return 0
	}
	if runtime.GOOS == "linux" {
		return maxrss * 1024
	}
	return maxrss
}

// terminationSignal reports the signal that killed the process, if
// one did.
func terminationSignal(ps *os.ProcessState) (os.Signal, bool) {
	ws, ok := ps.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return nil, false
	}
	return ws.Signal(), true
}

func isKill(sig os.Signal) bool { return sig == syscall.SIGKILL }
