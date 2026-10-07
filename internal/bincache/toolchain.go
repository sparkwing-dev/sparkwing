package bincache

import (
	"context"
	"io"
	"os"
	"os/exec"

	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
)

// RunGo runs a `go` subcommand in dir, streaming its output to the caller's
// stdout and stderr, and stops the toolchain when ctx is cancelled.
func RunGo(ctx context.Context, dir string, args, env []string) error {
	env, err := gotoolchain.BuildEnv(ctx, dir, env, EffectiveOverlay(dir))
	if err != nil {
		return err
	}
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	var captured lockedBuffer
	cmd.Stderr = io.MultiWriter(os.Stderr, &captured)
	cmd.Env = env
	err = gotoolchain.Run(ctx, cmd)
	if err != nil {
		if explanation := gotoolchain.ExplainOutput(ctx, captured.String(), env); explanation != nil {
			return explanation
		}
	}
	return err
}
