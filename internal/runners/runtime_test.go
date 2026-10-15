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
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
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
	var clients, executed, joined int
	config := RuntimeConfig{
		Controller: client.EngineOptions{URL: "http://private", HTTP: transport, Bearer: func(context.Context) (string, error) { return "root", nil }},
		Loop:       LoopConfig{Version: "v0.10.0", MaxConcurrent: 1, MaxClaims: 1}, Versions: runtimePolicy{index: index}, Target: releaseasset.Target{Binary: releaseasset.SparkwingRunner},
		Hosting: Hosting{
			Lease: time.Minute, Interval: time.Second,
			NewController: func(options client.EngineOptions) (controllerwire.Controller, error) {
				bearer, err := options.Bearer(t.Context())
				want := "root"
				if clients > 0 {
					want = unit.Capability
				}
				if err != nil || bearer != want || options.HTTP != transport || options.URL != "http://private" || options.Launcher {
					t.Fatalf("wrong client options: client=%d error=%v", clients, err)
				}
				clients++
				return runtimeController{award: unit}, nil
			},
			StartKeeper: func(ctx context.Context, _ lease.Keeper) (context.Context, func()) { return ctx, func() { joined++ } },
			PrepareRun: func(context.Context, controllerwire.ClaimedUnit, controllerwire.Controller) (nodehost.RunHost, nodehost.RunInput, func() error, error) {
				return runHostFunc(func(context.Context, nodehost.RunInput) (nodehost.RunCompletion, error) {
					executed++
					return nodehost.RunCompletion{}, nil
				}), nodehost.RunInput{}, nil, nil
			},
			PrepareNode: func(context.Context, controllerwire.ClaimedUnit, controllerwire.Controller) (nodehost.NodeHost, nodehost.NodeInput, func() error, error) {
				t.Fatal("wrong host")
				return nil, nodehost.NodeInput{}, nil, nil
			},
		},
	}
	runner, err := NewManagedRunner(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if clients != 2 || executed != 1 || joined != 1 {
		t.Fatalf("clients=%d executed=%d joined=%d", clients, executed, joined)
	}
}
