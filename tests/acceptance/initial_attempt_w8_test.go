package acceptance_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodehost"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/lease"

	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
)

func TestInitialAttemptW8Components(t *testing.T) {
	t.Run("runner", func(t *testing.T) {
		reached := errors.New("native binding reached")
		host := runners.Hosting{
			ControllerURL: "https://controller.example", NewController: runnerControllerFactoryW8(func(context.Context) (lease.Beat, error) { return lease.Beat{}, nil }),
			Bind: func(_ context.Context, unit controllerwire.ClaimedUnit, _ controllerwire.Controller) (nodehost.NativeServices, capability.Principal, error) {
				if unit.Binding.Attempt != 0 {
					t.Fatal("runner manufactured an ordinal before Start")
				}
				return nodehost.NativeServices{}, capability.Principal{}, reached
			},
		}
		if err := host.Host(t.Context(), runnerAuthorityAwardW8(capability.UnitNode)); !errors.Is(err, reached) {
			t.Fatal(err)
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
