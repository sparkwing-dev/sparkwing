//go:build !windows

package local

import (
	"log/slog"
	"os"
	"os/exec"
	"strconv"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func prepareNodeLiveness(cmd *exec.Cmd) (func(), func(), error) {
	read, write, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	cmd.ExtraFiles = []*os.File{read}
	// safety: only the process that actually passes fd 3 grants the child authority to read it.
	cmd.Env = append(cmd.Env, ParentLivenessFDEnv+"="+strconv.Itoa(ParentLivenessFD))
	return func() { _ = read.Close() }, func() { _ = write.Close() }, nil
}

func startNodeProcess(cmd *exec.Cmd, _ *slog.Logger) (nodeProcess, error) {
	return procgroup.Start(cmd)
}
