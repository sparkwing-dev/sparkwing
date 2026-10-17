package cluster

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

type capacityController struct {
	controllerwire.Controller
	claim func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error)
}

func TestRunnerCapacityFailureNeverClaims(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	boundary := errors.New("machine observation failed")
	loop := runners.Loop{
		Config:       runners.LoopConfig{Version: "dev", Kinds: []capability.UnitKind{capability.UnitNode}, MaxConcurrent: 1},
		CheckVersion: func(context.Context, string) error { return nil },
		Host:         &productionHost{},
		RequestCapacity: func(context.Context) (match.Resources, match.Resources, error) {
			return match.Resources{}, match.Resources{}, boundary
		},
		Controller: capacityController{claim: func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
			t.Fatal("observation failure became an unknown-capacity claim")
			return controllerwire.ClaimResponse{}, nil
		}},
	}
	if err := loop.Run(ctx); !errors.Is(err, boundary) {
		t.Fatalf("runner lost capacity failure: %v", err)
	}
}

func (c capacityController) Claim(ctx context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
	return c.claim(ctx, request)
}

func TestProductionRunnerAdvertisesMachineCapacity(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_CONFIG", filepath.Join(home, "config.yaml"))
	endpoint := "https://controller.invalid"
	runtime, err := ProductionRuntime("dev").Runner.Factory(t.Context(), RunnerOptions{Config: userconfig.RunnerConfig{Controller: &endpoint}})
	if err != nil {
		t.Fatal(err)
	}
	boundary := errors.New("observed production claim")
	loop := runners.Loop{
		Config:       runners.LoopConfig{Version: "dev", Kinds: []capability.UnitKind{capability.UnitNode}, MaxConcurrent: 1},
		CheckVersion: func(context.Context, string) error { return nil },
		Host:         runtime.Hosting, RequestCapacity: runtime.RequestCapacity,
		Controller: capacityController{claim: func(_ context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
			if request.AvailableCores < 0 || request.Capacity == nil || request.Capacity.Cores <= 0 {
				t.Errorf("production machine offered unknown or empty capacity: %+v", request)
			}
			return controllerwire.ClaimResponse{}, boundary
		}},
	}
	if err := loop.Run(ctx); !errors.Is(err, boundary) {
		t.Fatalf("runner did not reach its real claim loop: %v", err)
	}
}
