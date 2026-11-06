//go:build linux || darwin

package wingd

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"

	"github.com/sparkwing-dev/sparkwing/internal/crontimer"
	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

func startUnmanagedLocalRunner(ctx context.Context, host crontimer.Host, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := localRunnerDirectory(host, id)
	if err != nil {
		return err
	}
	log, err := fssecure.OpenFile(filepath.Join(dir, "runner.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY)
	if err != nil {
		return err
	}
	defer log.Close()
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer stdin.Close()
	environment, err := localRunnerEnvironment(host)
	if err != nil {
		return err
	}
	args := localRunnerArguments(host, id)
	command := procenv.ControlCommand(environment, args[0], args[1:]...)
	command.Stdin, command.Stdout, command.Stderr = stdin, log, log
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return err
	}
	// safety: reaping the child never binds its lifetime to the controller's context.
	go func() {
		if err := command.Wait(); err != nil {
			slog.Warn("unmanaged local runner exited", "instance", id, "error", err)
		}
	}()
	return nil
}
