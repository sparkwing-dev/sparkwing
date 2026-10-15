package acceptance_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
)

func TestHereRunnerNamedClaimW8Components(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "awarded", true: "response_lost"}[lost], func(t *testing.T) {
			unit := runnerAuthorityAwardW8(capability.UnitTrigger)
			unit.Purpose = capability.PurposeHere
			failure := errors.New("claim response lost")
			claims, hosted := 0, 0
			loop := runners.Loop{
				Config: runners.LoopConfig{Version: "v0.71.0", RunID: unit.RunID, MaxConcurrent: 1, MaxClaims: 1},
				Controller: &runnerControllerW8{claim: func(_ context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
					claims++
					if request.RunID != unit.RunID || request.JobID != "" || !slices.Equal(request.Kinds, []capability.UnitKind{capability.UnitTrigger}) {
						return controllerwire.ClaimResponse{}, errors.New("request cannot select the named trigger route")
					}
					if lost {
						return controllerwire.ClaimResponse{}, failure
					}
					return controllerwire.ClaimResponse{Claim: &unit}, nil
				}},
				Host: bootstrapHostW8(func(_ context.Context, got controllerwire.ClaimedUnit) error {
					hosted++
					if got.RunID != unit.RunID || got.Capability != unit.Capability || got.Binding != unit.Binding {
						return errors.New("here award changed before hosting")
					}
					return nil
				}),
				CheckVersion: runnerStorePolicyW8{}.CheckRunner,
			}
			if err := loop.Run(t.Context()); (lost && !errors.Is(err, failure)) || (!lost && err != nil) {
				t.Fatal(err)
			}
			wantHosted := 1
			if lost {
				wantHosted = 0
			}
			if claims != 1 || hosted != wantHosted {
				t.Fatalf("claims=%d hosted=%d, want one claim and %d hosts", claims, hosted, wantHosted)
			}
		})
	}
}
