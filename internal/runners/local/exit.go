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

// exitInfo is how a node process ended, in the two terms that decide
// the outcome: the code it chose, or the signal that chose for it.
type exitInfo struct {
	code     int
	signal   os.Signal
	signaled bool

	// waitErr is retained for the case where no ProcessState survives
	// (a reap that failed): the outcome then rests on the error text.
	waitErr error
}

// exitVerdict is a synthesized outcome plus the row fields that record
// it.
type exitVerdict struct {
	result   runner.Result
	message  string
	reason   string
	exitCode *int
}

// classifyExit decides the outcome of a node process that exited
// without writing its own terminal row.
//
// Three cases carry real information:
//
//   - SIGKILL with no cancellation. Nothing in sparkwing sends an
//     unprompted SIGKILL, so the kernel did, and out-of-memory is by
//     far its commonest reason. The message says so and names the
//     node's peak RSS, which is the number an operator needs to size
//     the node. The recorded reason stays FailureUnknown: an OOM
//     reason should mean a cgroup or a kernel said OOM, and claiming
//     it from a signal alone would put unfalsifiable OOM verdicts in
//     the run history.
//   - A signal after cancellation. That is this runner's own SIGTERM
//     (or the SIGKILL that followed the grace period), so the node was
//     cancelled, not failed.
//   - Any other non-zero exit. The node's process failed and its code
//     is the only detail available.
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

// exitInfoFrom reads how the process ended. A missing ProcessState
// means the reap itself failed, which leaves only the wait error.
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
