package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestUnitClaimRenewReleaseContentionProperty(t *testing.T) {
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

func TestUnitOperationsShareControllerFence(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			target := storetest.NewSQLite(t)
			if dialect == "postgres" {
				target = storetest.NewPostgres(t)
			}
			s := target.Open(t)
			demand := s.TestOnlyUnitDemand(t)
			_, err := s.AsOperator().AcquireSingletonLease(t.Context(), "controller.owner", "owner", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			var claim store.UnitClaim
			var reaper store.SingletonLease
			for _, operation := range []string{"claim", "renew", "release", "reap"} {
				t.Run(operation, func(t *testing.T) {
					if operation == "reap" {
						if _, err := s.DB().ExecContext(t.Context(), `UPDATE nodes SET ready_at=1 WHERE run_id='run'`); err != nil {
							t.Fatal(err)
						}
						var err error
						claim, err = s.AsOperator().ClaimUnit(t.Context(), demand)
						if err != nil {
							t.Fatal(err)
						}
						if _, err := s.DB().ExecContext(t.Context(), `UPDATE nodes SET lease_expires_at=(SELECT awarded_at FROM billing_awards WHERE team=nodes.team AND capability_prefix=nodes.capability_prefix) WHERE run_id='run'`); err != nil {
							t.Fatal(err)
						}
						reaper, err = s.AsOperator().AcquireSingletonLease(t.Context(), "controller.reaper", "reaper", time.Minute)
						if err != nil {
							t.Fatal(err)
						}
					}
					ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
					defer cancel()
					if dialect == "postgres" {
						reader, err := s.DB().BeginTx(ctx, nil)
						if err != nil {
							t.Fatal(err)
						}
						defer func() { _ = reader.Rollback() }()
						if _, err := reader.ExecContext(ctx, `SELECT holder FROM leases WHERE name='controller.owner' FOR KEY SHARE`); err != nil {
							t.Fatal(err)
						}
					}
					done := make(chan error, 1)
					go func() {
						var err error
						switch operation {
						case "claim":
							claim, err = s.AsOperator().ClaimUnit(ctx, demand)
						case "renew":
							_, err = s.AsOperator().RenewUnit(ctx, claim.Award.CapabilityPrefix, false)
						case "release":
							err = s.ReleaseUnit(ctx, claim, claim.Award.CapabilityPrefix)
						case "reap":
							_, err = s.AsOperator().ReapUnits(ctx, reaper, 10)
						}
						done <- err
					}()
					if err := <-done; err != nil {
						t.Fatal(err)
					}
				})
			}
		})
	}
}
