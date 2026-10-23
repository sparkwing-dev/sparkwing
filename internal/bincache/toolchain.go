package bincache

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

// runGo executes Go with captured inputs and cancellation of its child processes.
func runGo(ctx context.Context, dir string, args []string, options CompileOptions) error {
	if options.Environment == nil || options.Diagnostics == nil {
		return errors.New("Go execution requires a captured environment and diagnostic writer")
	}
	tool, err := gotoolchain.Executable(options.Environment)
	if err != nil {
		return err
	}
	var noticeErr error
	ctx = gotoolchain.WithSession(ctx, options.Environment, func(message string) {
		_, err := fmt.Fprintln(options.Diagnostics, message)
		noticeErr = errors.Join(noticeErr, err)
	})
	env, err := gotoolchain.BuildEnv(ctx, dir, options.Environment, EffectiveOverlay(dir, options.Environment))
	if err := errors.Join(err, noticeErr); err != nil {
		return err
	}
	cmd := procenv.ControlCommand(env, tool, args...)
	cmd.Dir = dir
	var captured lockedBuffer
	cmd.Stdout = io.MultiWriter(options.Diagnostics, &captured)
	cmd.Stderr = cmd.Stdout

	err = gotoolchain.Run(ctx, cmd)
	if err != nil {
		if explanation := gotoolchain.ExplainOutput(ctx, captured.String(), env); explanation != nil {
			return explanation
		}
	}
	return err
}
