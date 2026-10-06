//go:build windows

package cache

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync/atomic"

	"golang.org/x/sys/windows"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

var gitJobSequence atomic.Uint64

func configureGitCommand(*exec.Cmd) {}

func gitCommandOutput(cmd *exec.Cmd, combined bool) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	if combined {
		cmd.Stderr = &stdout
	} else {
		cmd.Stderr = &stderr
	}
	job, err := procgroup.NewJob(`Local\sparkwing-cache-git-` + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatUint(gitJobSequence.Add(1), 10))
	if err != nil {
		return nil, fmt.Errorf("protect git process: %w", err)
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
		return nil, err
	}
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = job.Terminate()
		_ = cmd.Wait()
		return nil, fmt.Errorf("watch git process: %w", err)
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
	var exit *exec.ExitError
	if !combined && errors.As(err, &exit) {
		exit.Stderr = stderr.Bytes()
	}
	return stdout.Bytes(), err
}
