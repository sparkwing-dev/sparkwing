//go:build !windows

package gitcmd

import (
	"bytes"
	"context"
	"errors"
	"os/exec"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

// Output runs and joins a Git process and its descendants.
func Output(ctx context.Context, cmd *exec.Cmd, combined bool) ([]byte, error) {
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = nil
	if combined {
		cmd.Stderr = &output
	}
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
