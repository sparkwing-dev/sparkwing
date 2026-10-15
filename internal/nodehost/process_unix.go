//go:build unix

package nodehost

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func startSDKProcess(cmd *exec.Cmd, requests, frames *os.File) (sessionProcess, error) {
	cmd.ExtraFiles = []*os.File{requests, frames}
	g, err := procgroup.StartSession(cmd)
	if err != nil {
		return nil, err
	}
	return groupProcess{Group: g, cmd: cmd}, nil
}

type groupProcess struct {
	*procgroup.Group
	cmd *exec.Cmd
}

func (p groupProcess) Finish(ctx context.Context, grace time.Duration) error {
	return p.reapLeader(p.Group.Finish(ctx, grace))
}

func (p groupProcess) Terminate(ctx context.Context, grace time.Duration) error {
	return p.reapLeader(p.Group.Terminate(ctx, grace))
}

// safety: the group reaps its leader only once every descendant is gone, so
// a descendant stuck past the cleanup deadline, or a failed session signal,
// would leave the leader running or a zombie. Its pid is still unreaped here,
// so killing it by pid cannot hit another process.
func (p groupProcess) reapLeader(err error) error {
	if err == nil || p.Reaped() {
		return err
	}
	if killErr := p.cmd.Process.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
		err = errors.Join(err, killErr)
	}
	timer := time.NewTimer(reapTimeout)
	defer timer.Stop()
	select {
	case <-p.LeaderExited():
		var exit *exec.ExitError
		if waitErr := p.cmd.Wait(); waitErr != nil && !errors.As(waitErr, &exit) {
			err = errors.Join(err, waitErr)
		}
	case <-timer.C:
	}
	return err
}
