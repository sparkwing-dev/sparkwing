package nodehost

import (
	"context"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestOwnerReportsCancellationOnTheFirstSelfClaimRenewal(t *testing.T) {
	input, node, deps, _ := executorFixture(t)
	expires := time.Now().Add(time.Hour)
	node.row.LeaseExpiresAt = &expires
	state, host, _ := newOwnerFixture(t, true)
	state.snapshot.Nodes = []runstate.NodeRow{node.row}
	state.nodeRenew = func(context.Context, runspec.NodeRef, runstate.Actor) (runstate.LeaseBeat, error) {
		return runstate.LeaseBeat{CancelRequested: true, ExpiresAt: expires}, nil
	}
	host.Lease = lease.Keeper{Interval: time.Second, Lease: time.Minute}
	host.Masker = deps.Masker
	err := host.executeAward(t.Context(), RunInput{GrantedRunner: input.GrantedRunner}, input.Accepted, input.Entry, runstate.ReconciledNode{Node: node.row})
	if state.snapshot.Nodes[0].Outcome != runstate.NodeCancelled {
		t.Fatalf("initial cancellation left the claim unsettled: %+v, %v", state.snapshot.Nodes[0], err)
	}
	if len(state.trace) != 1 || state.trace[0] != string(runstate.NodeCancelled) {
		t.Fatalf("initial cancellation started work: %v", state.trace)
	}
}
