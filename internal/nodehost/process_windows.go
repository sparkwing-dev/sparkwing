//go:build windows

package nodehost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/windows"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/internal/protofd"
)

func startSDKProcess(cmd *exec.Cmd, requests, frames *os.File) (sessionProcess, error) {
	handles := []syscall.Handle{syscall.Handle(requests.Fd()), syscall.Handle(frames.Fd())}
	for _, handle := range handles {
		if err := windows.SetHandleInformation(windows.Handle(handle), windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
			return nil, err
		}
		defer func() { _ = windows.SetHandleInformation(windows.Handle(handle), windows.HANDLE_FLAG_INHERIT, 0) }()
	}
	cmd.Args = append(cmd.Args, protofd.HandlesArg, fmt.Sprintf("%d,%d", handles[0], handles[1]))
	cmd.SysProcAttr = &syscall.SysProcAttr{AdditionalInheritedHandles: handles}
	job, err := procgroup.NewJob("")
	if err != nil {
		return nil, err
	}
	if err := job.Start(cmd); err != nil {
		if cmd.Process != nil {
			err = errors.Join(err, cmd.Process.Release())
		}
		return nil, errors.Join(err, job.Close())
	}
	process := &jobProcess{job: job, exited: make(chan struct{})}
	go func() {
		var exit *exec.ExitError
		if err := cmd.Wait(); !errors.As(err, &exit) {
			process.waitErr = err
		}
		close(process.exited)
	}()
	return process, nil
}

type jobProcess struct {
	job     *procgroup.Job
	exited  chan struct{}
	waitErr error
}

func (p *jobProcess) LeaderExited() <-chan struct{} { return p.exited }

func (p *jobProcess) Finish(ctx context.Context, _ time.Duration) error {
	select {
	case <-p.exited:
		return errors.Join(p.waitErr, p.job.Terminate(), p.job.Close())
	case <-ctx.Done():
		return errors.Join(fmt.Errorf("%w: wait for sdk process: %w", procgroup.ErrCleanup, ctx.Err()),
			p.job.Terminate(), p.job.Close())
	}
}

// safety: Windows has no SIGTERM to grant grace to, so the job ends at once.
func (p *jobProcess) Terminate(ctx context.Context, grace time.Duration) error {
	if err := p.job.Terminate(); err != nil {
		return fmt.Errorf("%w: terminate sdk job: %w", procgroup.ErrCleanup, err)
	}
	return p.Finish(ctx, grace)
}
