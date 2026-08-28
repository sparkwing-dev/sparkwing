package local

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/runner"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

type exitInfo struct {
	code     int
	signal   os.Signal
	signaled bool

	waitErr error
}

type exitVerdict struct {
	result   runner.Result
	message  string
	reason   string
	exitCode *int
}

func classifyExit(nodeID string, info exitInfo, cancelled bool, usage *runner.ResourceUsage) exitVerdict {
	if cancelled {
		// safety: wraps context.Canceled because the dispatcher classifies a
		// cancelled node by unwrapping to it.
		err := fmt.Errorf("node %s: its process was terminated: %w", nodeID, context.Canceled)
		return exitVerdict{
			result:  runner.Result{Outcome: sparkwing.Cancelled, Err: err},
			message: err.Error(),
			reason:  store.FailureUnknown,
		}
	}

	if info.signaled && isKill(info.signal) {
		msg := fmt.Sprintf(
			"node %s: its process was killed (SIGKILL) with no cancellation in flight; "+
				"the kernel out-of-memory killer is the usual cause%s",
			nodeID, peakRSSSuffix(usage))
		return exitVerdict{
			result:  runner.Result{Outcome: sparkwing.Failed, Err: errors.New(msg)},
			message: msg,
			reason:  store.FailureUnknown,
		}
	}

	if info.signaled {
		msg := fmt.Sprintf("node %s: its process was killed by signal %v", nodeID, info.signal)
		return exitVerdict{
			result:  runner.Result{Outcome: sparkwing.Failed, Err: errors.New(msg)},
			message: msg,
			reason:  store.FailureUnknown,
		}
	}

	if info.code == 0 {
		msg := fmt.Sprintf("node %s: its process exited 0 without writing a terminal outcome", nodeID)
		return exitVerdict{
			result:  runner.Result{Outcome: sparkwing.Failed, Err: errors.New(msg)},
			message: msg,
			reason:  store.FailureUnknown,
		}
	}

	code := info.code
	msg := fmt.Sprintf("node %s: its process exited %d without writing a terminal outcome", nodeID, code)
	if info.waitErr != nil {
		msg = fmt.Sprintf("node %s: its process exited %d without writing a terminal outcome (%v)",
			nodeID, code, info.waitErr)
	}
	return exitVerdict{
		result:   runner.Result{Outcome: sparkwing.Failed, Err: errors.New(msg)},
		message:  msg,
		reason:   store.FailureUnknown,
		exitCode: &code,
	}
}

func peakRSSSuffix(usage *runner.ResourceUsage) string {
	if usage == nil || usage.MaxRSSBytes <= 0 {
		return ""
	}
	return fmt.Sprintf(" (peak RSS %s)", humanBytes(usage.MaxRSSBytes))
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func exitInfoFrom(ps *os.ProcessState, waitErr error) exitInfo {
	if ps == nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) && ee.ProcessState != nil {
			ps = ee.ProcessState
		}
	}
	if ps == nil {
		return exitInfo{code: -1, waitErr: waitErr}
	}
	info := exitInfo{code: ps.ExitCode(), waitErr: waitErr}
	if sig, ok := terminationSignal(ps); ok {
		info.signal = sig
		info.signaled = true
	}
	return info
}
