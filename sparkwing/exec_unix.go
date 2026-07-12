//go:build unix

package sparkwing

import (
	"os/exec"
	"syscall"
	"time"
)

// configureProcessGroup puts the command in its own process group and
// rewrites cancellation to signal the whole group. A shell step or a tool
// that forks (bash pipelines, `make -j`, a test runner) leaves the direct
// child's descendants running when only the child is signalled; killing the
// negative pgid tears the entire subtree down on node cancellation.
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

// commandResourceUsage extracts the finished command's CPU time and peak
// resident memory from its wait4 rusage. The rusage aggregates the whole
// reaped subtree, so a command that forks is measured in full. Returns
// false when the platform did not populate a rusage.
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
