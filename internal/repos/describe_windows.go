//go:build windows

package repos

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/internal/protofd"
)

var describeJobs atomic.Uint64

type processTree struct{ job *procgroup.Job }

// hack: Windows has no descriptor numbering or process groups, so the pipes
// travel as inherited handles that protofd.HandlesEnv names and a
// kill-on-close job holds the tree.
func startTree(cmd *exec.Cmd, requests, frames *os.File) (*processTree, error) {
	handles := []syscall.Handle{syscall.Handle(requests.Fd()), syscall.Handle(frames.Fd())}
	for _, h := range handles {
		if err := syscall.SetHandleInformation(h, syscall.HANDLE_FLAG_INHERIT, syscall.HANDLE_FLAG_INHERIT); err != nil {
			return nil, fmt.Errorf("inherit protocol pipe: %w", err)
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{AdditionalInheritedHandles: handles}
	cmd.Env = append(cmd.Environ(), fmt.Sprintf("%s=%d,%d", protofd.HandlesEnv, handles[0], handles[1]))
	job, err := procgroup.NewJob(fmt.Sprintf(`Local\sparkwing-describe-%d-%d`, os.Getpid(), describeJobs.Add(1)))
	if err != nil {
		return nil, err
	}
	if err := job.Start(cmd); err != nil {
		return nil, errors.Join(err, job.Close())
	}
	return &processTree{job: job}, nil
}

func (t *processTree) kill() {
	_ = t.job.Terminate() //nolint:errcheck // a job whose members exited has nothing left to end
}

func (t *processTree) release() {
	_ = t.job.Close() //nolint:errcheck // closing ends any member still running; there is no recovery to make
}
