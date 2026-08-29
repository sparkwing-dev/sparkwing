//go:build unix

package sparkwing

import (
	"os/exec"
	"syscall"
	"time"
)

func commandResourceUsage(cmd *exec.Cmd) (cpu time.Duration, maxRSSBytes int64, ok bool) {
	if cmd.ProcessState == nil {
		return 0, 0, false
	}
	ru, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage)
	if !ok || ru == nil {
		return 0, 0, false
	}
	cpu = time.Duration(ru.Utime.Nano()) + time.Duration(ru.Stime.Nano())
	return cpu, maxRSSToBytes(ru.Maxrss), true
}
