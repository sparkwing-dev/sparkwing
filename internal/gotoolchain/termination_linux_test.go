//go:build linux

package gotoolchain

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestRunAttributesTermination(t *testing.T) {
	for _, mode := range []string{"owner-term", "owner-kill", "external-term", "external-kill", "exit23", "command-context"} {
		t.Run(mode, func(t *testing.T) {
			base, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			ctx, stop := context.WithCancel(base)
			defer stop()
			ready, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer ready.Close()
			defer writer.Close()
			script := "/bin/sleep 60 &\nprintf 'ready\\n'\nwait\n"
			if mode == "owner-kill" {
				script = "trap '' TERM\n" + script
			}
			if mode == "exit23" {
				script = "/bin/sleep 60 &\nprintf 'ready\\n'\nexit 23\n"
			}
			command := exec.Command("/bin/sh", "-c", script)
			if mode == "command-context" {
				command = exec.CommandContext(ctx, "/bin/sh", "-c", script)
			}
			command.Stdout = writer
			defer func() {
				if command.Process != nil {
					_ = command.Process.Kill()
				}
			}()
			inherited := &terminationContext{Context: ctx, command: command, ready: bufio.NewReader(ready), mode: mode, cancel: stop}
			err = Run(inherited, command)
			var exit *exec.ExitError
			genuine := mode == "external-term" || mode == "external-kill" || mode == "exit23"
			if errors.As(err, &exit) != genuine || !genuine && !errors.Is(err, context.Canceled) {
				t.Fatalf("%s termination attribution: %v", mode, err)
			}
			if genuine {
				status, ok := exit.Sys().(syscall.WaitStatus)
				if !ok {
					t.Fatalf("wait status: %v", err)
				}
				switch mode {
				case "external-term":
					if !status.Signaled() || status.Signal() != syscall.SIGTERM {
						t.Fatal(err)
					}
				case "external-kill":
					if !status.Signaled() || status.Signal() != syscall.SIGKILL {
						t.Fatal(err)
					}
				case "exit23":
					if exit.ExitCode() != 23 {
						t.Fatal(err)
					}
				}
			}
		})
	}
}

type terminationContext struct {
	context.Context
	command *exec.Cmd
	ready   *bufio.Reader
	mode    string
	cancel  context.CancelFunc
	once    sync.Once
}

func (c *terminationContext) Done() <-chan struct{} {
	c.once.Do(func() {
		line, err := c.ready.ReadString('\n')
		if err != nil || line != "ready\n" {
			panic(fmt.Sprintf("child readiness: %q %v", line, err))
		}
		if c.mode == "external-term" || c.mode == "external-kill" || c.mode == "exit23" {
			if c.mode != "exit23" {
				sig := syscall.SIGTERM
				if c.mode == "external-kill" {
					sig = syscall.SIGKILL
				}
				if err := syscall.Kill(c.command.Process.Pid, sig); err != nil {
					panic(err)
				}
			}
			// safety: WNOWAIT leaves status to the owner; ECHILD means the owner already reaped the child.
			for {
				var info unix.Siginfo
				err := unix.Waitid(unix.P_PID, c.command.Process.Pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
				if errors.Is(err, syscall.EINTR) {
					continue
				}
				if err != nil && !errors.Is(err, syscall.ECHILD) {
					panic(err)
				}
				break
			}
		}
		c.cancel()
	})
	return c.Context.Done()
}
