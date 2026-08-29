//go:build unix

package sparkwing

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/execdiag"
)

func configureProcessGroup(ctx context.Context, cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	policy, diagnosticPolicy := execdiag.FromContext(ctx)
	if diagnosticPolicy && policy.EscalationLimit > 0 {
		cmd.WaitDelay = policy.EscalationLimit
	}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		group := -cmd.Process.Pid
		if !diagnosticPolicy || policy.Expired == nil || !policy.Expired() || policy.EscalationLimit <= 0 {
			return killProcessGroup(group)
		}
		if err := syscall.Kill(group, syscall.SIGQUIT); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return nil
			}
			return killProcessGroup(group)
		}
		return nil
	}
}

func killProcessGroup(group int) error {
	err := syscall.Kill(group, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
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
