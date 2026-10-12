//go:build windows

package procgroup

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync/atomic"

	"golang.org/x/sys/windows"
)

var commandJobSequence atomic.Uint64

// CommandOutput captures output while RunCommand owns the command's process tree.
func CommandOutput(cmd *exec.Cmd, combined bool) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	if combined {
		cmd.Stderr = &stdout
	} else {
		cmd.Stderr = &stderr
	}
	err := RunCommand(cmd)
	var exit *exec.ExitError
	if !combined && errors.As(err, &exit) {
		exit.Stderr = stderr.Bytes()
	}
	return stdout.Bytes(), err
}

// RunCommand owns the command's process tree until its leader exits or is cancelled.
func RunCommand(cmd *exec.Cmd) error {
	job, err := NewJob(`Local\sparkwing-command-` + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatUint(commandJobSequence.Add(1), 10))
	if err != nil {
		return fmt.Errorf("protect command process: %w", err)
	}
	defer func() { _ = job.Close() }()
	started := make(chan struct{})
	if cmd.Cancel != nil {
		cmd.Cancel = func() error {
			// safety: cancellation must wait until the suspended leader is assigned to its job.
			<-started
			if cmd.Process == nil {
				return os.ErrProcessDone
			}
			process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(cmd.Process.Pid))
			if err != nil {
				_ = job.Terminate()
				return err
			}
			defer windows.CloseHandle(process)
			state, err := windows.WaitForSingleObject(process, 0)
			if err != nil {
				_ = job.Terminate()
				return err
			}
			if err := job.Terminate(); err != nil {
				return err
			}
			if state == windows.WAIT_OBJECT_0 {
				return os.ErrProcessDone
			}
			return nil
		}
	}
	err = job.Start(cmd)
	close(started)
	if err != nil {
		if cmd.Process != nil {
			_ = cmd.Wait()
		}
		return err
	}
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = job.Terminate()
		_ = cmd.Wait()
		return fmt.Errorf("watch command process: %w", err)
	}
	defer windows.CloseHandle(process)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_, _ = windows.WaitForSingleObject(process, windows.INFINITE)
		// safety: surviving helpers must release inherited pipes before Cmd.Wait can finish.
		_ = job.Terminate()
	}()
	err = cmd.Wait()
	<-finished
	return err
}
