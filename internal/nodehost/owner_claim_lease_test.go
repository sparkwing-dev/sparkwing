package nodehost

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestOwnerCannotExecuteBeforeItsSelfClaimRenews(t *testing.T) {
	for _, code := range []apierr.Code{apierr.LockHeld, apierr.Unavailable} {
		t.Run(string(code), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				input, state, _, _ := executorFixture(t)
				f := &ownerFixture{nodeRenew: func(context.Context, store.NodeRef, store.Actor) (store.LeaseBeat, error) {
					return store.LeaseBeat{}, &apierr.Error{Code: code}
				}}
				h := OwnerHost{Store: f, Lease: lease.Keeper{Interval: time.Second, Lease: time.Minute}, Executor: ownerNodeFunc(func(context.Context, NodeInput) error {
					t.Error("executed without a successful claim renewal")
					return nil
				})}
				err := h.executeAward(t.Context(), RunInput{GrantedRunner: input.GrantedRunner}, input.Accepted, input.Entry, store.ReconciledNode{Kind: store.Execute, Node: state.row})
				if !errors.Is(err, lease.ErrLeaseLost) && !errors.Is(err, lease.ErrSilent) {
					t.Fatalf("renewal refusal did not stop the claim: %v", err)
				}
			})
		})
	}
}
