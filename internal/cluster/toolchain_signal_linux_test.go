//go:build linux

package cluster

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

func TestRunnerToolchainTermination(t *testing.T) {
	if os.Getenv("SPARKWING_TEST_TOOLCHAIN_SIGNAL") == "1" {
		command, mode := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
		url, credentials := "https://controller.example", runnerCredentials(t)
		cli := RunnerCLI{ReadConfig: func(string) (userconfig.Config, error) {
			return userconfig.Config{Runner: &userconfig.RunnerConfig{Controller: &url, CredentialsDir: &credentials}}, nil
		}, Factory: func(ctx context.Context, _ RunnerOptions) (runners.RuntimeConfig, error) {
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
			child := exec.Command("/bin/sh", "-c", script)
			if mode == "command-context" {
				child = exec.CommandContext(ctx, "/bin/sh", "-c", script)
			}
			child.Stdout = writer
			defer func() {
				if child.Process != nil {
					_ = child.Process.Kill()
				}
			}()
			inherited := &runnerToolchainContext{Context: ctx, command: child, ready: bufio.NewReader(ready), mode: mode}
			err = gotoolchain.Run(inherited, child) //nolint:contextcheck // This inherited context orders external termination before the runner signal.
			return runners.RuntimeConfig{}, err
		}}
		os.Args = []string{"sparkwing-runner", command, "--config=/fixture/config.yaml"}
		if command == "warm" {
			os.Args = []string{"sparkwing-runner", command, "--controller=" + url, "--credentials-dir=" + credentials}
		}
		mainWithRuntime("v1.2.3", clusterRuntime{Runner: cli})
		return
	}
	for _, command := range []string{"start", "warm"} {
		for _, mode := range []string{"owner-term", "owner-kill", "external-term", "external-kill", "command-context"} {
			t.Run(command+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunnerToolchainTermination$", "--", command, mode)
				cmd.Env = append(os.Environ(), "SPARKWING_TEST_TOOLCHAIN_SIGNAL=1")
				output, err := cmd.CombinedOutput()
				if !bytes.Contains(output, []byte("toolchain shutdown ordering reached")) {
					t.Fatalf("fixture did not reach shutdown: %v %s", err, output)
				}
				if strings.HasPrefix(mode, "external-") {
					var exit *exec.ExitError
					if !errors.As(err, &exit) || exit.ExitCode() != 1 || !bytes.Contains(output, []byte("signal:")) {
						t.Fatalf("external termination hidden: %v %s", err, output)
					}
				} else if err != nil {
					t.Fatalf("owner termination became failure: %v %s", err, output)
				}
			})
		}
	}
}

type runnerToolchainContext struct {
	context.Context
	command *exec.Cmd
	ready   *bufio.Reader
	mode    string
	once    sync.Once
}

func (c *runnerToolchainContext) Done() <-chan struct{} {
	c.once.Do(func() {
		line, err := c.ready.ReadString('\n')
		if err != nil || line != "ready\n" {
			panic(fmt.Sprintf("child readiness: %q %v", line, err))
		}
		if strings.HasPrefix(c.mode, "external-") {
			sig := syscall.SIGTERM
			if c.mode == "external-kill" {
				sig = syscall.SIGKILL
			}
			if err := syscall.Kill(c.command.Process.Pid, sig); err != nil {
				panic(err)
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
		fmt.Fprintln(os.Stderr, "toolchain shutdown ordering reached")
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			panic(err)
		}
		<-c.Context.Done()
	})
	return c.Context.Done()
}
