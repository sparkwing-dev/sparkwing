//go:build windows

package local

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func prepareNodeLiveness(_ *exec.Cmd) (func(), func(), error) {
	// safety: Windows has no ExtraFiles support. The parent's non-inherited,
	// kill-on-close job handle owns the child even when the dispatcher dies.
	return func() {}, func() {}, nil
}

type windowsNodeProcess struct {
	cmd    *exec.Cmd
	job    *procgroup.Job
	mu     sync.Mutex
	closed bool
	done   chan struct{}
	err    error
}

var nodeJobSequence atomic.Uint64

func startNodeProcess(cmd *exec.Cmd, logger *slog.Logger) (nodeProcess, error) {
	return startNodeProcessAt(cmd, logger, time.Now().UnixNano())
}

func startNodeProcessAt(cmd *exec.Cmd, logger *slog.Logger, timestamp int64) (nodeProcess, error) {
	// safety: Windows clock ticks can repeat; a shared job couples otherwise independent node cleanup.
	name := fmt.Sprintf(`Local\sparkwing-node-%d-%d-%d`, os.Getpid(), timestamp, nodeJobSequence.Add(1))
	job, err := procgroup.NewJob(name)
	if err != nil {
		return nil, err
	}
	if err := job.Start(cmd); err != nil {
		_ = job.Close()
		if cmd.Process != nil {
			_ = cmd.Wait()
		}
		return nil, err
	}
	p := &windowsNodeProcess{cmd: cmd, job: job, done: make(chan struct{})}
	go p.reap(logger)
	return p, nil
}

func (p *windowsNodeProcess) ID() int { return p.cmd.Process.Pid }

func (p *windowsNodeProcess) Finish(ctx context.Context, _ time.Duration) error {
	select {
	case <-p.done:
		return p.err
	case <-ctx.Done():
		return fmt.Errorf("%w: wait for Windows node: %w", procgroup.ErrCleanup, ctx.Err())
	}
}

func (p *windowsNodeProcess) Terminate(ctx context.Context, grace time.Duration) error {
	p.mu.Lock()
	var err error
	if !p.closed {
		err = p.job.Terminate()
	}
	p.mu.Unlock()
	if err != nil {
		return fmt.Errorf("%w: terminate Windows node: %w", procgroup.ErrCleanup, err)
	}
	return p.Finish(ctx, grace)
}

func (p *windowsNodeProcess) reap(logger *slog.Logger) {
	p.err = p.cmd.Wait()
	// safety: a leader exiting is not evidence that its descendants exited.
	// Keep ownership and capacity until the kernel reports the job empty.
	retry := 10 * time.Millisecond
	for {
		p.mu.Lock()
		active, err := p.job.ActiveProcesses()
		if err == nil && active == 0 {
			if closeErr := p.job.Close(); closeErr != nil {
				p.err = fmt.Errorf("%w: close Windows node job: %w", procgroup.ErrCleanup, closeErr)
			}
			p.closed = true
			p.mu.Unlock()
			close(p.done)
			return
		}
		if err == nil {
			err = p.job.Terminate()
		}
		p.mu.Unlock()
		if err != nil {
			logger.Error("Windows node cleanup failed; retaining ownership", "pid", p.ID(), "err", err)
		}
		time.Sleep(retry)
		if retry < time.Second {
			retry *= 2
		}
	}
}
