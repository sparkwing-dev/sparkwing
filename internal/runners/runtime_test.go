package runners

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

type runtimeController struct {
	controllerwire.Controller
	award controllerwire.ClaimedUnit
}

func (c runtimeController) Claim(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
	return controllerwire.ClaimResponse{Claim: &c.award}, nil
}

func (runtimeController) Renew(context.Context, string, controllerwire.RenewalRequest) (controllerwire.RenewalResponse, error) {
	return controllerwire.RenewalResponse{}, nil
}

type runtimePolicy struct{ index engine.VerifiedIndex }

func (p runtimePolicy) Index(context.Context, time.Time) (engine.IndexResult, error) {
	return engine.IndexResult{Index: p.index}, nil
}

func (runtimePolicy) PrepareRunnerUpdate(context.Context, string, releaseasset.Target) (engine.SoftwareUpdate, error) {
	return engine.SoftwareUpdate{}, errors.New("unexpected update")
}

func TestManagedRunnerCompositionSeparatesClaimantAndHeldTransport(t *testing.T) {
	unit := hostingAward(capability.UnitTrigger)
	index, _ := runnerPolicyFixtures(t)
	transport := &http.Client{}
	clients, hosted := 0, 0
	config := RuntimeConfig{
		Controller: client.EngineOptions{URL: "http://private", HTTP: transport, Bearer: func(context.Context) (string, error) { return "root", nil }},
		NewController: func(options client.EngineOptions) (controllerwire.Controller, error) {
			bearer, err := options.Bearer(t.Context())
			if err != nil || bearer != "root" || options.HTTP != transport || options.URL != "http://private" || options.Launcher {
				t.Fatal("claimant transport changed")
			}
			clients++
			return runtimeController{award: unit}, nil
		},
		Loop: LoopConfig{Version: "v0.10.0", MaxConcurrent: 1, MaxClaims: 1}, Versions: runtimePolicy{index: index}, Target: releaseasset.Target{Binary: releaseasset.SparkwingRunner},
		Hosting: hostFunc(func(_ context.Context, got controllerwire.ClaimedUnit) error {
			if got.Capability != unit.Capability {
				t.Fatal("host lost its award bearer")
			}
			hosted++
			return nil
		}),
	}
	runner, err := NewManagedRunner(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if clients != 1 || hosted != 1 {
		t.Fatalf("clients=%d hosted=%d", clients, hosted)
	}
}
