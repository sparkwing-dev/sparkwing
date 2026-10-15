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
			for _, method := range []string{"queue", "named"} {
				t.Run(status+"/"+method+map[bool]string{false: "/free", true: "/metered"}[metered], func(t *testing.T) {
					s := storetest.Open(t)
					ctx := context.Background()
					claimant := store.ClaimIdentity{}
					if metered {
						claimant = meteredClaimant(t, s, "cloud")
						if _, err := tenant(t, s, store.DefaultTeam).RecordCreditGrant(ctx, store.CreditGrantRequest{Kind: store.CreditGrantPaid, AmountMicro: 100 * store.MicroCreditsPerCent, Reference: "funds", CreatedBy: "admin"}); err != nil {
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
						if method == "named" {
							return s.ClaimNamedNode(ctx, claimant, run, "ready", "holder", time.Minute, store.NamedClaimOptions{})
						}
						return s.ClaimNextReadyNode(ctx, claimant, "holder", time.Minute, nil)
					}
					n, err := claim("terminal")
					if n != nil || !(errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrLockHeld)) {
						t.Fatalf("terminal claim = %+v, %v", n, err)
					}
					charges, err := tenant(t, s, store.DefaultTeam).ListCreditCharges(ctx, 10)
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
