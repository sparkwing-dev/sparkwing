//go:build windows

package orchestrator

import (
	"errors"
	"log/slog"
	"os/exec"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

type windowsAssistedChildProcess struct {
	cmd     *exec.Cmd
	job     *procgroup.Job
	done    chan struct{}
	waitErr error
}

func startAssistedChildProcess(cmd *exec.Cmd, logger *slog.Logger) (assistedChildProcess, error) {
	job, err := procgroup.NewJob("")
	if err != nil {
		return nil, err
	}
	if err := job.Start(cmd); err != nil {
		if cmd.Process != nil {
			settleAssistedChildJob(job, cmd, func() { _ = cmd.Wait() }, logger)
		}
		return nil, errors.Join(err, job.Close())
	}
	child := &windowsAssistedChildProcess{cmd: cmd, job: job, done: make(chan struct{})}
	go func() {
		child.waitErr = cmd.Wait()
		close(child.done)
	}()
	return child, nil
}

func (p *windowsAssistedChildProcess) exited() <-chan struct{} { return p.done }

func (p *windowsAssistedChildProcess) finish(logger *slog.Logger) error {
	return p.settle(logger)
}

func (p *windowsAssistedChildProcess) terminate(logger *slog.Logger) error {
	return p.settle(logger)
}

func (p *windowsAssistedChildProcess) settle(logger *slog.Logger) error {
	settleAssistedChildJob(p.job, p.cmd, func() { <-p.done }, logger)
	return errors.Join(p.waitErr, p.job.Close())
}

func settleAssistedChildJob(job *procgroup.Job, cmd *exec.Cmd, wait func(), logger *slog.Logger) {
	failedAssistedChildCleanup{
		inspect: func() (bool, error) {
			active, err := job.ActiveProcesses()
			return active == 0, err
		},
		terminate:     job.Terminate,
		wait:          wait,
		sleep:         time.Sleep,
		processID:     cmd.Process.Pid,
		boundary:      "job_object",
		inspectAction: "query_job",
		stopAction:    "terminate_job",
	}.settle(logger)
}
