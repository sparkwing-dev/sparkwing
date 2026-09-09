package bincache

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

const toolchainTerminationTimeout = 2*procgroup.DefaultTerminationGrace + 30*time.Second

// runToolchain runs cmd and waits for it. Where the platform can own a process
// group, cancelling ctx terminates the whole group, so the compilers and linker
// a `go build` has already spawned stop with the caller instead of running on
// under a new parent. Where it cannot, cancellation reaches the `go` process
// alone.
func runToolchain(ctx context.Context, cmd *exec.Cmd) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := procgroup.Supported(); err != nil {
		return runToolchainUngrouped(ctx, cmd)
	}
	group, err := procgroup.Start(cmd)
	if err != nil {
		return err
	}
	waited := make(chan error, 1)
	go func() { waited <- group.Finish(context.WithoutCancel(ctx), procgroup.DefaultTerminationGrace) }()

	select {
	case waitErr := <-waited:
		return waitErr
	case <-ctx.Done():
	}

	termCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), toolchainTerminationTimeout)
	defer cancel()
	if err := group.Terminate(termCtx, procgroup.DefaultTerminationGrace); err != nil && !errors.Is(err, procgroup.ErrCleanup) {
		slog.Default().Debug("toolchain group terminate", "group", group.ID(), "err", err)
	}
	select {
	case <-waited:
	case <-termCtx.Done():
		// safety: the group outlived its own termination deadline. Returning
		// leaves it to the caller's report rather than parking the CLI on a
		// wait with nothing on stdout.
	}
	return ctx.Err()
}

func runToolchainUngrouped(ctx context.Context, cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	if err := cmd.Process.Kill(); err != nil {
		slog.Default().Debug("toolchain kill", "pid", cmd.Process.Pid, "err", err)
	}
	<-done
	return ctx.Err()
}

// RunGo runs a `go` subcommand in dir, streaming its output to the caller's
// stdout and stderr, and stops the toolchain when ctx is cancelled.
func RunGo(ctx context.Context, dir string, args, env []string) error {
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = env
	return runToolchain(ctx, cmd)
}
