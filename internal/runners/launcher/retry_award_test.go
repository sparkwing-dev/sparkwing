package launcher

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestReviewRetryPassesLauncherClaimEntryPoint(t *testing.T) {
	for _, retry := range []bool{false, true} {
		unit := wireAward(capability.UnitNode)
		if retry {
			unit.Request.RunID = "original"
			unit.Accepted.Spec.Request = unit.Request
		}
		control, err := NewControllerControl(launcherWireFake{claim: func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
			return controllerwire.ClaimResponse{Claim: &unit}, nil
		}}, controllerwire.ClaimRequest{RunnerVersion: "v1.2.3", Kinds: []capability.UnitKind{capability.UnitNode}})
		if err != nil {
			t.Fatal(err)
		}
		if award, err := control.Claim(t.Context(), "cloud", nil); err != nil || award == nil {
			t.Fatalf("retry=%v: claim rejected: %v", retry, err)
		}
	}
}

func TestReviewRetryPassesCloudExchangeEntryPoint(t *testing.T) {
	for _, retry := range []bool{false, true} {
		award, unit := bootstrapAward(t, capability.UnitNode)
		if retry {
			unit.Request.RunID = "original"
			unit.Accepted.Spec.Request = unit.Request
		}
		boundary := errors.New("host boundary")
		host := BootstrapHost{
			ControllerURL: "https://controller.example",
			NewController: func(client.EngineOptions) (controllerwire.Controller, error) {
				return bootstrapController{exchange: func(context.Context) (controllerwire.ExchangeResponse, error) {
					return controllerwire.ExchangeResponse{Claim: unit}, nil
				}}, nil
			}, Host: bootstrapUnitHost(func(context.Context, controllerwire.ClaimedUnit) error { return boundary }),
		}
		if err := host.Run(t.Context(), award); !errors.Is(err, boundary) {
			t.Fatalf("retry=%v: exchange rejected before hosting: %v", retry, err)
		}
	}
}
