package main

import (
	"context"
	"errors"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func TestOwnerHostsCannotOutliveAwardProperty(t *testing.T) {
	property := func(parent, binding, owner, authority uint8, expired, cancelBinding bool) bool {
		for _, here := range []bool{false, true} {
			award := localTestAward(t)
			if here {
				award = hereAward(t)
			}
			base := time.Now().Add(time.Hour)
			want := base.Add(time.Duration(parent) * time.Minute)
			deadlineCtx, stopDeadline := context.WithDeadline(t.Context(), want)
			ctx, cancelCause := context.WithCancelCause(deadlineCtx)
			cancel := func() { cancelCause(nil); stopDeadline() }
			lostAuthority := errors.New("owner authority ended")
			bound := func(value uint8) *time.Time {
				if value == 0 {
					return nil
				}
				end := base.Add(time.Duration(value) * time.Minute)
				if end.Before(want) {
					want = end
				}
				return &end
			}
			award.Binding.Deadline, award.OwnerDeadline, award.ParentCapabilityExpiry = bound(binding), bound(owner), bound(authority)
			if expired {
				end := time.Unix(1, 0)
				award.ParentCapabilityExpiry = &end
			}
			boundCalls, hosted, closed := 0, 0, 0
			valid := true
			bind := func(ctx context.Context, _ controllerwire.ClaimedUnit) (hereHostSession, error) {
				boundCalls++
				end, ok := ctx.Deadline()
				valid = valid && ok && end.Equal(want)
				if cancelBinding {
					cancelCause(lostAuthority)
				}
				return hereHostSession{
					close: func() error { closed++; return nil },
					host: hereTestHost(func(ctx context.Context, _ nodehost.RunInput) (nodehost.RunCompletion, error) {
						hosted++
						end, ok := ctx.Deadline()
						valid = valid && ok && end.Equal(want)
						return nodehost.RunCompletion{Outcome: "success"}, nil
					}),
				}, nil
			}
			var completion nodehost.RunCompletion
			var err error
			if here {
				completion, err = runHereAward(ctx, client.EngineOptions{}, award.RunID, award,
					func(ctx context.Context, _ client.EngineOptions, award controllerwire.ClaimedUnit) (hereHostSession, error) {
						return bind(ctx, award)
					})
			} else {
				runtime := localRunRuntime{bind: bind, controller: localTestController{claim: func(context.Context, controllerwire.ClaimRequest) (controllerwire.ClaimResponse, error) {
					return controllerwire.ClaimResponse{Claim: &award}, nil
				}}}
				completion, err = hostLocalRun(ctx, runtime, award.RunID, award.Request.Hash, "v1.2.3")
			}
			cancel()
			if expired {
				if !errors.Is(err, context.DeadlineExceeded) || boundCalls != 0 || hosted != 0 || closed != 0 {
					return false
				}
			} else if cancelBinding {
				if !valid || !errors.Is(err, lostAuthority) || boundCalls != 1 || hosted != 0 || closed != 1 {
					return false
				}
			} else if !valid || err != nil || completion.Outcome != "success" || boundCalls != 1 || hosted != 1 || closed != 1 {
				return false
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128}); err != nil {
		t.Fatal(err)
	}
}
