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

// safety: ordinary process failures must not match our job-termination status.
const ownerTerminationCode uint32 = 0x53574f43

type commandOutcome struct{ waited, other error }

// Run owns job cancellation while leaving post-wait cleanup to the caller.
func (j *Job) Run(ctx context.Context, cmd *exec.Cmd) error {
	return runOwnedCommand(ctx, cmd, func() error { return j.Start(cmd) }, func(windows.Handle) error { return j.TerminateWithCode(ownerTerminationCode) }, nil)
}

func runOwnedCommand(ctx context.Context, cmd *exec.Cmd, start func() error, terminate func(windows.Handle) error, afterExit func() error) (result error) {
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
			return errors.Join(startErr, terminate(0), cmd.Wait())
		}
		return startErr
	}
	process, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return errors.Join(fmt.Errorf("watch command process: %w", err), terminate(0), cmd.Wait())
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
			terminateErr = terminate(process)
			owned = terminateErr == nil
		case state != windows.WAIT_OBJECT_0:
			terminateErr = fmt.Errorf("command state returned %d", state)
		}
	}
	reap.Unlock()
	outcome := <-done
	if owned {
		if exit, ok := outcome.waited.(*exec.ExitError); ok && exit.ExitCode() == int(ownerTerminationCode) { //nolint:errorlint // Joined observation and cleanup errors are kept separately.
			outcome.waited = ctx.Err()
		}
	}
	return errors.Join(ctx.Err(), terminateErr, outcome.waited, outcome.other)
}
