//go:build windows

package procgroup

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"

	"golang.org/x/sys/windows"
)

type commandOutcome struct{ waited, other error }

// RunProcess owns cancellation of a leader without changing its descendant policy.
func RunProcess(ctx context.Context, cmd *exec.Cmd) error {
	return runOwnedCommand(ctx, cmd, func() error { return cmd.Start() }, func() error { return cmd.Process.Kill() }, nil)
}

// Run owns job cancellation while leaving post-wait cleanup to the caller.
func (j *Job) Run(ctx context.Context, cmd *exec.Cmd) error {
	return runOwnedCommand(ctx, cmd, func() error { return j.Start(cmd) }, j.Terminate, nil)
}

func runOwnedCommand(ctx context.Context, cmd *exec.Cmd, start, terminate, afterExit func() error) (result error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	drainDelay := cmd.WaitDelay
	// safety: only this owner may terminate; exec's context watchdog must not race its attribution.
	cmd.Cancel = nil
	cmd.WaitDelay = 0
	startErr := start()
	cmd.WaitDelay = drainDelay
	if startErr != nil {
		if cmd.Process != nil {
			return errors.Join(startErr, terminate(), cmd.Wait())
		}
		return startErr
	}
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return errors.Join(fmt.Errorf("watch command process: %w", err), terminate(), cmd.Wait())
	}
	defer func() {
		if closeErr := windows.CloseHandle(process); closeErr != nil {
			result = errors.Join(result, closeErr)
		}
	}()
	var reap sync.Mutex
	reaped := false
	done := make(chan commandOutcome, 1)
	go func() {
		state, observeErr := windows.WaitForSingleObject(process, windows.INFINITE)
		if observeErr == nil && state != windows.WAIT_OBJECT_0 {
			observeErr = fmt.Errorf("command wait returned %d", state)
		}
		var cleanupErr error
		if afterExit != nil {
			cleanupErr = afterExit()
		}
		reap.Lock()
		waitErr := cmd.Wait()
		reaped = true
		reap.Unlock()
		done <- commandOutcome{waited: waitErr, other: errors.Join(observeErr, cleanupErr)}
	}()
	select {
	case outcome := <-done:
		if outcome.other == nil {
			return outcome.waited
		}
		return errors.Join(outcome.waited, outcome.other)
	case <-ctx.Done():
	}
	reap.Lock()
	owned := false
	var terminateErr error
	if !reaped {
		state, observeErr := windows.WaitForSingleObject(process, 0)
		switch {
		case observeErr != nil:
			terminateErr = observeErr
		case state == uint32(windows.WAIT_TIMEOUT):
			// safety: the retained handle fixes identity. External death between observation and termination
			// is indistinguishable from our kill during shutdown.
			terminateErr = terminate()
			owned = terminateErr == nil
		case state != windows.WAIT_OBJECT_0:
			terminateErr = fmt.Errorf("command state returned %d", state)
		}
	}
	reap.Unlock()
	outcome := <-done
	if owned {
		if exit, ok := outcome.waited.(*exec.ExitError); ok && exit.ExitCode() == 1 { //nolint:errorlint // Joined observation and cleanup errors are kept separately.
			outcome.waited = ctx.Err()
		}
	}
	return errors.Join(ctx.Err(), terminateErr, outcome.waited, outcome.other)
}
