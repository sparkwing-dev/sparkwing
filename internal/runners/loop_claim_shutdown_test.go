package runners

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestLoopClaimFailureDuringStop(t *testing.T) {
	failure := errors.New("terminal claim denied")
	for _, stop := range []string{"cancel", "drain", "policy"} {
		for _, shape := range []string{"failure", "joined", "cancellation", "wrapped-cancellation"} {
			t.Run(stop+"/"+shape, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					started, stopped := make(chan struct{}), make(chan struct{})
					claimErr := failure
					switch shape {
					case "joined":
						claimErr = errors.Join(fmt.Errorf("request: %w", context.Canceled), failure)
					case "cancellation":
						claimErr = context.Canceled
					case "wrapped-cancellation":
						claimErr = fmt.Errorf("request: %w", context.Canceled)
					}
					checks := 0
					loop := Loop{
						Config:          LoopConfig{Version: "v1.2.3", MaxConcurrent: 1},
						OnClaimsStopped: func() { close(stopped) },
						CheckVersion: func(context.Context, string) error {
							checks++
							if stop == "policy" && checks > 1 {
								return errors.New("policy stopped claims")
							}
							return nil
						},
						Controller: claimFunc(func(claimCtx context.Context, _ controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
							close(started)
							<-claimCtx.Done()
							// safety: withholding the result forces deferred acceptance to expose shutdown error loss.
							if stop == "policy" {
								<-stopped
							}
							return controllerwire.ClaimResponse{}, claimErr
						}),
						Host: hostFunc(func(context.Context, controllerwire.ClaimedUnit) error { t.Error("unexpected award"); return nil }),
					}
					done := make(chan error, 1)
					go func() { done <- loop.Run(ctx) }()
					<-started
					if stop == "cancel" {
						cancel()
					} else if stop == "drain" {
						loop.Drain()
					}
					err := <-done
					wantFailure := shape == "failure" || shape == "joined"
					if errors.Is(err, failure) != wantFailure {
						t.Fatalf("claim failure lost: %v", err)
					}
					if stop == "drain" && !wantFailure && err != nil {
						t.Fatalf("clean drain failed: %v", err)
					}
				})
			})
		}
	}
}
