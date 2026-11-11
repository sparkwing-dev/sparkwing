//go:build !windows

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
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

func TestRunnerSignalExit(t *testing.T) {
	if os.Getenv("SPARKWING_TEST_SIGNAL_HELPER") == "1" {
		command, mode := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
		url, credentials := "https://controller.example", runnerCredentials(t)
		cli := RunnerCLI{ReadConfig: func(string) (userconfig.Config, error) {
			return userconfig.Config{Runner: &userconfig.RunnerConfig{Controller: &url, CredentialsDir: &credentials}}, nil
		}, Factory: func(ctx context.Context, _ RunnerOptions) (runners.RuntimeConfig, error) {
			if mode != "loop" {
				fmt.Println("ready")
				<-ctx.Done()
				if mode == "failure" {
					return runners.RuntimeConfig{}, errors.Join(context.Cause(ctx), errors.New("real runtime failure"))
				}
				return runners.RuntimeConfig{}, fmt.Errorf("startup interrupted: %w", context.Cause(ctx))
			}
			return cliRuntime(func(ctx context.Context, _ controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				fmt.Println("ready")
				<-ctx.Done()
				return controllerwire.ClaimResponse{}, context.Cause(ctx)
			}, cliHost(func(context.Context, controllerwire.ClaimedUnit) error { return nil }), cliVersions{index: cliVersionIndex(t)}), nil
		}}
		os.Args = []string{"sparkwing-runner", command, "--config=/fixture/config.yaml"}
		if command == "warm" {
			os.Args = []string{"sparkwing-runner", command, "--controller=" + url, "--credentials-dir=" + credentials}
		}
		mainWithRuntime("v1.2.3", clusterRuntime{Runner: cli})
		return
	}
	for _, command := range []string{"start", "warm"} {
		for _, mode := range []string{"loop", "startup", "failure"} {
			t.Run(command+"/"+mode, func(t *testing.T) {
				cmd := exec.Command(os.Args[0], "-test.run=^TestRunnerSignalExit$", "--", command, mode)
				cmd.Env = append(os.Environ(), "SPARKWING_TEST_SIGNAL_HELPER=1")
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				stdout, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- cmd.Wait() }()
				waited := false
				t.Cleanup(func() {
					if !waited {
						_ = cmd.Process.Kill()
						<-done
					}
				})
				ready := make(chan string, 1)
				go func() { text, _ := bufio.NewReader(stdout).ReadString('\n'); ready <- text }()
				select {
				case line := <-ready:
					if line != "ready\n" {
						t.Fatalf("runner did not reach runtime: %q", line)
					}
				case <-time.After(20 * time.Second):
					t.Fatal("runner never became ready")
				}
				if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-done:
					waited = true
					if mode == "failure" {
						var exit *exec.ExitError
						if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), "real runtime failure") {
							t.Fatalf("real failure lost: %v %s", err, &stderr)
						}
					} else if err != nil {
						t.Fatalf("clean SIGTERM exited unsuccessfully: %v %s", err, &stderr)
					}
				case <-time.After(20 * time.Second):
					_ = cmd.Process.Kill()
					<-done
					t.Fatal("runner did not finish after SIGTERM")
				}
			})
		}
	}
}
