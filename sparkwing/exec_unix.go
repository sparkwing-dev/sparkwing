//go:build unix

package sparkwing

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/execdiag"
	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func commandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	if policy, ok := execdiag.FromContext(ctx); ok && policy.Expired != nil {
		wrapped := []string{"-c", `ulimit -c 0; exec "$@"`, "sparkwing-exec", name}
		wrapped = append(wrapped, args...)
		return exec.CommandContext(ctx, "/bin/sh", wrapped...)
	}
	return exec.CommandContext(ctx, name, args...)
}

func configureProcessGroup(ctx context.Context, cmd *exec.Cmd, streamsDone <-chan struct{}) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	policy, diagnosticPolicy := execdiag.FromContext(ctx)
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if !diagnosticPolicy || policy.Expired == nil || !policy.Expired() || policy.EscalationLimit <= 0 {
			return killProcessGroup(-cmd.Process.Pid)
		}
		identity, err := procgroup.CaptureSession(cmd.Process.Pid)
		if err != nil {
			return killProcessGroup(-cmd.Process.Pid)
		}
		if err := procgroup.DiagnosticSession(identity); err != nil {
			return procgroup.KillSession(identity)
		}
		timer := time.NewTimer(policy.EscalationLimit)
		defer timer.Stop()
		select {
		case <-streamsDone:
		case <-timer.C:
		}
		return procgroup.KillSession(identity)
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
