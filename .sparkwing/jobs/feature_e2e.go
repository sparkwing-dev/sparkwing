package jobs

import (
	"context"
	"fmt"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// FeatureE2E exercises controller scheduling in isolated local processes.
type FeatureE2E struct{ sparkwing.Base }

// FeatureE2EArgs select a scenario and independent repetitions.
type FeatureE2EArgs struct {
	Scenario string `flag:"scenario" desc:"Run placement, retry_failed, child_cancellation, or all (default)."`
	Repeat   int    `flag:"repeat" desc:"Number of independent scenario runs. Default: 1."`
}

func (FeatureE2E) ShortHelp() string {
	return "Exercise placement, failed-only retry and child cancellation locally"
}

func (FeatureE2E) Plan(_ context.Context, plan *sparkwing.Plan, in FeatureE2EArgs, _ sparkwing.RunContext) error {
	selection := "^TestFeatureE2E$"
	switch in.Scenario {
	case "", "all":
	case "placement", "retry_failed", "child_cancellation":
		selection += "/^" + in.Scenario + "$"
	default:
		return fmt.Errorf("scenario must be all, placement, retry_failed, or child_cancellation")
	}
	if in.Repeat == 0 {
		in.Repeat = 1
	}
	if in.Repeat < 1 {
		return fmt.Errorf("repeat must be positive")
	}
	sparkwing.Job(plan, "scenarios", func(ctx context.Context) error {
		_, err := sparkwing.Bash(ctx, fmt.Sprintf("GOWORK=auto GOMAXPROCS=2 go test -p 1 -count=%d -v -timeout 25m ./tests/acceptance -run '%s'", in.Repeat, selection)).Run()
		return err
	}).Cores(2)
	return nil
}

func init() {
	sparkwing.Register("FeatureE2E", func() sparkwing.Pipeline[FeatureE2EArgs] { return FeatureE2E{} })
}
