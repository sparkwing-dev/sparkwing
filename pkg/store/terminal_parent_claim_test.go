package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestTerminalParentsCannotSupplyQueuedWorkOrReserveCredits(t *testing.T) {
	for _, status := range []string{"failed", "success", "cancelled"} {
		for _, metered := range []bool{false, true} {
			for _, method := range []string{"queue", "named", "reservation", "offer"} {
				t.Run(status+"/"+method+map[bool]string{false: "/free", true: "/metered"}[metered], func(t *testing.T) {
					s := storetest.Open(t)
					ctx := context.Background()
					claimant := store.ClaimIdentity{}
					if metered {
						claimant = meteredClaimant(t, s, "cloud")
						if _, err := s.GrantCredits(ctx, store.CreditGrantPaid, 100*store.MicroCreditsPerCent, "funds", "admin"); err != nil {
							t.Fatal(err)
						}
					}
					if method == "reservation" || method == "offer" {
						if !metered {
							claimant = unmeteredClaimant(t, s, "cloud")
						}
						if err := s.EnrollExecutor(ctx, claimant.TokenPrefix, store.Executor{
							Name: "cloud", Principal: claimant.Principal, Kind: "agent", Location: "local",
							BasePriority: 100, PriorityCeiling: 100, MaxConcurrent: 1,
							Budget: store.ExecutorResource{Cores: 4, MemoryBytes: 4 << 30},
						}); err != nil {
							t.Fatal(err)
						}
						if err := s.HeartbeatExecutor(ctx, claimant, "cloud", store.ExecutorResource{Cores: 4, MemoryBytes: 4 << 30}, 0, time.Now()); err != nil {
							t.Fatal(err)
						}
					}
					readyNode(t, s, "terminal", "ready")
					if err := s.CreateNode(ctx, store.Node{RunID: "terminal", NodeID: "waiting", Status: "pending"}); err != nil {
						t.Fatal(err)
					}
					if err := s.FinishRun(ctx, "terminal", status, ""); err != nil {
						t.Fatal(err)
					}
					counts, err := s.CountNodesByQueueState(ctx)
					if err != nil || counts["ready"] != 0 || counts["waiting"] != 0 {
						t.Fatalf("terminal queue = %v, %v", counts, err)
					}
					if count, err := s.CountPendingNodes(ctx); err != nil || count != 0 {
						t.Fatalf("pending = %d, %v", count, err)
					}
					claim := func(run string) (*store.Node, error) {
						if method == "reservation" || method == "offer" {
							summary, err := s.SchedulingSummary(ctx, run, "ready")
							if err != nil {
								return nil, err
							}
							if method == "offer" {
								result, err := s.TestOnlyOfferExecutorClaim(ctx, claimant, store.ExecutorClaimOffer{
									ExecutorName: "cloud", HolderID: "holder", RunID: run, NodeID: "ready",
									ReservationID: "reservation", ResourceDigest: summary.ResourceDigest, Slot: 0, Lease: time.Minute,
								})
								return result.Node, err
							}
							return s.ClaimReadyNodeForExecutorWithReservation(ctx, claimant, "cloud", run, "ready", "holder", time.Minute, "reservation", 0, summary.ResourceDigest)
						}
						if method == "named" {
							return s.ClaimNamedNode(ctx, claimant, run, "ready", "holder", time.Minute, store.NamedClaimOptions{})
						}
						return s.ClaimNextReadyNode(ctx, claimant, "holder", time.Minute, nil)
					}
					n, err := claim("terminal")
					if n != nil || !(errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrLockHeld)) {
						t.Fatalf("terminal claim = %+v, %v", n, err)
					}
					charges, err := s.ListCreditCharges(ctx, 10)
					if err != nil || len(charges) != 0 {
						t.Fatalf("terminal credit charges = %v, %v", charges, err)
					}
					readyNode(t, s, "active", "ready")
					counts, err = s.CountNodesByQueueState(ctx)
					if err != nil || counts["ready"] != 1 {
						t.Fatalf("active queue = %v, %v", counts, err)
					}
					if n, err = claim("active"); err != nil || n == nil || n.RunID != "active" {
						t.Fatalf("active claim = %+v, %v", n, err)
					}
				})
			}
		}
	}
}
