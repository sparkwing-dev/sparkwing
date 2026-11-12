//go:build windows

package procgroup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync/atomic"
)

var commandJobSequence atomic.Uint64

// CommandOutput captures output while RunCommand owns the command's process tree.
func CommandOutput(ctx context.Context, cmd *exec.Cmd, combined bool) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	if combined {
		cmd.Stderr = &stdout
	} else {
		cmd.Stderr = &stderr
	}
	err := RunCommand(ctx, cmd)
	var exit *exec.ExitError
	if !combined && errors.As(err, &exit) {
		exit.Stderr = stderr.Bytes()
	}
	return stdout.Bytes(), err
}

// RunCommand owns the command's job and preserves deaths preceding cancellation.
func RunCommand(ctx context.Context, cmd *exec.Cmd) (result error) {
	job, err := NewJob(`Local\sparkwing-command-` + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatUint(commandJobSequence.Add(1), 10))
	if err != nil {
		return fmt.Errorf("protect command process: %w", err)
	}
	defer func() {
		if closeErr := job.Close(); closeErr != nil {
			result = errors.Join(result, closeErr)
		}
	}()
	return runOwnedCommand(ctx, cmd, func() error { return job.Start(cmd) }, job.Terminate, job.Terminate)
}
