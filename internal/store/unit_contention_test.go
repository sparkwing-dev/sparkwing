package store_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestUnitClaimRenewReleaseContentionProperty(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 1.2s measured in the short class; the full suite runs it")
	}
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			target := storetest.NewSQLite(t)
			if dialect == "postgres" {
				target = storetest.NewPostgres(t)
			}
			s := target.Open(t)
			now := time.Unix(1700000000, 0).UTC()
			s.SetTestClock(t, func() time.Time { return now })
			demand := s.TestOnlyUnitDemand(t)
			replica := target.Open(t)
			replica.SetTestClock(t, func() time.Time { return now })
			for round := range 8 {
				t.Run(fmt.Sprint(round), func(t *testing.T) {
					if _, err := s.DB().ExecContext(t.Context(), `UPDATE nodes SET ready_at=1 WHERE run_id='run'`); err != nil {
						t.Fatal(err)
					}
					start := make(chan struct{})
					claims := make(chan store.UnitClaim, 12)
					var wg sync.WaitGroup
					for i := range 12 {
						request := demand
						claimant := s
						if i%2 == 1 {
							claimant = replica
						}
						wg.Go(func() {
							<-start
							claim, err := claimant.AsOperator().ClaimUnit(t.Context(), request)
							if err == nil {
								claims <- claim
							} else {
								var refused *apierr.Error
								var holder *store.ErrIllegalTransition
								if !(errors.As(err, &refused) && refused.Code == apierr.LockHeld) && !(errors.As(err, &holder) && holder.Reason == store.ReasonHolderRights) {
									t.Errorf("claim refusal: %v", err)
								}
							}
						})
					}
					close(start)
					wg.Wait()
					close(claims)
					if len(claims) != 1 {
						t.Fatalf("claim winners = %d", len(claims))
					}
					claim := <-claims
					if int64(claim.Award.Generation) != int64(round+1) {
						t.Fatalf("generation = %d", claim.Award.Generation)
					}
					start = make(chan struct{})
					wg.Go(func() {
						<-start
						_, err := replica.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false)
						if err != nil {
							var refused *store.ErrIllegalTransition
							if !errors.As(err, &refused) || refused.Reason != store.ReasonHolderRights {
								t.Errorf("renewal refusal: %v", err)
							}
						}
					})
					wg.Go(func() {
						<-start
						if err := s.ReleaseUnit(t.Context(), claim, claim.Award.CapabilityPrefix); err != nil {
							t.Error(err)
						}
					})
					close(start)
					wg.Wait()
					_, err := s.AsOperator().RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false)
					var refused *store.ErrIllegalTransition
					if !errors.As(err, &refused) || refused.Reason != store.ReasonHolderRights {
						t.Fatalf("released award renewed: %v", err)
					}
				})
			}
		})
	}
}

func TestConcurrentQueuedClaimsAwardOnlyOnce(t *testing.T) {
	s := storetest.Open(t)
	s.DB().SetMaxOpenConns(12)
	demand := s.TestOnlyUnitDemand(t)
	demand.Target, demand.Kinds = nil, []capability.UnitKind{capability.UnitNode}
	start := make(chan struct{})
	claims := make(chan store.UnitClaim, 12)
	var group sync.WaitGroup
	for range 12 {
		group.Go(func() {
			<-start
			claim, err := s.AsOperator().ClaimUnit(t.Context(), demand)
			if errors.Is(err, store.ErrNotFound) {
				return
			}
			if err != nil {
				t.Error(err)
				return
			}
			claims <- claim
		})
	}
	close(start)
	group.Wait()
	close(claims)
	if len(claims) != 1 {
		t.Fatalf("claim winners=%d; want one", len(claims))
	}
	claim := <-claims
	if claim.Team != "a" || claim.RunID != "run" || claim.NodeID != "job" || claim.Award.Generation != 1 {
		t.Fatalf("queued award: %+v", claim)
	}
}
