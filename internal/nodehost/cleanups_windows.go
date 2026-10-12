package nodehost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync/atomic"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

var cleanupJobSequence atomic.Uint64

// safety: the cleanup runs in its own kill-on-close job, so every process it
// starts ends with it instead of outliving its time limit.
func runCleanupProcess(ctx context.Context, cmd *exec.Cmd) (err error) {
	job, err := procgroup.NewJob(fmt.Sprintf(`Local\sparkwing-cleanup-%d-%d`, os.Getpid(), cleanupJobSequence.Add(1)))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, job.Close()) }()
	if err := job.Start(cmd); err != nil {
		if cmd.Process != nil {
			err = errors.Join(err, cmd.Wait())
		}
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-ctx.Done():
		err = errors.Join(job.Terminate(), <-done)
	}
	return errors.Join(err, job.Terminate())
}
