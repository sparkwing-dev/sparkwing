package runners

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

// A held empty claim already waited for work, so polling again only after the
// interval would delay a run submitted the moment the hold ended.
func TestIdleLoopClaimsAgainAtOnceAfterAHeldEmptyClaim(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		start, calls := time.Now(), 0
		loop := &Loop{
			Config: LoopConfig{Version: "v1.0.0", MaxConcurrent: 1, PollInterval: time.Second}, CheckVersion: func(context.Context, string) error { return nil },
			Controller: claimFunc(func(ctx context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				calls++
				if request.Wait != controllerwire.MaxClaimWait {
					t.Errorf("idle claim %d offered to wait %s, want %s", calls, request.Wait, controllerwire.MaxClaimWait)
				}
				if calls == 2 {
					if elapsed := time.Since(start); elapsed != controllerwire.MaxClaimWait {
						t.Errorf("claimed again after %s, want %s with no poll gap", elapsed, controllerwire.MaxClaimWait)
					}
					cancel()
					return controllerwire.ClaimResponse{}, ctx.Err()
				}
				time.Sleep(request.Wait)
				return controllerwire.ClaimResponse{Held: true}, nil
			}), Host: hostFunc(func(context.Context, controllerwire.ClaimedUnit) error { t.Error("unexpected work"); return nil }),
		}
		if err := loop.Run(ctx); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
}

// A controller that answers at once, such as Cloud or an older local daemon,
// still gets the runner's poll interval between empty claims.
func TestLoopKeepsItsPollWhenTheControllerDoesNotHold(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		start, calls := time.Now(), 0
		loop := &Loop{
			Config: LoopConfig{Version: "v1.0.0", MaxConcurrent: 1, PollInterval: time.Second}, CheckVersion: func(context.Context, string) error { return nil },
			Controller: claimFunc(func(ctx context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				calls++
				if calls == 2 {
					if elapsed := time.Since(start); elapsed != time.Second {
						t.Errorf("claimed again after %s, want the 1s poll", elapsed)
					}
					cancel()
					return controllerwire.ClaimResponse{}, ctx.Err()
				}
				return controllerwire.ClaimResponse{}, nil
			}), Host: hostFunc(func(context.Context, controllerwire.ClaimedUnit) error { t.Error("unexpected work"); return nil }),
		}
		if err := loop.Run(ctx); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
}

// A held request keeps the kinds it was sent with, so a runner holding work
// must not wait: a slot freed meanwhile would sit behind the hold.
func TestBusyLoopDoesNotOfferToWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		calls := 0
		loop := &Loop{
			Config: LoopConfig{Version: "v1.0.0", MaxConcurrent: 2, PollInterval: time.Second, Kinds: []capability.UnitKind{capability.UnitNode}}, CheckVersion: func(context.Context, string) error { return nil },
			Controller: claimFunc(func(ctx context.Context, request controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
				calls++
				if calls == 1 {
					return controllerwire.ClaimResponse{Claim: &controllerwire.ClaimedUnit{Unit: capability.UnitNode, UnitID: "unit", RunID: "run", Capability: "private"}}, nil
				}
				if request.Wait != 0 {
					t.Errorf("claim with a unit running offered to wait %s", request.Wait)
				}
				cancel()
				return controllerwire.ClaimResponse{}, ctx.Err()
			}), Host: hostFunc(func(ctx context.Context, _ controllerwire.ClaimedUnit) error { <-ctx.Done(); return ctx.Err() }),
		}
		if err := loop.Run(ctx); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
}
