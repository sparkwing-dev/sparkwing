package cluster

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/signal"
	"syscall"

	flag "github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/sparkwing-dev/sparkwing/internal/credentials"
	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
)

// LauncherFactory binds the shared controller transport and the verified Cloud
// boundary to the launcher. The bearer remains in the transport, never a Job.
type LauncherFactory func(context.Context, launcher.CloudJobConfig, string) (*launcher.Launcher, error)

// RunLaunchCLI accepts the final Job configuration and refuses to start without
// both an award transport and a boundary that establishes isolation at runtime.
func RunLaunchCLI(ctx context.Context, args []string, factory LauncherFactory) error {
	fs := flag.NewFlagSet("launch", flag.ContinueOnError)
	var cfg launcher.CloudJobConfig
	fs.StringVar(&cfg.ControllerURL, "controller", "", "controller URL the launcher and its Jobs reach")
	fs.StringVar(&cfg.Namespace, "namespace", "sparkwing-jobs", "namespace the Jobs run in")
	fs.StringVar(&cfg.RunnerImage, "image", "", "runner image pinned by digest")
	fs.StringVar(&cfg.DockerImage, "docker-image", "", "Docker-in-Docker image pinned by digest")
	cpu := fs.String("cpu-ceiling", "", "most cores one Job may request")
	memory := fs.String("memory-ceiling", "", "most memory one Job may request")
	credentialsDir := fs.String(credentials.FlagName, "", "directory holding the launcher credential")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	cpuQuantity, err := resource.ParseQuantity(*cpu)
	if err != nil {
		return fmt.Errorf("--cpu-ceiling: %w", err)
	}
	cfg.CPUCeiling = cpuQuantity.AsApproximateFloat64()
	memoryQuantity, err := resource.ParseQuantity(*memory)
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
	creds, err := credentials.Open(*credentialsDir)
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
	runner, err := factory(ctx, cfg, token)
	if err != nil {
		return err
	}
	if runner == nil {
		return errors.New("launcher runtime returned no launcher")
	}
	return runner.Run(ctx)
}

func runLaunchCLI(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return RunLaunchCLI(ctx, args, nil)
}
