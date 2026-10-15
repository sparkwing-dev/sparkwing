package acceptance_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
)

func TestInitialAttemptW8Components(t *testing.T) {
	t.Run("runner", func(t *testing.T) {
		prepared := errors.New("preparation reached")
		host := runners.Hosting{
			ControllerURL: "https://controller.example", Lease: time.Minute, Interval: 10 * time.Second,
			NewController: runnerControllerFactoryW8(func(context.Context) (lease.Beat, error) { return lease.Beat{}, nil }),
			StartKeeper:   func(ctx context.Context, _ lease.Keeper) (context.Context, func()) { return ctx, func() {} },
			PrepareNode: func(_ context.Context, unit controllerwire.ClaimedUnit, _ controllerwire.Controller) (nodehost.NodeHost, nodehost.NodeInput, func() error, error) {
				if unit.Binding.Attempt != 0 {
					t.Fatal("runner manufactured an execution ordinal before Start")
				}
				return nil, nodehost.NodeInput{}, nil, prepared
			},
		}
		unit := runnerAuthorityAwardW8(capability.UnitNode)
		if err := host.Host(t.Context(), unit); !errors.Is(err, prepared) {
			t.Fatalf("unstarted award never reached host preparation: %v", err)
		}
	})
	t.Run("launcher", func(t *testing.T) {
		now, cfg, award := cloudFixture(launcher.CloudWork)
		award.Binding.Attempt = 0
		resources, err := launcher.BuildJob(t.Context(), now, cfg, award, &cloudBoundaryFake{})
		if err != nil {
			t.Fatal(err)
		}
		restored, err := launcher.ReadBootstrapAward(writeBootstrapW8(t, resources.BootstrapSecret.Data))
		if err != nil || restored.Binding.Attempt != 0 {
			t.Fatalf("unstarted Cloud award lost its ordinal: %v", err)
		}
	})
}
