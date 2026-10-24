package nodehost

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestOwnerCannotExecuteBeforeItsSelfClaimRenews(t *testing.T) {
	for _, code := range []apierr.Code{apierr.LockHeld, apierr.Unavailable} {
		t.Run(string(code), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				input, state, _, _ := executorFixture(t)
				f := &ownerFixture{nodeRenew: func(context.Context, runspec.NodeRef, runstate.Actor) (runstate.LeaseBeat, error) {
					return runstate.LeaseBeat{}, &apierr.Error{Code: code}
				}}
				h := OwnerHost{Store: f, Lease: lease.Keeper{Interval: time.Second, Lease: time.Minute}, Executor: ownerNodeFunc(func(context.Context, NodeInput) error {
					t.Error("executed without a successful claim renewal")
					return nil
				})}
				err := h.executeAward(t.Context(), RunInput{GrantedRunner: input.GrantedRunner}, input.Accepted, input.Entry, runstate.ReconciledNode{Kind: runstate.Execute, Node: state.row})
				if !errors.Is(err, lease.ErrLeaseLost) && !errors.Is(err, lease.ErrSilent) {
					t.Fatalf("renewal refusal did not stop the claim: %v", err)
				}
			})
		})
	}
}
