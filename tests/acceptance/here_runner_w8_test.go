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
		name                  string
		lost, delayed, denied bool
	}{
		{"awarded", false, false, false},
		{"response_lost", true, false, false},
		{"response_lost_after_refresh", true, true, false},
		{"pending_award_after_policy_refusal", false, true, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				unit := runnerAuthorityAwardW8(capability.UnitTrigger)
				unit.Purpose = capability.PurposeHere
				failure := errors.New("claim response lost")
				if scenario.denied {
					failure = errors.New("runner version revoked")
				}
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
								if !scenario.denied {
									return controllerwire.ClaimResponse{}, ctx.Err()
								}
							}
						}
						if scenario.lost {
							return controllerwire.ClaimResponse{}, failure
						}
						return controllerwire.ClaimResponse{Claim: &unit}, nil
					}},
					Host: bootstrapHostW8(func(hostCtx context.Context, got controllerwire.ClaimedUnit) error {
						hosted++
						if err := hostCtx.Err(); err != nil {
							return err
						}
						if got.RunID != unit.RunID || got.Capability != unit.Capability || got.Binding != unit.Binding {
							return errors.New("here award changed before hosting")
						}
						return nil
					}),
					CheckVersion: func(ctx context.Context, version string) error {
						checks++
						if scenario.denied && checks > 1 {
							return failure
						}
						if checks == 3 {
							close(respond)
						}
						return runnerStorePolicyW8{}.CheckRunner(ctx, version)
					},
				}
				wantError := scenario.lost || scenario.denied
				if err := loop.Run(ctx); (wantError && !errors.Is(err, failure)) || (!wantError && err != nil) {
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
