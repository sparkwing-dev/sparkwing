package sparkwing

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/sdkserve"
)

// hostExecCmd hands the command to the host, which owns its process tree, output
// masking and cleanup. A logged command's ExecResult keeps at most the last
// nodeproto.ExecTailBytes of each stream; the whole output is in the job log.
func hostExecCmd(ctx context.Context, run sdkserve.HostExecFunc, display, name string, args []string, dir string, extraEnv map[string]string) (ExecResult, error) {
	env := os.Environ()
	for key, value := range extraEnv {
		env = append(env, key+"="+value)
	}
	argv := append([]string{name}, args...)
	// Resolve as exec.Command did here, against this process's PATH, so lookups keep their meaning.
	path := name
	if filepath.Base(name) == name {
		resolved, err := exec.LookPath(name)
		if err != nil {
			return ExecResult{Command: display}, &ExecError{Command: display, ExitCode: ExitNotStarted, Cause: err}
		}
		path = resolved
	}
	reply, err := run(ctx, nodeproto.ExecRequest{Step: stepFromContext(ctx), Path: path, Argv: argv, Dir: dir, Env: env, Capture: isSilent(ctx)})
	if err != nil {
		return ExecResult{Command: display}, &ExecError{Command: display, ExitCode: ExitNotStarted, Cause: err}
	}
	done := reply.Done
	if done.StartError != "" {
		cause := errors.New(done.StartError)
		if done.NotFound {
			cause = &exec.Error{Name: name, Err: exec.ErrNotFound}
		}
		return ExecResult{Command: display}, &ExecError{Command: display, ExitCode: ExitNotStarted, Cause: cause}
	}
	res := ExecResult{Command: display, Stdout: string(reply.Stdout), Stderr: string(reply.Stderr), ExitCode: done.ExitCode}
	Debug(ctx, "exec done: exit=%d bytes_stdout=%d bytes_stderr=%d", res.ExitCode, len(res.Stdout), len(res.Stderr))
	if done.ExitCode == 0 && !done.Signaled {
		return res, nil
	}
	failure := &ExecError{Command: display, Stdout: res.Stdout, Stderr: res.Stderr, ExitCode: res.ExitCode, Cause: fmt.Errorf("exit status %d", done.ExitCode)}
	if done.Signaled {
		failure.Cause = errors.New("signal: killed")
		failure.Terminated = "signal"
		if ctx.Err() != nil {
			failure.Terminated = "cancellation"
			if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
				failure.Terminated = "timeout"
				failure.Cause = errors.Join(failure.Cause, context.Cause(ctx))
				if deadline, ok := ctx.Deadline(); ok {
					failure.Cause = fmt.Errorf("deadline %s: %w", deadline.UTC().Format(time.RFC3339Nano), failure.Cause)
				}
			}
		}
	}
	return res, failure
}
