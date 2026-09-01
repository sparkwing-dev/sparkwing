//go:build unix

package local

import (
	"os"
	"runtime"
	"syscall"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
)

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

func maxRSSBytes(maxrss int64) int64 {
	if maxrss <= 0 {
		return 0
	}
	if runtime.GOOS == "linux" {
		return maxrss * 1024
	}
	return maxrss
}

func terminationSignal(ps *os.ProcessState) (os.Signal, bool) {
	ws, ok := ps.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return nil, false
	}
	return ws.Signal(), true
}

func isKill(sig os.Signal) bool { return sig == syscall.SIGKILL }
