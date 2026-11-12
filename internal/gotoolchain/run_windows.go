//go:build windows

package gotoolchain

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

func runToolchain(ctx context.Context, cmd *exec.Cmd) (result error) {
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = time.Second
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	process, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return errors.Join(fmt.Errorf("watch toolchain child: %w", err), cmd.Process.Kill(), cmd.Wait())
	}
	defer func() { result = errors.Join(result, windows.CloseHandle(process)) }()
	var reap sync.Mutex
	reaped := false
	done := make(chan error, 1)
	go func() {
		state, observeErr := windows.WaitForSingleObject(process, windows.INFINITE)
		if observeErr == nil && state != windows.WAIT_OBJECT_0 {
			observeErr = fmt.Errorf("toolchain child wait returned %d", state)
		}
		reap.Lock()
		waitErr := cmd.Wait()
		reaped = true
		reap.Unlock()
		if observeErr != nil {
			waitErr = errors.Join(observeErr, waitErr)
		}
		done <- waitErr
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	reap.Lock()
	killed := false
	var killErr error
	if !reaped {
		state, observeErr := windows.WaitForSingleObject(process, 0)
		switch {
		case observeErr != nil:
			killErr = observeErr
		case state == uint32(windows.WAIT_TIMEOUT):
			// safety: the retained handle fixes identity; a death between this check and termination is the shutdown race.
			killErr = windows.TerminateProcess(process, 1)
			killed = killErr == nil
		case state != windows.WAIT_OBJECT_0:
			killErr = fmt.Errorf("toolchain child state returned %d", state)
		}
	}
	reap.Unlock()
	waitErr := <-done
	if killed {
		if exit, ok := waitErr.(*exec.ExitError); ok && exit.ExitCode() == 1 { //nolint:errorlint // Joined failures must retain every error.
			waitErr = ctx.Err()
		}
	}
	return errors.Join(ctx.Err(), killErr, waitErr)
}
