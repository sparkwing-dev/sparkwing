//go:build !windows

package bincache

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

const childTerminationGrace = 5 * time.Second

func execChild(bin string, args []string, env []string) error {
	return execChildWith(bin, args, env, nil)
}

func execChildWith(bin string, args []string, env []string, started func(*exec.Cmd)) error {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	cmd := exec.Command(bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr, cmd.Env = os.Stdin, os.Stdout, os.Stderr, env
	if err := cmd.Start(); err != nil {
		signal.Stop(signals)
		return err
	}
	if started != nil {
		started(cmd)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var deadline <-chan time.Time
	for {
		select {
		case received := <-signals:
			_ = cmd.Process.Signal(received)
			if deadline == nil {
				deadline = time.After(childTerminationGrace)
			}
		case <-deadline:
			_ = cmd.Process.Kill()
			deadline = nil
		case err := <-done:
			signal.Stop(signals)
			if err == nil {
				os.Exit(0) //nolint:forbidigo // foreground wrapper preserves the pipeline's exit status
			}
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				return err
			}
			status, ok := exitErr.Sys().(syscall.WaitStatus)
			if ok && status.Signaled() {
				childSignal := status.Signal()
				signal.Reset(childSignal)
				_ = syscall.Kill(os.Getpid(), childSignal)
				os.Exit(128 + int(childSignal)) //nolint:forbidigo // fallback if self-signal is refused
			}
			os.Exit(exitErr.ExitCode()) //nolint:forbidigo // foreground wrapper preserves the pipeline's exit status
		}
	}
}
