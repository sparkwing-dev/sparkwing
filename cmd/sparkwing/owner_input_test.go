package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func TestOwnerAwardMismatchCannotReachBindingProperty(t *testing.T) {
	property := func(value string, mutation uint8, here bool) bool {
		award := localTestAward(t)
		if here {
			award = hereAward(t)
		}
		plan := json.RawMessage(`{"jobs":[]}`)
		spec, err := runspec.SealSpec(runspec.SealInput{
			Request: award.Request, Plan: plan,
			Resolved: runspec.Resolved{SecretArgs: []string{"password"}, Args: award.Request.Request.RequestedArgs},
		})
		if err != nil {
			t.Fatal(err)
		}
		award.Accepted = &controllerwire.PlanAccepted{Spec: spec, Plan: plan, PlanHash: spec.PlanHash}
		run, hash := award.RunID, award.Request.Hash
		switch mutation % 10 {
		case 0:
			award.RunID += value + "-foreign"
		case 1:
			award.Binding.RunID += value + "-foreign"
		case 2:
			award.JobID = value + "-foreign"
		case 3:
			award.Binding.NodeID = value + "-foreign"
		case 4:
			award.Request.Hash += runspec.Hash(value + "-foreign")
		case 5:
			award.Request.Request.RequestedArgs["password"] += value + "-foreign"
		case 6:
			award.Accepted.Spec.Hash += runspec.Hash(value + "-foreign")
		case 7:
			award.Accepted.Plan = json.RawMessage(`{"jobs":[],"changed":true}`)
		case 8:
			award.LeaseExpiresAt = time.Time{}
		case 9:
			award.Binding.Generation = 0
		}
		bound := false
		bind := func(context.Context, controllerwire.ClaimedUnit) (hereHostSession, error) {
			bound = true
			return hereHostSession{}, errors.New("unexpected binding")
		}
		if here {
			_, err = runHereAward(t.Context(), client.EngineOptions{}, run, award,
				func(ctx context.Context, _ client.EngineOptions, unit controllerwire.ClaimedUnit) (hereHostSession, error) {
					return bind(ctx, unit)
				})
		} else {
			runtime := localRunRuntime{bind: bind, controller: localTestController{claim: func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				return controllerwire.ClaimResponse{Claim: &award}, nil
			}}}
			_, err = hostLocalRun(t.Context(), runtime, run, hash, "v1.2.3")
		}
		var failure *apierr.Error
		return !bound && errors.As(err, &failure) && failure.Code == apierr.ProtocolError
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 256}); err != nil {
		t.Fatal(err)
	}
}
