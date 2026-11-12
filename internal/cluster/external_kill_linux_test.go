//go:build linux

package cluster

import (
	"bytes"
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

	"github.com/sparkwing-dev/sparkwing/internal/gitcmd"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

type signalAfterExternalKill struct {
	context.Context
	command *exec.Cmd
	once    sync.Once
}

func (c *signalAfterExternalKill) Done() <-chan struct{} {
	c.once.Do(func() {
		if err := c.command.Process.Kill(); err != nil {
			panic(err)
		}
		var info unix.Siginfo
		for {
			err := unix.Waitid(unix.P_PID, c.command.Process.Pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			if err != nil {
				panic(err)
			}
			break
		}
		fmt.Fprintln(os.Stderr, "EXTERNAL SIGKILL confirmed before runner SIGTERM")
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			panic(err)
		}
		<-c.Context.Done()
	})
	return c.Context.Done()
}

func TestRunnerExternalKillIsFailure(t *testing.T) {
	if os.Getenv("SPARKWING_TEST_EXTERNAL_KILL") == "1" {
		command := os.Args[len(os.Args)-1]
		url, credentials := "https://controller.example", runnerCredentials(t)
		cli := RunnerCLI{ReadConfig: func(string) (userconfig.Config, error) {
			return userconfig.Config{Runner: &userconfig.RunnerConfig{Controller: &url, CredentialsDir: &credentials}}, nil
		}, Factory: func(ctx context.Context, _ RunnerOptions) (runners.RuntimeConfig, error) {
			child := exec.Command("/bin/sleep", "30")
			_, err := gitcmd.Output(&signalAfterExternalKill{Context: ctx, command: child}, child, false) //nolint:contextcheck // The inherited context orders external death before shutdown.
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
		t.Run(command, func(t *testing.T) {
			clean, failed := 0, 0
			for i := 0; i < 16; i++ {
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunnerExternalKillIsFailure$", "--", command)
				child.Env = append(os.Environ(), "SPARKWING_TEST_EXTERNAL_KILL=1")
				output, err := child.CombinedOutput()
				cancel()
				if !bytes.Contains(output, []byte("EXTERNAL SIGKILL confirmed before runner SIGTERM")) {
					t.Fatalf("probe never reached ordering: %v %s", err, output)
				}
				if err == nil {
					clean++
				} else {
					var exit *exec.ExitError
					if !errors.As(err, &exit) || exit.ExitCode() != 1 {
						t.Fatalf("unexpected failure: %v %s", err, output)
					}
					failed++
				}
			}
			t.Logf("external failure before SIGTERM: clean_exit_0=%d failure_exit_1=%d", clean, failed)
			if clean != 0 {
				t.Fatalf("genuine external SIGKILL was hidden by clean exit in %d/16 cases", clean)
			}
		})
	}
}
