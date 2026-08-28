//go:build unix

package sparkwing

import (
	"os/exec"
	"syscall"
	"time"
)

func configureProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// safety: negative pid signals the whole process group (Setpgid above
		// made the child its group leader), reaching forked grandchildren.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

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
