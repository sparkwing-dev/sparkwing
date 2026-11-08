//go:build windows

package sparkwing

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/execdiag"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

func executeCommand(ctx context.Context, name string, args []string, dir string, extraEnv map[string]string) (ExecResult, error) {
	display := renderCommand(name, args)
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(WorkDir(), dir)
	}
	if info, statErr := os.Stat(dir); statErr != nil {
		return ExecResult{Command: display}, &ExecError{
			Command:  display,
			ExitCode: ExitNotStarted,
			Cause:    fmt.Errorf("dir %q: %w", dir, statErr),
		}
	} else if !info.IsDir() {
		return ExecResult{Command: display}, &ExecError{
			Command:  display,
			ExitCode: ExitNotStarted,
			Cause:    fmt.Errorf("dir %q: not a directory", dir),
		}
	}

	var environment []string
	if len(extraEnv) > 0 {
		environment = os.Environ()
		for k, v := range extraEnv {
			environment = append(environment, k+"="+v)
		}
	}
	cmd := commandContext(ctx, environment, name, args...)
	cmd.Dir = dir
	streamsDone := make(chan struct{})
	configureProcessGroup(ctx, cmd, streamsDone)

	logger := loggerFromContext(ctx)
	logger.Emit(recordEnvelope(ctx, LogRecord{
		TS:    time.Now(),
		Level: "info",
		JobID: nodeFromContext(ctx),
		Event: "exec_start",
		Msg:   "$ " + display,
	}))
	Debug(ctx, "exec: %s (dir=%s)", display, dir)
	if len(extraEnv) > 0 {
		Debug(ctx, "exec env: %s", formatEnvDiff(extraEnv))
	}

	// safety: own the pipes so Cmd.Wait cannot close them before reader
	// goroutines drain a fast child's output.
	outR, outW, err := os.Pipe()
	if err != nil {
		return ExecResult{Command: display}, &ExecError{Command: display, ExitCode: ExitNotStarted, Cause: err}
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		closeFiles(outR, outW)
		return ExecResult{Command: display}, &ExecError{Command: display, ExitCode: ExitNotStarted, Cause: err}
	}
	cmd.Stdout, cmd.Stderr = outW, errW

	startedAt := time.Now()
	job, err := startStepCommand(cmd, display)
	if err != nil {
		closeFiles(outR, outW, errR, errW)
		return ExecResult{Command: display}, &ExecError{Command: display, ExitCode: ExitNotStarted, Cause: err}
	}
	// safety: the child owns the write ends now; dropping the parent's copies
	// is what lets the readers ever see EOF.
	closeFiles(outW, errW)
	unrecord := recordStepSession(ctx, cmd, job)

	var outBuf, errBuf strings.Builder
	limiter := newDiagnosticOutputLimiter(ctx)
	var wg sync.WaitGroup
	wg.Add(2)
	commandID := commandStreamSequence.Add(1)
	go streamChunks(ctx, &wg, outR, "info", logger, &outBuf, limiter, fmt.Sprintf("exec-%d-stdout", commandID))
	go streamChunks(ctx, &wg, errR, "info", logger, &errBuf, limiter, fmt.Sprintf("exec-%d-stderr", commandID))
	go func() {
		wg.Wait()
		close(streamsDone)
	}()

	waitErr := cmd.Wait()
	unrecord()
	job.close()
	wall := time.Since(startedAt)
	// safety: report at reap before draining so a sampler tick cannot charge the
	// same RUSAGE_CHILDREN delta again during the drain window.
	emitCommandResources(ctx, cmd, wall)

	drainStreams(&wg, outR, errR)

	res := ExecResult{
		Command:  display,
		Stdout:   outBuf.String(),
		Stderr:   errBuf.String(),
		ExitCode: cmd.ProcessState.ExitCode(),
	}
	Debug(ctx, "exec done: exit=%d bytes_stdout=%d bytes_stderr=%d",
		res.ExitCode, len(res.Stdout), len(res.Stderr))
	if waitErr != nil {
		var ee *exec.ExitError
		if errors.As(waitErr, &ee) {
			failure := &ExecError{
				Command:    display,
				Stdout:     res.Stdout,
				Stderr:     res.Stderr,
				ExitCode:   res.ExitCode,
				Cause:      waitErr,
				Terminated: terminationReason(ctx, ee, job.wasCancelled()),
			}
			if failure.Terminated == "timeout" {
				failure.Cause = errors.Join(waitErr, context.Cause(ctx))
				if deadline, ok := ctx.Deadline(); ok {
					failure.Cause = fmt.Errorf("deadline %s: %w", deadline.UTC().Format(time.RFC3339Nano), failure.Cause)
				}
			}
			return res, failure
		}
		return res, &ExecError{
			Command:  display,
			ExitCode: ExitNotStarted,
			Cause:    waitErr,
		}
	}
	return res, nil
}

func terminationReason(ctx context.Context, ee *exec.ExitError, cancelled bool) string {
	if ee.Exited() && !cancelled {
		return ""
	}
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
			return "timeout"
		}
		return "cancellation"
	}
	return "signal"
}

func emitCommandResources(ctx context.Context, cmd *exec.Cmd, wall time.Duration) {
	cpu, maxRSS, ok := commandResourceUsage(cmd)
	if !ok {
		return
	}
	reportResource(ctx, resourceSample{CPUMillicores: amortizedMillicores(cpu, wall), MemoryBytes: maxRSS, CPUTime: cpu})
}

func amortizedMillicores(cpu, wall time.Duration) int64 {
	if wall <= 0 {
		return 0
	}
	millicores := int64(cpu.Seconds() / wall.Seconds() * 1000.0)
	if millicores < 0 {
		return 0
	}
	return millicores
}

const streamDrainGrace = 500 * time.Millisecond

func drainStreams(wg *sync.WaitGroup, pipes ...*os.File) {
	drained := make(chan struct{})
	go func() {
		wg.Wait()
		close(drained)
	}()
	timer := time.NewTimer(streamDrainGrace)
	defer timer.Stop()
	select {
	case <-drained:
	case <-timer.C:
		closeFiles(pipes...)
		<-drained
	}
}

func closeFiles(files ...*os.File) {
	for _, f := range files {
		_ = f.Close()
	}
}

var commandStreamSequence atomic.Uint64

func streamChunks(ctx context.Context, wg *sync.WaitGroup, r io.ReadCloser, level string, logger Logger, buf *strings.Builder, limiter *diagnosticOutputLimiter, streamID string) {
	defer wg.Done()
	defer r.Close()
	node := nodeFromContext(ctx)
	silent := isSilent(ctx)
	chunk := make([]byte, 32<<10)
	for {
		n, err := r.Read(chunk)
		if n > 0 {
			raw := append([]byte(nil), chunk[:n]...)
			if !silent {
				logger.Emit(recordEnvelope(ctx, LogRecord{TS: time.Now(), Level: level, JobID: node, Event: "exec_line", streamID: streamID, streamData: raw}))
			}
			if limiter != nil {
				raw = limiter.capture(raw)
			}
			_, _ = buf.Write(raw)
		}
		if err != nil {
			break
		}
	}
	if !silent {
		logger.Emit(recordEnvelope(ctx, LogRecord{TS: time.Now(), Level: level, JobID: node, Event: "exec_line", streamID: streamID, streamEnd: true}))
	}
}

const diagnosticTruncationMarker = "...[diagnostic output truncated]"

type diagnosticOutputLimiter struct {
	mu        sync.Mutex
	policy    execdiag.Policy
	remaining int
	marked    bool
}

func newDiagnosticOutputLimiter(ctx context.Context) *diagnosticOutputLimiter {
	policy, ok := execdiag.FromContext(ctx)
	if !ok || policy.Expired == nil || policy.OutputLimit <= 0 {
		return nil
	}
	return &diagnosticOutputLimiter{policy: policy, remaining: policy.OutputLimit}
}

func (l *diagnosticOutputLimiter) capture(raw []byte) []byte {
	if !l.policy.Expired() {
		return raw
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.marked {
		return nil
	}
	markerBytes := len(diagnosticTruncationMarker)
	if len(raw)+markerBytes <= l.remaining {
		l.remaining -= len(raw)
		return raw
	}
	l.marked = true
	if markerBytes > l.remaining {
		return nil
	}
	l.remaining -= markerBytes
	return []byte(diagnosticTruncationMarker)
}

func processCommandContext(ctx context.Context, environment []string, name string, args ...string) *exec.Cmd {
	if environment == nil {
		return procenv.HostCommandContext(ctx, name, args...)
	}
	return procenv.ControlCommandContext(ctx, environment, name, args...)
}
