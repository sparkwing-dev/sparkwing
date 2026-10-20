package cluster

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/sparkwing-dev/sparkwing/internal/credentials"
	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
)

const agentTokenCredential = "agent-token"

// LauncherFactory binds the shared controller transport and the verified Cloud
// boundary to the launcher. The bearer remains in the transport, never a Job.
type LauncherFactory func(context.Context, launcher.CloudJobConfig, func(context.Context) (string, error)) (*launcher.Launcher, error)

// RunLaunchCLI accepts the final Job configuration and refuses to start without
// both an award transport and a boundary that establishes isolation at runtime.
func RunLaunchCLI(ctx context.Context, args []string, factory LauncherFactory) error {
	fs, err := commandFlags("launch", args)
	if err != nil {
		return parsedHelp(err)
	}
	cfg := launcher.CloudJobConfig{
		ControllerURL: textFlag(fs, "controller"), Namespace: "sparkwing-jobs",
		RunnerImage: textFlag(fs, "image"), DockerImage: textFlag(fs, "docker-image"),
	}
	if fs.Changed("namespace") {
		cfg.Namespace = textFlag(fs, "namespace")
	}
	cfg.Labels, _ = fs.GetStringSlice("label")
	var poll time.Duration
	if fs.Changed("poll") {
		poll, err = time.ParseDuration(textFlag(fs, "poll"))
		if err != nil || poll <= 0 {
			return errors.New("--poll must be a positive duration")
		}
	}
	cpuQuantity, err := resource.ParseQuantity(textFlag(fs, "cpu-ceiling"))
	if err != nil {
		return fmt.Errorf("--cpu-ceiling: %w", err)
	}
	cfg.CPUCeiling = cpuQuantity.AsApproximateFloat64()
	memoryQuantity, err := resource.ParseQuantity(textFlag(fs, "memory-ceiling"))
	if err != nil {
		return fmt.Errorf("--memory-ceiling: %w", err)
	}
	if memoryQuantity.Sign() <= 0 || memoryQuantity.Cmp(*resource.NewQuantity(math.MaxInt64, resource.DecimalSI)) > 0 {
		return errors.New("--memory-ceiling must be positive and fit in a signed 64-bit byte count")
	}
	cfg.MemoryCeiling = memoryQuantity.Value()
	if err := cfg.Validate(); err != nil {
		return err
	}
	if factory == nil {
		return errors.New("launcher runtime is unavailable: wire the controller Cloud awards and verified node/network boundary before launching Jobs")
	}
	creds, err := credentials.Open(textFlag(fs, credentials.FlagName))
	if err != nil {
		return err
	}
	token, err := creds.Read(agentTokenCredential)
	if err != nil {
		return err
	}
	if token == "" {
		return errors.New("launcher requires a credential in " + agentTokenCredential)
	}
	bearer := func(ctx context.Context) (string, error) {
		if err := context.Cause(ctx); err != nil {
			return "", err
		}
		token, err := creds.Read(agentTokenCredential)
		if err == nil && token == "" {
			err = errors.New("launcher requires a credential in " + agentTokenCredential)
		}
		return token, err
	}
	runner, err := factory(ctx, cfg, bearer)
	if err != nil {
		return err
	}
	if runner == nil {
		return errors.New("launcher runtime returned no launcher")
	}
	if fs.Changed("poll") {
		runner.Poll = poll
	}
	return runner.Run(ctx)
}
