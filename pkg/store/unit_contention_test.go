package store_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/internal/storetest"
)

func TestUnitClaimRenewReleaseCapacityContentionProperty(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			target := storetest.NewSQLite(t)
			if dialect == "postgres" {
				target = storetest.NewPostgres(t)
			}
			s := target.Open(t)
			demand := s.TestOnlyUnitDemand(t)
			replica := target.Open(t)
			if dialect == "sqlite" {
				replica.TestOnlyClock(time.Unix(1700000000, 0).UTC())
			}
			random := rand.New(rand.NewPCG(91, 773))
			for round := range 8 {
				t.Run(fmt.Sprint(round), func(t *testing.T) {
					if _, err := s.DB().ExecContext(t.Context(), `UPDATE nodes SET ready_at=1 WHERE run_id='run'`); err != nil {
						t.Fatal(err)
					}
					insufficient := demand
					insufficient.Available.Cores = random.Float64() * 4
					_, err := s.ClaimUnit(t.Context(), insufficient)
					var capacity *apierr.Error
					if !errors.As(err, &capacity) || capacity.Code != apierr.LockHeld {
						t.Fatalf("capacity boundary: %v", err)
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
						request.Available.Cores = 4 + random.Float64()*4
						if i%3 == 0 {
							request.Available.Cores = random.Float64() * 4
						}
						wg.Go(func() {
							<-start
							claim, err := claimant.ClaimUnit(t.Context(), request)
							if err == nil {
								if request.Available.Cores < 4 {
									t.Error("claim exceeded available capacity")
								}
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
					if claim.Award.Generation != int64(round+1) {
						t.Fatalf("generation = %d", claim.Award.Generation)
					}
					start = make(chan struct{})
					wg.Go(func() {
						<-start
						_, err := replica.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false)
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
					_, err = s.RenewUnit(t.Context(), claim.Award.CapabilityPrefix, false)
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
						claim, err = s.ClaimUnit(t.Context(), demand)
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
							claim, err = s.ClaimUnit(ctx, demand)
						case "renew":
							_, err = s.RenewUnit(ctx, claim.Award.CapabilityPrefix, false)
						case "release":
							err = s.ReleaseUnit(ctx, claim, claim.Award.CapabilityPrefix)
						case "reap":
							_, err = s.ReapUnits(ctx, reaper, 10)
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
