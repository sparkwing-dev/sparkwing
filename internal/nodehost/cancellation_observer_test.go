package nodehost

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/lease"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

func TestClaimCancellationObservationRetriesTransientReads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		input, state, _, _ := claimedNodeFixture(t)
		actor := executorActor(input)
		at := time.Now()
		state.row.CancelRequestedAt, state.row.CancelReason = &at, "parent cancelled"
		reads := 0
		read := func(ctx context.Context, ref runspec.NodeRef) (runstate.NodeRow, error) {
			reads++
			if reads == 1 {
				return runstate.NodeRow{}, errors.New("temporary transport failure")
			}
			return state.ReadNode(ctx, ref)
		}
		result := waitClaimCancellation(t.Context(), read, state.row.Ref, actor)
		if result == nil || result.Reason != "parent cancelled" || reads != 2 {
			t.Fatalf("cancellation=%v reads=%d", result, reads)
		}
	})
}

func TestClaimCancellationObservationRejectsOtherClaims(t *testing.T) {
	for _, changed := range []string{"run", "job", "generation", "holder", "capability"} {
		t.Run(changed, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				input, state, _, _ := claimedNodeFixture(t)
				actor, ref := executorActor(input), state.row.Ref
				at := time.Now()
				state.row.CancelRequestedAt = &at
				switch changed {
				case "run":
					state.row.Ref.RunID = "other"
				case "job":
					state.row.Ref.NodeID = runspec.NodeID("other")
				case "generation":
					state.row.Generation++
				case "holder":
					state.row.ClaimedBy = "other"
				case "capability":
					state.row.CapabilityPrefix = "other"
				}
				ctx, cancel := context.WithCancel(t.Context())
				result := make(chan *lease.CancelError, 1)
				go func() { result <- waitClaimCancellation(ctx, state.ReadNode, ref, actor) }()
				time.Sleep(3 * time.Second)
				select {
				case cancellation := <-result:
					t.Fatalf("observed another claim's cancellation: %v", cancellation)
				default:
				}
				cancel()
				if cancellation := <-result; cancellation != nil {
					t.Fatalf("context stop invented a cancellation: %v", cancellation)
				}
			})
		})
	}
}
