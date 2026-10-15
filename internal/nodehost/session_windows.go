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

// hack: Windows has neither descriptor numbering nor process sessions, so the pipes travel as inherited
// handles named in argv and a kill-on-close job stands in for the session.
func startProcess(cmd *exec.Cmd, requests, frames *os.File) (sessionProcess, error) {
	handles := []syscall.Handle{syscall.Handle(requests.Fd()), syscall.Handle(frames.Fd())}
	for _, h := range handles {
		if err := windows.SetHandleInformation(windows.Handle(h), windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
			return nil, fmt.Errorf("inherit protocol pipe: %w", err)
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{AdditionalInheritedHandles: handles}
	cmd.Args = append(cmd.Args, protofd.HandlesArg, fmt.Sprintf("%d,%d", handles[0], handles[1]))
	// safety: an unnamed job has no handle but the host's, so no other
	// process can keep it, and its members, alive past Close.
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
	p := &jobProcess{job: job, exited: make(chan struct{})}
	go func() {
		// safety: the exit status is read from cmd.ProcessState; only a failed wait is an error.
		var exit *exec.ExitError
		if err := cmd.Wait(); !errors.As(err, &exit) {
			p.waitErr = err
		}
		close(p.exited)
	}()
	return p, nil
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
