//go:build linux

package cluster

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

func TestReviewRunnerClaimFailureDuringShutdown(t *testing.T) {
	if os.Getenv("SPARKWING_REVIEW_CLAIM_FAILURE") == "1" {
		command := os.Args[len(os.Args)-1]
		url, credentials := "https://controller.example", runnerCredentials(t)
		cli := RunnerCLI{ReadConfig: func(string) (userconfig.Config, error) {
			return userconfig.Config{Runner: &userconfig.RunnerConfig{Controller: &url, CredentialsDir: &credentials}}, nil
		}, Factory: func(ctx context.Context, _ RunnerOptions) (runners.RuntimeConfig, error) {
			loop := runners.Loop{
				Config:       runners.LoopConfig{Version: "v1.2.3", MaxConcurrent: 1},
				CheckVersion: func(context.Context, string) error { return nil },
				Controller: cliWire{claim: func(claimCtx context.Context, _ controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
					fmt.Fprintln(os.Stderr, "terminal claim failure reached before return")
					if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
						panic(err)
					}
					<-claimCtx.Done()
					return controllerwire.ClaimResponse{}, errors.New("terminal claim permission refused")
				}},
				Host: cliHost(func(context.Context, controllerwire.ClaimedUnit) error { return nil }),
			}
			return runners.RuntimeConfig{}, loop.Run(ctx)
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
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReviewRunnerClaimFailureDuringShutdown$", "--", command)
			child.Env = append(os.Environ(), "SPARKWING_REVIEW_CLAIM_FAILURE=1")
			output, err := child.CombinedOutput()
			if !bytes.Contains(output, []byte("terminal claim failure reached before return")) {
				t.Fatalf("claim was never exercised: %v %s", err, output)
			}
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 || !bytes.Contains(output, []byte("terminal claim permission refused")) {
				t.Fatalf("genuine terminal claim failure became successful shutdown: %v %s", err, output)
			}
		})
	}
}
