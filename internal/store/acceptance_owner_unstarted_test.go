package store_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

func checkOwnerUnstartedRecovery(t *testing.T) (err error) {
	ctx := t.Context()
	target := storetest.NewSQLite(t)
	s, err := target.TryOpen()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.Close()) }()
	tenant, plan, err := storePlanSealRequest(ctx, s)
	if err != nil {
		return err
	}
	var rootPrefix string
	if err := s.DB().QueryRowContext(ctx, `SELECT parent_id FROM capabilities WHERE prefix=?`, plan.Actor.CapabilityPrefix).Scan(&rootPrefix); err != nil {
		return err
	}
	initial, err := tenant.ReadRun(ctx, plan.RunID)
	if err != nil {
		return err
	}
	lease, err := s.AsOperator().AcquireSingletonLease(ctx, "controller.reaper", "owner-proof", time.Hour)
	if err != nil {
		return err
	}
	for range 3 {
		beat, err := s.RenewUnit(ctx, plan.Actor.CapabilityPrefix, false)
		if err != nil {
			return err
		}
		var expiry int64
		if err := s.DB().QueryRowContext(ctx, `SELECT lease_expires_at FROM triggers WHERE team=? AND id=?`, acceptanceTeam, plan.RunID).Scan(&expiry); err != nil {
			return err
		}
		if expiry != beat.ExpiresAt.UnixNano() {
			return errors.New("owner renewal did not persist its lease")
		}
		if reaped, err := s.ReapUnits(ctx, lease, 10); err != nil || len(reaped) != 0 {
			return fmt.Errorf("live owner recovered: %+v: %v", reaped, err)
		}
		now, err := s.TestOnlyDatabaseTime(ctx)
		if err != nil {
			return err
		}
		if _, err := s.DB().ExecContext(ctx, `UPDATE triggers SET lease_expires_at=? WHERE team=? AND id=?`, now.UnixNano(), acceptanceTeam, plan.RunID); err != nil {
			return err
		}
		for replay := range 2 {
			reaped, err := s.ReapUnits(ctx, lease, 10)
			if err != nil || len(reaped) != 1-replay {
				return fmt.Errorf("owner recovery %d: %+v: %v", replay, reaped, err)
			}
			if replay == 0 && (!reaped[0].Requeued || reaped[0].Disruptions != 0 || reaped[0].RunID != plan.RunID || reaped[0].Generation != plan.Actor.Owner.Generation) {
				return fmt.Errorf("unstarted recovery consumed execution or changed identity: %+v", reaped)
			}
		}
		if err := s.Close(); err != nil {
			return err
		}
		reopened, err := target.TryOpen()
		if err != nil {
			return err
		}
		s = reopened
		s.SetClaimProfileResolver(acceptanceClaimProfile{})
		tenant, err = s.ForTeam(ctx, acceptanceTeam)
		if err != nil {
			return err
		}
		snapshot, err := tenant.ReadSnapshot(ctx, plan.RunID)
		if err != nil || snapshot.Run.State != store.RunPending || snapshot.Run.Spec != nil || snapshot.Run.FinishedAt != nil || snapshot.Run.Request.Hash != initial.Request.Hash || !snapshot.Run.CreatedAt.Equal(initial.CreatedAt) || len(snapshot.Nodes) != 0 {
			return fmt.Errorf("recovered intake changed across restart: %+v: %v", snapshot, err)
		}
		if _, err := tenant.AcceptPlan(ctx, plan); err == nil {
			return errors.New("former owner accepted a plan after recovery")
		}
		if _, err := s.RenewUnit(ctx, plan.Actor.CapabilityPrefix, false); err == nil {
			return errors.New("former owner renewed after recovery")
		}
		claim, err := s.ClaimUnit(ctx, store.UnitDemand{CapabilityPrefix: rootPrefix, Target: &store.UnitRef{Kind: capability.UnitTrigger, RunID: plan.RunID}, Available: match.Resources{Cores: 8}})
		if err != nil || claim.Award.Generation != plan.Actor.Owner.Generation+1 || len(claim.Bearer) == 0 || claim.RunID != plan.RunID {
			return fmt.Errorf("owner reclaim: %+v: %v", claim, err)
		}
		plan.Actor = store.Actor{Kind: store.ActorRunHost, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: claim.Award.CapabilityPrefix}, CapabilityPrefix: claim.Award.CapabilityPrefix, Owner: claim.Owner}
	}
	if _, err := tenant.AcceptPlan(ctx, plan); err != nil {
		return err
	}
	var runs, attempts int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM runs`).Scan(&runs); err != nil {
		return err
	}
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM node_attempts`).Scan(&attempts); err != nil {
		return err
	}
	if runs != 1 || attempts != 0 {
		return fmt.Errorf("unstarted owner recovery created runs=%d attempts=%d", runs, attempts)
	}
	return nil
}

func TestStoreOwnerUnstartedRecovery(t *testing.T) {
	if err := checkOwnerUnstartedRecovery(t); err != nil {
		t.Fatal(err)
	}
}
