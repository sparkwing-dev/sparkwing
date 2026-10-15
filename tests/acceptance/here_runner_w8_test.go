package acceptance_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
)

func TestHereRunnerNamedClaimW8Components(t *testing.T) {
	for _, scenario := range []struct {
		name          string
		lost, delayed bool
	}{{"awarded", false, false}, {"response_lost", true, false}, {"response_lost_after_refresh", true, true}} {
		t.Run(scenario.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				unit := runnerAuthorityAwardW8(capability.UnitTrigger)
				unit.Purpose = capability.PurposeHere
				failure := errors.New("claim response lost")
				ctx, cancel := context.WithTimeout(t.Context(), 3*limits.VersionIndexRefresh)
				defer cancel()
				respond := make(chan struct{})
				claims, hosted, checks := 0, 0, 0
				loop := runners.Loop{
					Config: runners.LoopConfig{Version: "v0.71.0", RunID: unit.RunID, MaxConcurrent: 1, MaxClaims: 1},
					Controller: &runnerControllerW8{claim: func(ctx context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
						claims++
						if request.RunID != unit.RunID || request.JobID != "" || !slices.Equal(request.Kinds, []capability.UnitKind{capability.UnitTrigger}) {
							return controllerwire.ClaimResponse{}, errors.New("request cannot select the named trigger route")
						}
						if scenario.delayed {
							select {
							case <-respond:
							case <-ctx.Done():
								return controllerwire.ClaimResponse{}, ctx.Err()
							}
						}
						if scenario.lost {
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
					CheckVersion: func(ctx context.Context, version string) error {
						checks++
						if checks == 3 {
							close(respond)
						}
						return runnerStorePolicyW8{}.CheckRunner(ctx, version)
					},
				}
				if err := loop.Run(ctx); (scenario.lost && !errors.Is(err, failure)) || (!scenario.lost && err != nil) {
					t.Fatal(err)
				}
				wantHosted := 1
				if scenario.lost {
					wantHosted = 0
				}
				if claims != 1 || hosted != wantHosted {
					t.Fatalf("claims=%d hosted=%d, want one claim and %d hosts", claims, hosted, wantHosted)
				}
			})
		})
	}
}
