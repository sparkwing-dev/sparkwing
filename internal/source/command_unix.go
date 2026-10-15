//go:build !windows

package source

import (
	"bytes"
	"context"
	"errors"
	"os/exec"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func commandOutput(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = nil
	cmd.Cancel = nil
	group, err := procgroup.Start(cmd)
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		err = group.Kill()
	case <-group.LeaderExited():
	}
	err = errors.Join(err, group.Finish(context.WithoutCancel(ctx), 0))
	return output.Bytes(), err
}
